package driver_test

import (
	"bytes"
	"context"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
	"github.com/jhoblitt/rgw-go/internal/testutil/locktest"
)

// setXattrIn is the value the steps of w set name to, and whether they set it.
func setXattrIn(w fakerados.RecordedWrite, name string) ([]byte, bool) {
	for _, st := range w.Steps() {
		if x, ok := st.(*radosclient.SetXattrStep); ok && x.Name == name {
			return x.Value, true
		}
	}
	return nil, false
}

// The lock specs' settings: the retention a request or an upload names, the
// legal hold an upload names, and the placement of the uploads.
var (
	lockRetention = meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)}
	lockHold      = meta.ObjectLegalHold{Status: "ON"}
	lockPlacement = meta.PlacementRule{Name: "default-placement"}
)

// The lock fixture is the PUT fixture's bucket as CreateBucket makes one with
// object lock, versioned and locked, its rule a day of GOVERNANCE, written
// with instance keys as the ops choose them in a versioned bucket, on a clock
// with nanoseconds.
var _ = Describe("a create write to a bucket with object lock", func() {
	const day, year = 24 * time.Hour, 31556952 * time.Second
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		rec   *op.BucketRecord
		mtime time.Time
		mu    sync.Mutex
		now   time.Time
		body  []byte
	)
	setNow := func(t time.Time) {
		mu.Lock()
		defer mu.Unlock()
		now = t
	}
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	setRule := func(r meta.DefaultRetention) {
		rec.Info.SetObjectLock(meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: r}})
	}
	// undecodable is a lock configuration whose framing passes and whose
	// rule_exist is missing.
	undecodable := func() { rec.Info.ObjLock = meta.RawStruct{1, 1, 1, 0, 0, 0, 1} }
	open := func(ctx context.Context, rel denc.Release) {
		GinkgoHelper()
		var err error
		s, err = driver.Open(ctx, c, conf(nil), driver.Options{Release: &rel})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, clock)
		s.SetRandForTest(fixedRand(putPrefix))
		s.SetReshardWaitForTest(0)
		driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error { return nil })
	}
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 27, 9, 30, 15, 123456789, time.UTC)
		setNow(mtime)
		c = newPutCluster()
		c.SetClock(clock)
		open(ctx, denc.Squid)
		rec = testBucket(putBucketID, 11)
		rec.Info.Flags |= meta.BucketVersioned | meta.BucketObjLockEnabled
		setRule(meta.DefaultRetention{Mode: "GOVERNANCE", Days: 1})
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		body = bytes.Repeat([]byte("0123456789abcdef"), (5<<20)/16)
	})
	AfterEach(func() { settle(s) })

	put := func(ctx context.Context, instance string, data []byte, p op.PutParams) (*op.PutResult, error) {
		if p.Attrs == nil {
			p.Attrs = attrsWithACL(initiator)
		}
		p.Size = int64(len(data))
		p.Tag = "tx-" + instance
		return s.PutObject(ctx, rec, version(instance), bytes.NewReader(data), p)
	}
	mustPut := func(ctx context.Context, instance string, data []byte, p op.PutParams) {
		GinkgoHelper()
		_, err := put(ctx, instance, data, p)
		Expect(err).NotTo(HaveOccurred(), "the put of %q", instance)
		settle(s)
	}
	head := func(instance string) *fakerados.Object {
		return c.Object(testDataPool, "", instanceHeadOID(version(instance)))
	}
	listed := func() []string {
		var out []string
		for _, en := range c.ListEntries(rookIndexPool, "", indexShardOID(rec, "k"), "k") {
			out = append(out, en.Key.Instance)
		}
		return out
	}
	// headWrite is the head write of instance's head: the last write to it
	// that set its manifest, which no OLH step sets.
	headWrite := func(instance string) fakerados.RecordedWrite {
		GinkgoHelper()
		var hw fakerados.RecordedWrite
		found := false
		for _, w := range c.WritesTo(testDataPool, "", instanceHeadOID(version(instance))) {
			if _, ok := setXattrIn(w, meta.AttrManifest); ok {
				hw, found = w, true
			}
		}
		Expect(found).To(BeTrue(), "a head write of %q", instance)
		return hw
	}
	// retention is the retention attr instance's head stores, checked to be
	// the one its head write's own op set.
	retention := func(instance string) []byte {
		GinkgoHelper()
		h := head(instance)
		Expect(h).NotTo(BeNil(), "the head of %q", instance)
		Expect(h.Xattrs).To(HaveKey(meta.AttrObjectRetention), "the head of %q", instance)
		set, ok := setXattrIn(headWrite(instance), meta.AttrObjectRetention)
		Expect(ok).To(BeTrue(), "the head write's op sets the retention, as _do_write_meta's op.setxattr does")
		Expect(set).To(Equal(h.Xattrs[meta.AttrObjectRetention]))
		return h.Xattrs[meta.AttrObjectRetention]
	}
	prefetch := func(ctx context.Context, instance string) *op.ObjectState {
		GinkgoHelper()
		st, err := s.PrefetchObject(ctx, rec, version(instance))
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue())
		return st
	}
	// upload creates an upload of k with p's lock settings and puts one part
	// of n bytes, returning the upload and the part's ETag.
	upload := func(ctx context.Context, p op.UploadParams, n int) (*op.Upload, string) {
		GinkgoHelper()
		p.Owner, p.OwnerName = initiator, "Alice"
		p.Placement = lockPlacement
		p.Attrs = attrsWithACL(initiator)
		up, err := s.CreateUpload(ctx, rec, version(""), p)
		Expect(err).NotTo(HaveOccurred())
		r, err := s.PutPart(ctx, up, 1, bytes.NewReader(bytes.Repeat([]byte("p"), n)), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(n)})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		return up, r.ETag
	}
	// completeInto completes up into version instance, as the op hands the
	// driver the instance it chose.
	completeInto := func(ctx context.Context, up *op.Upload, instance, etag string) error {
		u := *up
		u.Key = version(instance)
		_, err := s.Complete(ctx, &u, []op.CompletePart{{Number: 1, ETag: `"` + etag + `"`}})
		settle(s)
		return err
	}
	complete := func(ctx context.Context, up *op.Upload, instance, etag string) {
		GinkgoHelper()
		Expect(completeInto(ctx, up, instance, etag)).To(Succeed())
	}

	DescribeTable("takes the rule's mode retained until the stamped mtime plus the rule's period, in its head write's op",
		func(ctx SpecContext, write func(context.Context) string) {
			later := mtime.Add(90 * time.Minute)
			setNow(later)
			instance := write(ctx)
			Expect(head(instance).Mtime).To(Equal(later), "the head's mtime is the clock's")
			Expect(retention(instance)).To(Equal(locktest.RetentionAttr("GOVERNANCE", later.Add(day))))
			olh := c.Object(testDataPool, "", olhHeadOID(version(instance)))
			Expect(olh).NotTo(BeNil(), "the key's OLH head")
			Expect(olh.Xattrs).NotTo(HaveKey(meta.AttrObjectRetention), "the OLH head, which only the link writes, takes none")
		},
		Entry("PutObject, the atomic processor's PUT_OBJ_CREATE", func(ctx context.Context) string {
			mustPut(ctx, "v1", body, op.PutParams{})
			return "v1"
		}),
		Entry("a copy that shares its source's tails, copy_obj's PUT_OBJ_CREATE", func(ctx context.Context) string {
			mustPut(ctx, "v0", body, op.PutParams{})
			src := prefetch(ctx, "v0")
			Expect(src.Manifest.HasTail()).To(BeTrue(), "a source whose tails a copy shares")
			_, err := s.CopyObject(ctx, src, rec, version("v1"), op.CopyParams{Attrs: attrsWithACL(initiator), Tag: "tx-copy"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			m := meta.DecodeManifest(denc.NewDecoder(head("v1").Xattrs[meta.AttrManifest]))
			Expect(m.Prefix).To(Equal(src.Manifest.Prefix), "the copy names its source's tails")
			return "v1"
		}),
		Entry("a copy that streams its source into another pool, copy_obj_data's processor", func(ctx context.Context) string {
			mustPut(ctx, "v0", body, op.PutParams{})
			_, err := s.CopyObject(ctx, prefetch(ctx, "v0"), rec, version("v1"), op.CopyParams{Attrs: attrsWithACL(initiator), StorageClass: "COLD", Tag: "tx-copy"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(head("v1").Xattrs).To(HaveKeyWithValue(meta.AttrStorageClass, []byte("COLD")), "the streamed copy")
			return "v1"
		}),
		Entry("CompleteMultipartUpload's PUT_OBJ_CREATE", func(ctx context.Context) string {
			up, etag := upload(ctx, op.UploadParams{}, 1024)
			complete(ctx, up, "v1", etag)
			return "v1"
		}),
	)

	It("gives a null version written through writeNullVersion the default on the key's own head", func(ctx SpecContext) {
		mustPut(ctx, "v1", body, op.PutParams{})
		// A lock bucket's versioning cannot be suspended over S3; the
		// suspended record stands for the null write PUT_OBJ_CREATE covers too.
		rec.Info.Flags |= meta.BucketVersionsSuspended
		later := mtime.Add(time.Hour)
		setNow(later)
		mustPut(ctx, "", []byte("null"), op.PutParams{IfMatch: md5hex(body)})
		Expect(head("").Data).To(Equal([]byte("null")), "the null version's data on the key's own head")
		steps := headWrite("").Steps()
		Expect(initsOLHSteps(steps) && writesDataSteps(steps)).To(BeTrue(), "the conditional null write's head write, which inits its modification")
		Expect(retention("")).To(Equal(locktest.RetentionAttr("GOVERNANCE", later.Add(day))))
	})

	It("gives a copy the default from its own mtime, not its source's retention", func(ctx SpecContext) {
		mustPut(ctx, "v0", body, op.PutParams{})
		Expect(retention("v0")).To(Equal(locktest.RetentionAttr("GOVERNANCE", mtime.Add(day))))
		later := mtime.Add(3 * day)
		setNow(later)
		_, err := s.CopyObject(ctx, prefetch(ctx, "v0"), rec, version("v1"), op.CopyParams{Attrs: attrsWithACL(initiator), Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(retention("v1")).To(Equal(locktest.RetentionAttr("GOVERNANCE", later.Add(day))),
			"copy_obj drops the source's retention, rgw_rados.cc:4800-4801 at v19.2.7")
	})

	It("takes the date from the mtime the write is given, not the clock", func(ctx SpecContext) {
		given := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
		mustPut(ctx, "v1", body, op.PutParams{Mtime: given})
		Expect(head("v1").Mtime).To(Equal(given))
		Expect(retention("v1")).To(Equal(locktest.RetentionAttr("GOVERNANCE", given.Add(day))))
	})

	DescribeTable("adds the rule's period in its unit, the release's encoding alike",
		func(ctx SpecContext, rel denc.Release, r meta.DefaultRetention, want time.Time) {
			open(ctx, rel)
			setRule(r)
			mustPut(ctx, "v1", body[:1024], op.PutParams{})
			Expect(retention("v1")).To(Equal(locktest.RetentionAttr(r.Mode, want)))
		},
		Entry("a day of GOVERNANCE on Squid", denc.Squid, meta.DefaultRetention{Mode: "GOVERNANCE", Days: 1},
			time.Date(2026, 10, 28, 9, 30, 15, 123456789, time.UTC)),
		Entry("30 days of COMPLIANCE on Tentacle", denc.Tentacle, meta.DefaultRetention{Mode: "COMPLIANCE", Days: 30},
			time.Date(2026, 11, 26, 9, 30, 15, 123456789, time.UTC)),
		Entry("2 years of COMPLIANCE, std::chrono::years of 365.2425 days", denc.Squid, meta.DefaultRetention{Mode: "COMPLIANCE", Years: 2},
			time.Date(2026, 10, 27, 9, 30, 15, 123456789, time.UTC).Add(2*year)),
		Entry("600 years, clamped to real_time's last instant", denc.Tentacle, meta.DefaultRetention{Mode: "COMPLIANCE", Years: 600},
			meta.MaxRealTime),
	)

	Describe("writes no retention", func() {
		It("on a part head or the meta object, which are no PUT_OBJ_CREATE", func(ctx SpecContext) {
			up, _ := upload(ctx, op.UploadParams{}, 1024)
			mo := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
			Expect(mo).NotTo(BeNil())
			Expect(mo.Xattrs).NotTo(HaveKey(meta.AttrObjectRetention), "the meta object's PUT_OBJ_CREATE_EXCL")
			ph := c.Object(testDataPool, "", partHeadOID("k."+up.ID, 1))
			Expect(ph).NotTo(BeNil())
			Expect(ph.Xattrs).NotTo(HaveKey(meta.AttrObjectRetention), "a part head sets no flag")
		})
		It("for a lock without a rule, RGWObjectLock()", func(ctx SpecContext) {
			rec.Info.ObjLock = nil
			mustPut(ctx, "v1", body, op.PutParams{})
			Expect(head("v1").Xattrs).NotTo(HaveKey(meta.AttrObjectRetention))
		})
		It("for a rule on a record whose flags do not enable object lock", func(ctx SpecContext) {
			rec.Info.Flags &^= meta.BucketObjLockEnabled
			Expect(rec.Info.ObjLock).NotTo(BeEmpty(), "the rule is still stored")
			mustPut(ctx, "v1", body, op.PutParams{})
			Expect(head("v1").Xattrs).NotTo(HaveKey(meta.AttrObjectRetention))
		})
	})

	Describe("keeps a retention the write already carries", func() {
		header := func() []byte { return locktest.RetentionAttr(lockRetention.Mode, lockRetention.RetainUntil) }
		It("from PutObject's request", func(ctx SpecContext) {
			attrs := attrsWithACL(initiator)
			attrs[meta.AttrObjectRetention] = header()
			mustPut(ctx, "v1", body, op.PutParams{Attrs: attrs})
			Expect(retention("v1")).To(Equal(header()))
		})
		It("from CopyObject's request", func(ctx SpecContext) {
			mustPut(ctx, "v0", body, op.PutParams{})
			attrs := attrsWithACL(initiator)
			attrs[meta.AttrObjectRetention] = header()
			_, err := s.CopyObject(ctx, prefetch(ctx, "v0"), rec, version("v1"), op.CopyParams{Attrs: attrs, Tag: "tx-copy"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(retention("v1")).To(Equal(header()))
		})
		It("from the upload a completion completes", func(ctx SpecContext) {
			up, etag := upload(ctx, op.UploadParams{Retention: new(lockRetention)}, 1024)
			complete(ctx, up, "v1", etag)
			Expect(retention("v1")).To(Equal(header()), "the upload's, rgw_sal_rados.cc:3601-3605 at v19.2.7")
		})
		It("but takes the default over an empty one, which the head write would skip", func(ctx SpecContext) {
			attrs := attrsWithACL(initiator)
			attrs[meta.AttrObjectRetention] = []byte{}
			mustPut(ctx, "v1", body, op.PutParams{Attrs: attrs})
			Expect(retention("v1")).To(Equal(locktest.RetentionAttr("GOVERNANCE", mtime.Add(day))))
			Expect(attrs[meta.AttrObjectRetention]).To(BeEmpty(), "the caller's attrs are not changed")
		})
		It("and takes the default beside an upload's legal hold alone", func(ctx SpecContext) {
			up, etag := upload(ctx, op.UploadParams{LegalHold: new(lockHold)}, 1024)
			complete(ctx, up, "v1", etag)
			Expect(head("v1").Xattrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, locktest.LegalHoldAttr("ON")))
			Expect(retention("v1")).To(Equal(locktest.RetentionAttr("GOVERNANCE", mtime.Add(day))))
		})
	})

	Describe("fails with UnknownError and writes no head when the lock configuration does not decode", func() {
		It("for PutObject, deleting the tails it wrote", func(ctx SpecContext) {
			undecodable()
			_, err := put(ctx, "v1", body, op.PutParams{})
			Expect(err).To(MatchError(op.ErrUnknown), "radosgw's -EIO for a bucket whose lock does not decode")
			settle(s)
			Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty(), "no head, and the write's tails deleted")
			Expect(listed()).To(BeEmpty(), "no index entry")
		})
		It("for CopyObject, dropping the references it took", func(ctx SpecContext) {
			mustPut(ctx, "v0", body, op.PutParams{})
			src := prefetch(ctx, "v0")
			undecodable()
			_, err := s.CopyObject(ctx, src, rec, version("v1"), op.CopyParams{Attrs: attrsWithACL(initiator), Tag: "tx-copy"})
			Expect(err).To(MatchError(op.ErrUnknown))
			settle(s)
			Expect(head("v1")).To(BeNil())
			Expect(listed()).To(Equal([]string{"v0"}), "no index entry for the copy")
			referenced := 0
			for _, oid := range poolObjects(ctx, c, testDataPool, "") {
				b, ok := c.Object(testDataPool, "", oid).Xattrs[refcount.XattrName]
				if !ok {
					continue
				}
				referenced++
				Expect(refcount.DecodeRefcount(denc.NewDecoder(b)).Refs).NotTo(HaveKey("tx-copy\x00"), "tail %s", oid)
			}
			Expect(referenced).To(BeNumerically(">", 0), "the copy took its references before its head write")
		})
		It("for CompleteMultipartUpload, clearing its record and keeping the upload", func(ctx SpecContext) {
			up, etag := upload(ctx, op.UploadParams{}, 1024)
			undecodable()
			Expect(completeInto(ctx, up, "v1", etag)).To(MatchError(op.ErrUnknown))
			Expect(head("v1")).To(BeNil())
			Expect(listed()).To(BeEmpty(), "no index entry")
			mo := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
			Expect(mo).NotTo(BeNil(), "the upload stays for a retry")
			Expect(mo.Xattrs).NotTo(HaveKey("user.rgw.mp_completion_tag"), "the completion's record is cleared")
		})
	})

	It("refuses a completion whose upload info does not decode, where radosgw completes it without the upload's lock", func(ctx SpecContext) {
		up, etag := upload(ctx, op.UploadParams{Retention: new(lockRetention), LegalHold: new(lockHold)}, 1024)
		mo := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
		mo.Data = []byte{0xff, 0xff, 0xff}
		Expect(completeInto(ctx, up, "v1", etag)).To(MatchError(op.ErrUnknown), "get_info's -EIO, which radosgw's complete does not check")
		Expect(head("v1")).To(BeNil(), "no object without the upload's retention and legal hold")
		Expect(listed()).To(BeEmpty(), "no index entry")
		Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).NotTo(BeNil(), "the upload stays")
	})
})

var _ = Describe("CreateUpload's object lock settings", func() {
	var (
		c   *fakerados.Cluster
		s   *driver.Store
		rec *op.BucketRecord
	)
	BeforeEach(func() {
		c = newPutCluster()
		rec = testBucket(putBucketID, 11)
		rec.Info.Flags |= meta.BucketVersioned | meta.BucketObjLockEnabled
		seedShards(c, rec)
		seedRecordInstance(c, rec)
	})
	AfterEach(func() { settle(s) })

	DescribeTable("are multipart_upload_info's four fields",
		func(ctx SpecContext, rel denc.Release, p op.UploadParams, want meta.MultipartUploadInfo) {
			now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
			c.SetClock(func() time.Time { return now })
			s = openPutStore(ctx, c, rel, nil, now)
			p.Owner, p.Placement, p.Attrs = initiator, lockPlacement, attrsWithACL(initiator)
			up, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: "k"}, p)
			Expect(err).NotTo(HaveOccurred())
			mo := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
			Expect(mo).NotTo(BeNil())
			e := denc.NewEncoder()
			want.Encode(e, rel)
			Expect(mo.Data).To(Equal(e.Bytes()))
			Expect(mo.Xattrs).NotTo(HaveKey(meta.AttrObjectRetention), "the settings are the info's, not the meta object's attrs")
			Expect(mo.Xattrs).NotTo(HaveKey(meta.AttrObjectLegalHold))
		},
		Entry("none on Squid", denc.Squid, op.UploadParams{}, meta.MultipartUploadInfo{DestPlacement: lockPlacement}),
		Entry("both on Squid", denc.Squid, op.UploadParams{Retention: new(lockRetention), LegalHold: new(lockHold)},
			meta.MultipartUploadInfo{DestPlacement: lockPlacement, Retention: new(lockRetention), LegalHold: new(lockHold)}),
		Entry("a retention alone on Tentacle", denc.Tentacle, op.UploadParams{Retention: new(lockRetention)},
			meta.MultipartUploadInfo{DestPlacement: lockPlacement, Retention: new(lockRetention)}),
		Entry("a legal hold alone on Tentacle", denc.Tentacle, op.UploadParams{LegalHold: new(lockHold)},
			meta.MultipartUploadInfo{DestPlacement: lockPlacement, LegalHold: new(lockHold)}),
	)
})
