package admin_test

import (
	"context"
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// headersApart is a bucket admin store whose stored and recalculated
// headers differ.
type headersApart struct{ op.BucketAdminStore }

func (headersApart) CheckIndex(context.Context, *op.BucketRecord) (existing, calculated map[string]op.CategoryStats, err error) {
	return map[string]op.CategoryStats{"rgw.main": {NumObjects: 2}},
		map[string]op.CategoryStats{"rgw.main": {NumObjects: 3}, "rgw.multimeta": {NumObjects: 1}}, nil
}

// rgwUsage is go-ceph's rgw/admin RgwUsage (rgw/admin/bucket.go at the
// pinned go-ceph), cut to the fields the specs read.
type rgwUsage struct {
	SizeActual *uint64 `json:"size_actual"`
	NumObjects *uint64 `json:"num_objects"`
}

// checkBucketIndexResponse is go-ceph's rgw/admin CheckBucketIndexResponse
// (rgw/admin/check_bucket_index.go at the pinned go-ceph), which Rook reads
// the check into, its usage cut to the main category. Importing go-ceph's
// package would add its AWS SDK test modules to go.sum.
type checkBucketIndexResponse struct {
	InvalidMultipartEntries []string `json:"invalid_multipart_entries"`
	CheckResult             struct {
		ExistingHeader struct {
			Usage struct {
				RgwMain rgwUsage `json:"rgw.main"`
			} `json:"usage"`
		} `json:"existing_header"`
		CalculatedHeader struct {
			Usage struct {
				RgwMain rgwUsage `json:"rgw.main"`
			} `json:"usage"`
		} `json:"calculated_header"`
	} `json:"check_result"`
}

var _ = Describe("admin bucket index route", func() {
	const usage = `{"usage":{"rgw.main":{"size":10,"size_actual":8192,"size_utilized":10,"size_kb":1,"size_kb_actual":8,"size_kb_utilized":1,"num_objects":2}}}`
	var (
		fx     *fixture
		orphan meta.ObjKey
	)
	bucket := func() *op.BucketRecord {
		GinkgoHelper()
		rec, err := fx.store.GetBucket(context.Background(), "", "test")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	put := func(key meta.ObjKey) {
		GinkgoHelper()
		_, err := fx.store.PutObject(context.Background(), bucket(), key, strings.NewReader("hello"), op.PutParams{Size: 5})
		Expect(err).NotTo(HaveOccurred())
	}
	orphanThere := func() bool {
		GinkgoHelper()
		st, err := fx.store.StatObject(context.Background(), bucket(), orphan)
		Expect(err).NotTo(HaveOccurred())
		return st.Exists
	}
	addUser := func(id, caps string) {
		GinkgoHelper()
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: id}
		if caps != "" {
			Expect(info.Caps.AddString(caps)).To(Succeed())
		}
		fx.store.AddUser(info)
	}
	BeforeEach(func() {
		orphan = meta.ObjKey{Name: "orph.2~zzz.1", NS: meta.NSMultipart}
		fx = newFixture(admin.Config{})
		_, err := fx.store.CreateBucket(context.Background(), op.CreateBucketParams{
			Name: "test", Owner: meta.UserOwner(meta.UserID{ID: "admin"}), Zonegroup: fx.store.ZoneGroup().ID,
			Placement: meta.PlacementRule{Name: "default-placement"},
		})
		Expect(err).NotTo(HaveOccurred())
		put(meta.ObjKey{Name: "a"})
		put(meta.ObjKey{Name: "b"})
	})

	It("renders the check in JSON, repeating the objects' key, the top-level name dropped", func() {
		res := fx.get("/admin/bucket?index&bucket=test&check-objects=true&fix=true", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(body(res)).To(Equal(`{"invalid_multipart_entries":[],"objects":{"object":"a","object":"b"},"check_result":{"existing_header":` +
			usage + `,"calculated_header":` + usage + `}}`))
	})
	It("leaves the objects out without check-objects, and go-ceph's response decodes the document", func() {
		Expect(body(fx.get("/admin/bucket?index&bucket=test", "admin"))).To(Equal(
			`{"invalid_multipart_entries":[],"check_result":{"existing_header":` + usage + `,"calculated_header":` + usage + `}}`))
		// go-ceph's CheckBucketIndex sends its three arguments, sorted.
		b := body(fx.get("/admin/bucket?index&bucket=test&check-objects=true&fix=true", "admin"))
		var got checkBucketIndexResponse
		Expect(json.Unmarshal([]byte(b), &got)).To(Succeed(), b)
		Expect(got.InvalidMultipartEntries).To(BeEmpty())
		Expect(got.CheckResult.ExistingHeader.Usage.RgwMain.NumObjects).To(HaveValue(Equal(uint64(2))))
		Expect(got.CheckResult.CalculatedHeader.Usage.RgwMain.SizeActual).To(HaveValue(Equal(uint64(8192))))
	})
	It("renders the stored header as existing_header and the recalculated one as calculated_header", func() {
		fx.env.BucketAdmin = headersApart{fx.store}
		zero := `"size":0,"size_actual":0,"size_utilized":0,"size_kb":0,"size_kb_actual":0,"size_kb_utilized":0,`
		Expect(body(fx.get("/admin/bucket?index&bucket=test", "admin"))).To(Equal(`{"invalid_multipart_entries":[],"check_result":{` +
			`"existing_header":{"usage":{"rgw.main":{` + zero + `"num_objects":2}}},` +
			`"calculated_header":{"usage":{"rgw.main":{` + zero + `"num_objects":3},"rgw.multimeta":{` + zero + `"num_objects":1}}}}}`))
	})
	It("renders the check in XML", func() {
		res := fx.get("/admin/bucket?index&bucket=test&check-objects=true&fix=true&format=xml", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><bucket_check><invalid_multipart_entries></invalid_multipart_entries>` +
			`<objects><object>a</object><object>b</object></objects><check_result><existing_header><usage><rgw.main><size>`))
	})
	It("reports a part without its upload's meta entry, and removes it with fix", func() {
		put(orphan)
		b := body(fx.get("/admin/bucket?index&bucket=test", "admin"))
		Expect(b).To(HavePrefix(`{"invalid_multipart_entries":["_multipart_orph.2~zzz.1"],"check_result":`))
		Expect(orphanThere()).To(BeTrue())
		b = body(fx.get("/admin/bucket?index&bucket=test&fix=true", "admin"))
		Expect(b).To(HavePrefix(`{"invalid_multipart_entries":["_multipart_orph.2~zzz.1"],"check_result":`))
		Expect(orphanThere()).To(BeFalse())
		b = body(fx.get("/admin/bucket?index&bucket=test&format=xml", "admin"))
		Expect(b).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><bucket_check><invalid_multipart_entries></invalid_multipart_entries><check_result>`))
	})
	It("answers check-objects without fix 400, removing nothing", func() {
		put(orphan)
		res := fx.get("/admin/bucket?index&bucket=test&check-objects=true", "admin")
		Expect(res.StatusCode).To(Equal(400))
		Expect(errCode(res)).To(Equal("InvalidArgument"))
		Expect(orphanThere()).To(BeTrue())
	})
	It("answers a bucket that does not exist 404 NoSuchKey", func() {
		res := fx.get("/admin/bucket?index&bucket=foo", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchKey"))
	})
	DescribeTable("refuses a fix it cannot parse or that is empty, removing nothing",
		func(query string) {
			put(orphan)
			res := fx.get("/admin/bucket?index&bucket=test&"+query, "admin")
			Expect(res.StatusCode).To(Equal(400))
			Expect(errCode(res)).To(Equal("InvalidArgument"))
			Expect(orphanThere()).To(BeTrue())
		},
		Entry("fix=ture", "fix=ture"),
		Entry("an empty fix", "fix="),
		Entry("a bare fix", "fix"),
	)
	It("reads check-objects as radosgw does, an unparsable value false", func() {
		res := fx.get("/admin/bucket?index&bucket=test&check-objects=ture&fix=true", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(HavePrefix(`{"invalid_multipart_entries":[],"check_result":`))
	})
	DescribeTable("refuses a caller without buckets=write before anything is read or removed",
		func(caps, query string) {
			put(orphan)
			addUser("caller", caps)
			res := fx.get("/admin/bucket?index&bucket=test&"+query, "caller")
			Expect(res.StatusCode).To(Equal(403))
			Expect(errCode(res)).To(Equal("AccessDenied"))
			Expect(orphanThere()).To(BeTrue(), "nothing removed")
		},
		Entry("no caps", "", "fix=true"),
		Entry("buckets=read", "buckets=read", "fix=true"),
		Entry("the wrong cap type", "users=*;metadata=*;usage=*", "fix=true"),
		Entry("no caps, before an unparsable fix", "", "fix=ture"),
		Entry("buckets=read, before check-objects without fix", "buckets=read", "check-objects=true"),
	)
	It("admits buckets=write", func() {
		addUser("caller", "buckets=write")
		Expect(fx.get("/admin/bucket?index&bucket=test", "caller").StatusCode).To(Equal(200))
	})
})
