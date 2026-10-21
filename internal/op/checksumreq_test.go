package op_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// checksumFields is a ChecksumInput.Field over the headers kv names, read as
// a request's headers are.
func checksumFields(kv ...string) func(string) (string, bool) {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return func(name string) (string, bool) { return op.HeaderValue(h, name) }
}

// trailerSet answers op.Trailers from its values, keyed by lower-case name.
type trailerSet map[string]string

func (t trailerSet) Trailer(name string) (string, bool) {
	v, ok := t[strings.ToLower(name)]
	return v, ok
}

// asked is what a checksum request asks a store for, its Expected answered
// once the body handed with it has been read to its end.
type asked struct {
	Type     cksum.Type
	Flags    uint16
	Expects  bool
	Value    string
	Supplied bool
}

func askedOf(req *op.ChecksumRequest, body io.Reader) *asked {
	GinkgoHelper()
	if body != nil {
		_, err := io.Copy(io.Discard, body)
		Expect(err).NotTo(HaveOccurred(), "reading the body")
	}
	if req == nil {
		return nil
	}
	a := &asked{Type: req.Type, Flags: req.Flags, Expects: req.Expected != nil}
	if req.Expected != nil {
		a.Value, a.Supplied = req.Expected()
	}
	return a
}

// readCounter counts the Reads of its body.
type readCounter struct {
	r     io.Reader
	reads int
}

func (c *readCounter) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}

// recordOf is the checksum record of type t over b with flags.
func recordOf(t cksum.Type, flags uint16, b []byte) cksum.Cksum {
	GinkgoHelper()
	h, err := cksum.NewHash(t)
	Expect(err).NotTo(HaveOccurred())
	h.Write(b)
	return cksum.Finish(t, h, flags)
}

func armorOf(t cksum.Type, b []byte) string {
	GinkgoHelper()
	return recordOf(t, 0, b).Armor()
}

var bothReleases = []denc.Release{denc.Squid, denc.Tentacle}

const checksummedBody = "a body to checksum"

