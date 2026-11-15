package s3

import (
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("setReadExpiration", func() {
	// overKiB is a request for k in a bucket whose one rule expires objects
	// over 1000 bytes a day after their mtime.
	overKiB := func() *op.Request {
		c := lifecycle.Configuration{Rules: []lifecycle.Rule{{
			ID: "rule1", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "1"}, Filter: lifecycle.Filter{SizeGT: "1000"},
		}}}
		e := denc.NewEncoder()
		c.Encode(e, denc.Squid)
		return &op.Request{Object: meta.ObjKey{Name: "k"}, BucketRec: &op.BucketRecord{Attrs: map[string][]byte{op.AttrLC: e.Bytes()}}}
	}

	It("sizes a partNumber read by its whole object, CondState, not by the part it serves", func() {
		mtime := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		part := &op.ObjectState{Size: 1, Mtime: mtime}
		object := &op.ObjectState{Size: 5<<20 + 1, Mtime: mtime}
		h := http.Header{}
		setReadExpiration(h, overKiB(), &op.GetObject{State: part, CondState: object})
		Expect(h).To(HaveKeyWithValue("x-amz-expiration", []string{`expiry-date="Wed, 30 Sep 2026 00:00:00 GMT", rule-id="rule1"`}))

		h = http.Header{}
		small := &op.ObjectState{Size: 1, Mtime: mtime}
		setReadExpiration(h, overKiB(), &op.GetObject{State: small, CondState: small})
		Expect(h).NotTo(HaveKey("x-amz-expiration"), "an object the filter excludes")
	})
})
