package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"strings"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // Ginkgo's DSL is meant to be dot-imported
	. "github.com/onsi/gomega"    //nolint:revive // Gomega's DSL is meant to be dot-imported

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// RunObjectChecksums declares the store-conformance specs of the checksums
// PutObject and PutPart compute, verify and store, against Run's kind of
// Env factory. Run leaves the checksum specs out, so that a store joins
// each checksum entry point once it implements what that one pins.
func RunObjectChecksums(newEnv func(ctx context.Context) (*op.Env, func())) {
	Describe("object checksum conformance", func() {
		var f checksumFixture
		f.beforeEach(newEnv)

		It("stores a verified checksum and returns it on stat, asking for the value once the body is read", func(ctx SpecContext) {
			body := []byte("a body to checksum")
			want := crc32cOf(body, 0)
			r := &endedReader{r: bytes.NewReader(body)}
			var asked, early bool
			req := &op.ChecksumRequest{Type: cksum.CRC32C, Expected: func() (string, bool) {
				asked, early = true, early || !r.ended
				return armor(want), true
			}}
			res, err := f.putObject(ctx, "k", r, len(body), req)
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(BeTrue(), "the value was asked for")
			Expect(early).To(BeFalse(), "the value was asked for before the body ended, when a trailer's is not there")
			Expect(res.Checksum).To(Equal(new(want)), "the result")
			Expect(f.stat(ctx, "k").Checksum).To(Equal(new(want)), "the stat")
		})

		DescribeTable("stores the checksum of a write that supplies no value unverified",
			func(ctx SpecContext, expected func() (string, bool)) {
				body := []byte("a body no value vouches for")
				_, err := f.putObject(ctx, "k", bytes.NewReader(body), len(body), &op.ChecksumRequest{Type: cksum.SHA256, Expected: expected})
				Expect(err).NotTo(HaveOccurred())
				Expect(f.stat(ctx, "k").Checksum).To(Equal(new(sha256Of(body, 0))))
			},
			Entry("no Expected", nil),
			Entry("an Expected that answers it has no value", func() (string, bool) { return "not a checksum", false }),
		)

		It("refuses an object or a part whose checksum is not the value supplied with BadDigest, writing nothing", func(ctx SpecContext) {
			f.put(ctx, "k", "first")
			before := f.stat(ctx, "k")
			body := []byte("second")
			wrong := &op.ChecksumRequest{Type: cksum.CRC32C, Expected: supplied(armor(crc32cOf([]byte("other"), 0)))}
			_, err := f.putObject(ctx, "k", bytes.NewReader(body), len(body), wrong)
			Expect(err).To(MatchError(op.ErrBadDigest), "the object")
			after := f.stat(ctx, "k")
			Expect(after.ETag).To(Equal(before.ETag), "the object it would have replaced stays")
			Expect(after.Checksum).To(BeNil(), "the object it would have replaced stays")

			up := f.newUpload(ctx, op.UploadChecksum{})
			_, err = f.env.Multipart.PutPart(ctx, up, 1, bytes.NewReader(body), op.PutParams{Size: int64(len(body)), Checksum: wrong})
			Expect(err).To(MatchError(op.ErrBadDigest), "the part")
			parts, err := f.env.Multipart.ListParts(ctx, up, 0, 1000)
			Expect(err).NotTo(HaveOccurred())
			Expect(parts.Parts).To(BeEmpty(), "the part")
		})
	})
}