var _ = Describe("ChecksumForPut", func() {
	DescribeTable("takes the algorithm field, else the one value's algorithm, and that value",
		func(fs []string, trailers op.Trailers, want *asked) {
			for _, rel := range bothReleases {
				body := strings.NewReader(checksummedBody)
				req, got, err := op.ChecksumForPut(op.ChecksumInput{Field: checksumFields(fs...), Trailers: trailers, Body: body}, rel)
				Expect(err).NotTo(HaveOccurred(), "release %d", rel)
				Expect(askedOf(req, got)).To(Equal(want), "release %d", rel)
			}
		},
		Entry("nothing, for no field", nil, nil, (*asked)(nil)),
		Entry("x-amz-checksum-algorithm, no value supplied",
			[]string{"x-amz-checksum-algorithm", "CRC32"}, nil, &asked{Type: cksum.CRC32}),
		Entry("x-amz-checksum-algorithm in lower case",
			[]string{"x-amz-checksum-algorithm", "crc32c"}, nil, &asked{Type: cksum.CRC32C}),
		Entry("x-amz-sdk-checksum-algorithm",
			[]string{"x-amz-sdk-checksum-algorithm", "SHA256"}, nil, &asked{Type: cksum.SHA256}),
		Entry("x-amz-checksum-algorithm over x-amz-sdk-checksum-algorithm, which is not read",
			[]string{"x-amz-checksum-algorithm", "SHA1", "x-amz-sdk-checksum-algorithm", "MD5"}, nil, &asked{Type: cksum.SHA1}),
		Entry("the algorithm's value field",
			[]string{"x-amz-checksum-algorithm", "SHA512", "x-amz-checksum-sha512", "v=="}, nil,
			&asked{Type: cksum.SHA512, Expects: true, Value: "v==", Supplied: true}),
		Entry("a lone value field, its algorithm inferred",
			[]string{"X-Amz-Checksum-CRC64NVME", "v=="}, nil,
			&asked{Type: cksum.CRC64NVME, Expects: true, Value: "v==", Supplied: true}),
		Entry("an empty value field, which no checksum equals",
			[]string{"x-amz-checksum-crc32", ""}, nil, &asked{Type: cksum.CRC32, Expects: true, Supplied: true}),
		Entry("a lone checksum trailer, its algorithm inferred and its value the trailer's once the body has ended",
			[]string{"x-amz-trailer", "x-amz-checksum-sha256"}, trailerSet{"x-amz-checksum-sha256": "t=="},
			&asked{Type: cksum.SHA256, Expects: true, Value: "t==", Supplied: true}),
		Entry("the algorithm's trailer, named in any case",
			[]string{"x-amz-sdk-checksum-algorithm", "CRC32", "x-amz-trailer", "X-Amz-Checksum-CRC32"}, trailerSet{"x-amz-checksum-crc32": "t=="},
			&asked{Type: cksum.CRC32, Expects: true, Value: "t==", Supplied: true}),
		Entry("a trailer listed twice",
			[]string{"x-amz-trailer", "x-amz-checksum-crc32,X-AMZ-CHECKSUM-CRC32"}, trailerSet{"x-amz-checksum-crc32": "t=="},
			&asked{Type: cksum.CRC32, Expects: true, Value: "t==", Supplied: true}),
		Entry("a trailer the payload never sent, the empty string, which no checksum equals",
			[]string{"x-amz-trailer", "x-amz-checksum-crc32"}, trailerSet{},
			&asked{Type: cksum.CRC32, Expects: true, Supplied: true}),
		Entry("a trailer of a payload that keeps none, the empty string, which no checksum equals",
			[]string{"x-amz-checksum-algorithm", "CRC32", "x-amz-trailer", "x-amz-checksum-crc32"}, nil,
			&asked{Type: cksum.CRC32, Expects: true, Supplied: true}),
		Entry("nothing, for trailers that name no checksum",
			[]string{"x-amz-trailer", "x-amz-meta-a,x-amz-checksum-type,x-amz-checksum-crc33, x-amz-checksum-crc32,"}, trailerSet{}, (*asked)(nil)),
	)

	DescribeTable("refuses a request it cannot verify, before the body is read",
		func(want error, fs ...string) {
			for _, rel := range bothReleases {
				c := &readCounter{r: strings.NewReader(checksummedBody)}
				req, got, err := op.ChecksumForPut(op.ChecksumInput{Field: checksumFields(fs...), Trailers: trailerSet{}, Body: c}, rel)
				Expect(err).To(MatchError(want), "release %d", rel)
				Expect([]any{req, got, c.reads}).To(Equal([]any{(*op.ChecksumRequest)(nil), nil, 0}), "release %d", rel)
			}
		},
		Entry("an empty algorithm", op.ErrInvalidArgument, "x-amz-checksum-algorithm", ""),
		Entry("an empty x-amz-sdk-checksum-algorithm", op.ErrInvalidArgument, "x-amz-sdk-checksum-algorithm", ""),
		Entry("an algorithm naming none", op.ErrInvalidArgument, "x-amz-checksum-algorithm", "NONE"),
		Entry("an algorithm naming no algorithm", op.ErrInvalidArgument, "x-amz-sdk-checksum-algorithm", "crc33"),
		Entry("two value fields", op.ErrInvalidRequest, "x-amz-checksum-crc32", "a", "x-amz-checksum-sha256", "b"),
		Entry("a value field of another algorithm than the algorithm field's", op.ErrInvalidRequest,
			"x-amz-checksum-algorithm", "CRC32", "x-amz-checksum-crc32c", "a"),
		Entry("a trailer of another algorithm than the algorithm field's", op.ErrInvalidRequest,
			"x-amz-checksum-algorithm", "CRC32", "x-amz-trailer", "x-amz-checksum-sha1"),
		Entry("a value field and a trailer of its algorithm", op.ErrInvalidRequest,
			"x-amz-checksum-crc32", "a", "x-amz-trailer", "x-amz-checksum-crc32"),
		Entry("two checksum trailers", op.ErrInvalidRequest, "x-amz-trailer", "x-amz-checksum-crc32,x-amz-checksum-sha1"),
		Entry("a refused name beside a value it does not refuse", op.ErrInvalidArgument,
			"x-amz-checksum-crc32", "a", "x-amz-checksum-md5", "b"),
	)

	Describe("a trailer's value", func() {
		in := func(body io.Reader, trailers op.Trailers) op.ChecksumInput {
			return op.ChecksumInput{Field: checksumFields("x-amz-trailer", "x-amz-checksum-crc32"), Trailers: trailers, Body: body}
		}
		It("is the empty string, supplied, until the body has returned io.EOF, then the trailer's", func() {
			req, body, err := op.ChecksumForPut(in(strings.NewReader(checksummedBody), trailerSet{"x-amz-checksum-crc32": "t=="}), denc.Tentacle)
			Expect(err).NotTo(HaveOccurred())
			ask := func() []any { v, ok := req.Expected(); return []any{v, ok} }
			Expect(ask()).To(Equal([]any{"", true}), "before any Read")
			_, err = body.Read(make([]byte, 4))
			Expect(err).NotTo(HaveOccurred())
			Expect(ask()).To(Equal([]any{"", true}), "after a Read short of the end")
			_, err = io.Copy(io.Discard, body)
			Expect(err).NotTo(HaveOccurred())
			Expect(ask()).To(Equal([]any{"t==", true}), "once the body has ended")
		})
		It("is the empty string, supplied, before and after the end of a body that keeps no trailers", func() {
			req, body, err := op.ChecksumForPut(in(strings.NewReader(checksummedBody), nil), denc.Squid)
			Expect(err).NotTo(HaveOccurred())
			v, ok := req.Expected()
			Expect([]any{v, ok}).To(Equal([]any{"", true}), "before the end")
			Expect(io.ReadAll(body)).To(Equal([]byte(checksummedBody)), "the body is the body")
			v, ok = req.Expected()
			Expect([]any{v, ok}).To(Equal([]any{"", true}), "after the end")
		})
		It("is asked of the trailer the request lists, by the name it lists", func() {
			var named []string
			trailers := trailerFunc(func(name string) (string, bool) { named = append(named, name); return "t==", true })
			fs := checksumFields("x-amz-trailer", "X-Amz-Checksum-Crc32")
			req, body, err := op.ChecksumForPut(op.ChecksumInput{Field: fs, Trailers: trailers, Body: strings.NewReader("")}, denc.Tentacle)
			Expect(err).NotTo(HaveOccurred())
			Expect(askedOf(req, body)).To(Equal(&asked{Type: cksum.CRC32, Expects: true, Value: "t==", Supplied: true}))
			Expect(named).To(Equal([]string{"X-Amz-Checksum-Crc32"}))
		})
	})

	It("hands back the body itself when no trailer supplies the value", func() {
		body := strings.NewReader(checksummedBody)
		for _, fs := range [][]string{nil, {"x-amz-checksum-algorithm", "CRC32"}, {"x-amz-checksum-crc32", "v=="}} {
			_, got, err := op.ChecksumForPut(op.ChecksumInput{Field: checksumFields(fs...), Body: body}, denc.Tentacle)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeIdenticalTo(body), "fields %q", fs)
		}
	})

	It("reads no field from an input without a lookup", func() {
		body := strings.NewReader(checksummedBody)
		req, got, err := op.ChecksumForPut(op.ChecksumInput{Body: body}, denc.Tentacle)
		Expect([]any{req, got, err}).To(Equal([]any{(*op.ChecksumRequest)(nil), io.Reader(body), nil}))
	})
})

// trailerFunc is op.Trailers over a function.
type trailerFunc func(name string) (string, bool)

func (f trailerFunc) Trailer(name string) (string, bool) { return f(name) }

// crc32c is a CRC32C upload's checksum type and flags.
var crc32c = op.UploadChecksum{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}

