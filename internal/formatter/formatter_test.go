package formatter_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// The byte strings below follow ceph's Formatter.cc (v19.2.6 and v20.2.4
// write the same bytes): JSONFormatter's print_comma, print_name and
// close_section (:172-281), XMLFormatter (:377-654) and HTMLFormatter
// (HTMLFormatter.cc:34-168).
var _ = Describe("formatter", func() {
	// sample makes the calls dump_user_info's opening makes: an object at the
	// top, scalars of every kind, an array of objects, an empty array and a
	// time through dump_stream.
	sample := func(f formatter.Formatter) {
		f.OpenObjectSection("user_info")
		f.DumpString("tenant", "")
		f.DumpString("user_id", `a"b<c>`)
		f.DumpInt("suspended", -1)
		f.DumpUnsigned("size", 18446744073709551615)
		f.OpenArraySection("keys")
		f.OpenObjectSection("key")
		f.DumpString("access_key", "AK")
		f.DumpBool("active", true)
		f.CloseSection()
		f.CloseSection()
		f.OpenArraySection("caps")
		f.CloseSection()
		f.DumpStream("create_date", "2026-09-29T00:00:00.000000Z")
		f.CloseSection()
	}
	It("writes JSONFormatter's compact form: no name at the top level or inside an array", func() {
		f := formatter.NewJSON(false)
		sample(f)
		Expect(string(f.Bytes())).To(Equal(`{"tenant":"","user_id":"a\"b<c>","suspended":-1,"size":18446744073709551615,"keys":[{"access_key":"AK","active":true}],"caps":[],"create_date":"2026-09-29T00:00:00.000000Z"}`))
	})
	It("writes JSONFormatter's pretty form, the form ceph-dencoder prints", func() {
		f := formatter.NewJSON(true)
		sample(f)
		Expect(string(f.Bytes())).To(Equal("{\n" +
			"    \"tenant\": \"\",\n" +
			"    \"user_id\": \"a\\\"b<c>\",\n" +
			"    \"suspended\": -1,\n" +
			"    \"size\": 18446744073709551615,\n" +
			"    \"keys\": [\n" +
			"        {\n" +
			"            \"access_key\": \"AK\",\n" +
			"            \"active\": true\n" +
			"        }\n" +
			"    ],\n" +
			"    \"caps\": [],\n" +
			"    \"create_date\": \"2026-09-29T00:00:00.000000Z\"\n" +
			"}\n"))
	})
	It("writes XMLFormatter's elements, array entries keeping their names", func() {
		f := formatter.NewXML(false, false)
		sample(f)
		Expect(string(f.Bytes())).To(Equal(`<user_info><tenant></tenant><user_id>a&quot;b&lt;c&gt;</user_id><suspended>-1</suspended><size>18446744073709551615</size><keys><key><access_key>AK</access_key><active>true</active></key></keys><caps></caps><create_date>2026-09-29T00:00:00.000000Z</create_date></user_info>`))
	})
	It("indents a pretty XMLFormatter by one space per open section", func() {
		f := formatter.NewXML(true, false)
		f.OpenObjectSection("a")
		f.DumpString("b", "c")
		f.CloseSection()
		Expect(string(f.Bytes())).To(Equal("<a>\n <b>c</b>\n</a>\n"))
	})
	It("writes HTMLFormatter's scalars as list items inside XML sections", func() {
		f := formatter.NewHTML(false)
		sample(f)
		Expect(string(f.Bytes())).To(Equal(`<user_info><li>tenant: </li><li>user_id: a&quot;b&lt;c&gt;</li><li>suspended: -1</li><li>size: 18446744073709551615</li><keys><key><li>access_key: AK</li><li>active: true</li></key></keys><caps></caps><li>create_date: 2026-09-29T00:00:00.000000Z</li></user_info>`))
	})
	It("writes HTMLFormatter's status page and closes it with output_footer", func() {
		f := formatter.NewHTML(false)
		f.SetStatus(403, "Forbidden")
		f.OutputHeader()
		f.DumpString("Code", "AccessDenied")
		f.OutputFooter()
		Expect(string(f.Bytes())).To(Equal(`<html><head><title>403 Forbidden</title></head><body><h1>403 Forbidden</h1><ul><li>Code: AccessDenied</li></ul></body></html>`))
	})
	It("keeps HTMLFormatter's status name when set_status is given none", func() {
		f := formatter.NewHTML(false)
		f.SetStatus(500, "Internal Server Error")
		f.SetStatus(503, "")
		f.OutputHeader()
		f.OutputFooter()
		Expect(string(f.Bytes())).To(Equal(`<html><head><title>503 Internal Server Error</title></head><body><h1>503 Internal Server Error</h1><ul></ul></body></html>`))
	})
	It("writes the XML declaration once until a reset", func() {
		f := formatter.NewXML(false, false)
		f.OutputHeader()
		f.OutputHeader()
		Expect(string(f.Bytes())).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>`))
		f.Reset()
		f.OutputHeader()
		Expect(string(f.Bytes())).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>`))
		Expect(string(formatter.NewJSON(false).Bytes())).To(BeEmpty(), "JSONFormatter has no header")
	})
	It("writes names raw, underscores XML element names and honors lowercased", func() {
		j := formatter.NewJSON(false)
		j.OpenObjectSection("tagset")
		j.DumpString(`k"1`, "v")
		j.CloseSection()
		Expect(string(j.Bytes())).To(Equal(`{"k"1":"v"}`), "print_name writes the name unescaped")
		x := formatter.NewXML(false, true)
		x.DumpString("Tag Name", "v")
		x.DumpStream("Tag Name", "v")
		Expect(string(x.Bytes())).To(Equal(`<tag_name>v</tag_name><Tag Name>v</Tag Name>`), "dump_stream skips get_xml_name")
	})
	It("writes a top-level scalar without a name and dump_null in each form", func() {
		j := formatter.NewJSON(false)
		j.DumpString("ignored", "v")
		Expect(string(j.Bytes())).To(Equal(`"v"`))
		j.Reset()
		j.OpenObjectSection("o")
		j.DumpNull("n")
		j.DumpUnquoted("u", "1.5")
		j.CloseSection()
		Expect(string(j.Bytes())).To(Equal(`{"n":null,"u":1.5}`))
		x := formatter.NewXML(false, false)
		x.DumpNull("n")
		Expect(string(x.Bytes())).To(Equal(`<n xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:nil="true" />`))
		h := formatter.NewHTML(false)
		h.DumpNull("n")
		Expect(string(h.Bytes())).To(Equal(`<n xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:nil="true" />`), "HTMLFormatter keeps XMLFormatter's dump_null")
	})
	It("writes a dump_stream with an empty name as an empty tag and keeps its value for the next", func() {
		x := formatter.NewXML(false, false)
		x.DumpStream("", "a")
		x.DumpString("k", "v")
		x.DumpStream("t", "b")
		Expect(string(x.Bytes())).To(Equal("<><k>v</k><t>ab</t>"), "finish_pending_string writes nothing for an empty name, Formatter.cc:624-635")
	})
	It("cuts an unquoted value at its first NUL, where vsnprintf's %s stops", func() {
		j := formatter.NewJSON(false)
		j.OpenObjectSection("o")
		j.DumpUnquoted("u", "1\x002")
		j.CloseSection()
		Expect(string(j.Bytes())).To(Equal(`{"u":1}`))
		x := formatter.NewXML(false, false)
		x.DumpUnquoted("u", "1\x002")
		Expect(string(x.Bytes())).To(Equal(`<u>1</u>`))
		h := formatter.NewHTML(false)
		h.DumpUnquoted("u", "1\x002")
		Expect(string(h.Bytes())).To(Equal(`<li>u: 1</li>`))
	})
	It("closes every open section in output_footer", func() {
		f := formatter.NewXML(false, false)
		f.OpenObjectSection("a")
		f.OpenArraySection("b")
		f.OutputFooter()
		Expect(string(f.Bytes())).To(Equal(`<a><b></b></a>`))
	})
	It("keeps the first failure until a reset", func() {
		for _, f := range []formatter.Formatter{formatter.NewJSON(false), formatter.NewXML(false, false), formatter.NewHTML(false)} {
			f.Fail(errors.New("first"))
			f.Fail(errors.New("second"))
			Expect(f.Err()).To(MatchError("first"))
			f.Reset()
			Expect(f.Err()).NotTo(HaveOccurred())
		}
	})
	DescribeTable("escapes JSON as json_stream_escaper does",
		func(in, want string) {
			Expect(formatter.EscapeJSON(in)).To(Equal(want))
		},
		Entry("quotes", `"'`, `\"'`),
		Entry("markup", "<&>", "<&>"),
		Entry("backslash", `\`, `\\`),
		Entry("tab and newline", "\t\n", `\t\n`),
		Entry("other control bytes", "\r\x01\x7f", `\u000d\u0001\u007f`),
		Entry("UTF-8 is copied", "é", "é"),
	)
	It("writes XML and HTML text through xml_stream_escaper", func() {
		x := formatter.NewXML(false, false)
		x.DumpString("k", "\"'<&>\t\r\x01\x7f\xff")
		Expect(string(x.Bytes())).To(Equal("<k>&quot;&apos;&lt;&amp;&gt;\t&#x0d;&#x01;&#x7f;\xff</k>"))
		h := formatter.NewHTML(false)
		h.DumpString("k", "a<b")
		Expect(string(h.Bytes())).To(Equal("<li>k: a&lt;b</li>"))
	})
})
