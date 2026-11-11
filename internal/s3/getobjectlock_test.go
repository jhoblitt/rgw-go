package s3_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/testutil/locktest"
)

// lockReadAuthz is an authorizer that refuses the object actions in deny and
// counts the object-lock read actions it is asked, asking next for the rest.
type lockReadAuthz struct {
	op.Authorizer
	deny  map[policy.Action]bool
	asked *int
}

func (l lockReadAuthz) VerifyObject(ctx context.Context, r *op.Request, a policy.Action, perm acl.Permission) error {
	if a == policy.S3GetObjectRetention || a == policy.S3GetObjectLegalHold {
		*l.asked++
	}
	if l.deny[a] {
		return op.ErrAccessDenied
	}
	return l.Authorizer.VerifyObject(ctx, r, a, perm)
}

// The headers GET and HEAD send for a version's lock.
const (
	hdrLockMode  = "x-amz-object-lock-mode"
	hdrLockUntil = "x-amz-object-lock-retain-until-date"
	hdrLockHold  = "x-amz-object-lock-legal-hold"
)

// The retention and the legal hold the read specs store, and the headers
// that show them.
var (
	retentionAttr = locktest.RetentionAttr("COMPLIANCE", lockUntil)
	holdAttr      = locktest.LegalHoldAttr("ON")
	lockSent      = [3]string{"COMPLIANCE", lockUntilHeader, "ON"}
)

var _ = Describe("the object-lock headers of GET and HEAD", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() { lockReadSpecs(rel) })
	}
})

