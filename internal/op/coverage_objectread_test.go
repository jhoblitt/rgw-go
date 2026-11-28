package op_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// The specs of this file pin entries of docs/differences.md, "Object read
// differences", that no other spec pinned.
var _ = Describe("object reads, as docs/differences.md records them", func() {
	const (
		badKey    = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key."
		badKeyMD5 = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key md5."
	)
	var (
		f       *objectReadFixture
		objects *opfakes.FakeObjectStore
	)
	BeforeEach(func(ctx SpecContext) {
		f = newObjectReadFixture(ctx, denc.Squid)
		objects = &opfakes.FakeObjectStore{}
		f.env.Objects = objects
	})
	onRelease := func(rel denc.Release) {
		if rel >= denc.Tentacle {
			zone := &opfakes.FakeZoneInfo{}
			zone.ReleaseReturns(denc.Tentacle)
			f.env.Zone = zone
		}
	}
	// state is a 1 KiB object k whose ETag attr is "e", changed by mut.
	state := func(mut func(st *op.ObjectState)) *op.ObjectState {
		st := &op.ObjectState{
			Exists: true, Size: 1024, Mtime: time.Unix(1700000000, 0), ETag: "e",
			Attrs: map[string][]byte{meta.AttrETag: []byte("e")}, Key: meta.ObjKey{Name: "k"},
		}
		if mut != nil {
			mut(st)
		}
		return st
	}
	sseCObject := func() *op.ObjectState {
		return state(func(st *op.ObjectState) {
			st.Attrs[meta.AttrCryptMode] = []byte("SSE-C-AES256")
			st.Attrs[meta.AttrCryptKeyMD5] = sseCKeyMD5()
		})
	}
	get := func(ctx context.Context, st *op.ObjectState, mut func(o *op.GetObject)) error {
		objects.StatObjectReturns(st, nil)
		objects.PrefetchObjectReturns(st, nil)
		o := &op.GetObject{GetData: true, Sink: &captureSink{}}
		if mut != nil {
			mut(o)
		}
		return op.Run(ctx, o, f.req("k"))
	}

	// docs/differences.md, "SSE-C reads": GetObjectAttributes makes
	// GetObject's checks and answers the same 501, where a v20.2.4 radosgw
	// sets up no decryption and answers the attributes.
	DescribeTable("answer GetObjectAttributes of an SSE-C object whose key headers pass 501, on both releases",
		func(ctx SpecContext, rel denc.Release) {
			onRelease(rel)
			objects.StatObjectReturns(sseCObject(), nil)
			o := &op.GetObjectAttributes{Requested: op.AttrETag | op.AttrObjectSize}
			o.Secure = true
			o.SSECAlgorithm = new("AES256")
			o.SSECKey = base64.StdEncoding.EncodeToString(sseCKey())
			o.SSECKeyMD5 = base64.StdEncoding.EncodeToString(sseCKeyMD5())
			Expect(op.Run(ctx, o, f.req("k"))).To(MatchError(op.ErrNotImplemented))
		},
		Entry("on Squid, which answers ?attributes as a GET", denc.Squid),
		Entry("on Tentacle, whose get_decrypt_filter reads no data", denc.Tentacle),
	)

	// docs/differences.md, "SSE-C key headers that are all '='": rgw-go's
	// fromBase64 decodes them to no bytes, where radosgw's from_base64 reads
	// before the start of its input.
	DescribeTable("answer an SSE-C key or key-MD5 header of '=' alone 400 InvalidArgument, its length being neither size",
		func(ctx SpecContext, key, keyMD5, msg string) {
			err := get(ctx, sseCObject(), func(o *op.GetObject) {
				o.Secure, o.SSECAlgorithm, o.SSECKey, o.SSECKeyMD5 = true, new("AES256"), key, keyMD5
			})
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(op.AsError(err).Message).To(Equal(msg))
		},
		Entry("a key of one '='", "=", base64.StdEncoding.EncodeToString(sseCKeyMD5()), badKey),
		Entry("a key of four '='", "====", base64.StdEncoding.EncodeToString(sseCKeyMD5()), badKey),
		Entry("a key-MD5 of four '='", base64.StdEncoding.EncodeToString(sseCKey()), "====", badKeyMD5),
	)

	// docs/differences.md, "Conditionals use the object's mtime alone":
	// Tentacle compares the dates with the later of the mtime and
	// user.rgw.rgw-internal-mtime; rgw-go reads no such attr.
	Describe("on Tentacle, an object whose user.rgw.rgw-internal-mtime is an hour past its mtime", func() {
		// between is half an hour past the object's mtime, 1700000000.
		const between = "Tue, 14 Nov 2023 22:43:20 UTC"
		later := func() *op.ObjectState {
			return state(func(st *op.ObjectState) {
				// a ceph::real_time as utime_t encodes it: u32 seconds, u32 nanoseconds
				v := binary.LittleEndian.AppendUint32(nil, 1700000000+3600)
				st.Attrs[meta.AttrPrefix+"rgw-internal-mtime"] = binary.LittleEndian.AppendUint32(v, 0)
			})
		}
		BeforeEach(func() { onRelease(denc.Tentacle) })
		It("answers 304 to an If-Modified-Since between the two, where Tentacle serves it", func(ctx SpecContext) {
			Expect(get(ctx, later(), func(o *op.GetObject) { o.IfModifiedSince = between })).To(MatchError(op.ErrNotModified))
		})
		It("serves an If-Unmodified-Since between the two, where Tentacle answers 412", func(ctx SpecContext) {
			Expect(get(ctx, later(), func(o *op.GetObject) { o.IfUnmodifiedSince = between })).To(Succeed())
		})
	})
})