// RunMultipartChecksums declares the store-conformance specs of an upload's
// checksum type, which only Tentacle's multipart_upload_info carries, and of
// the checksum its completion combines, against Run's kind of Env factory.
func RunMultipartChecksums(newEnv func(ctx context.Context) (*op.Env, func())) {
	Describe("multipart checksum conformance", func() {
		var f checksumFixture
		f.beforeEach(newEnv)

		Context("on Tentacle", func() {
			// first is a part at rgw_multipart_min_part_size's default.
			var first []byte
			BeforeEach(func() {
				if !f.tentacle() {
					Skip("only a Tentacle upload has a checksum type")
				}
				first = bytes.Repeat([]byte("one "), 5<<20/4)
			})

			DescribeTable("refuses a completion whose part has another checksum type than its upload's, or none, with InvalidRequest, writing nothing",
				func(ctx SpecContext, second *op.ChecksumRequest) {
					uc := op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}
					up := f.newUpload(ctx, uc)
					r1 := f.putPart(ctx, up, 1, first, &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags})
					r2 := f.putPart(ctx, up, 2, []byte("two"), second)
					_, err := f.env.Multipart.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: r1.ETag}, {Number: 2, ETag: r2.ETag}})
					Expect(err).To(MatchError(op.ErrInvalidRequest))
					f.uploadKept(ctx, up)
				},
				Entry("a part of another type", &op.ChecksumRequest{Type: cksum.SHA256, Flags: cksum.FlagFullObject}),
				Entry("a part without a checksum", nil),
			)

			// radosgw combines before it matches the parts, and would answer
			// InvalidRequest here
			// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7273-L7282 at v20.2.4).
			It("matches every named part before it combines their checksums, so a mismatched part is InvalidPart", func(ctx SpecContext) {
				up := f.newUpload(ctx, op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject})
				r1 := f.putPart(ctx, up, 1, first, nil)
				f.putPart(ctx, up, 2, []byte("two"), &op.ChecksumRequest{Type: cksum.CRC32C, Flags: cksum.FlagFullObject})
				_, err := f.env.Multipart.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: r1.ETag}, {Number: 2, ETag: r1.ETag}})
				Expect(err).To(MatchError(op.ErrInvalidPart), "part 1 has no checksum, and part 2 is named under part 1's ETag")
				f.uploadKept(ctx, up)
			})

			DescribeTable("combines the checksums of exactly the parts a completion names, in number order",
				func(ctx SpecContext, uc op.UploadChecksum, part func([]byte, uint16) cksum.Cksum, combined func(parts ...[]byte) cksum.Cksum, supply func(cksum.Cksum) string) {
					replaced := bytes.Repeat([]byte("1"), 5<<20)
					second := bytes.Repeat([]byte("two "), 5<<20/4)
					third := []byte("three")
					up := f.newUpload(ctx, uc)
					Expect(up.Checksum).To(Equal(uc), "the upload created")
					got, err := f.env.Multipart.GetUpload(ctx, f.rec, up.Key, up.ID)
					Expect(err).NotTo(HaveOccurred())
					Expect(got.Checksum).To(Equal(uc), "the upload read back")

					req := &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags}
					r3 := f.putPart(ctx, up, 3, third, req)
					f.putPart(ctx, up, 1, replaced, req)
					r1 := f.putPart(ctx, up, 1, first, req)
					_, err = f.putObject(ctx, "src", bytes.NewReader(second), len(second), nil)
					Expect(err).NotTo(HaveOccurred())
					r2, err := f.env.Multipart.CopyPart(ctx, up, 2, f.stat(ctx, "src"), op.ByteRange{Length: uint64(len(second))})
					Expect(err).NotTo(HaveOccurred(), "copying part 2")
					Expect([]*cksum.Cksum{r1.Checksum, r2.Checksum, r3.Checksum}).To(Equal([]*cksum.Cksum{
						new(part(first, uc.Flags)), new(part(second, uc.Flags)), new(part(third, uc.Flags)),
					}), "each part's own, at its upload's flags, the copied part's included")
					listed, err := f.env.Multipart.ListParts(ctx, up, 0, 1000)
					Expect(err).NotTo(HaveOccurred())
					Expect(listed.Parts).To(HaveExactElements(
						HaveField("Checksum", Equal(r1.Checksum)), HaveField("Checksum", Equal(r2.Checksum)), HaveField("Checksum", Equal(r3.Checksum)),
					), "the part records")

					_, err = f.env.Multipart.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: r1.ETag}, {Number: 2, ETag: r2.ETag}})
					Expect(err).To(MatchError(op.ErrInvalidPart), "a completion that leaves part 3 out")
					f.uploadKept(ctx, up)

					want := combined(first, second, third)
					up.SuppliedChecksum = &op.ChecksumRequest{Type: uc.Type, Expected: supplied(supply(want))}
					res, err := f.env.Multipart.Complete(ctx, up, []op.CompletePart{
						{Number: 3, ETag: r3.ETag}, {Number: 1, ETag: r1.ETag}, {Number: 2, ETag: r2.ETag},
					})
					Expect(err).NotTo(HaveOccurred())
					Expect(res.Checksum).To(Equal(new(want)), "the result")
					Expect(f.stat(ctx, "mp").Checksum).To(Equal(new(want)), "the stat")
				},
				Entry("a CRC32C upload's full-object CRC, supplied exactly",
					op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}, crc32cOf,
					func(parts ...[]byte) cksum.Cksum {
						return crc32cOf(bytes.Join(parts, nil), cksum.FlagV2|cksum.FlagCombined|cksum.FlagFullObject)
					},
					armor),
				Entry("a SHA256 upload's composite, supplied without its part count",
					op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}, sha256Of, sha256Composite,
					armor),
				Entry("a SHA256 upload's composite, supplied with its part count",
					op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}, sha256Of, sha256Composite,
					func(c cksum.Cksum) string { return armor(c) + "-3" }),
			)

			It("stores the combined checksum of a completion that supplies no value", func(ctx SpecContext) {
				uc := op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}
				up := f.newUpload(ctx, uc)
				req := &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags}
				second := []byte("two")
				r1 := f.putPart(ctx, up, 1, first, req)
				r2 := f.putPart(ctx, up, 2, second, req)
				up.SuppliedChecksum = &op.ChecksumRequest{Type: uc.Type, Expected: func() (string, bool) { return "not a checksum", false }}
				res, err := f.env.Multipart.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: r1.ETag}, {Number: 2, ETag: r2.ETag}})
				Expect(err).NotTo(HaveOccurred(), "an Expected that answers it has no value is not compared")
				Expect(res.Checksum).To(Equal(new(sha256Composite(first, second))))
			})

			DescribeTable("refuses a completion whose supplied checksum is not the combined one with BadDigest, writing nothing",
				func(ctx SpecContext, supply func(armored string) *op.ChecksumRequest) {
					uc := op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}
					up := f.newUpload(ctx, uc)
					req := &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags}
					second := []byte("two")
					r1 := f.putPart(ctx, up, 1, first, req)
					r2 := f.putPart(ctx, up, 2, second, req)
					up.SuppliedChecksum = supply(armor(sha256Composite(first, second)))
					_, err := f.env.Multipart.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: r1.ETag}, {Number: 2, ETag: r2.ETag}})
					Expect(err).To(MatchError(op.ErrBadDigest))
					f.uploadKept(ctx, up)
				},
				Entry("one that only starts with the combined one", func(a string) *op.ChecksumRequest {
					return &op.ChecksumRequest{Type: cksum.SHA256, Expected: supplied(a + "-2x")}
				}),
				Entry("one with another part count", func(a string) *op.ChecksumRequest {
					return &op.ChecksumRequest{Type: cksum.SHA256, Expected: supplied(a + "-3")}
				}),
				Entry("the combined value under another type", func(a string) *op.ChecksumRequest {
					return &op.ChecksumRequest{Type: cksum.SHA1, Expected: supplied(a + "-2")}
				}),
			)
		})

		It("keeps no upload checksum type on Squid and combines nothing, though a part's own is computed", func(ctx SpecContext) {
			if f.tentacle() {
				Skip("a Tentacle upload keeps its checksum type")
			}
			up := f.newUpload(ctx, op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject})
			Expect(up.Checksum).To(BeZero(), "the upload created")
			got, err := f.env.Multipart.GetUpload(ctx, f.rec, up.Key, up.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Checksum).To(BeZero(), "the upload read back")
			body := []byte("one")
			r1 := f.putPart(ctx, up, 1, body, &op.ChecksumRequest{Type: cksum.CRC32C})
			Expect(r1.Checksum).To(Equal(new(crc32cOf(body, 0))), "the part head's own")
			listed, err := f.env.Multipart.ListParts(ctx, up, 0, 1000)
			Expect(err).NotTo(HaveOccurred())
			Expect(listed.Parts).To(HaveExactElements(HaveField("Checksum", BeNil())), "Squid's part record holds none")
			up.SuppliedChecksum = &op.ChecksumRequest{Type: cksum.CRC32C, Expected: supplied("not a checksum")}
			res, err := f.env.Multipart.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: r1.ETag}})
			Expect(err).NotTo(HaveOccurred(), "the supplied value is not compared")
			Expect(res.Checksum).To(BeNil(), "the result")
			Expect(f.stat(ctx, "mp").Checksum).To(BeNil(), "the stat")
		})
	})
}

