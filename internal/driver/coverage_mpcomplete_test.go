package driver_test

import (
	"bytes"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// This pins docs/differences.md, "A CompleteMultipartUpload records itself
// on the meta object before its head write": a completion copies the meta
// object's attrs onto the head, as radosgw's does, but strips the record from
// them, where a floor radosgw carries it onto the head.
var _ = Describe("a completion of an upload whose meta object carries a record's instance", func() {
	It("leaves the record off the head it writes", func(ctx SpecContext) {
		mtime := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c := newPutCluster()
		s := openPutStore(ctx, c, denc.Squid, nil, mtime)
		rec := testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key := meta.ObjKey{Name: "k"}

		attrs := attrsWithACL(initiator)
		up, err := s.CreateUpload(ctx, rec, key, op.UploadParams{
			Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrs,
		})
		Expect(err).NotTo(HaveOccurred())
		body := bytes.Repeat([]byte{'a'}, 5<<20)
		part, err := s.PutPart(ctx, up, 1, bytes.NewReader(body), op.PutParams{
			Attrs: map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(initiator, "Alice"))}, Size: int64(len(body)),
		})
		Expect(err).NotTo(HaveOccurred())
		settle(s)

		mo := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
		Expect(mo).NotTo(BeNil())
		mo.Xattrs["user.rgw.mp_completion_instance"] = []byte("null")

		up.WriteTag = "tx-complete"
		_, err = s.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: `"` + part.ETag + `"`}})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		head := c.Object(testDataPool, "", putBucketID+"_k")
		Expect(head).NotTo(BeNil())
		Expect(head.Xattrs).To(HaveKey(meta.AttrACL), "the meta object's attrs are copied")
		Expect(head.Xattrs).NotTo(HaveKey("user.rgw.mp_completion_instance"), "the record is stripped from the copy")
		Expect(head.Xattrs).NotTo(HaveKey("user.rgw.mp_completion_tag"))
	})
})