var _ = Describe("ChecksumForPart", func() {
	DescribeTable("takes its upload's type when it names none, and its upload's flags, on Tentacle",
		func(up op.UploadChecksum, fs []string, want *asked) {
			body := strings.NewReader(checksummedBody)
			req, got, err := op.ChecksumForPart(op.ChecksumInput{Field: checksumFields(fs...), Trailers: trailerSet{}, Body: body}, up, denc.Tentacle)
			Expect(err).NotTo(HaveOccurred())
			Expect(askedOf(req, got)).To(Equal(want))
		},
		Entry("the upload's type and flags, for no field", crc32c, nil,
			&asked{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}),
		Entry("the upload's flags beside its own algorithm field", crc32c, []string{"x-amz-checksum-algorithm", "crc32c"},
			&asked{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}),
		Entry("its value field of the upload's type, verified though it names no algorithm",
			op.UploadChecksum{Type: cksum.CRC64NVME, Flags: cksum.FlagFullObject}, []string{"x-amz-checksum-crc64nvme", "v=="},
			&asked{Type: cksum.CRC64NVME, Flags: cksum.FlagFullObject, Expects: true, Value: "v==", Supplied: true}),
		Entry("its own algorithm, with the flags, of an upload without a type",
			op.UploadChecksum{Flags: cksum.FlagComposite}, []string{"x-amz-checksum-algorithm", "SHA256"},
			&asked{Type: cksum.SHA256, Flags: cksum.FlagComposite}),
		Entry("nothing, for an upload without a type and a part that names none", op.UploadChecksum{}, nil, (*asked)(nil)),
	)

	DescribeTable("refuses, before the body is read, on Tentacle",
		func(up op.UploadChecksum, want error, fs ...string) {
			c := &readCounter{r: strings.NewReader(checksummedBody)}
			req, got, err := op.ChecksumForPart(op.ChecksumInput{Field: checksumFields(fs...), Trailers: trailerSet{}, Body: c}, up, denc.Tentacle)
			Expect(err).To(MatchError(want))
			Expect([]any{req, got, c.reads}).To(Equal([]any{(*op.ChecksumRequest)(nil), nil, 0}))
		},
		Entry("an algorithm field of another type than the upload's", crc32c, op.ErrInvalidRequest, "x-amz-checksum-algorithm", "SHA256"),
		Entry("a lone value field of another type than the upload's", crc32c, op.ErrInvalidRequest, "x-amz-checksum-sha256", "v=="),
		Entry("a lone trailer of another type than the upload's", crc32c, op.ErrInvalidRequest, "x-amz-trailer", "x-amz-checksum-crc32"),
		Entry("a part of an xxh3 upload", op.UploadChecksum{Type: cksum.XXH3}, op.ErrNotImplemented),
		Entry("a part of a blake3 upload, whatever it names", op.UploadChecksum{Type: cksum.BLAKE3}, op.ErrNotImplemented,
			"x-amz-checksum-algorithm", "CRC32"),
		Entry("a part of an upload of main's md5, 9", op.UploadChecksum{Type: 9}, op.ErrNotImplemented),
		Entry("a part of an upload of main's xxhash64, 10", op.UploadChecksum{Type: 10}, op.ErrNotImplemented),
		Entry("a part of an upload of main's xxhash128, 11", op.UploadChecksum{Type: 11}, op.ErrNotImplemented),
		Entry("a part of an upload of type 65535", op.UploadChecksum{Type: 65535}, op.ErrNotImplemented),
	)

	DescribeTable("takes no upload type or flags on Squid, whose uploads have none, but serves the part's own",
		func(up op.UploadChecksum, fs []string, want *asked) {
			body := strings.NewReader(checksummedBody)
			req, got, err := op.ChecksumForPart(op.ChecksumInput{Field: checksumFields(fs...), Body: body}, up, denc.Squid)
			Expect(err).NotTo(HaveOccurred())
			Expect(askedOf(req, got)).To(Equal(want))
		},
		Entry("nothing, for no field", crc32c, nil, (*asked)(nil)),
		Entry("its own algorithm without the flags", crc32c, []string{"x-amz-checksum-algorithm", "SHA256"}, &asked{Type: cksum.SHA256}),
		Entry("its own value", crc32c, []string{"x-amz-checksum-sha1", "v=="},
			&asked{Type: cksum.SHA1, Expects: true, Value: "v==", Supplied: true}),
		Entry("nothing refused for a type no Squid upload has", op.UploadChecksum{Type: 9}, nil, (*asked)(nil)),
	)
})

var _ = Describe("ChecksumForCreate", func() {
	DescribeTable("takes the algorithm and the checksum type on Tentacle",
		func(want op.UploadChecksum, fs ...string) {
			Expect(op.ChecksumForCreate(op.ChecksumInput{Field: checksumFields(fs...)}, denc.Tentacle)).To(Equal(want))
		},
		Entry("none, for no field", op.UploadChecksum{}),
		Entry("a CRC full-object by default", op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagFullObject},
			"x-amz-checksum-algorithm", "CRC32"),
		Entry("any other type composite by default", op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite},
			"x-amz-checksum-algorithm", "sha256"),
		Entry("x-amz-sdk-checksum-algorithm", op.UploadChecksum{Type: cksum.SHA1, Flags: cksum.FlagComposite},
			"x-amz-sdk-checksum-algorithm", "SHA1"),
		Entry("a CRC made composite", op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagComposite},
			"x-amz-checksum-algorithm", "CRC32", "x-amz-checksum-type", "composite"),
		Entry("FULL_OBJECT in upper case", op.UploadChecksum{Type: cksum.CRC64NVME, Flags: cksum.FlagFullObject},
			"x-amz-checksum-algorithm", "CRC64NVME", "x-amz-checksum-type", "FULL_OBJECT"),
		Entry("no flag for a checksum type that is neither", op.UploadChecksum{Type: cksum.CRC32C},
			"x-amz-checksum-algorithm", "CRC32C", "x-amz-checksum-type", "FULL-OBJECT"),
		Entry("none, without an error, for a name that is no algorithm", op.UploadChecksum{},
			"x-amz-checksum-algorithm", "crc33"),
		Entry("none, without an error, for an empty name", op.UploadChecksum{}, "x-amz-checksum-algorithm", ""),
		Entry("a checksum type without an algorithm, kept as radosgw keeps it", op.UploadChecksum{Flags: cksum.FlagComposite},
			"x-amz-checksum-type", "COMPOSITE"),
		Entry("nothing from a value field or a trailer, which CreateMultipartUpload does not read", op.UploadChecksum{},
			"x-amz-checksum-crc32", "v==", "x-amz-trailer", "x-amz-checksum-sha256"),
	)

	DescribeTable("refuses a pair permitted_cksum_algo_and_type refuses with InvalidRequest on Tentacle",
		func(fs ...string) {
			_, err := op.ChecksumForCreate(op.ChecksumInput{Field: checksumFields(fs...)}, denc.Tentacle)
			Expect(err).To(MatchError(op.ErrInvalidRequest))
		},
		Entry("a full-object SHA256", "x-amz-checksum-algorithm", "SHA256", "x-amz-checksum-type", "full_object"),
		Entry("a composite CRC64NVME", "x-amz-checksum-algorithm", "CRC64NVME", "x-amz-checksum-type", "composite"),
		Entry("a SHA1 with no flag", "x-amz-checksum-algorithm", "SHA1", "x-amz-checksum-type", "other"),
	)

	It("reads nothing on Squid, whose uploads keep no checksum type", func() {
		for _, fs := range [][]string{
			{"x-amz-checksum-algorithm", "CRC32"},
			{"x-amz-checksum-algorithm", "MD5"},
			{"x-amz-checksum-algorithm", "SHA256", "x-amz-checksum-type", "full_object"},
		} {
			Expect(op.ChecksumForCreate(op.ChecksumInput{Field: checksumFields(fs...)}, denc.Squid)).To(BeZero(), "fields %q", fs)
		}
	})
})

