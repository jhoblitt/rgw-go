package clsutil_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// u32Request encodes one u32, bumped by one for Tentacle so the release
// visibly reaches Encode.
type u32Request uint32

func (v u32Request) Encode(e *denc.Encoder, r denc.Release) {
	if r == denc.Tentacle {
		v++
	}
	e.U32(uint32(v))
}

func decodeU32(d *denc.Decoder) uint32 { return d.U32() }

// reply returns an exec result that ran with out and rval.
func reply(out []byte, rval int32) *radosclient.ExecResult {
	res := radosclient.NewReadOp().Exec("c", "m", nil)
	res.Set(out, rval)
	return res
}

var _ = Describe("Encode", func() {
	It("encodes the request for the release given", func() {
		Expect(clsutil.Encode(u32Request(7), denc.Squid)).To(Equal([]byte{7, 0, 0, 0}))
		Expect(clsutil.Encode(u32Request(7), denc.Tentacle)).To(Equal([]byte{8, 0, 0, 0}))
	})
})

var _ = Describe("ReadFramed", func() {
	It("returns one framed value, header included, and leaves what follows", func() {
		framed := []byte{2, 1, 3, 0, 0, 0, 'a', 'b', 'c'}
		d := denc.NewDecoder(append(append([]byte{}, framed...), 0xff))
		Expect(clsutil.ReadFramed(d)).To(Equal(framed))
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(d.U8()).To(Equal(uint8(0xff)))
		Expect(d.Remaining()).To(BeZero())
	})

	It("returns an empty frame as its header alone", func() {
		d := denc.NewDecoder([]byte{1, 1, 0, 0, 0, 0})
		Expect(clsutil.ReadFramed(d)).To(Equal([]byte{1, 1, 0, 0, 0, 0}))
		Expect(d.Err()).NotTo(HaveOccurred())
	})

	It("fails a frame longer than the bytes left", func() {
		d := denc.NewDecoder([]byte{1, 1, 4, 0, 0, 0, 'a'})
		Expect(clsutil.ReadFramed(d)).To(BeNil())
		Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
	})
})

var _ = Describe("DecodeReply", func() {
	It("decodes the reply and ignores trailing bytes, as the C++ clients do", func() {
		v, err := clsutil.DecodeReply(reply([]byte{5, 0, 0, 0, 0xff}, 0), "user", "get_header", decodeU32)
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(BeEquivalentTo(5))
	})

	It("names the class and method when the reply does not decode", func() {
		_, err := clsutil.DecodeReply(reply([]byte{1}, 0), "user", "get_header", decodeU32)
		Expect(err).To(MatchError(denc.ErrShortBuffer))
		Expect(err).To(MatchError(HavePrefix("user: decoding get_header reply: ")))
	})

	It("passes the method's own error through", func() {
		_, err := clsutil.DecodeReply(reply(nil, -2), "user", "get_header", decodeU32)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})

	It("reports an op that has not run", func() {
		res := radosclient.NewReadOp().Exec("c", "m", nil)
		_, err := clsutil.DecodeReply(res, "user", "get_header", decodeU32)
		Expect(err).To(MatchError(radosclient.ErrIncomplete))
	})
})
