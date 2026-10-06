package admin_test

import (
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("ParseArgs", func() {
	It("keeps query order and the last value of a repeated key", func() {
		a := admin.ParseArgs("uid=a&uid=b&key&quota&x=1%202")
		v, ok := a.String("uid", "")
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("b"), "RGWHTTPArgs::append assigns: val_map[name] = val, rgw_common.cc:923")
		v, _ = a.String("x", "")
		Expect(v).To(Equal("1 2"))
		Expect(a.SubResource()).To(Equal("key"), "the first admin sub-resource in the query wins")
		Expect(admin.ParseArgs("quota&key").SubResource()).To(Equal("quota"))
		Expect(admin.ParseArgs("uid=a").SubResource()).To(BeEmpty())
		Expect(admin.ParseArgs("acl&caps").SubResource()).To(Equal("caps"), "an S3 sub-resource is not an admin one")
	})
	It("decodes each pair whole, so an escaped '=' separates and '+' is a space", func() {
		a := admin.ParseArgs("a%3Db=c&d=e+f&X-Amz-Date=1")
		v, ok := a.Get("a")
		Expect([]any{v, ok}).To(Equal([]any{"b=c", true}))
		v, _ = a.Get("d")
		Expect(v).To(Equal("e f"))
		Expect(a.Has("x-amz-date")).To(BeTrue(), "a name holding X-Amz- is lowercased but for its dashes")
		Expect(admin.ParseArgs("X-Amz-%FF=1").Has("x-amz-\xff")).To(BeTrue(), "tolower leaves a byte past ASCII alone")
		_, ok = a.Get("absent")
		Expect(ok).To(BeFalse())
		sys := admin.ParseArgs("rgwx-uid=u&uid=v")
		Expect(sys.Has("rgwx-uid")).To(BeFalse(), "append keeps an rgwx- name in sys_val_map, which get and exists never read")
		v, _ = sys.Get("uid")
		Expect(v).To(Equal("v"))
	})
	It("decodes a value again in String, as RESTArgs::get_string does, and once in Get", func() {
		a := admin.ParseArgs("uid=a%252Bb&display-name=x%2B")
		v, _ := a.String("uid", "")
		Expect(v).To(Equal("a+b"), "%252B decodes to %2B, then to +")
		v, _ = a.Get("uid")
		Expect(v).To(Equal("a%2Bb"))
		v, _ = a.String("display-name", "")
		Expect(v).To(Equal("x "), "%2B decodes to +, which the second decode reads as a space")
		v, ok := a.String("absent", "def")
		Expect([]any{v, ok}).To(Equal([]any{"def", false}))
	})
	DescribeTable("Bool is RESTArgs::get_bool",
		func(q string, def, want, wantPresent, wantErr bool) {
			v, present, err := admin.ParseArgs(q).Bool("suspended", def)
			Expect(present).To(Equal(wantPresent))
			if wantErr {
				Expect(err).To(MatchError(op.ErrInvalidArgument))
				Expect(v).To(Equal(def), "the default survives a bad value")
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal(want))
		},
		Entry("absent", "uid=x", false, false, false, false),
		Entry("empty is true", "suspended", false, true, true, false),
		Entry("true", "suspended=True", false, true, true, false),
		Entry("1", "suspended=1", false, true, true, false),
		Entry("false", "suspended=FALSE", true, false, true, false),
		Entry("0", "suspended=0", true, false, true, false),
		Entry("yes is invalid", "suspended=yes", true, true, true, true),
		Entry("strcasecmp folds ASCII alone", "suspended=fal%C5%BFe", true, true, true, true),
		Entry("text after a NUL is not compared", "suspended=true%00x", false, true, true, false),
	)
	// rgw_string.h:38-100 at v19.2.6 and v20.2.4: strtoll and its kin, then a
	// refusal of the type's maximum and of trailing text, and nothing else.
	DescribeTable("Int64 is stringtoll",
		func(q string, want int64, wantErr bool) {
			v, present, err := admin.ParseArgs(q).Int64("n", 7)
			if wantErr {
				Expect(err).To(MatchError(op.ErrInvalidArgument))
				Expect(v).To(Equal(int64(7)), "a refused value leaves the default")
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(present).To(Equal(q != ""))
			Expect(v).To(Equal(want))
		},
		Entry("absent takes the default", "", int64(7), false),
		Entry("decimal", "n=-12", int64(-12), false),
		Entry("a query plus is a space, which strtoll skips", "n=+%2012", int64(12), false),
		Entry("leading space and a plus sign", "n=%20%2B12", int64(12), false),
		Entry("trailing text", "n=12x", int64(0), true),
		Entry("empty is zero, as strtoll leaves nothing behind", "n=", int64(0), false),
		Entry("a sign alone", "n=-", int64(0), true),
		Entry("LLONG_MAX itself is refused", "n=9223372036854775807", int64(0), true),
		Entry("overflow is LLONG_MAX, refused", "n=9223372036854775808", int64(0), true),
		Entry("underflow is LLONG_MIN, kept", "n=-9223372036854775809", int64(-9223372036854775808), false),
		Entry("text after a NUL is not read", "n=5%00x", int64(5), false),
	)
	It("reads the unsigned and 32-bit forms as strtoull, strtol and strtoul do, then truncates", func() {
		a := admin.ParseArgs("max=18446744073709551615&less=18446744073709551614&neg=-1&neg2=-2&i=-5&i33=4294967297&w=4294967295&big=4294967296")
		_, _, err := a.Uint64("max", 0)
		Expect(err).To(MatchError(op.ErrInvalidArgument), "ULLONG_MAX is refused")
		u, _, err := a.Uint64("less", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(u).To(Equal(uint64(18446744073709551614)))
		_, _, err = a.Uint64("neg", 0)
		Expect(err).To(MatchError(op.ErrInvalidArgument), "strtoull negates -1 to ULLONG_MAX")
		u, _, err = a.Uint64("neg2", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(u).To(Equal(uint64(18446744073709551614)), "strtoull negates -2")
		i, _, err := a.Int32("i", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(i).To(Equal(int32(-5)))
		i, _, err = a.Int32("i33", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(i).To(Equal(int32(1)), "stringtol casts a long to int32_t")
		w, _, err := a.Uint32("w", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(w).To(Equal(uint32(4294967295)))
		w, _, err = a.Uint32("big", 9)
		Expect(err).NotTo(HaveOccurred())
		Expect(w).To(BeZero(), "stringtoul casts an unsigned long to uint32_t")
		_, _, err = a.Uint32("neg", 9)
		Expect(err).To(MatchError(op.ErrInvalidArgument), "ULONG_MAX is refused")
	})
	DescribeTable("Epoch is utime_t::parse_date",
		func(in string, want uint64, ok bool) {
			v, _, err := admin.ParseArgs("start="+url.QueryEscape(in)).Epoch("start", 7)
			if !ok {
				Expect(err).To(MatchError(op.ErrInvalidArgument))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal(want))
		},
		Entry("date", "2012-09-25", uint64(1348531200), true),
		Entry("date time", "2012-09-25 16:00:00", uint64(1348588800), true),
		Entry("T and fraction", "2012-09-25T16:00:00.123", uint64(1348588800), true),
		Entry("a zone offset", "2012-09-25T16:00:00.5+0200", uint64(1348581600), true),
		Entry("hours and minutes alone", "2012-09-25 16:30", uint64(1348590600), true),
		Entry("trailing text after the date", "2012-09-25x", uint64(1348531200), true),
		Entry("seconds.usec", "1348588800.5", uint64(1348588800), true),
		// glibc's get_number stops before a digit that would take the value
		// past its field's maximum and leaves it for the format's next byte.
		Entry("a day past 31 stops at its first digit", "2012-01-50", uint64(1325721600), true),
		Entry("a day of 45 is the 4th", "2012-01-45", uint64(1325635200), true),
		Entry("a second of 75 is second 7", "2012-09-25T16:00:75", uint64(1348588807), true),
		Entry("minutes and seconds run together", "2012-09-25T16:7550", uint64(1348589270), true),
		// The time's format is the request's own text, so a conversion in it
		// runs: here %Y reads the 5 before it as the year.
		Entry("a conversion in the request text runs", "2012-01-01T1:000005%Y", uint64(18446744011700188816), true),
		// internal_timegm's days_from_0 counts year 0 as 365 days.
		Entry("year 0 as internal_timegm counts it", "0000-03-01", uint64(18446744011547602816), true),
		// sscanf's %d saturates through strtol, then stores an int.
		Entry("seconds past a long saturate, then truncate to an int", "99999999999999999999.5", uint64(18446744073709551615), true),
		Entry("seconds alone", "1348588800", uint64(0), false),
		Entry("an hour without minutes", "2012-09-25T16", uint64(0), false),
		Entry("garbage", "yesterday", uint64(0), false),
	)
	It("takes the default for an absent date", func() {
		v, present, err := admin.ParseArgs("").Epoch("start", 7)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{v, present}).To(Equal([]any{uint64(7), false}))
	})
})

var _ = Describe("ParseRequest", func() {
	It("parses the resource path and refuses what radosgw registers no manager for", func() {
		q, err := admin.ParseRequest("/admin/metadata/bucket.instance", "admin", "key=b%3A1")
		Expect(err).NotTo(HaveOccurred())
		Expect(q.Resource).To(Equal("metadata"))
		Expect(q.Sub).To(Equal("bucket.instance"))
		v, _ := q.Args.String("key", "")
		Expect(v).To(Equal("b:1"))
		q, err = admin.ParseRequest("/admin/info/x/y", "admin", "")
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{q.Resource, q.Sub}).To(Equal([]string{"info", "x"}), "a third segment is ignored")
		_, err = admin.ParseRequest("/admin", "admin", "")
		Expect(err).To(MatchError(op.ErrMethodNotAllowed))
		_, err = admin.ParseRequest("/admin/", "admin", "")
		Expect(err).To(MatchError(op.ErrMethodNotAllowed))
		_, err = admin.ParseRequest("/admin//info", "admin", "")
		Expect(err).To(MatchError(op.ErrMethodNotAllowed), "an empty segment names no manager")
		q, err = admin.ParseRequest("/admin/nosuch", "admin", "format=xml")
		Expect(err).To(MatchError(op.ErrMethodNotAllowed))
		Expect(q.Format).To(Equal(admin.FormatJSON))
		_, err = admin.ParseRequest("/admin/Info", "admin", "")
		Expect(err).To(MatchError(op.ErrMethodNotAllowed), "the manager lookup is case-sensitive")
	})
	It("strips a nested entry", func() {
		q, err := admin.ParseRequest("/a/b/user", "a/b", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(q.Resource).To(Equal("user"))
	})
	It("knows every manager radosgw registers at both releases", func() {
		for _, r := range []string{"info", "usage", "account", "user", "bucket", "metadata", "log", "config", "realm", "ratelimit"} {
			q, err := admin.ParseRequest("/admin/"+r, "admin", "")
			Expect(err).NotTo(HaveOccurred(), r)
			Expect(q.Resource).To(Equal(r))
		}
	})
})

var _ = DescribeTable("SelectFormat is allocate_formatter with the admin default, JSON",
	func(query, accept string, want admin.Format) {
		Expect(admin.SelectFormat(admin.ParseArgs(query), accept)).To(Equal(want))
	},
	Entry("default", "", "", admin.FormatJSON),
	Entry("format=xml", "format=xml", "", admin.FormatXML),
	Entry("format=html", "format=html", "", admin.FormatHTML),
	Entry("format wins over Accept", "format=json", "application/xml", admin.FormatJSON),
	Entry("an unknown format falls to Accept", "format=XML", "text/html", admin.FormatHTML),
	Entry("Accept up to ';'", "", "application/xml; q=0.9", admin.FormatXML),
	Entry("text/xml", "", "text/xml", admin.FormatXML),
	Entry("application/json", "", "application/json", admin.FormatJSON),
	Entry("a list is not parsed", "", "application/xml, text/html", admin.FormatJSON),
	Entry("Accept is exact", "", "Application/XML", admin.FormatJSON),
)