var _ = Describe("SuppliedForComplete", func() {
	DescribeTable("is the one value field, of any type",
		func(want *asked, fs ...string) {
			req, err := op.SuppliedForComplete(op.ChecksumInput{Field: checksumFields(fs...)})
			Expect(err).NotTo(HaveOccurred())
			Expect(askedOf(req, nil)).To(Equal(want))
		},
		Entry("none, for no field", (*asked)(nil)),
		Entry("none, for an algorithm field, which a completion does not read", (*asked)(nil), "x-amz-checksum-algorithm", "CRC32"),
		Entry("CRC64NVME, which radosgw does not read", &asked{Type: cksum.CRC64NVME, Expects: true, Value: "v==", Supplied: true},
			"x-amz-checksum-crc64nvme", "v=="),
		Entry("SHA256 with its part count", &asked{Type: cksum.SHA256, Expects: true, Value: "v==-2", Supplied: true},
			"x-amz-checksum-sha256", "v==-2"),
		Entry("a name with no Tentacle type, as none, which no upload's type is", &asked{Expects: true, Value: "v==", Supplied: true},
			"x-amz-checksum-md5", "v=="),
	)

	It("refuses two value fields with InvalidRequest", func() {
		_, err := op.SuppliedForComplete(op.ChecksumInput{Field: checksumFields("x-amz-checksum-crc32", "a", "x-amz-checksum-crc64nvme", "b")})
		Expect(err).To(MatchError(op.ErrInvalidRequest))
	})
})

// refusedName is a checksum algorithm name a write refuses with
// InvalidArgument, and the form of the field that carries it.
type refusedName struct {
	name, form string
}

// fields are the request fields that carry n in its form.
func (n refusedName) fields() []string {
	switch n.form {
	case "a lone value field":
		return []string{"x-amz-checksum-" + n.name, "AAAAAA=="}
	case "x-amz-trailer":
		return []string{"x-amz-trailer", "x-amz-checksum-" + n.name}
	}
	return []string{n.form, n.name}
}

// refusedNameEntries are the entries of every refused name in every form.
func refusedNameEntries(names ...string) []TableEntry {
	var entries []TableEntry
	for _, name := range names {
		for _, form := range []string{"x-amz-checksum-algorithm", "x-amz-sdk-checksum-algorithm", "a lone value field", "x-amz-trailer"} {
			entries = append(entries, Entry(name+" as "+form, refusedName{name: name, form: form}))
		}
	}
	return entries
}

// The algorithms that have no Tentacle type, in several cases.
var foreignAlgorithms = []string{"MD5", "md5", "Md5", "XXHASH64", "xxhash64", "XXHASH128", "xxHash128"}

// The Tentacle algorithms rgw-go does not compute, under each name.
var uncomputedAlgorithms = []string{"XXHASH3", "xxhash3", "XXH3", "xxh3", "BLAKE3", "blake3"}

