package tags_test

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// multimap writes a std::multimap<string,string> body: a u32 count, then each
// key and value, in the order given.
func multimap(e *denc.Encoder, kv ...string) {
	e.U32(uint32(len(kv) / 2))
	for _, s := range kv {
		e.String(s)
	}
}

// tagsBytes writes RGWObjTags as its encode does (rgw_tag.h:26-30):
// ENCODE_START(v, compat) around the multimap.
func tagsBytes(v, compat uint8, kv ...string) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(v, compat)
	multimap(e, kv...)
	e.EndStruct(f)
	return e.Bytes()
}

func encode(s tags.Set) []byte {
	e := denc.NewEncoder()
	s.Encode(e, denc.Squid)
	return e.Bytes()
}

// decodeWhole decodes b and requires that it succeed and consume every byte.
func decodeWhole(b []byte) tags.Set {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	s := tags.Decode(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	Expect(d.Remaining()).To(BeZero())
	return s
}

// fill returns a set holding n distinct tags, each added with limit.
func fill(n, limit int) tags.Set {
	GinkgoHelper()
	var s tags.Set
	for i := range n {
		Expect(s.Add(fmt.Sprintf("k%02d", i), "v", limit)).To(Succeed())
	}
	return s
}

// interleaved returns n tags whose keys cycle through c, a and b, each value
// its tag's position, and the order a std::multimap holds them in: by key,
// equal keys in insertion order. It is built without sorting, so a sort that
// moves equal keys cannot also produce it.
func interleaved(n int) ([]tags.Tag, []tags.Tag) {
	keys := []string{"c", "a", "b"}
	in := make([]tags.Tag, n)
	for i := range in {
		in[i] = tags.Tag{Key: keys[i%len(keys)], Value: fmt.Sprintf("%03d", i)}
	}
	var want []tags.Tag
	for _, k := range []string{"a", "b", "c"} {
		for _, t := range in {
			if t.Key == k {
				want = append(want, t)
			}
		}
	}
	return in, want
}

// headerOf returns n distinct "key=value" items joined by "&".
func headerOf(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("k%02d=v", i)
	}
	return strings.Join(items, "&")
}

var _ = Describe("Attr", func() {
	It("is RGW_ATTR_TAGS", func() {
		Expect(tags.Attr).To(Equal("user.rgw.x-amz-tagging"))
	})
})

var _ = Describe("Set.Add", func() {
	DescribeTable("refuses what check_and_add_tag refuses",
		func(existing int, key, value string, limit int) {
			s := fill(existing, limit)
			Expect(s.Add(key, value, limit)).To(MatchError(tags.ErrInvalidTag))
			Expect(s.Len()).To(Equal(existing))
		},
		Entry("an empty key", 0, "", "v", tags.MaxObjectTags),
		Entry("a 129-byte key", 0, strings.Repeat("k", 129), "v", tags.MaxObjectTags),
		Entry("a 257-byte value", 0, "k", strings.Repeat("v", 257), tags.MaxObjectTags),
		Entry("an eleventh tag at MaxObjectTags", 10, "k", "v", tags.MaxObjectTags),
		Entry("a 51st tag at MaxBucketTags", 50, "k", "v", tags.MaxBucketTags),
	)
	DescribeTable("accepts each limit's largest value",
		func(existing int, key, value string, limit int) {
			s := fill(existing, limit)
			Expect(s.Add(key, value, limit)).To(Succeed())
			Expect(s.Len()).To(Equal(existing + 1))
			Expect(s.Tags).To(ContainElement(tags.Tag{Key: key, Value: value}))
		},
		Entry("a 128-byte key", 0, strings.Repeat("k", 128), "v", tags.MaxObjectTags),
		Entry("a 256-byte value", 0, "k", strings.Repeat("v", 256), tags.MaxObjectTags),
		Entry("an empty value", 0, "k", "", tags.MaxObjectTags),
		Entry("a tenth tag at MaxObjectTags", 9, "k", "v", tags.MaxObjectTags),
		Entry("a 50th tag at MaxBucketTags", 49, "k", "v", tags.MaxBucketTags),
	)
	It("keeps multimap order: sorted by key, equal keys in insertion order", func() {
		var s tags.Set
		for _, kv := range [][2]string{{"b", "1"}, {"a", "x"}, {"b", "2"}, {"a", "y"}, {"B", "0"}} {
			Expect(s.Add(kv[0], kv[1], tags.MaxObjectTags)).To(Succeed())
		}
		Expect(s.Tags).To(Equal([]tags.Tag{
			{Key: "B", Value: "0"},
			{Key: "a", Value: "x"},
			{Key: "a", Value: "y"},
			{Key: "b", Value: "1"},
			{Key: "b", Value: "2"},
		}))
		Expect(s.Len()).To(Equal(5))
		Expect(encode(s)).To(Equal(tagsBytes(1, 1, "B", "0", "a", "x", "a", "y", "b", "1", "b", "2")))
	})
})

