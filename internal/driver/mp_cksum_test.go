package driver_test

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // SHA1 is a checksum type S3 defines
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"hash/crc32"
	"hash/crc64"
	"slices"
	"strconv"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// crc64NVMETable is CRC-64/NVME's reflected polynomial, which hash/crc64 runs
// with the catalog's all-ones init and xorout.
var crc64NVMETable = crc64.MakeTable(0x9a6c9329ac4bc9b5)

// digestOf is t's digest of b as a record stores it, a CRC big-endian,
// computed without rgw-go's cksum package.
func digestOf(t cksum.Type, b []byte) []byte {
	GinkgoHelper()
	switch t {
	case cksum.CRC32:
		return be32(crc32.ChecksumIEEE(b))
	case cksum.CRC32C:
		return be32(crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)))
	case cksum.CRC64NVME:
		return be64(crc64.Checksum(b, crc64NVMETable))
	case cksum.SHA1:
		s := sha1.Sum(b) //nolint:gosec // SHA1 is a checksum type S3 defines
		return s[:]
	case cksum.SHA256:
		s := sha256.Sum256(b)
		return s[:]
	case cksum.SHA512:
		s := sha512.Sum512(b)
		return s[:]
	}
	Fail("no digest of type " + t.Info().Name)
	return nil
}

// combinedOf is the record a completion of parts stores for an upload of
// type t created with flags: a CRC's full-object CRC unless the flags say
// COMPOSITE, else the type's digest over the parts' digests in order.
func combinedOf(t cksum.Type, flags uint16, parts ...[]byte) cksum.Cksum {
	GinkgoHelper()
	if t == cksum.CRC32 || t == cksum.CRC32C || t == cksum.CRC64NVME {
		if flags&cksum.FlagComposite == 0 {
			return cksum.Cksum{Type: t, Digest: digestOf(t, bytes.Join(parts, nil)), Flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagFullObject}
		}
	}
	var digests []byte
	for _, p := range parts {
		digests = append(digests, digestOf(t, p)...)
	}
	return cksum.Cksum{Type: t, Digest: digestOf(t, digests), Flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagComposite}
}

func armorOf(c cksum.Cksum) string { return base64.StdEncoding.EncodeToString(c.Digest) }

// supplying is a completion's supplied checksum of type t and value v.
func supplying(t cksum.Type, v string) *op.ChecksumRequest {
	return &op.ChecksumRequest{Type: t, Expected: func() (string, bool) { return v, true }}
}

// encodeTentacle renders a meta type as a Tentacle radosgw stores it.
func encodeTentacle(v encoder) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Tentacle)
	return e.Bytes()
}