var _ = Describe("a checksum algorithm a v20.2.4 radosgw cannot read is refused at write", func() {
	for _, set := range []struct {
		what  string
		names []string
	}{
		{"MD5, XXHASH64 and XXHASH128, which have no Tentacle type", foreignAlgorithms},
		{"XXHASH3 and BLAKE3, which rgw-go does not compute", uncomputedAlgorithms},
	} {
		Context(set.what, func() {
			DescribeTable("PutObject refuses it with InvalidArgument on both releases, calling no store and reading no body",
				func(ctx SpecContext, n refusedName) {
					for _, rel := range bothReleases {
						f := newWriteFixture(ctx, rel)
						stub := &opfakes.FakeObjectStore{}
						f.env.Objects = stub
						c := &readCounter{r: strings.NewReader("hello")}
						o := &op.PutObject{
							Body: c, Size: 5, ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"),
							ChecksumField: checksumFields(n.fields()...),
						}
						Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument), "release %d", rel)
						Expect([]int{stub.PutObjectCallCount(), c.reads}).To(Equal([]int{0, 0}), "release %d", rel)
					}
				},
				refusedNameEntries(set.names...),
			)
			DescribeTable("UploadPart refuses it with InvalidArgument on both releases, calling no store and reading no body",
				func(ctx SpecContext, n refusedName) {
					for _, rel := range bothReleases {
						f := newMPFixture(ctx, rel)
						stub := &opfakes.FakeMultipartStore{}
						stub.GetUploadReturns(&op.Upload{ID: "2~x", Checksum: op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagFullObject}}, nil)
						f.env.Multipart = stub
						c := &readCounter{r: strings.NewReader("hello")}
						o := f.partOf("2~x", 1, "")
						o.Body, o.Size = c, 5
						o.ChecksumField = checksumFields(n.fields()...)
						Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument), "release %d", rel)
						Expect([]int{stub.PutPartCallCount(), c.reads}).To(Equal([]int{0, 0}), "release %d", rel)
					}
				},
				refusedNameEntries(set.names...),
			)
			DescribeTable("CreateMultipartUpload refuses it with InvalidArgument on Tentacle, calling no store",
				func(ctx SpecContext, n refusedName) {
					f := newMPFixture(ctx, denc.Tentacle)
					stub := &opfakes.FakeMultipartStore{}
					f.env.Multipart = stub
					o := &op.InitMultipart{ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"), ChecksumField: checksumFields(n.fields()...)}
					Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
					Expect(stub.CreateUploadCallCount()).To(BeZero())
				},
				refusedNameEntries(set.names...),
			)
		})
	}

	It("lets CreateMultipartUpload on Squid ignore the algorithm, creating an upload without a type", func(ctx SpecContext) {
		f := newMPFixture(ctx, denc.Squid)
		stub := &opfakes.FakeMultipartStore{}
		stub.CreateUploadReturns(&op.Upload{ID: "2~x"}, nil)
		f.env.Multipart = stub
		names := slices.Concat(foreignAlgorithms, uncomputedAlgorithms, []string{"CRC32"})
		for _, name := range names {
			o := &op.InitMultipart{ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"), ChecksumField: checksumFields("x-amz-checksum-algorithm", name)}
			Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", "k"))).To(Succeed(), "algorithm %s", name)
		}
		Expect(stub.CreateUploadCallCount()).To(Equal(len(names)))
		for i := range stub.CreateUploadCallCount() {
			_, _, _, p := stub.CreateUploadArgsForCall(i)
			Expect(p.Checksum).To(BeZero(), "upload %d", i)
		}
	})

	DescribeTable("a part, and a completion, of an upload whose stored type rgw-go does not compute answer NotImplemented on Tentacle, calling neither PutPart nor Complete",
		func(ctx SpecContext, t cksum.Type) {
			f := newMPFixture(ctx, denc.Tentacle)
			stub := &opfakes.FakeMultipartStore{}
			stub.GetUploadReturns(&op.Upload{ID: "2~x", Checksum: op.UploadChecksum{Type: t}}, nil)
			f.env.Multipart = stub
			for _, fs := range [][]string{nil, {"x-amz-checksum-algorithm", "CRC32"}} {
				c := &readCounter{r: strings.NewReader("hello")}
				o := f.partOf("2~x", 1, "")
				o.Body, o.Size, o.ChecksumField = c, 5, checksumFields(fs...)
				Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNotImplemented), "the part, fields %q", fs)
				Expect(c.reads).To(BeZero(), "the part's body, fields %q", fs)
			}
			cm := &op.CompleteMultipart{UploadID: "2~x"}
			Expect(op.Run(ctx, cm, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", "x"))))).To(MatchError(op.ErrNotImplemented), "the completion")
			Expect([]int{stub.PutPartCallCount(), stub.CompleteCallCount()}).To(Equal([]int{0, 0}))
		},
		Entry("xxh3, 3", cksum.XXH3),
		Entry("blake3, 7", cksum.BLAKE3),
		Entry("main's md5, 9", cksum.Type(9)),
		Entry("main's xxhash64, 10", cksum.Type(10)),
		Entry("main's xxhash128, 11", cksum.Type(11)),
	)
})

// askedNow is req with its value asked for at once, before the body is
// read, and held to the answer it got then.
func askedNow(req *op.ChecksumRequest) *op.ChecksumRequest {
	if req == nil || req.Expected == nil {
		return req
	}
	v, ok := req.Expected()
	early := *req
	early.Expected = func() (string, bool) { return v, ok }
	return &early
}

// askEarly is an object store that asks for a write's checksum value before
// it reads the body.
type askEarly struct{ op.ObjectStore }

func (s askEarly) PutObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, p op.PutParams) (*op.PutResult, error) {
	p.Checksum = askedNow(p.Checksum)
	return s.ObjectStore.PutObject(ctx, rec, key, body, p)
}

// askEarlyParts is a multipart store that asks for a part's checksum value
// before it reads the body.
type askEarlyParts struct{ op.MultipartStore }

func (s askEarlyParts) PutPart(ctx context.Context, up *op.Upload, n int, body io.Reader, p op.PutParams) (*op.PartResult, error) {
	p.Checksum = askedNow(p.Checksum)
	return s.MultipartStore.PutPart(ctx, up, n, body, p)
}