// checksumFixture is what a checksum spec runs on: an Env, and a user and a
// bucket of the spec's own, both removed when it ends, the bucket's objects
// first.
type checksumFixture struct {
	env *op.Env
	u   *op.UserRecord
	rec *op.BucketRecord
}

// beforeEach declares the BeforeEach that sets f up for each spec.
func (f *checksumFixture) beforeEach(newEnv func(ctx context.Context) (*op.Env, func())) {
	BeforeEach(func(ctx SpecContext) {
		var done func()
		f.env, done = newEnv(ctx)
		DeferCleanup(done)
		name := "rgwgo-" + strings.ToLower(rand.Text()[:10]) + "-cksum"
		f.u = f.createUser(ctx, name)
		f.rec = f.createBucket(ctx, name)
	})
}

func (f *checksumFixture) createUser(ctx context.Context, name string) *op.UserRecord {
	GinkgoHelper()
	info := meta.NewUserInfo()
	info.UserID = meta.UserID{ID: name}
	info.DisplayName = "Conformance checksums"
	rec := &op.UserRecord{Info: info}
	Expect(f.env.Users.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true})).To(Succeed(), "putting %s", name)
	DeferCleanup(func(ctx context.Context) {
		cur, err := f.env.Users.GetUser(ctx, info.UserID)
		Expect(err).NotTo(HaveOccurred(), "reading %s back to remove it", name)
		Expect(f.env.Users.RemoveUser(ctx, cur)).To(Succeed(), "removing %s", name)
	})
	return rec
}

