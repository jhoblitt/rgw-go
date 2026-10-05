package driver_test

import (
	"bytes"
	"context"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("the head write's cancel", func() {
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		rec   *op.BucketRecord
		shard string
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, nil, time.Now())
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		shard = indexShardOID(rec, "k")
	})
	errno := func(n syscall.Errno) error { return &radosclient.Error{Errno: int32(n), Op: "write"} }

	DescribeTable("is done_cancel: the index entry is canceled unless the write timed out, then the race is judged",
		func(ctx SpecContext, ifMatch, ifNoneMatch string, cause error, wantCancel bool, want error) {
			x := s.NewIndexOpForTest(rec, meta.ObjKey{Name: "k"}, "tag")
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			canceled, err := s.CancelWriteForTest(x, ifMatch, ifNoneMatch, cause)
			if want == nil {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(want))
			}
			Expect(canceled).To(Equal(wantCancel))
			settle(s)
			writes := 1
			if wantCancel {
				writes = 2
				Expect(execIn(c.LastWrite(rookIndexPool, "", shard), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp).Op).To(Equal(rgwcls.OpCancel))
			}
			Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(writes))
		},
		Entry("a replaced head without conditions is success", "", "", errno(syscall.ECANCELED), true, nil),
		Entry("a removed head without conditions is success", "", "", errno(syscall.ENOENT), true, nil),
		Entry("a created head without conditions is success", "", "", errno(syscall.EEXIST), true, nil),
		Entry("another failure is the error", "", "", errno(syscall.EIO), true, op.ErrUnknown),
		Entry("a timeout leaves the entry pending for listing to repair", "", "", errno(syscall.ETIMEDOUT), false, op.ErrRequestTimedOut),
		Entry("If-Match * on a removed head is 412", "*", "", errno(syscall.ENOENT), true, op.ErrPreconditionFailed),
		Entry("If-Match * on a replaced head is success", "*", "", errno(syscall.ECANCELED), true, nil),
		Entry("If-Match etag on a replaced head is the error", `"e"`, "", errno(syscall.ECANCELED), true, op.ErrConcurrentModification),
		Entry("If-None-Match * on a created head is 412", "", "*", errno(syscall.EEXIST), true, op.ErrPreconditionFailed),
		Entry("If-None-Match * on a removed head is success", "", "*", errno(syscall.ENOENT), true, nil),
		Entry("If-None-Match etag on a removed head is NoSuchKey", "", "e", errno(syscall.ENOENT), true, op.ErrNoSuchKey),
	)
})

var _ = Describe("the head write's guard", func() {
	var (
		c       *fakerados.Cluster
		rec     *op.BucketRecord
		key     meta.ObjKey
		headOID string
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		headOID = putBucketID + "_k"
	})
	// seedTagless writes a 6 MiB object, which has a manifest, and drops its
	// write tag, as an object from before write tags has none.
	seedTagless := func(ctx context.Context, s *driver.Store) {
		GinkgoHelper()
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(bytes.Repeat([]byte("o"), 6<<20)), op.PutParams{Size: 6 << 20, Tag: "old"})
		Expect(err).NotTo(HaveOccurred())
		delete(c.Object(testDataPool, "", headOID).Xattrs, meta.AttrIDTag)
	}

	DescribeTable("checks the conditions of a head whose tag radosgw would fake, without the guard",
		func(ctx SpecContext, rel denc.Release) {
			s := openPutStore(ctx, c, rel, nil, time.Now())
			seedTagless(ctx, s)
			_, err := s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Size: 3, Tag: "new", IfNoneMatch: "*"})
			Expect(err).To(MatchError(op.ErrPreconditionFailed), "v19.2.6's need_guard skips this check (rgw_rados.cc:6493-6495), losing the update")
			_, err = s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Size: 3, Tag: "new", IfMatch: md5hex("other")})
			Expect(err).To(MatchError(op.ErrPreconditionFailed), "check_preconditions runs whatever the guard, rgw_rados.cc:3294-3297 at v20.2.4")
			Expect(c.Object(testDataPool, "", headOID).Data).To(HaveLen(4<<20), "the old head stands")
			_, err = s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Size: 3, Tag: "new", IfMatch: "*"})
			Expect(err).NotTo(HaveOccurred())
			Expect(c.LastWrite(testDataPool, "", headOID).Steps()[0]).To(Equal(&radosclient.CreateStep{Exclusive: false}), "a fake tag is never compared")
		},
		Entry("on Squid, which v20.2.4's check governs too (docs/exclusions.md)", denc.Squid),
		Entry("on Tentacle", denc.Tentacle),
	)

	It("guards an If-None-Match: * write by existence alone", func(ctx SpecContext) {
		s := openPutStore(ctx, c, denc.Tentacle, nil, time.Now())
		_, err := s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Size: 3, Tag: "new", IfNoneMatch: "*"})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.LastWrite(testDataPool, "", headOID).Steps()[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}),
			"no cmpxattr for If-None-Match: *, the exclusive create guards")
	})
})