// initChecksumUpload starts alice's upload of k in plain with the checksum
// fields kv and returns its id.
func (f *writeFixture) initChecksumUpload(ctx context.Context, kv ...string) string {
	GinkgoHelper()
	o := &op.InitMultipart{ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"), ChecksumField: checksumFields(kv...)}
	Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", "k"))).To(Succeed())
	return o.UploadID
}

// trailerCase is a write whose CRC32C value the trailer x-amz-trailer lists
// supplies, through the request's trailers.
type trailerCase struct {
	// trailers are the request's, for the write's body.
	trailers func(body []byte) op.Trailers
	// early has the store ask for the value before it reads the body.
	early bool
	// want is the write's refusal, nil when the value is verified and stored.
	want error
}

func trailerCases() []TableEntry {
	crc32c := func(of string) func([]byte) op.Trailers {
		return func(body []byte) op.Trailers {
			if of != "" {
				body = []byte(of)
			}
			return trailerSet{"x-amz-checksum-crc32c": armorOf(cksum.CRC32C, body)}
		}
	}
	return []TableEntry{
		Entry("the value the request's trailers hold, verified once the body has ended", trailerCase{trailers: crc32c("")}),
		Entry("a value the request's trailers hold that is not the body's, BadDigest",
			trailerCase{trailers: crc32c("other"), want: op.ErrBadDigest}),
		Entry("a declared trailer the payload never sent, BadDigest",
			trailerCase{trailers: func([]byte) op.Trailers { return trailerSet{} }, want: op.ErrBadDigest}),
		Entry("a declared trailer of a payload that keeps none, BadDigest",
			trailerCase{trailers: func([]byte) op.Trailers { return nil }, want: op.ErrBadDigest}),
		Entry("the right value, asked for by a store before the body has ended, BadDigest",
			trailerCase{trailers: crc32c(""), early: true, want: op.ErrBadDigest}),
	}
}

// crc32cTrailer is the field lookup of a request that lists the trailer
// x-amz-checksum-crc32c.
func crc32cTrailer() func(string) (string, bool) {
	return checksumFields("x-amz-trailer", "x-amz-checksum-crc32c")
}

var _ = Describe("a checksum value from a trailer", func() {
	DescribeTable("is the request's trailer, on PutObject, and a write it cannot verify stores nothing",
		func(ctx SpecContext, c trailerCase) {
			body := []byte(checksummedBody)
			f := newWriteFixture(ctx, denc.Tentacle)
			if c.early {
				f.env.Objects = askEarly{f.store}
			}
			o := &op.PutObject{
				Body: bytes.NewReader(body), Size: int64(len(body)), ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"),
				ChecksumField: crc32cTrailer(),
			}
			r := f.req(http.MethodPut, "plain", "k")
			r.Trailers = c.trailers(body)
			err := op.Run(ctx, o, r)
			st := f.stat(ctx, "k")
			if c.want != nil {
				Expect(err).To(MatchError(c.want))
				Expect(st.Exists).To(BeFalse(), "nothing is stored")
				return
			}
			Expect(err).NotTo(HaveOccurred())
			want := recordOf(cksum.CRC32C, 0, body)
			Expect([]*cksum.Cksum{o.Checksum, st.Checksum}).To(Equal([]*cksum.Cksum{&want, &want}))
		},
		trailerCases(),
	)

	DescribeTable("is the request's trailer, on UploadPart, and a part it cannot verify stores nothing",
		func(ctx SpecContext, c trailerCase) {
			body := []byte(partBody)
			f := newMPFixture(ctx, denc.Tentacle)
			id := f.initChecksumUpload(ctx, "x-amz-checksum-algorithm", "CRC32C")
			if c.early {
				f.env.Multipart = askEarlyParts{f.store}
			}
			o := f.partOf(id, 1, "")
			o.Body, o.Size, o.ChecksumField = bytes.NewReader(body), int64(len(body)), crc32cTrailer()
			r := f.req(http.MethodPut, "plain", "k")
			r.Trailers = c.trailers(body)
			err := op.Run(ctx, o, r)
			parts := f.parts(ctx, "k", id)
			if c.want != nil {
				Expect(err).To(MatchError(c.want))
				Expect(parts).To(BeEmpty(), "nothing is stored")
				return
			}
			Expect(err).NotTo(HaveOccurred())
			want := recordOf(cksum.CRC32C, cksum.FlagFullObject, body)
			Expect(o.Checksum).To(Equal(&want))
			Expect(parts).To(HaveExactElements(HaveField("Checksum", Equal(&want))))
		},
		trailerCases(),
	)
})

var _ = Describe("PutObject's checksum", func() {
	put := func(ctx context.Context, f *writeFixture, kv ...string) (*op.PutObject, error) {
		o := &op.PutObject{
			Body: strings.NewReader(checksummedBody), Size: int64(len(checksummedBody)), ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"),
			ChecksumField: checksumFields(kv...),
		}
		return o, op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))
	}

	It("is verified and stored on both releases, the stored record reported", func(ctx SpecContext) {
		body := []byte(checksummedBody)
		for _, rel := range bothReleases {
			f := newWriteFixture(ctx, rel)
			o, err := put(ctx, f, "x-amz-checksum-crc32", armorOf(cksum.CRC32, body))
			Expect(err).NotTo(HaveOccurred(), "release %d", rel)
			want := recordOf(cksum.CRC32, 0, body)
			Expect([]*cksum.Cksum{o.Checksum, f.stat(ctx, "k").Checksum}).To(Equal([]*cksum.Cksum{&want, &want}), "release %d", rel)
		}
	})

	It("refuses a body whose checksum is not the value supplied with BadDigest, storing nothing", func(ctx SpecContext) {
		f := newWriteFixture(ctx, denc.Tentacle)
		_, err := put(ctx, f, "x-amz-checksum-algorithm", "SHA256", "x-amz-checksum-sha256", armorOf(cksum.SHA256, []byte("other")))
		Expect(err).To(MatchError(op.ErrBadDigest))
		Expect(f.stat(ctx, "k").Exists).To(BeFalse())
	})

	It("is asked of the store with the request's other params", func(ctx SpecContext) {
		body := []byte(checksummedBody)
		f := newWriteFixture(ctx, denc.Tentacle)
		stub := &opfakes.FakeObjectStore{}
		stub.PutObjectReturns(&op.PutResult{Checksum: &cksum.Cksum{Type: cksum.SHA1}}, nil)
		f.env.Objects = stub
		o, err := put(ctx, f, "x-amz-checksum-algorithm", "SHA1")
		Expect(err).NotTo(HaveOccurred())
		_, _, _, got, p := stub.PutObjectArgsForCall(0)
		Expect(got).To(BeIdenticalTo(o.Body), "the body, which no trailer supplies a value for")
		Expect(askedOf(p.Checksum, nil)).To(Equal(&asked{Type: cksum.SHA1}))
		Expect([]any{p.Tag, p.Size}).To(Equal([]any{writeReqID, int64(len(body))}))
		Expect(o.Checksum).To(Equal(&cksum.Cksum{Type: cksum.SHA1}), "the store's result")
	})

	It("is read after authorization: a refused requester learns nothing of the fields", func(ctx SpecContext) {
		f := newWriteFixture(ctx, denc.Tentacle)
		o := &op.PutObject{Body: strings.NewReader("x"), Size: 1, ChecksumField: checksumFields("x-amz-checksum-algorithm", "MD5")}
		r := f.req(http.MethodPut, "plain", "k")
		r.Identity = f.bob
		Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
	})
})

// partBody is a part's body.
const partBody = "a part to checksum"