func (f *checksumFixture) createBucket(ctx context.Context, name string) *op.BucketRecord {
	GinkgoHelper()
	zg := f.env.Zone.ZoneGroup()
	rec, err := f.env.Buckets.CreateBucket(ctx, op.CreateBucketParams{
		Name: name, Owner: f.owner(), Zonegroup: zg.ID, Placement: zg.DefaultPlacement,
		Attrs: map[string][]byte{meta.AttrACL: f.acl()}, Exclusive: true,
	})
	Expect(err).NotTo(HaveOccurred(), "creating %s", name)
	DeferCleanup(func(ctx context.Context) {
		cur, err := f.env.Buckets.GetBucket(ctx, "", name)
		Expect(err).NotTo(HaveOccurred(), "reading %s back to remove it", name)
		for {
			res, err := f.env.Buckets.ListObjects(ctx, cur, op.ListObjectsParams{MaxKeys: 1000})
			Expect(err).NotTo(HaveOccurred(), "listing %s to empty it", name)
			for i := range res.Entries {
				key := res.Entries[i].Key
				Expect(f.env.Objects.DeleteObject(ctx, cur, key, op.DeleteParams{})).Error().To(Succeed(), "deleting %s/%s", name, key.Name)
			}
			if !res.Truncated {
				break
			}
		}
		Expect(f.env.Buckets.DeleteBucket(ctx, cur)).To(Succeed(), "deleting %s", name)
	})
	return rec
}

func (f *checksumFixture) tentacle() bool { return f.env.Zone.Release() >= denc.Tentacle }

func (f *checksumFixture) owner() meta.Owner { return meta.UserOwner(f.u.Info.UserID) }

func (f *checksumFixture) acl() []byte {
	e := denc.NewEncoder()
	acl.DefaultPolicy(f.owner(), f.u.Info.DisplayName).Encode(e, f.env.Zone.Release())
	return e.Bytes()
}

func (f *checksumFixture) stat(ctx context.Context, key string) *op.ObjectState {
	GinkgoHelper()
	st, err := f.env.Objects.StatObject(ctx, f.rec, meta.ObjKey{Name: key})
	Expect(err).NotTo(HaveOccurred(), "stating %s", key)
	return st
}