var _ = Describe("a multipart upload's checksum", func() {
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		key     meta.ObjKey
		headOID string
		shard   string
		stores  []*driver.Store
		mtime   time.Time
		// first and second are parts above the specs' minimum part size,
		// last one below it.
		first, second, last []byte
	)
	// open opens a store at rel over the spec's cluster, its minimum part size
	// 64 bytes so that the specs' parts stay small.
	open := func(ctx context.Context, rel denc.Release) *driver.Store {
		GinkgoHelper()
		st := openPutStore(ctx, c, rel, map[string]string{"rgw_multipart_min_part_size": "64"}, mtime)
		stores = append(stores, st)
		return st
	}
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		c.SetRequiredOSDRelease("tentacle")
		c.SetClock(func() time.Time { return mtime })
		stores = nil
		s = open(ctx, denc.Tentacle)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		headOID = putBucketID + "_k"
		shard = indexShardOID(rec, "k")
		first = bytes.Repeat([]byte("first part "), 10)
		second = bytes.Repeat([]byte("the second part "), 7)
		last = []byte("last")
	})
	AfterEach(func() {
		for _, st := range stores {
			settle(st)
		}
		expectReadableChecksums(c)
	})

	newUpload := func(ctx context.Context, st *driver.Store, uc op.UploadChecksum) *op.Upload {
		GinkgoHelper()
		up, err := st.CreateUpload(ctx, rec, key, op.UploadParams{
			Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator), Checksum: uc,
		})
		Expect(err).NotTo(HaveOccurred())
		return up
	}
	// putPart puts body as part n of up through st with req and returns its
	// ETag.
	putPart := func(ctx context.Context, st *driver.Store, up *op.Upload, n int, body []byte, req *op.ChecksumRequest) string {
		GinkgoHelper()
		r, err := st.PutPart(ctx, up, n, bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body)), Checksum: req})
		Expect(err).NotTo(HaveOccurred(), "putting part %d", n)
		return r.ETag
	}
	// putParts puts each body as part i+1 of up, computing up's type over it
	// with up's flags, as the op layer asks for a part naming no algorithm.
	putParts := func(ctx context.Context, st *driver.Store, up *op.Upload, bodies ...[]byte) []string {
		GinkgoHelper()
		var req *op.ChecksumRequest
		if up.Checksum.Type != cksum.None {
			req = &op.ChecksumRequest{Type: up.Checksum.Type, Flags: up.Checksum.Flags}
		}
		etags := make([]string, len(bodies))
		for i, b := range bodies {
			etags[i] = putPart(ctx, st, up, i+1, b, req)
		}
		settle(st)
		return etags
	}
	completeParts := func(etags []string) []op.CompletePart {
		out := make([]op.CompletePart, len(etags))
		for i, e := range etags {
			out[i] = op.CompletePart{Number: i + 1, ETag: e}
		}
		return out
	}
	// nothingWritten asserts that a refused completion of up wrote no head,
	// no record, no index change and no GC entry, and released its lock;
	// the counters were reset before it.
	nothingWritten := func(up *op.Upload) {
		GinkgoHelper()
		settle(s)
		Expect(c.Object(testDataPool, "", headOID)).To(BeNil(), "no head")
		mOID := metaOID(rec, "k", up.ID)
		Expect(execMethods(c.WritesTo(extraPoolName, "", mOID))).To(Equal([]string{"lock", "unlock"}), "no completion record")
		Expect(c.Locks(extraPoolName, "", mOID, "RGWCompleteMultipart")).To(BeEmpty())
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "no index change")
		Expect(allGCEntries(c)).To(BeEmpty(), "nothing queued for the GC")
	}
	headChecksum := func() []byte {
		GinkgoHelper()
		head := c.Object(testDataPool, "", headOID)
		Expect(head).NotTo(BeNil())
		return head.Xattrs[meta.AttrCksum]
	}

	It("computes the specs' digests as the CRC catalog and the SHA standards do", func() {
		for t, want := range check9Digests() {
			Expect(digestOf(t, check9)).To(Equal(want), t.Info().Name)
		}
	})

	DescribeTable("combines the checksums of the parts a completion names into the head's user.rgw.cksum and the result",
		func(ctx SpecContext, uc op.UploadChecksum, parts int) {
			bodies := [][]byte{first, second, last}
			if parts == 2 {
				bodies = [][]byte{first, last}
			}
			up := newUpload(ctx, s, uc)
			etags := putParts(ctx, s, up, bodies...)
			want := combinedOf(uc.Type, uc.Flags, bodies...)
			value := armorOf(want)
			if want.Composite() {
				value += "-" + strconv.Itoa(parts)
			}
			up.SuppliedChecksum = supplying(uc.Type, value)
			res, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred(), "the armored value, with -<parts named> for a composite")
			Expect(res.Checksum).To(Equal(&want), "the result")
			Expect(headChecksum()).To(Equal(cksumRecord(want.Type, want.Digest, want.Flags)), "the head's record, as a Tentacle radosgw encodes it")
			st, err := s.StatObject(ctx, rec, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Checksum).To(Equal(&want), "the stat")
		},
		Entry("two CRC32 parts, full-object", op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagFullObject}, 2),
		Entry("three CRC32 parts, full-object", op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagFullObject}, 3),
		Entry("two CRC32C parts, full-object", op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}, 2),
		Entry("three CRC32C parts, full-object", op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}, 3),
		Entry("two CRC64NVME parts, full-object", op.UploadChecksum{Type: cksum.CRC64NVME, Flags: cksum.FlagFullObject}, 2),
		Entry("three CRC64NVME parts, full-object", op.UploadChecksum{Type: cksum.CRC64NVME, Flags: cksum.FlagFullObject}, 3),
		Entry("two CRC32 parts, composite", op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagComposite}, 2),
		Entry("three CRC32C parts, composite", op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagComposite}, 3),
		Entry("two SHA1 parts, composite", op.UploadChecksum{Type: cksum.SHA1, Flags: cksum.FlagComposite}, 2),
		Entry("three SHA1 parts, composite", op.UploadChecksum{Type: cksum.SHA1, Flags: cksum.FlagComposite}, 3),
		Entry("two SHA256 parts, composite", op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}, 2),
		Entry("three SHA256 parts, composite", op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}, 3),
		Entry("two SHA512 parts, composite", op.UploadChecksum{Type: cksum.SHA512, Flags: cksum.FlagComposite}, 2),
		Entry("three SHA512 parts, composite", op.UploadChecksum{Type: cksum.SHA512, Flags: cksum.FlagComposite}, 3),
	)

	It("combines a compressed upload's CRCs over each part's plaintext length, its accounted size", func(ctx SpecContext) {
		withCompressionClasses(c, compression.Zlib)
		st := open(ctx, denc.Tentacle)
		uc := op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}
		up, err := st.CreateUpload(ctx, rec, key, op.UploadParams{
			Owner: initiator, Placement: meta.PlacementRule{Name: "default-placement", StorageClass: compClass(compression.Zlib)},
			Attrs: attrsWithACL(initiator), Checksum: uc,
		})
		Expect(err).NotTo(HaveOccurred())
		bodies := [][]byte{bytes.Repeat([]byte("a"), 64<<10), bytes.Repeat([]byte("b"), 32<<10)}
		etags := putParts(ctx, st, up, bodies...)
		p1 := storedPart(c, metaOID(rec, "k", up.ID), meta.MultipartPartKey(1))
		Expect(p1.Size).To(BeNumerically("<", p1.AccountedSize), "the part is stored compressed")
		res, err := st.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Checksum).To(Equal(new(combinedOf(uc.Type, uc.Flags, bodies...))))
	})

	It("combines part records as a Tentacle radosgw writes them, at the upload's flags without V2", func(ctx SpecContext) {
		uc := op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagFullObject}
		up := newUpload(ctx, s, uc)
		bodies := [][]byte{first, last}
		etags := make([]string, len(bodies))
		mOID := metaOID(rec, "k", up.ID)
		for i, b := range bodies {
			etags[i] = putPart(ctx, s, up, i+1, b, nil)
			k := meta.MultipartPartKey(uint32(i + 1)) //nolint:gosec // a small part number
			info := storedPart(c, mOID, k)
			Expect(info.Cksum).To(BeNil(), "rgw-go stored no checksum")
			info.Cksum = slices.Concat([]byte{1}, cksumRecord(cksum.CRC32, digestOf(cksum.CRC32, b), cksum.FlagFullObject))
			c.Object(extraPoolName, "", mOID).Omap[k] = encodeTentacle(info)
		}
		settle(s)
		res, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		want := combinedOf(cksum.CRC32, cksum.FlagFullObject, bodies...)
		Expect(res.Checksum).To(Equal(&want))
		Expect(headChecksum()).To(Equal(cksumRecord(cksum.CRC32, want.Digest, want.Flags)))
	})

	DescribeTable("compares the whole supplied value, of the upload's type, before anything is written",
		func(ctx SpecContext, uc op.UploadChecksum, supply func(combined cksum.Cksum) *op.ChecksumRequest, wantErr error) {
			up := newUpload(ctx, s, uc)
			etags := putParts(ctx, s, up, first, last)
			want := combinedOf(uc.Type, uc.Flags, first, last)
			up.SuppliedChecksum = supply(want)
			c.ResetCounters()
			res, err := s.Complete(ctx, up, completeParts(etags))
			if wantErr == nil {
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Checksum).To(Equal(&want))
				return
			}
			Expect(err).To(MatchError(wantErr))
			nothingWritten(up)
		},
		Entry("a composite's armor without its part count, as radosgw accepts it for minio's clients",
			op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite},
			func(w cksum.Cksum) *op.ChecksumRequest { return supplying(cksum.SHA256, armorOf(w)) }, nil),
		Entry("a composite's armor with another part count, which radosgw's prefix match accepts",
			op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite},
			func(w cksum.Cksum) *op.ChecksumRequest { return supplying(cksum.SHA256, armorOf(w)+"-3") }, op.ErrBadDigest),
		Entry("a composite's armored value with a trailing byte, which radosgw's prefix match accepts",
			op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite},
			func(w cksum.Cksum) *op.ChecksumRequest { return supplying(cksum.SHA256, armorOf(w)+"-2=") }, op.ErrBadDigest),
		Entry("a full-object CRC with a trailing byte, which radosgw's prefix match accepts",
			op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject},
			func(w cksum.Cksum) *op.ChecksumRequest { return supplying(cksum.CRC32C, armorOf(w)+"A") }, op.ErrBadDigest),
		Entry("a full-object CRC with a part count, which radosgw's prefix match accepts",
			op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject},
			func(w cksum.Cksum) *op.ChecksumRequest { return supplying(cksum.CRC32C, armorOf(w)+"-2") }, op.ErrBadDigest),
		Entry("a CRC64NVME upload's combination, which radosgw never reads",
			op.UploadChecksum{Type: cksum.CRC64NVME, Flags: cksum.FlagFullObject},
			func(w cksum.Cksum) *op.ChecksumRequest { return supplying(cksum.CRC64NVME, armorOf(w)) }, nil),
		Entry("a CRC64NVME value that is not the combination, which radosgw never reads",
			op.UploadChecksum{Type: cksum.CRC64NVME, Flags: cksum.FlagFullObject},
			func(cksum.Cksum) *op.ChecksumRequest {
				return supplying(cksum.CRC64NVME, armorOf(combinedOf(cksum.CRC64NVME, cksum.FlagFullObject, []byte("other"))))
			}, op.ErrBadDigest),
		Entry("an empty value, which radosgw does not compare",
			op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite},
			func(cksum.Cksum) *op.ChecksumRequest { return supplying(cksum.SHA256, "") }, op.ErrBadDigest),
		Entry("the combination under another type's header, which radosgw compares as the upload's",
			op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject},
			func(w cksum.Cksum) *op.ChecksumRequest { return supplying(cksum.CRC32, armorOf(w)) }, op.ErrBadDigest),
		Entry("no value",
			op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject},
			func(cksum.Cksum) *op.ChecksumRequest {
				return &op.ChecksumRequest{Type: cksum.CRC32C, Expected: func() (string, bool) { return "not a checksum", false }}
			}, nil),
	)

	DescribeTable("refuses a named part without a checksum, of another type or whose checksum does not decode with InvalidRequest, writing nothing",
		func(ctx SpecContext, second func(up *op.Upload) *op.ChecksumRequest, mangle func(info *meta.UploadPartInfo)) {
			uc := op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}
			up := newUpload(ctx, s, uc)
			e1 := putPart(ctx, s, up, 1, first, &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags})
			e2 := putPart(ctx, s, up, 2, last, second(up))
			if mangle != nil {
				mOID := metaOID(rec, "k", up.ID)
				info := storedPart(c, mOID, meta.MultipartPartKey(2))
				mangle(&info)
				c.Object(extraPoolName, "", mOID).Omap[meta.MultipartPartKey(2)] = encodeTentacle(info)
			}
			settle(s)
			c.ResetCounters()
			_, err := s.Complete(ctx, up, completeParts([]string{e1, e2}))
			Expect(err).To(MatchError(op.ErrInvalidRequest))
			nothingWritten(up)
		},
		Entry("a part without a checksum (rgw_op.cc:7108-7114 at v20.2.4)", func(*op.Upload) *op.ChecksumRequest { return nil }, nil),
		Entry("a part of another type (rgw_op.cc:7121-7131 at v20.2.4)", func(up *op.Upload) *op.ChecksumRequest {
			return &op.ChecksumRequest{Type: cksum.SHA256, Flags: up.Checksum.Flags}
		}, nil),
		Entry("a part whose record holds a type with no Tentacle id", func(up *op.Upload) *op.ChecksumRequest {
			return &op.ChecksumRequest{Type: up.Checksum.Type, Flags: up.Checksum.Flags}
		}, func(info *meta.UploadPartInfo) {
			info.Cksum = slices.Concat([]byte{1}, cksumRecord(9, make([]byte, 16), cksum.FlagFullObject))
		}),
	)

	DescribeTable("answers InvalidPart for an upload holding a part the completion leaves out, whatever that part's checksum, writing nothing",
		func(ctx SpecContext, third func(up *op.Upload) *op.ChecksumRequest, supplied bool) {
			uc := op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}
			up := newUpload(ctx, s, uc)
			req := &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags}
			e1 := putPart(ctx, s, up, 1, first, req)
			e2 := putPart(ctx, s, up, 2, second, req)
			putPart(ctx, s, up, 3, last, third(up))
			if supplied {
				up.SuppliedChecksum = supplying(uc.Type, armorOf(combinedOf(uc.Type, uc.Flags, first, second)))
			}
			settle(s)
			c.ResetCounters()
			_, err := s.Complete(ctx, up, completeParts([]string{e1, e2}))
			Expect(err).To(MatchError(op.ErrInvalidPart))
			nothingWritten(up)
		},
		Entry("a part without a checksum, which radosgw's last page holds, answering InvalidRequest",
			func(*op.Upload) *op.ChecksumRequest { return nil }, false),
		Entry("a part of another type, which radosgw's last page holds, answering InvalidRequest",
			func(up *op.Upload) *op.ChecksumRequest {
				return &op.ChecksumRequest{Type: cksum.SHA1, Flags: up.Checksum.Flags}
			}, false),
		Entry("a part radosgw combines alone, answering BadDigest to the value the named parts make",
			func(up *op.Upload) *op.ChecksumRequest {
				return &op.ChecksumRequest{Type: up.Checksum.Type, Flags: up.Checksum.Flags}
			}, true),
	)

	DescribeTable("answers a part fault before a checksum fault, writing nothing",
		func(ctx SpecContext, fault func(ctx context.Context, up *op.Upload) []op.CompletePart, want error) {
			uc := op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}
			up := newUpload(ctx, s, uc)
			parts := fault(ctx, up)
			settle(s)
			c.ResetCounters()
			_, err := s.Complete(ctx, up, parts)
			Expect(err).To(MatchError(want))
			nothingWritten(up)
		},
		Entry("EntityTooSmall beside a part without a checksum", func(ctx context.Context, up *op.Upload) []op.CompletePart {
			return completeParts([]string{putPart(ctx, s, up, 1, last, nil), putPart(ctx, s, up, 2, first, nil)})
		}, op.ErrEntityTooSmall),
		Entry("InvalidPart for a wrong ETag beside a supplied value that is not the combination", func(ctx context.Context, up *op.Upload) []op.CompletePart {
			etags := putParts(ctx, s, up, first, last)
			up.SuppliedChecksum = supplying(cksum.SHA256, "AAAA")
			return completeParts([]string{etags[0], md5hex("other")})
		}, op.ErrInvalidPart),
		Entry("InvalidPart for a part whose head is gone beside a part of another type", func(ctx context.Context, up *op.Upload) []op.CompletePart {
			e1 := putPart(ctx, s, up, 1, first, &op.ChecksumRequest{Type: cksum.SHA1, Flags: up.Checksum.Flags})
			e2 := putPart(ctx, s, up, 2, last, &op.ChecksumRequest{Type: up.Checksum.Type, Flags: up.Checksum.Flags})
			c.Remove(testDataPool, "", partHeadOID("k."+up.ID, 2))
			return completeParts([]string{e1, e2})
		}, op.ErrInvalidPart),
	)

	It("keeps no upload checksum type on Squid, and combines and compares nothing", func(ctx SpecContext) {
		c.SetRequiredOSDRelease("squid")
		sq := open(ctx, denc.Squid)
		up := newUpload(ctx, sq, op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject})
		Expect(up.Checksum).To(BeZero())
		data := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID)).Data
		Expect(data[0]).To(Equal(byte(2)), "Squid's version 2 info")
		e1 := putPart(ctx, sq, up, 1, first, &op.ChecksumRequest{Type: cksum.CRC32C})
		e2 := putPart(ctx, sq, up, 2, last, &op.ChecksumRequest{Type: cksum.CRC32C})
		Expect(storedPart(c, metaOID(rec, "k", up.ID), meta.MultipartPartKey(1)).Cksum).To(BeNil(), "Squid's part record holds none")
		settle(sq)
		up.SuppliedChecksum = supplying(cksum.CRC32C, "not a checksum")
		res, err := sq.Complete(ctx, up, completeParts([]string{e1, e2}))
		Expect(err).NotTo(HaveOccurred(), "the supplied value is not compared")
		Expect(res.Checksum).To(BeNil())
		Expect(c.Object(testDataPool, "", headOID).Xattrs).NotTo(HaveKey(meta.AttrCksum))
	})

	It("combines nothing on Squid for an upload info a Tentacle radosgw wrote with a type", func(ctx SpecContext) {
		c.SetRequiredOSDRelease("squid")
		sq := open(ctx, denc.Squid)
		up := newUpload(ctx, sq, op.UploadChecksum{})
		mOID := metaOID(rec, "k", up.ID)
		c.Object(extraPoolName, "", mOID).Data = encodeTentacle(meta.MultipartUploadInfo{
			DestPlacement: meta.PlacementRule{Name: "default-placement"}, CksumType: uint16(cksum.CRC32C), CksumFlags: cksum.FlagFullObject,
		})
		got, err := sq.GetUpload(ctx, rec, key, up.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Checksum).To(BeZero(), "Squid's decode reads no type")
		etags := putParts(ctx, sq, got, first, last)
		res, err := sq.Complete(ctx, got, completeParts(etags))
		Expect(err).NotTo(HaveOccurred(), "no part has a checksum, and none is asked for")
		Expect(res.Checksum).To(BeNil())
	})

	Describe("a retried completion", func() {
		var (
			up    *op.Upload
			etags []string
			want  cksum.Cksum
		)
		BeforeEach(func(ctx SpecContext) {
			uc := op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}
			up = newUpload(ctx, s, uc)
			etags = putParts(ctx, s, up, first, last)
			want = combinedOf(uc.Type, uc.Flags, first, last)
			up.WriteTag = "tx-first"
		})
		// leaveMeta completes up with the meta object's removal failing, so
		// that the meta object, with its completion record, outlives the
		// head write.
		leaveMeta := func(ctx context.Context) {
			GinkgoHelper()
			mOID := metaOID(rec, "k", up.ID)
			armed := false
			c.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
				if !armed && c.Object(testDataPool, "", headOID) != nil {
					armed = true
					c.FailNextWrite(extraPoolName, "", mOID, syscall.EIO)
				}
			})
			_, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(c.Object(extraPoolName, "", mOID)).NotTo(BeNil(), "the failed removal left the meta object")
			c.BeforeWrite(extraPoolName, "", mOID, nil)
		}
		// retry completes up again, asserting that the head was not written
		// again, and returns the result.
		retry := func(ctx context.Context) *op.PutResult {
			GinkgoHelper()
			headWrites := c.Writes(testDataPool, "", headOID)
			up.WriteTag, up.SuppliedChecksum = "tx-retry", supplying(cksum.SHA1, "not compared")
			res, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred())
			Expect(c.Writes(testDataPool, "", headOID)).To(Equal(headWrites), "no head written again")
			return res
		}

		It("answers from the checksum the object stores once the upload is gone, as check_previously_completed decodes it", func(ctx SpecContext) {
			_, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(retry(ctx).Checksum).To(Equal(&want), "rgw_op.cc:7474-7501 at v20.2.4")
		})
		It("answers no checksum once the upload is gone when the stored one does not decode, as radosgw still answers 200", func(ctx SpecContext) {
			_, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			c.Object(testDataPool, "", headOID).Xattrs[meta.AttrCksum] = []byte{2, 1, 0}
			Expect(retry(ctx).Checksum).To(BeNil(), "rgw_op.cc:7498-7500 at v20.2.4")
		})
		It("finishes a recorded completion whose meta object outlived its head write with the stored checksum", func(ctx SpecContext) {
			leaveMeta(ctx)
			Expect(retry(ctx).Checksum).To(Equal(&want))
			Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil(), "the retry finished the removal")
		})
		It("finishes a completion a gateway without the record left with the stored checksum, comparing nothing", func(ctx SpecContext) {
			leaveMeta(ctx)
			obj := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
			delete(obj.Xattrs, "user.rgw.mp_completion_tag")
			delete(obj.Xattrs, "user.rgw.mp_completion_instance")
			Expect(retry(ctx).Checksum).To(Equal(&want))
			Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil(), "the retry finished the removal")
		})
	})

	It("serves an upload a Tentacle radosgw created with xxh3: GetUpload reads its type, and its part, its copy and its completion answer NotImplemented, writing nothing", func(ctx SpecContext) {
		uc := op.UploadChecksum{Type: cksum.XXH3, Flags: cksum.FlagComposite}
		up := newUpload(ctx, s, op.UploadChecksum{})
		mOID := metaOID(rec, "k", up.ID)
		c.Object(extraPoolName, "", mOID).Data = encodeTentacle(meta.MultipartUploadInfo{
			DestPlacement: meta.PlacementRule{Name: "default-placement"}, CksumType: uint16(uc.Type), CksumFlags: uc.Flags,
		})
		got, err := s.GetUpload(ctx, rec, key, up.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Checksum).To(Equal(uc), "get_info's cksum_type and cksum_flags")

		_, _, err = op.ChecksumForPart(op.ChecksumInput{Body: bytes.NewReader(first)}, got.Checksum, s.Release())
		Expect(err).To(MatchError(op.ErrNotImplemented), "the part's checksum request")
		_, err = s.PutObject(ctx, rec, meta.ObjKey{Name: "src"}, bytes.NewReader(first), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(first))})
		Expect(err).NotTo(HaveOccurred())
		src, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "src"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		c.ResetCounters()
		_, err = s.CopyPart(ctx, got, 1, src, op.ByteRange{Length: uint64(len(first))})
		Expect(err).To(MatchError(op.ErrNotImplemented), "the copy")
		Expect(c.Writes(extraPoolName, "", mOID)).To(BeZero(), "no part registered")

		e1 := putPart(ctx, s, got, 1, first, nil)
		settle(s)
		c.ResetCounters()
		_, err = s.Complete(ctx, got, completeParts([]string{e1}))
		Expect(err).To(MatchError(op.ErrNotImplemented), "the completion")
		nothingWritten(got)
	})
})