var _ = Describe("UploadPart's checksum", func() {
	part := func(ctx context.Context, f *writeFixture, id string, c io.Reader, kv ...string) (*op.UploadPart, error) {
		o := f.partOf(id, 1, "")
		o.Body, o.Size, o.ChecksumField = c, int64(len(partBody)), checksumFields(kv...)
		return o, op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))
	}

	It("takes its upload's type and flags on Tentacle, as its record keeps them", func(ctx SpecContext) {
		body := []byte(partBody)
		f := newMPFixture(ctx, denc.Tentacle)
		id := f.initChecksumUpload(ctx, "x-amz-checksum-algorithm", "CRC32C")
		o, err := part(ctx, f, id, bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		want := recordOf(cksum.CRC32C, cksum.FlagFullObject, body)
		Expect(o.Checksum).To(Equal(&want))
		Expect(f.parts(ctx, "k", id)).To(HaveExactElements(HaveField("Checksum", Equal(&want))))
	})

	It("refuses a part of another type than its upload's with InvalidRequest on Tentacle, reading no body and storing nothing", func(ctx SpecContext) {
		body := []byte(partBody)
		f := newMPFixture(ctx, denc.Tentacle)
		id := f.initChecksumUpload(ctx, "x-amz-checksum-algorithm", "CRC32C")
		c := &readCounter{r: bytes.NewReader(body)}
		_, err := part(ctx, f, id, c, "x-amz-checksum-sha256", armorOf(cksum.SHA256, body))
		Expect(err).To(MatchError(op.ErrInvalidRequest))
		Expect(c.reads).To(BeZero())
		Expect(f.parts(ctx, "k", id)).To(BeEmpty())
	})

	It("takes no upload type on Squid, but verifies the part's own value", func(ctx SpecContext) {
		body := []byte(partBody)
		f := newMPFixture(ctx, denc.Squid)
		id := f.initChecksumUpload(ctx, "x-amz-checksum-algorithm", "CRC32C")
		o, err := part(ctx, f, id, bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Checksum).To(BeNil(), "a part naming no algorithm")
		o, err = part(ctx, f, id, bytes.NewReader(body), "x-amz-checksum-sha256", armorOf(cksum.SHA256, body))
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Checksum).To(Equal(new(recordOf(cksum.SHA256, 0, body))), "a part naming its own")
		_, err = part(ctx, f, id, bytes.NewReader(body), "x-amz-checksum-sha256", armorOf(cksum.SHA256, []byte("other")))
		Expect(err).To(MatchError(op.ErrBadDigest), "a part whose value is wrong")
	})

	It("answers NoSuchUpload for a missing upload before it reads a checksum field", func(ctx SpecContext) {
		body := []byte(partBody)
		for _, rel := range bothReleases {
			f := newMPFixture(ctx, rel)
			_, err := part(ctx, f, "2~nope", bytes.NewReader(body), "x-amz-checksum-algorithm", "MD5")
			Expect(err).To(MatchError(op.ErrNoSuchUpload), "release %d", rel)
		}
	})

	It("hands the store the upload's placement and type with the part's request", func(ctx SpecContext) {
		body := []byte(partBody)
		f := newMPFixture(ctx, denc.Tentacle)
		stub := &opfakes.FakeMultipartStore{}
		uc := op.UploadChecksum{Type: cksum.SHA1, Flags: cksum.FlagComposite}
		dest := meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}
		stub.GetUploadReturns(&op.Upload{ID: "2~x", Placement: dest, Checksum: uc}, nil)
		stub.PutPartReturns(&op.PartResult{}, nil)
		f.env.Multipart = stub
		o, err := part(ctx, f, "2~x", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(stub.GetUploadCallCount()).To(Equal(1), "get_info, once")
		_, rec, key, id := stub.GetUploadArgsForCall(0)
		Expect([]any{rec.Info.Bucket.Name, key, id}).To(Equal([]any{"plain", meta.ObjKey{Name: "k"}, "2~x"}))
		_, up, _, got, p := stub.PutPartArgsForCall(0)
		Expect(up.Placement).To(Equal(dest), "the destination placement get_info read, which the part is written to")
		Expect(up.Checksum).To(Equal(uc))
		Expect(got).To(BeIdenticalTo(o.Body))
		Expect(askedOf(p.Checksum, nil)).To(Equal(&asked{Type: cksum.SHA1, Flags: cksum.FlagComposite}))
	})

	It("hands CopyPart the placement, checksum type and flags of the upload it read", func(ctx SpecContext) {
		f := newMPFixture(ctx, denc.Tentacle)
		f.put(ctx, "src", []byte(partBody))
		stub := &opfakes.FakeMultipartStore{}
		uc := op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}
		dest := meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}
		stub.GetUploadReturns(&op.Upload{ID: "2~x", Placement: dest, Checksum: uc}, nil)
		stub.CopyPartReturns(&op.PartResult{}, nil)
		f.env.Multipart = stub
		o := f.copyPartOf("2~x", 1, "src", "")
		o.ChecksumField = checksumFields("x-amz-checksum-algorithm", "CRC32")
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		Expect(stub.CopyPartCallCount()).To(Equal(1))
		_, up, _, _, _ := stub.CopyPartArgsForCall(0)
		Expect(up.Placement).To(Equal(dest))
		Expect(up.Checksum).To(Equal(uc), "the upload's, not the copy request's")
	})

	DescribeTable("UploadPartCopy reads no checksum field, its part taking its upload's type",
		func(ctx SpecContext, kv ...string) {
			body := []byte(partBody)
			f := newMPFixture(ctx, denc.Tentacle)
			id := f.initChecksumUpload(ctx, "x-amz-checksum-algorithm", "CRC32C")
			f.put(ctx, "src", body)
			o := f.copyPartOf(id, 1, "src", "")
			o.ChecksumField = checksumFields(kv...)
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			want := recordOf(cksum.CRC32C, cksum.FlagFullObject, body)
			Expect(o.Checksum).To(Equal(&want))
			Expect(f.parts(ctx, "k", id)).To(HaveExactElements(HaveField("Checksum", Equal(&want))))
		},
		Entry("an algorithm of another type and a wrong value", "x-amz-checksum-algorithm", "SHA256", "x-amz-checksum-sha256", "AAAAAA=="),
		Entry("an algorithm UploadPart refuses, MD5", "x-amz-checksum-algorithm", "MD5"),
		Entry("an algorithm UploadPart refuses, BLAKE3", "x-amz-checksum-algorithm", "BLAKE3"),
	)
})

var _ = Describe("CreateMultipartUpload's checksum", func() {
	It("gives the upload its type and flags on Tentacle and nothing on Squid", func(ctx SpecContext) {
		for _, c := range []struct {
			rel  denc.Release
			want op.UploadChecksum
		}{
			{denc.Squid, op.UploadChecksum{}},
			{denc.Tentacle, op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagComposite}},
		} {
			rel, want := c.rel, c.want
			f := newMPFixture(ctx, rel)
			o := &op.InitMultipart{
				ACL:           acl.DefaultPolicy(f.alice.Owner, "Alice"),
				ChecksumField: checksumFields("x-amz-checksum-algorithm", "CRC32", "x-amz-checksum-type", "COMPOSITE"),
			}
			Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", "k"))).To(Succeed(), "release %d", rel)
			up, err := f.upload(ctx, "k", o.UploadID)
			Expect(err).NotTo(HaveOccurred())
			Expect([]op.UploadChecksum{o.Upload.Checksum, up.Checksum}).To(Equal([]op.UploadChecksum{want, want}), "release %d", rel)
		}
	})

	It("refuses a type its algorithm does not permit with InvalidRequest, creating nothing", func(ctx SpecContext) {
		f := newMPFixture(ctx, denc.Tentacle)
		stub := &opfakes.FakeMultipartStore{}
		f.env.Multipart = stub
		o := &op.InitMultipart{
			ACL:           acl.DefaultPolicy(f.alice.Owner, "Alice"),
			ChecksumField: checksumFields("x-amz-checksum-algorithm", "SHA256", "x-amz-checksum-type", "FULL_OBJECT"),
		}
		Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", "k"))).To(MatchError(op.ErrInvalidRequest))
		Expect(stub.CreateUploadCallCount()).To(BeZero())
	})
})

