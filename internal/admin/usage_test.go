package admin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// goCephUsage is go-ceph's rgw/admin Usage document (rgw/admin/usage.go at
// the pinned fork commit), the shape Rook decodes.
type goCephUsage struct {
	Entries []struct {
		User    string `json:"user"`
		Buckets []struct {
			Bucket     string `json:"bucket"`
			Time       string `json:"time"`
			Epoch      uint64 `json:"epoch"`
			Owner      string `json:"owner"`
			Categories []struct {
				Category      string `json:"category"`
				BytesSent     uint64 `json:"bytes_sent"`
				BytesReceived uint64 `json:"bytes_received"`
				Ops           uint64 `json:"ops"`
				SuccessfulOps uint64 `json:"successful_ops"`
			} `json:"categories"`
		} `json:"buckets"`
	} `json:"entries"`
	Summary []struct {
		User  string `json:"user"`
		Total struct {
			BytesReceived uint64 `json:"bytes_received"`
			Ops           uint64 `json:"ops"`
		} `json:"total"`
	} `json:"summary"`
}

var _ = Describe("admin usage routes", func() {
	const acct = "RGW00000000000000001"
	var (
		fx    *fixture
		usage *opfakes.FakeUsageReader
	)
	at := func(day, h int) uint64 { return uint64(time.Date(2026, 9, day, h, 0, 0, 0, time.UTC).Unix()) }
	put := func(user, bucket string, epoch uint64, cat string, ops uint64) {
		fx.store.AddUsage(op.UsageRecord{
			Owner: user, Bucket: bucket, Epoch: epoch,
			Categories: map[string]op.UsageData{cat: {BytesReceived: 10 * ops, Ops: ops, SuccessfulOps: ops}},
		})
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
		fx = newFixture(admin.Config{})
		usage = new(opfakes.FakeUsageReader)
		usage.ReadUsageStub = fx.store.ReadUsage
		usage.TrimUsageStub = fx.store.TrimUsage
		fx.env.UsageReader = usage
		addUser("reader", "usage=read")
		addUser("writer", "usage=write")
		addUser("usager", "usage=*")
		addUser("other", "users=*;buckets=*;metadata=*;zone=read;info=read")
		for _, b := range []struct {
			name  string
			owner meta.Owner
		}{
			{"plain", meta.UserOwner(meta.UserID{ID: "alice"})},
			{"acctb", meta.AccountOwner(acct)},
		} {
			_, err := fx.store.CreateBucket(context.Background(), op.CreateBucketParams{
				Name: b.name, Owner: b.owner, Placement: meta.PlacementRule{Name: "default-placement"},
			})
			Expect(err).NotTo(HaveOccurred())
		}
	})
	get := func(path string) string {
		GinkgoHelper()
		res := fx.get(path, "usager")
		b := body(res)
		Expect(res.StatusCode).To(Equal(http.StatusOK), b)
		return b
	}
	left := func() []string {
		GinkgoHelper()
		recs, _, err := fx.store.ReadUsage(context.Background(), "", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, r := range recs {
			out = append(out, r.User+"/"+r.Bucket)
		}
		return out
	}
	cat := func(name string, ops uint64) string {
		return fmt.Sprintf(`{"category":%q,"bytes_sent":0,"bytes_received":%d,"ops":%d,"successful_ops":%d}`, name, 10*ops, ops, ops)
	}

	It("shows the entries and summary as RGWUsage::show writes them, which go-ceph decodes", func() {
		e := at(25, 1)
		put("alice", "plain", e, "put_obj", 1)
		res := fx.get("/admin/usage?show-summary=true", "usager")
		b := body(res)
		Expect(res.StatusCode).To(Equal(http.StatusOK), b)
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(b).To(Equal(fmt.Sprintf(`{"entries":[{"user":"alice","buckets":[{"bucket":"plain","time":"2026-09-25T01:00:00.000000Z",`+
			`"epoch":%d,"owner":"alice","categories":[%s],"s3select":{"bytes_processed":0,"bytes_returned":0}}]}],`+
			`"summary":[{"user":"alice","categories":[%s],"total":{"bytes_sent":0,"bytes_received":10,"ops":1,"successful_ops":1,`+
			`"bytes_processed":0,"bytes_returned":0}}]}`, e, cat("put_obj", 1), cat("put_obj", 1))))
		var doc goCephUsage
		Expect(json.Unmarshal([]byte(b), &doc)).To(Succeed())
		Expect(doc.Entries[0].Buckets[0].Epoch).To(Equal(e))
		Expect(doc.Summary[0].Total.Ops).To(Equal(uint64(1)))
	})

	It("names a payer that is not the owner, and lists the categories in name order", func() {
		fx.store.AddUsage(op.UsageRecord{
			Owner: "alice", Payer: "carol", Bucket: "plain", Epoch: at(25, 1),
			Categories: map[string]op.UsageData{"put_obj": {Ops: 1}, "get_obj": {Ops: 2}},
		})
		b := get("/admin/usage?show-summary=false")
		Expect(b).To(ContainSubstring(`"user":"carol"`))
		Expect(b).To(ContainSubstring(`"owner":"alice","payer":"carol","categories":[{"category":"get_obj"`))
	})

	It("writes the records of no user at the listing's start outside any user section", func() {
		put("", "nobody", at(25, 1), "put_obj", 1)
		put("alice", "plain", at(25, 1), "put_obj", 1)
		b := get("/admin/usage?show-summary=false")
		Expect(b).To(HavePrefix(`{"entries":[{"bucket":"nobody","time":`))
		Expect(b).To(ContainSubstring(`"s3select":{"bytes_processed":0,"bytes_returned":0}},{"user":"alice","buckets":[{"bucket":"plain"`))
	})

	It("opens a section for records of no user once another user's came first", func() {
		pages := new(opfakes.FakeUsageReader)
		pages.ReadUsageReturnsOnCall(0, []op.UsageRecord{{User: "alice", Owner: "alice", Bucket: "plain"}}, true, nil)
		pages.ReadUsageReturnsOnCall(1, []op.UsageRecord{{Bucket: "nobody"}}, false, nil)
		fx.env.UsageReader = pages
		b := get("/admin/usage?show-summary=false")
		Expect(b).To(ContainSubstring(`]},{"user":"","buckets":[{"bucket":"nobody"`))
	})

	It("filters by uid and by start and end dates", func() {
		put("alice", "plain", at(25, 1), "put_obj", 1)
		put("alice", "plain", at(25, 17), "get_obj", 2)
		put("bob", "b", at(25, 17), "put_obj", 1)
		var doc goCephUsage
		Expect(json.Unmarshal([]byte(get("/admin/usage?uid=alice&start=2026-09-25+16:00:00&end=2026-09-26")), &doc)).To(Succeed())
		Expect(doc.Entries).To(HaveLen(1))
		Expect(doc.Entries[0].User).To(Equal("alice"))
		Expect(doc.Entries[0].Buckets).To(HaveLen(1))
		Expect(doc.Entries[0].Buckets[0].Epoch).To(Equal(at(25, 17)))
		Expect(doc.Entries[0].Buckets[0].Categories[0].Category).To(Equal("get_obj"))
	})

	It("reads a date it cannot parse as the default, as radosgw drops get_epoch's return", func() {
		put("alice", "plain", at(25, 1), "put_obj", 1)
		var doc goCephUsage
		Expect(json.Unmarshal([]byte(get("/admin/usage?start=yesterday&end=never")), &doc)).To(Succeed())
		Expect(doc.Entries).To(HaveLen(1))
	})

	It("reads a boolean it cannot parse as its default, as get_bool does", func() {
		put("alice", "plain", at(25, 1), "put_obj", 1)
		Expect(get("/admin/usage?show-entries=flase&show-summary=0")).To(HavePrefix(`{"entries":[{"user":"alice"`))
		Expect(get("/admin/usage?show-entries=false&show-summary=")).To(HavePrefix(`{"summary":[`), "an empty value is true")
	})

	It("shows an empty usage section when neither part is asked for", func() {
		Expect(get("/admin/usage")).To(Equal(`{"entries":[],"summary":[]}`), "an empty usage log")
		put("alice", "plain", at(25, 1), "put_obj", 1)
		Expect(get("/admin/usage?show-entries=false&show-summary=false")).To(Equal(`{}`))
		Expect(get("/admin/usage?show-entries=false&show-summary=false&format=xml")).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><usage></usage>`))
	})

	It("renders XML as radosgw's XMLFormatter does", func() {
		put("alice", "plain", at(25, 1), "put_obj", 1)
		b := get("/admin/usage?show-summary=true&format=xml")
		Expect(b).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><usage><entries><user><user>alice</user><buckets><bucket><bucket>plain</bucket><time>`))
		Expect(b).To(ContainSubstring(`<owner>alice</owner><categories><entry><category>put_obj</category><bytes_sent>0</bytes_sent>` +
			`<bytes_received>10</bytes_received><ops>1</ops><successful_ops>1</successful_ops></entry></categories><s3select>` +
			`<bytes_processed>0</bytes_processed><bytes_returned>0</bytes_returned></s3select></bucket></buckets></user></entries>` +
			`<summary><user><user>alice</user>`))
		Expect(b).To(HaveSuffix(`<total><bytes_sent>0</bytes_sent><bytes_received>10</bytes_received><ops>1</ops>` +
			`<successful_ops>1</successful_ops><bytes_processed>0</bytes_processed><bytes_returned>0</bytes_returned></total>` +
			`</user></summary></usage>`))
	})

	It("filters the categories shown, s3select's counters among them", func() {
		put("alice", "plain", at(25, 1), "put_obj", 1)
		b := get("/admin/usage?categories=get_obj&format=xml")
		Expect(b).To(ContainSubstring(`<bucket><bucket>plain</bucket>`))
		Expect(b).To(ContainSubstring(`<owner>alice</owner><categories></categories><s3select></s3select></bucket>`))
		Expect(b).To(ContainSubstring(`<total><bytes_sent>0</bytes_sent><bytes_received>0</bytes_received><ops>0</ops>`))
		b = get("/admin/usage?categories=get_obj;put_obj%20s3select&show-summary=false")
		Expect(b).To(ContainSubstring(`"categories":[`+cat("put_obj", 1)+`],"s3select":{"bytes_processed":0`), "get_str_list's delimiters")
	})

	It("reads a bucket's usage under its owner", func() {
		put("alice", "plain", at(25, 1), "put_obj", 1)
		put("alice", "other", at(25, 1), "put_obj", 1)
		var doc goCephUsage
		Expect(json.Unmarshal([]byte(get("/admin/usage?bucket=plain&uid=bob")), &doc)).To(Succeed())
		Expect(doc.Entries).To(HaveLen(1))
		Expect(doc.Entries[0].Buckets).To(HaveLen(1))
		Expect(doc.Entries[0].Buckets[0].Bucket).To(Equal("plain"))
	})

	It("answers 404 NoSuchKey for a missing bucket and 500 UnknownError for an account's", func() {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			res := fx.request(method, "/admin/usage?bucket=missing", "usager")
			Expect(res.StatusCode).To(Equal(404), method)
			Expect(body(res)).To(ContainSubstring(`"Code":"NoSuchKey"`))
			res = fx.request(method, "/admin/usage?bucket=acctb", "usager")
			Expect(res.StatusCode).To(Equal(500), method)
			Expect(body(res)).To(ContainSubstring(`"Code":"UnknownError"`))
		}
		Expect(usage.TrimUsageCallCount()).To(BeZero())
	})

	Describe("DELETE", func() {
		BeforeEach(func() {
			put("alice", "plain", at(25, 1), "put_obj", 1)
			put("bob", "b", at(25, 17), "put_obj", 1)
		})

		It("refuses to trim everything without remove-all", func() {
			res := fx.request(http.MethodDelete, "/admin/usage", "writer")
			Expect(res.StatusCode).To(Equal(400))
			Expect(body(res)).To(ContainSubstring(`"Code":"InvalidArgument"`))
			Expect(fx.request(http.MethodDelete, "/admin/usage?remove-all=maybe", "writer").StatusCode).To(Equal(400))
			Expect(left()).To(Equal([]string{"alice/plain", "bob/b"}))
		})

		It("trims everything with remove-all, and a following GET shows no entries", func() {
			res := fx.request(http.MethodDelete, "/admin/usage?remove-all=true", "writer")
			Expect(res.StatusCode).To(Equal(200))
			Expect(body(res)).To(BeEmpty())
			Expect(get("/admin/usage")).To(Equal(`{"entries":[],"summary":[]}`))
		})

		It("trims only the uid's usage", func() {
			Expect(fx.request(http.MethodDelete, "/admin/usage?uid=alice", "writer").StatusCode).To(Equal(200))
			Expect(left()).To(Equal([]string{"bob/b"}))
		})

		It("trims a range of every user's usage without remove-all", func() {
			Expect(fx.request(http.MethodDelete, "/admin/usage?start=2026-09-25+16:00:00", "writer").StatusCode).To(Equal(200))
			Expect(left()).To(Equal([]string{"alice/plain"}))
		})

		It("refuses a date it cannot parse with 400, trimming nothing", func() {
			for _, q := range []string{"uid=alice&start=yesterday", "uid=alice&end=never", "remove-all=true&start=2026-09-25T16:00:00%25q"} {
				res := fx.request(http.MethodDelete, "/admin/usage?"+q, "writer")
				Expect(res.StatusCode).To(Equal(400), q)
			}
			Expect(usage.TrimUsageCallCount()).To(BeZero())
			Expect(left()).To(Equal([]string{"alice/plain", "bob/b"}))
		})
	})

	DescribeTable("refuses a caller without radosgw's cap with 403, reading and trimming nothing",
		func(method, query, user string) {
			put("alice", "plain", at(25, 1), "put_obj", 1)
			res := fx.request(method, "/admin/usage"+query, user)
			Expect(res.StatusCode).To(Equal(403), body(res))
			Expect(usage.ReadUsageCallCount()).To(BeZero())
			Expect(usage.TrimUsageCallCount()).To(BeZero())
			Expect(left()).To(Equal([]string{"alice/plain"}))
		},
		Entry("an anonymous show", http.MethodGet, "", ""),
		Entry("a show without caps", http.MethodGet, "", "nocaps"),
		Entry("a show with usage=write alone", http.MethodGet, "", "writer"),
		Entry("a show with other cap types", http.MethodGet, "", "other"),
		Entry("an anonymous trim", http.MethodDelete, "?remove-all=true", ""),
		Entry("a trim without caps", http.MethodDelete, "?remove-all=true", "nocaps"),
		Entry("a trim with usage=read alone", http.MethodDelete, "?remove-all=true", "reader"),
		Entry("a trim with other cap types", http.MethodDelete, "?remove-all=true", "other"),
		Entry("a trim naming a uid with usage=read alone", http.MethodDelete, "?uid=alice", "admin"),
	)

	DescribeTable("admits a caller with radosgw's cap",
		func(method, query, user string) {
			put("alice", "plain", at(25, 1), "put_obj", 1)
			Expect(fx.request(method, "/admin/usage"+query, user).StatusCode).To(Equal(200))
		},
		Entry("a show with usage=read", http.MethodGet, "", "reader"),
		Entry("a trim with usage=write", http.MethodDelete, "?remove-all=true", "writer"),
	)
})