var _ = Describe("Set.Encode", func() {
	It("writes ENCODE_START(1, 1) around the multimap", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: "k1", Value: "v1"}, {Key: "k2", Value: "v2"}}}
		Expect(encode(s)).To(Equal([]byte{
			1, 1, 0x1c, 0, 0, 0, 2, 0, 0, 0,
			2, 0, 0, 0, 'k', '1', 2, 0, 0, 0, 'v', '1',
			2, 0, 0, 0, 'k', '2', 2, 0, 0, 0, 'v', '2',
		}))
	})
	It("writes the empty set as a zero count", func() {
		Expect(encode(tags.Set{})).To(Equal([]byte{1, 1, 4, 0, 0, 0, 0, 0, 0, 0}))
	})
	It("writes a set built out of order in multimap order", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: "b", Value: "1"}, {Key: "a", Value: "2"}, {Key: "b", Value: "0"}}}
		Expect(encode(s)).To(Equal(tagsBytes(1, 1, "a", "2", "b", "1", "b", "0")))
		Expect(s.Tags[0]).To(Equal(tags.Tag{Key: "b", Value: "1"}), "Encode must not reorder the caller's slice")
	})
})

var _ = Describe("Decode", func() {
	It("reads the ENCODE_START(1, 1) form", func() {
		Expect(decodeWhole(tagsBytes(1, 1, "k1", "v1", "k2", "v2"))).To(Equal(tags.Set{Tags: []tags.Tag{
			{Key: "k1", Value: "v1"}, {Key: "k2", Value: "v2"},
		}}))
	})
	It("inserts stored entries in multimap order", func() {
		Expect(decodeWhole(tagsBytes(1, 1, "b", "1", "a", "x", "b", "2", "a", "y"))).To(Equal(tags.Set{Tags: []tags.Tag{
			{Key: "a", Value: "x"}, {Key: "a", Value: "y"}, {Key: "b", Value: "1"}, {Key: "b", Value: "2"},
		}}))
	})
	It("keeps equal keys in stored order across a large set", func() {
		in, want := interleaved(300)
		kv := make([]string, 0, 2*len(in))
		for _, t := range in {
			kv = append(kv, t.Key, t.Value)
		}
		Expect(decodeWhole(tagsBytes(1, 1, kv...)).Tags).To(Equal(want))
	})
	It("stops at the struct end and leaves what follows it", func() {
		b := append(tagsBytes(1, 1, "k", "v"), 0xAB)
		d := denc.NewDecoder(b)
		Expect(tags.Decode(d)).To(Equal(tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}))
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(d.U8()).To(Equal(uint8(0xAB)))
	})
	It("skips what a later version appends, as DECODE_FINISH does", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(2, 1)
		multimap(e, "k", "v")
		e.Raw([]byte{0xAA, 0xBB})
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes())).To(Equal(tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}))
	})
	It("reads a struct_v 0 encoding, which carries neither compat nor length", func() {
		e := denc.NewEncoder()
		e.U8(0)
		multimap(e, "k", "v")
		Expect(decodeWhole(e.Bytes())).To(Equal(tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}))
	})

	Context("when the binary form does not decode", func() {
		It("reads the whole buffer as the URL-encoded form", func() {
			Expect(decodeWhole([]byte("k1=v1&k2=v%202"))).To(Equal(tags.Set{Tags: []tags.Tag{
				{Key: "k1", Value: "v1"}, {Key: "k2", Value: "v 2"},
			}}))
		})
		It("strips trailing NULs first", func() {
			Expect(decodeWhole([]byte("k=v\x00\x00"))).To(Equal(tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}))
		})
		It("reads the header bytes it already consumed as part of the text", func() {
			b := tagsBytes(1, 2)
			Expect(decodeWhole(b)).To(Equal(tags.Set{Tags: []tags.Tag{{Key: "\x01\x02\x04"}}}),
				"an incompatible struct falls back like any other decode failure")
		})
		It("falls back when the multimap overruns the struct length", func() {
			b := tagsBytes(1, 1, "k", "v")
			b[2]-- // struct_len one byte short of the multimap
			Expect(decodeWhole(b)).To(HaveField("Tags", HaveLen(1)))
		})
		DescribeTable("fails with the binary decoder's error",
			func(b []byte, want error) {
				d := denc.NewDecoder(b)
				Expect(tags.Decode(d)).To(Equal(tags.Set{}))
				Expect(d.Err()).To(MatchError(want))
			},
			Entry("on an empty buffer", []byte{}, denc.ErrShortBuffer),
			Entry("when only NULs remain", []byte{0, 0}, denc.ErrShortBuffer),
			Entry("when the text holds more than MaxObjectTags tags", []byte(headerOf(11)), denc.ErrIncompatible),
			Entry("when the text holds an empty key", []byte("k1=v1&=v2"), denc.ErrIncompatible),
		)
		It("decodes a text of exactly MaxObjectTags tags", func() {
			Expect(decodeWhole([]byte(headerOf(10)))).To(HaveField("Tags", HaveLen(10)))
		})
	})

	It("returns the zero set from a decoder that already failed", func() {
		d := denc.NewDecoder(nil)
		d.U8()
		Expect(tags.Decode(d)).To(Equal(tags.Set{}))
		Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
	})
})