var _ = Describe("CompleteMultipartUpload's checksum", func() {
	var (
		f            *writeFixture
		id           string
		whole, doc   string
		first, other []byte
	)
	// startOf is an upload of two parts on rel, created with the checksum
	// fields kv.
	startOf := func(ctx context.Context, rel denc.Release, kv ...string) {
		GinkgoHelper()
		f = newMPFixture(ctx, rel)
		id = f.initChecksumUpload(ctx, kv...)
		first, other = bytes.Repeat([]byte("one "), 5<<20/4), []byte("two")
		e1, e2 := f.uploadPart(ctx, "k", id, 1, first), f.uploadPart(ctx, "k", id, 2, other)
		whole = string(append(append([]byte{}, first...), other...))
		doc = completeDoc(xmlPart("1", e1), xmlPart("2", e2))
	}
	// start is a CRC64NVME upload of two parts on rel.
	start := func(ctx context.Context, rel denc.Release) {
		GinkgoHelper()
		startOf(ctx, rel, "x-amz-checksum-algorithm", "CRC64NVME")
	}
	complete := func(ctx context.Context, kv ...string) (*op.CompleteMultipart, error) {
		o := &op.CompleteMultipart{UploadID: id, ChecksumField: checksumFields(kv...)}
		return o, op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", doc))
	}

	It("compares a CRC64NVME value and stores the combined checksum on Tentacle", func(ctx SpecContext) {
		start(ctx, denc.Tentacle)
		o, err := complete(ctx, "x-amz-checksum-crc64nvme", armorOf(cksum.CRC64NVME, []byte(whole)))
		Expect(err).NotTo(HaveOccurred())
		want := recordOf(cksum.CRC64NVME, cksum.FlagV2|cksum.FlagCombined|cksum.FlagFullObject, []byte(whole))
		Expect([]*cksum.Cksum{o.Checksum, f.stat(ctx, "k").Checksum}).To(Equal([]*cksum.Cksum{&want, &want}))
	})

	DescribeTable("refuses on Tentacle, writing nothing",
		func(ctx SpecContext, want error, value func() []string) {
			start(ctx, denc.Tentacle)
			_, err := complete(ctx, value()...)
			Expect(err).To(MatchError(want))
			Expect(f.stat(ctx, "k").Exists).To(BeFalse())
			_, err = f.upload(ctx, "k", id)
			Expect(err).NotTo(HaveOccurred(), "the upload is still in progress")
		},
		Entry("a wrong CRC64NVME value, with BadDigest", op.ErrBadDigest, func() []string {
			return []string{"x-amz-checksum-crc64nvme", armorOf(cksum.CRC64NVME, first)}
		}),
		Entry("a value of another type, with BadDigest", op.ErrBadDigest, func() []string {
			return []string{"x-amz-checksum-crc32", armorOf(cksum.CRC32, []byte(whole))}
		}),
		Entry("a value of a type with no Tentacle id, with BadDigest", op.ErrBadDigest, func() []string {
			return []string{"x-amz-checksum-md5", "AAAAAAAAAAAAAAAAAAAAAA=="}
		}),
		Entry("two values, with InvalidRequest", op.ErrInvalidRequest, func() []string {
			return []string{"x-amz-checksum-crc64nvme", armorOf(cksum.CRC64NVME, []byte(whole)), "x-amz-checksum-crc32", "AAAAAA=="}
		}),
	)

	It("answers a completion retried after it succeeded", func(ctx SpecContext) {
		start(ctx, denc.Tentacle)
		value := armorOf(cksum.CRC64NVME, []byte(whole))
		first, err := complete(ctx, "x-amz-checksum-crc64nvme", value)
		Expect(err).NotTo(HaveOccurred())
		again, err := complete(ctx, "x-amz-checksum-crc64nvme", value)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.ETag).To(Equal(first.ETag))
	})

	It("ignores the supplied values of an upload without a checksum type on Tentacle, which combines nothing", func(ctx SpecContext) {
		startOf(ctx, denc.Tentacle)
		o, err := complete(ctx, "x-amz-checksum-crc64nvme", "AAAAAAAAAAA=", "x-amz-checksum-crc32", "AAAAAA==")
		Expect(err).NotTo(HaveOccurred(), "two values, neither compared")
		Expect(o.Checksum).To(BeNil())
	})

	It("answers an upload read's failure on Tentacle, completing nothing", func(ctx SpecContext) {
		start(ctx, denc.Tentacle)
		stub := &opfakes.FakeMultipartStore{}
		stub.GetUploadReturns(nil, op.ErrServiceUnavailable)
		f.env.Multipart = stub
		_, err := complete(ctx, "x-amz-checksum-crc64nvme", armorOf(cksum.CRC64NVME, []byte(whole)))
		Expect(err).To(MatchError(op.ErrServiceUnavailable))
		Expect(stub.CompleteCallCount()).To(BeZero())
	})

	It("ignores the supplied value on Squid, whose uploads combine nothing", func(ctx SpecContext) {
		start(ctx, denc.Squid)
		stub := &countingUploads{MultipartStore: f.store}
		f.env.Multipart = stub
		o, err := complete(ctx, "x-amz-checksum-crc64nvme", "AAAAAAAAAAA=", "x-amz-checksum-crc32", "AAAAAA==")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Checksum).To(BeNil())
		Expect(stub.gets).To(BeZero(), "the upload's info is not read for its type")
	})
})

// countingUploads counts its GetUpload calls.
type countingUploads struct {
	op.MultipartStore
	gets int
}

func (c *countingUploads) GetUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, id string) (*op.Upload, error) {
	c.gets++
	return c.MultipartStore.GetUpload(ctx, rec, key, id)
}
