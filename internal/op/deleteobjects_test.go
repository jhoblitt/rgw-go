package op_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// multiDelete records what a DeleteObjects hands its handler.
type multiDelete struct {
	mu       sync.Mutex
	events   []string
	statuses []error
	results  []op.DeleteResult
	parsed   int
}

func (m *multiDelete) record(event string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

// op returns a DeleteObjects whose Parse returns entries and err.
func (m *multiDelete) op(entries []op.DeleteObjectsEntry, err error) *op.DeleteObjects {
	return &op.DeleteObjects{
		Parse: func(body []byte) ([]op.DeleteObjectsEntry, error) {
			m.mu.Lock()
			m.parsed++
			m.mu.Unlock()
			m.record("parse " + string(body))
			return entries, err
		},
		Status: func(err error) {
			m.mu.Lock()
			m.statuses = append(m.statuses, err)
			m.mu.Unlock()
			m.record("status")
		},
		Begin: func() error {
			m.record("begin")
			return nil
		},
		Result: func(res op.DeleteResult) {
			m.mu.Lock()
			m.results = append(m.results, res)
			m.mu.Unlock()
			m.record("result")
		},
	}
}

func entriesOf(names ...string) []op.DeleteObjectsEntry {
	es := make([]op.DeleteObjectsEntry, len(names))
	for i, n := range names {
		es[i] = op.DeleteObjectsEntry{Key: meta.ObjKey{Name: n}}
	}
	return es
}

// multiReq is alice's POST ?delete on plain carrying body with a
// Content-Length header.
func (f *writeFixture) multiReq(body string) *op.Request {
	r := f.req(http.MethodPost, "plain", "")
	r.Body = strings.NewReader(body)
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return r
}

// errByKey is each result's error keyed by its key's name.
func errByKey(results []op.DeleteResult) map[string]error {
	m := map[string]error{}
	for _, res := range results {
		m[res.Key.Name] = res.Err
	}
	return m
}

var _ = Describe("DeleteObjects", func() {
	var (
		f *writeFixture
		m *multiDelete
	)
	BeforeEach(func(ctx SpecContext) {
		f = newWriteFixture(ctx, denc.Squid)
		f.put(ctx, "a", []byte("a"))
		m = &multiDelete{}
	})

	It("is radosgw's multi_object_delete, a delete", func() {
		o := &op.DeleteObjects{}
		Expect(o.Name()).To(Equal("multi_object_delete"))
		Expect(o.Action()).To(Equal(policy.S3DeleteObject))
		Expect(o.OpMask()).To(Equal(op.OpTypeDelete))
	})
	It("parses the body read whole, begins once, then hands each key's outcome to Result", func(ctx SpecContext) {
		o := m.op(entriesOf("small", "missing", "", "a"), nil)
		Expect(op.Run(ctx, o, f.multiReq("x"))).To(Succeed())
		Expect(m.events[0]).To(Equal("parse x"))
		Expect(m.events[1]).To(Equal("begin"))
		Expect(m.events[2:]).To(HaveEach("result"))
		Expect(m.statuses).To(BeEmpty())
		Expect(errByKey(m.results)).To(Equal(map[string]error{"small": nil, "missing": nil, "": op.ErrInvalidArgument, "a": nil}))
		for _, res := range m.results {
			Expect(res.DeleteMarker).To(BeFalse(), res.Key.Name)
			Expect(res.MarkerVersionID).To(BeEmpty(), res.Key.Name)
		}
		Expect(f.stat(ctx, "small").Exists).To(BeFalse())
		Expect(f.stat(ctx, "a").Exists).To(BeFalse())
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("never calls Result concurrently while deleting up to rgw_multi_obj_del_max_aio keys at once", func(ctx SpecContext) {
		f.conf["rgw_multi_obj_del_max_aio"] = "4"
		names := make([]string, 8)
		for i := range names {
			names[i] = fmt.Sprintf("k%d", i)
			f.put(ctx, names[i], []byte("v"))
		}
		var inResult, maxInResult atomic.Int32
		o := m.op(entriesOf(names...), nil)
		o.Result = func(res op.DeleteResult) {
			n := inResult.Add(1)
			for {
				cur := maxInResult.Load()
				if n <= cur || maxInResult.CompareAndSwap(cur, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond) // holds the call open so an overlap would be seen
			inResult.Add(-1)
			m.mu.Lock()
			m.results = append(m.results, res)
			m.mu.Unlock()
		}
		Expect(op.Run(ctx, o, f.multiReq("x"))).To(Succeed())
		Expect(maxInResult.Load()).To(Equal(int32(1)))
		Expect(m.results).To(HaveLen(8))
	})
	It("keeps at most rgw_multi_obj_del_max_aio deletes in flight", func(ctx SpecContext) {
		f.conf["rgw_multi_obj_del_max_aio"] = "3"
		stub := &opfakes.FakeObjectStore{}
		var inFlight, maxInFlight atomic.Int32
		stub.DeleteObjectStub = func(context.Context, *op.BucketRecord, meta.ObjKey, op.DeleteParams) error {
			n := inFlight.Add(1)
			for {
				cur := maxInFlight.Load()
				if n <= cur || maxInFlight.CompareAndSwap(cur, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond) // holds the delete open so the overlap is seen
			inFlight.Add(-1)
			return nil
		}
		f.env.Objects = stub
		Expect(op.Run(ctx, m.op(entriesOf("k0", "k1", "k2", "k3", "k4", "k5", "k6", "k7"), nil), f.multiReq("x"))).To(Succeed())
		Expect(stub.DeleteObjectCallCount()).To(Equal(8))
		Expect(maxInFlight.Load()).To(BeNumerically("<=", 3))
		Expect(maxInFlight.Load()).To(BeNumerically(">", 1), "the deletes overlap")
	})
	DescribeTable("takes a non-positive rgw_multi_obj_del_max_aio as 1, as std::max<uint32_t>(1, ...) does",
		func(ctx SpecContext, value string) {
			f.conf["rgw_multi_obj_del_max_aio"] = value
			Expect(op.Run(ctx, m.op(entriesOf("small", "a"), nil), f.multiReq("x"))).To(Succeed())
			Expect(m.results).To(HaveLen(2))
		},
		Entry("zero", "0"),
		Entry("one past uint32, which narrows to zero", "4294967296"),
	)
	Describe("a check that fails before the first key goes to Status alone", func() {
		It("refuses more keys than rgw_delete_multi_obj_max_num with Squid's bare MalformedXML", func(ctx SpecContext) {
			names := make([]string, 1001)
			for i := range names {
				names[i] = fmt.Sprintf("k%d", i)
			}
			o := m.op(entriesOf(names...), nil)
			err := op.Run(ctx, o, f.multiReq("x"))
			Expect(err).To(MatchError(op.ErrMalformedXML))
			Expect(messageOf(err)).To(BeEmpty())
			Expect(m.statuses).To(ConsistOf(MatchError(op.ErrMalformedXML)))
			Expect(m.events).NotTo(ContainElement("begin"))
		})
		It("refuses them on Tentacle with radosgw's message", func(ctx SpecContext) {
			f = newWriteFixture(ctx, denc.Tentacle)
			f.conf["rgw_delete_multi_obj_max_num"] = "2"
			err := op.Run(ctx, m.op(entriesOf("a", "b", "c"), nil), f.multiReq("x"))
			Expect(err).To(MatchError(op.ErrMalformedXML))
			Expect(messageOf(err)).To(Equal("Object count limit 2 exceeded"))
			Expect(m.events).NotTo(ContainElement("begin"))
		})
		DescribeTable("takes the limit as radosgw's int holds it",
			func(ctx SpecContext, value string, keys int, want error) {
				f.conf["rgw_delete_multi_obj_max_num"] = value
				names := make([]string, keys)
				for i := range names {
					names[i] = fmt.Sprintf("k%d", i)
				}
				err := op.Run(ctx, m.op(entriesOf(names...), nil), f.multiReq("x"))
				if want == nil {
					Expect(err).NotTo(HaveOccurred())
					return
				}
				Expect(err).To(MatchError(want))
			},
			Entry("a negative limit is 1000", "-1", 1000, nil),
			Entry("a negative limit refuses 1001", "-5", 1001, op.ErrMalformedXML),
			Entry("a limit narrowed to zero by the int refuses one key", "4294967296", 1, op.ErrMalformedXML),
		)
		It("sends a Parse error through Status", func(ctx SpecContext) {
			perr := op.ErrMalformedXML.WithMessage("Failed to parse xml input")
			err := op.Run(ctx, m.op(nil, perr), f.multiReq("x"))
			Expect(err).To(MatchError(op.ErrMalformedXML))
			Expect(m.statuses).To(ConsistOf(MatchError(perr)))
			Expect(m.events).NotTo(ContainElement("begin"))
		})
		It("refuses an empty body as InvalidArgument without parsing it", func(ctx SpecContext) {
			err := op.Run(ctx, m.op(entriesOf("a"), nil), f.multiReq(""))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(m.parsed).To(BeZero())
			Expect(m.statuses).To(ConsistOf(MatchError(op.ErrInvalidArgument)))
		})
		It("refuses an empty list on Tentacle with radosgw's message", func(ctx SpecContext) {
			f = newWriteFixture(ctx, denc.Tentacle)
			err := op.Run(ctx, m.op(nil, nil), f.multiReq("x"))
			Expect(err).To(MatchError(op.ErrMalformedXML))
			Expect(messageOf(err)).To(Equal("Missing required element Object"))
			Expect(m.statuses).To(HaveLen(1))
			Expect(m.events).NotTo(ContainElement("begin"))
		})
	})
	It("begins and sends no result for an empty list on Squid", func(ctx SpecContext) {
		Expect(op.Run(ctx, m.op(nil, nil), f.multiReq("x"))).To(Succeed())
		Expect(m.events).To(Equal([]string{"parse x", "begin"}))
	})
	It("ends at a Begin that fails", func(ctx SpecContext) {
		stub := &opfakes.FakeObjectStore{}
		f.env.Objects = stub
		o := m.op(entriesOf("small"), nil)
		o.Begin = func() error { return io.ErrClosedPipe }
		Expect(op.Run(ctx, o, f.multiReq("x"))).To(MatchError(io.ErrClosedPipe))
		Expect(stub.DeleteObjectCallCount()).To(BeZero())
		Expect(m.results).To(BeEmpty())
	})
	Describe("Init reads the body as read_all_input does without chunked input", func() {
		DescribeTable("refuses a request without a length as MissingContentLength",
			func(ctx SpecContext, contentLength int64, header string) {
				r := f.req(http.MethodPost, "plain", "")
				r.Body, r.ContentLength = failingReader{}, contentLength
				if header != "" {
					r.Header.Set("Content-Length", header)
				}
				Expect(op.Run(ctx, m.op(nil, nil), r)).To(MatchError(op.ErrMissingContentLength))
				Expect(m.events).To(BeEmpty())
			},
			Entry("a chunked body", int64(-1), ""),
			Entry("no Content-Length header", int64(0), ""),
		)
		It("refuses a Content-Length over rgw_max_put_param_size as InvalidRange unread", func(ctx SpecContext) {
			f.conf["rgw_max_put_param_size"] = "4"
			r := f.multiReq("12345")
			r.Body = failingReader{}
			Expect(op.Run(ctx, m.op(nil, nil), r)).To(MatchError(op.ErrInvalidRange))
			Expect(m.events).To(BeEmpty())
		})
		It("reads a body at rgw_max_put_param_size", func(ctx SpecContext) {
			f.conf["rgw_max_put_param_size"] = "4"
			Expect(op.Run(ctx, m.op(nil, nil), f.multiReq("1234"))).To(Succeed())
			Expect(m.events[0]).To(Equal("parse 1234"))
		})
		It("refuses a missing bucket before the body is touched", func(ctx SpecContext) {
			r := f.multiReq("x")
			r.Bucket, r.Body = "nope", failingReader{}
			Expect(op.Run(ctx, m.op(nil, nil), r)).To(MatchError(op.ErrNoSuchBucket))
			Expect(m.events).To(BeEmpty())
		})
		It("returns the body's verification verdict from its final Read, before parsing", func(ctx SpecContext) {
			r := f.multiReq("xyz")
			r.Body = io.MultiReader(strings.NewReader("xyz"), errReader{op.ErrContentSHA256Mismatch})
			Expect(op.Run(ctx, m.op(entriesOf("small"), nil), r)).To(MatchError(op.ErrContentSHA256Mismatch))
			Expect(m.events).To(BeEmpty())
			Expect(f.stat(ctx, "small").Exists).To(BeTrue())
		})
		It("reads an empty body to its final Read, whose verdict it returns", func(ctx SpecContext) {
			r := f.multiReq("")
			r.Body = errReader{op.ErrContentSHA256Mismatch}
			Expect(op.Run(ctx, m.op(nil, nil), r)).To(MatchError(op.ErrContentSHA256Mismatch))
			Expect(m.events).To(BeEmpty())
		})
		It("answers a body that ends short of its length as RequestTimeout", func(ctx SpecContext) {
			r := f.multiReq("xyz")
			r.Body = io.MultiReader(strings.NewReader("x"), errReader{io.ErrUnexpectedEOF})
			Expect(op.Run(ctx, m.op(nil, nil), r)).To(MatchError(op.ErrRequestTimeout))
		})
	})
	Describe("the request-level check", func() {
		It("refuses a suspended bucket once, marked before the op mask", func(ctx SpecContext) {
			authz := &opfakes.FakeAuthorizer{}
			authz.VerifyBucketReturns(op.BeforeVerify(op.ErrUserSuspended))
			f.env.Authz = authz
			r := f.multiReq("x")
			r.Identity.OpMask = op.OpTypeRead
			err := op.Run(ctx, m.op(entriesOf("small"), nil), r)
			Expect(err).To(MatchError(op.ErrUserSuspended))
			Expect(op.IsBeforeVerify(err)).To(BeTrue())
			Expect(authz.VerifyBucketInCallCount()).To(BeZero())
			Expect(m.events).To(BeEmpty())
		})
		It("passes a refusal that is not marked to the per-key checks", func(ctx SpecContext) {
			authz := &opfakes.FakeAuthorizer{}
			authz.VerifyBucketReturns(op.ErrAccessDenied)
			f.env.Authz = authz
			Expect(op.Run(ctx, m.op(entriesOf("small"), nil), f.multiReq("x"))).To(Succeed())
			Expect(authz.VerifyBucketInCallCount()).To(Equal(1))
			Expect(f.stat(ctx, "small").Exists).To(BeFalse())
		})
	})
	It("refuses a key the authorizer refuses with AccessDenied in its result and deletes the others", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		authz.VerifyBucketInStub = func(_ context.Context, _ *op.Request, _ policy.Action, _ acl.Permission, _ *op.BucketRecord, key meta.ObjKey) error {
			if key.Name == "small" {
				return op.ErrUserSuspended
			}
			return nil
		}
		f.env.Authz = authz
		Expect(op.Run(ctx, m.op(entriesOf("small", "a"), nil), f.multiReq("x"))).To(Succeed())
		Expect(errByKey(m.results)).To(Equal(map[string]error{"small": op.ErrAccessDenied, "a": nil}))
		Expect(f.stat(ctx, "small").Exists).To(BeTrue())
		Expect(f.stat(ctx, "a").Exists).To(BeFalse())
	})
	DescribeTable("authorizes each key in the request's bucket with the action its version selects",
		func(ctx SpecContext, instance string, want policy.Action) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			Expect(op.Run(ctx, m.op([]op.DeleteObjectsEntry{{Key: meta.ObjKey{Name: "small", Instance: instance}}}, nil), f.multiReq("x"))).To(Succeed())
			_, _, a, perm, bucket, key := authz.VerifyBucketInArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
			Expect(bucket.Info.Bucket.Name).To(Equal("plain"))
			Expect(key).To(Equal(meta.ObjKey{Name: "small", Instance: instance}))
		},
		Entry("no version", "", policy.S3DeleteObject),
	)
	DescribeTable("checks each entry's ETag, Size and LastModifiedTime on both releases",
		func(ctx SpecContext, release denc.Release, e op.DeleteObjectsEntry, want error) {
			f = newWriteFixture(ctx, release)
			e.Key = meta.ObjKey{Name: "small"}
			Expect(op.Run(ctx, m.op([]op.DeleteObjectsEntry{e}, nil), f.multiReq("x"))).To(Succeed())
			Expect(m.results).To(HaveLen(1))
			if want != nil {
				Expect(m.results[0].Err).To(MatchError(want))
				Expect(f.stat(ctx, "small").Exists).To(BeTrue())
				return
			}
			Expect(m.results[0].Err).NotTo(HaveOccurred())
			Expect(f.stat(ctx, "small").Exists).To(BeFalse())
		},
		Entry("another ETag on Squid", denc.Squid, op.DeleteObjectsEntry{IfMatch: new(md5Hex([]byte("other")))}, op.ErrPreconditionFailed),
		Entry("another ETag on Tentacle", denc.Tentacle, op.DeleteObjectsEntry{IfMatch: new(md5Hex([]byte("other")))}, op.ErrPreconditionFailed),
		Entry("the ETag", denc.Tentacle, op.DeleteObjectsEntry{IfMatch: new(md5Hex(payload(1024)))}, nil),
		Entry("an empty ETag, which no ETag is a prefix of", denc.Tentacle, op.DeleteObjectsEntry{IfMatch: new("")}, op.ErrPreconditionFailed),
		Entry("another size", denc.Squid, op.DeleteObjectsEntry{IfMatchSize: new(uint64(1))}, op.ErrPreconditionFailed),
		Entry("another last-modified time", denc.Squid,
			op.DeleteObjectsEntry{IfMatchLastModified: smallMtime().Add(time.Second)}, op.ErrPreconditionFailed),
	)
	It("reports a lost race and any other failure as that key's error", func(ctx SpecContext) {
		stub := &opfakes.FakeObjectStore{}
		stub.DeleteObjectStub = func(_ context.Context, _ *op.BucketRecord, key meta.ObjKey, _ op.DeleteParams) error {
			switch key.Name {
			case "race":
				return op.ErrConcurrentModification
			case "gone":
				return fmt.Errorf("object gone: %w", op.ErrNoSuchKey)
			}
			return errors.New("boom")
		}
		f.env.Objects = stub
		Expect(op.Run(ctx, m.op(entriesOf("race", "gone", "boom"), nil), f.multiReq("x"))).To(Succeed())
		got := errByKey(m.results)
		Expect(got["race"]).To(MatchError(op.ErrConcurrentModification))
		Expect(got["gone"]).NotTo(HaveOccurred())
		Expect(got["boom"]).To(MatchError("boom"))
	})
	Describe("on a bucket with MFA delete", func() {
		BeforeEach(func(ctx SpecContext) { f.setBucketFlags(ctx, meta.BucketMFAEnabled) })

		DescribeTable("refuses a request naming a version before the first key, on both releases",
			func(ctx SpecContext, release denc.Release) {
				if release != denc.Squid {
					f = newWriteFixture(ctx, release)
					f.setBucketFlags(ctx, meta.BucketMFAEnabled)
				}
				es := []op.DeleteObjectsEntry{{Key: meta.ObjKey{Name: "a"}}, {Key: meta.ObjKey{Name: "small", Instance: "null"}}}
				err := op.Run(ctx, m.op(es, nil), f.multiReq("x"))
				Expect(err).To(MatchError(op.ErrMFARequired))
				Expect(m.statuses).To(ConsistOf(MatchError(op.ErrMFARequired)))
				Expect(m.events).NotTo(ContainElement("begin"))
				Expect(f.stat(ctx, "small").Exists).To(BeTrue())
			},
			Entry("Squid", denc.Squid),
			Entry("Tentacle, whose radosgw inverts the check", denc.Tentacle),
		)
		It("deletes keys without versions", func(ctx SpecContext) {
			f = newWriteFixture(ctx, denc.Tentacle)
			f.setBucketFlags(ctx, meta.BucketMFAEnabled)
			Expect(op.Run(ctx, m.op(entriesOf("small"), nil), f.multiReq("x"))).To(Succeed())
			Expect(errByKey(m.results)).To(Equal(map[string]error{"small": nil}))
		})
	})
})