var _ = Describe("ParseHeader", func() {
	It("splits on & and =, URL-decodes each side and takes a bare key as an empty value", func() {
		s, err := tags.ParseHeader("a=1&b&c=%26", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Tags).To(Equal([]tags.Tag{{Key: "a", Value: "1"}, {Key: "b", Value: ""}, {Key: "c", Value: "&"}}))
	})
	It("returns the empty set for an empty header", func() {
		s, err := tags.ParseHeader("", tags.MaxObjectTags)
		Expect(err).NotTo(HaveOccurred())
		Expect(s).To(Equal(tags.Set{}))
	})
	It("accepts MaxObjectTags items and refuses one more", func() {
		s, err := tags.ParseHeader(headerOf(10), tags.MaxObjectTags)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Len()).To(Equal(10))
		s, err = tags.ParseHeader(headerOf(11), tags.MaxObjectTags)
		Expect(err).To(MatchError(tags.ErrInvalidTag))
		Expect(s).To(Equal(tags.Set{}))
	})
	DescribeTable("refuses an item check_and_add_tag refuses",
		func(h string) {
			_, err := tags.ParseHeader(h, tags.MaxObjectTags)
			Expect(err).To(MatchError(tags.ErrInvalidTag))
		},
		Entry("an empty item between two others", "a=1&&b=2"),
		Entry("a trailing &", "a=1&"),
		Entry("an empty key", "=v"),
		Entry("a key whose escape is not hex", "%zz=v"),
		Entry("a 129-byte key once decoded", strings.Repeat("%41", 129)+"=v"),
	)
	DescribeTable("decodes each side as url_decode does",
		func(h string, want tags.Tag) {
			s, err := tags.ParseHeader(h, tags.MaxObjectTags)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Tags).To(Equal([]tags.Tag{want}))
		},
		Entry("hex digits in either case", "%4b%4B=%7e", tags.Tag{Key: "KK", Value: "~"}),
		Entry("a + kept as a plus", "a+b=c+d", tags.Tag{Key: "a+b", Value: "c+d"}),
		Entry("a + after a ? read as a space", "k=q?a+b", tags.Tag{Key: "k", Value: "q?a b"}),
		Entry("a ? in the key leaves the value's + alone", "k?=a+b", tags.Tag{Key: "k?", Value: "a+b"}),
		Entry("an = after the first kept in the value", "k=a=b", tags.Tag{Key: "k", Value: "a=b"}),
		Entry("an escape cut short ending the side", "k=ab%4", tags.Tag{Key: "k", Value: "ab"}),
		Entry("a lone % ending the side", "k=ab%", tags.Tag{Key: "k", Value: "ab"}),
		Entry("a value whose escape is not hex read as empty", "k=abc%zz", tags.Tag{Key: "k", Value: ""}),
		Entry("an escaped NUL kept", "k=%00", tags.Tag{Key: "k", Value: "\x00"}),
	)
})

var _ = Describe("Set.MarshalJSON", func() {
	It("dumps the tags as one tagset object", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: "key1", Value: "val1"}, {Key: "key2", Value: "val2"}}}
		Expect(json.Marshal(s)).To(MatchJSON(`{"tagset": {"key1": "val1", "key2": "val2"}}`))
	})
	It("dumps the empty set as an empty tagset", func() {
		Expect(json.Marshal(tags.Set{})).To(MatchJSON(`{"tagset": {}}`))
	})
	It("leaves <, > and & as they are, as json_stream_escaper does", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: "a<b", Value: "c&d>"}}}
		Expect(s.MarshalJSON()).To(Equal([]byte(`{"tagset":{"a<b":"c&d>"}}`)))
		Expect(json.Marshal(s)).To(Equal([]byte(`{"tagset":{"a\u003cb":"c\u0026d\u003e"}}`)),
			"json.Marshal escapes for HTML what MarshalJSON returns")
	})
	It("writes in another form the characters its comment names, as the same string", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: "k", Value: "\r\b\f\x7f\u2028\u2029"}}}
		b, err := s.MarshalJSON()
		Expect(err).NotTo(HaveOccurred())
		Expect(b).To(Equal([]byte(`{"tagset":{"k":"\r\b\f`+"\x7f"+`\u2028\u2029"}}`)),
			"json_stream_escaper writes \\u000d\\u0008\\u000c\\u007f and copies U+2028 and U+2029")
		Expect(b).To(MatchJSON(`{"tagset":{"k":"\u000d\u0008\u000c\u007f` + "\u2028\u2029" + `"}}`))
	})
	It("repeats a key that occurs twice, as dump does", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: "k", Value: "2"}, {Key: "a", Value: "1"}, {Key: "k", Value: "3"}}}
		Expect(json.Marshal(s)).To(Equal([]byte(`{"tagset":{"a":"1","k":"2","k":"3"}}`)))
	})
})