// lockReadSpecs are the specs of the object-lock headers of GET and HEAD on
// rel.
func lockReadSpecs(rel denc.Release) {
	var (
		w     *writeWorld
		deny  map[policy.Action]bool
		asked int
	)
	BeforeEach(func(ctx SpecContext) {
		w = newWriteWorld(ctx, rel)
		w.lockPlain(ctx, nil)
		deny, asked = map[policy.Action]bool{}, 0
		w.env.Authz = lockReadAuthz{Authorizer: w.env.Authz, deny: deny, asked: &asked}
	})
	// lockHeadersOf is the lock headers of a read of target by method,
	// which succeeded, each "" when absent.
	lockHeadersOf := func(method, target string, hdr ...string) [3]string {
		GinkgoHelper()
		rec := w.send(w.alice, method, target, "", hdr...)
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		for _, name := range []string{hdrLockMode, hdrLockUntil, hdrLockHold} {
			Expect(rec.Header()[http.CanonicalHeaderKey(name)]).To(BeEmpty(), "the header goes out as radosgw spells it")
		}
		return [3]string{exactHeader(rec.Header(), hdrLockMode), exactHeader(rec.Header(), hdrLockUntil), exactHeader(rec.Header(), hdrLockHold)}
	}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		DescribeTable(method+" sends each header its permission allows, from an attr that decodes",
			func(ctx SpecContext, attrs map[string][]byte, refused []policy.Action, want [3]string) {
				w.put(ctx, "k", "data", attrs)
				for _, a := range refused {
					deny[a] = true
				}
				Expect(lockHeadersOf(method, "/plain/k")).To(Equal(want))
			},
			Entry("both attrs, both allowed",
				map[string][]byte{meta.AttrObjectRetention: retentionAttr, meta.AttrObjectLegalHold: holdAttr}, nil, lockSent),
			Entry("both attrs, s3:GetObjectRetention refused",
				map[string][]byte{meta.AttrObjectRetention: retentionAttr, meta.AttrObjectLegalHold: holdAttr},
				[]policy.Action{policy.S3GetObjectRetention}, [3]string{"", "", "ON"}),
			Entry("both attrs, s3:GetObjectLegalHold refused",
				map[string][]byte{meta.AttrObjectRetention: retentionAttr, meta.AttrObjectLegalHold: holdAttr},
				[]policy.Action{policy.S3GetObjectLegalHold}, [3]string{"COMPLIANCE", lockUntilHeader, ""}),
			Entry("both attrs, both refused",
				map[string][]byte{meta.AttrObjectRetention: retentionAttr, meta.AttrObjectLegalHold: holdAttr},
				[]policy.Action{policy.S3GetObjectRetention, policy.S3GetObjectLegalHold}, [3]string{}),
			Entry("no attr, both allowed", nil, nil, [3]string{}),
			Entry("a retention alone", map[string][]byte{meta.AttrObjectRetention: retentionAttr}, nil, [3]string{"COMPLIANCE", lockUntilHeader, ""}),
			Entry("a legal hold alone", map[string][]byte{meta.AttrObjectLegalHold: holdAttr}, nil, [3]string{"", "", "ON"}),
			Entry("a retention with a byte past its encoding, which radosgw's whole-buffer decode refuses",
				map[string][]byte{meta.AttrObjectRetention: slices.Concat(retentionAttr, []byte{0}), meta.AttrObjectLegalHold: holdAttr}, nil, [3]string{"", "", "ON"}),
			Entry("a legal hold cut short",
				map[string][]byte{meta.AttrObjectRetention: retentionAttr, meta.AttrObjectLegalHold: holdAttr[:len(holdAttr)-1]}, nil,
				[3]string{"COMPLIANCE", lockUntilHeader, ""}),
			Entry("an empty retention", map[string][]byte{meta.AttrObjectRetention: {}}, nil, [3]string{}),
			Entry("a mode holding CR and LF, which would start a header line",
				map[string][]byte{meta.AttrObjectRetention: locktest.RetentionAttr("GOVERNANCE\r\nX-Injected: 1", lockUntil)}, nil, [3]string{}),
			Entry("a status holding a NUL", map[string][]byte{meta.AttrObjectLegalHold: locktest.LegalHoldAttr("ON\x00")}, nil, [3]string{}),
			Entry("a status holding a tab", map[string][]byte{meta.AttrObjectLegalHold: locktest.LegalHoldAttr("O\tN")}, nil, [3]string{}),
		)
	}
	It("sends a whole-second date with nine fractional digits, as to_iso_8601 writes it", func(ctx SpecContext) {
		w.put(ctx, "k", "data", map[string][]byte{
			meta.AttrObjectRetention: locktest.RetentionAttr("GOVERNANCE", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)),
		})
		Expect(lockHeadersOf(http.MethodHead, "/plain/k")).To(Equal([3]string{"GOVERNANCE", "2030-01-01T00:00:00.000000000Z", ""}))
	})
	It("sends nothing on a bucket without object lock, and evaluates neither permission", func(ctx SpecContext) {
		w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags &^= meta.BucketObjLockEnabled })
		w.put(ctx, "k", "data", map[string][]byte{meta.AttrObjectRetention: retentionAttr, meta.AttrObjectLegalHold: holdAttr})
		Expect(lockHeadersOf(http.MethodGet, "/plain/k")).To(Equal([3]string{}))
		Expect(asked).To(BeZero())
	})
	It("sends the lock of the version a request names", func(ctx SpecContext) {
		w.put(ctx, "k", "data", map[string][]byte{meta.AttrObjectLegalHold: holdAttr})
		_, err := w.store.PutObject(ctx, w.bucket(ctx), meta.ObjKey{Name: "k", Instance: "v1"}, strings.NewReader("v1"), op.PutParams{
			Size: 2, Attrs: map[string][]byte{
				meta.AttrACL:             encodeACL(acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice")),
				meta.AttrObjectRetention: retentionAttr,
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(lockHeadersOf(http.MethodGet, "/plain/k?versionId=v1")).To(Equal([3]string{"COMPLIANCE", lockUntilHeader, ""}))
		Expect(lockHeadersOf(http.MethodGet, "/plain/k")).To(Equal([3]string{"", "", "ON"}))
	})
	It("sends none on a 304, which carries send_response_data's four headers alone", func(ctx SpecContext) {
		w.put(ctx, "k", "data", map[string][]byte{meta.AttrObjectRetention: retentionAttr, meta.AttrObjectLegalHold: holdAttr})
		rec := w.send(w.alice, http.MethodGet, "/plain/k", "", "If-None-Match", `"`+md5Hex("data")+`"`)
		Expect(rec.Code).To(Equal(http.StatusNotModified))
		for _, name := range []string{hdrLockMode, hdrLockUntil, hdrLockHold} {
			Expect(rec.Header()).NotTo(haveHeaderAnyCase(name))
		}
	})
	It("logs the attr it leaves out", func(ctx SpecContext) {
		logs := captureLog()
		w.put(ctx, "k", "data", map[string][]byte{meta.AttrObjectRetention: slices.Concat(retentionAttr, []byte{0})})
		Expect(lockHeadersOf(http.MethodGet, "/plain/k")).To(Equal([3]string{}))
		Expect(logs.String()).To(ContainSubstring(meta.AttrObjectRetention))
	})
	It("sends a part read the object's lock, not the one its part's head holds", func() {
		objects := multipartObjects()
		head := objects.StatObjectStub
		withLock := func(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
			st, err := head(ctx, rec, key)
			if err != nil {
				return nil, err
			}
			if key.Name == "k" {
				st.Attrs[meta.AttrObjectRetention] = locktest.RetentionAttr("GOVERNANCE", lockSourceUntil)
				return st, nil
			}
			st.Attrs[meta.AttrObjectRetention] = retentionAttr
			st.Attrs[meta.AttrObjectLegalHold] = holdAttr
			return st, nil
		}
		objects.StatObjectStub, objects.PrefetchObjectStub = withLock, withLock
		w.env.Objects = objects
		want := [3]string{"GOVERNANCE", "2031-01-01T00:00:00.000000000Z", ""}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			Expect(lockHeadersOf(method, "/plain/k?partNumber=2")).To(Equal(want), method)
			Expect(lockHeadersOf(method, "/plain/k")).To(Equal(want), method)
		}
	})
}