func (f *checksumFixture) putObject(ctx context.Context, key string, body io.Reader, size int, req *op.ChecksumRequest) (*op.PutResult, error) {
	return f.env.Objects.PutObject(ctx, f.rec, meta.ObjKey{Name: key}, body,
		op.PutParams{Size: int64(size), Attrs: map[string][]byte{meta.AttrACL: f.acl()}, Checksum: req})
}

// put writes body as key with no checksum.
func (f *checksumFixture) put(ctx context.Context, key, body string) {
	GinkgoHelper()
	_, err := f.putObject(ctx, key, strings.NewReader(body), len(body), nil)
	Expect(err).NotTo(HaveOccurred(), "putting %s", key)
}

// newUpload creates an upload of type uc of the key "mp" and aborts it when
// the spec ends, before the bucket is removed, unless it completed.
func (f *checksumFixture) newUpload(ctx context.Context, uc op.UploadChecksum) *op.Upload {
	const key = "mp"
	GinkgoHelper()
	up, err := f.env.Multipart.CreateUpload(ctx, f.rec, meta.ObjKey{Name: key}, op.UploadParams{
		Owner: f.owner(), OwnerName: f.u.Info.DisplayName, Placement: f.rec.Info.PlacementRule,
		Attrs: map[string][]byte{meta.AttrACL: f.acl()}, Checksum: uc,
	})
	Expect(err).NotTo(HaveOccurred(), "creating an upload of %s", key)
	DeferCleanup(func(ctx context.Context) {
		if cur, err := f.env.Multipart.GetUpload(ctx, f.rec, up.Key, up.ID); err == nil {
			Expect(f.env.Multipart.Abort(ctx, cur)).To(Succeed(), "aborting the upload of %s", key)
		}
	})
	return up
}

func (f *checksumFixture) putPart(ctx context.Context, up *op.Upload, n int, body []byte, req *op.ChecksumRequest) *op.PartResult {
	GinkgoHelper()
	res, err := f.env.Multipart.PutPart(ctx, up, n, bytes.NewReader(body), op.PutParams{Size: int64(len(body)), Checksum: req})
	Expect(err).NotTo(HaveOccurred(), "putting part %d", n)
	return res
}

// uploadKept asserts that up wrote no object and is still in progress.
func (f *checksumFixture) uploadKept(ctx context.Context, up *op.Upload) {
	GinkgoHelper()
	Expect(f.stat(ctx, up.Key.Name).Exists).To(BeFalse(), "an object of %s", up.Key.Name)
	_, err := f.env.Multipart.GetUpload(ctx, f.rec, up.Key, up.ID)
	Expect(err).NotTo(HaveOccurred(), "the upload is still in progress")
}

// endedReader is a body that records whether it has returned io.EOF.
type endedReader struct {
	r     io.Reader
	ended bool
}

func (e *endedReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		e.ended = true
	}
	return n, err
}

// supplied is a ChecksumRequest's Expected for the value v.
func supplied(v string) func() (string, bool) { return func() (string, bool) { return v, true } }

// crc32cOf is b's CRC32C record with flags, its digest big-endian as
// radosgw stores a CRC, computed without rgw-go's cksum package.
func crc32cOf(b []byte, flags uint16) cksum.Cksum {
	crc := crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))
	return cksum.Cksum{Type: cksum.CRC32C, Digest: binary.BigEndian.AppendUint32(nil, crc), Flags: flags}
}

// sha256Of is b's SHA256 record with flags.
func sha256Of(b []byte, flags uint16) cksum.Cksum {
	sum := sha256.Sum256(b)
	return cksum.Cksum{Type: cksum.SHA256, Digest: sum[:], Flags: flags}
}

// sha256Composite is the combined record of an upload of SHA256 parts: the
// SHA256 of their digests in order.
func sha256Composite(parts ...[]byte) cksum.Cksum {
	var digests []byte
	for _, p := range parts {
		digests = append(digests, sha256Of(p, 0).Digest...)
	}
	return sha256Of(digests, cksum.FlagV2|cksum.FlagCombined|cksum.FlagComposite)
}

// armor is c's value as a client supplies it, the base64 of its digest.
func armor(c cksum.Cksum) string { return base64.StdEncoding.EncodeToString(c.Digest) }
