package s3

import (
	"errors"
	"hash/crc32"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing/iotest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/jhoblitt/rgw-go/internal/op"
)

const formBoundary = "bnd"

const formCT = "multipart/form-data; boundary=" + formBoundary

// formFieldPart is a part's headers and data for the field name holding value.
func formFieldPart(name, value string) string {
	return "Content-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" + value
}

// formFilePart is the file part, named filename, holding data.
func formFilePart(filename, data string) string {
	return "Content-Disposition: form-data; name=\"file\"; filename=\"" + filename + "\"\r\n" +
		"Content-Type: text/plain\r\n\r\n" + data
}

// formOf is a body holding parts, each after its delimiter, and the close
// delimiter.
func formOf(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("--" + formBoundary + "\r\n" + p + "\r\n")
	}
	b.WriteString("--" + formBoundary + "--\r\n")
	return b.String()
}

// formRead is what a whole form read gives: the fields as All yields them,
// the file's name and data.
type formRead struct {
	fields   [][2]string
	filename string
	file     string
}

func fieldsOf(f formFields) [][2]string {
	var out [][2]string
	for name, value := range f.All() {
		out = append(out, [2]string{name, value})
	}
	return out
}

// readWholeForm reads body as the form handler would: the fields, the file to
// its end, then the parts after it.
func readWholeForm(body io.Reader, contentType string, maxLine int) (formRead, error) {
	fr, err := newFormReader(body, contentType, maxLine)
	if err != nil {
		return formRead{}, err
	}
	fields, file, err := fr.Fields()
	if err != nil {
		return formRead{}, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return formRead{}, err
	}
	if err := fr.Drain(); err != nil {
		return formRead{}, err
	}
	return formRead{fieldsOf(fields), file.Filename, string(data)}, nil
}

// splitReaders are the readers a spec reads body through: whole, one byte at a
// time, and split in two at every byte, so a delimiter falls across two reads
// at each of its offsets.
func splitReaders(body string) map[string]io.Reader {
	rs := map[string]io.Reader{
		"whole":                 strings.NewReader(body),
		"one byte at a time":    iotest.OneByteReader(strings.NewReader(body)),
		"with EOF on its data":  iotest.DataErrReader(strings.NewReader(body)),
		"half of each read ask": iotest.HalfReader(strings.NewReader(body)),
	}
	for s := 1; s < len(body); s++ {
		rs["split at "+strconv.Itoa(s)] = io.MultiReader(strings.NewReader(body[:s]), strings.NewReader(body[s:]))
	}
	return rs
}

// rawField is the field's raw value, or "<absent>".
func rawField(f formFields, name string) string {
	v, ok := f.Raw(name)
	if !ok {
		return "<absent>"
	}
	return v
}

// trimmedField is the field's trimmed value, or "<absent>".
func trimmedField(f formFields, name string) string {
	v, ok := f.Trimmed(name)
	if !ok {
		return "<absent>"
	}
	return v
}

// formError matches an InvalidArgument whose message is msg.
func formError(msg string) types.GomegaMatcher {
	return SatisfyAll(
		MatchError(op.ErrInvalidArgument),
		WithTransform(func(err error) string { return op.AsError(err).Message }, Equal(msg)),
	)
}

// plainForm is a form of one field, key, and a file part.
func plainForm() string { return formOf(formFieldPart("key", "k"), formFilePart("f.txt", "data")) }

var _ = Describe("newFormReader", func() {
	DescribeTable("takes a multipart/form-data Content-Type with a boundary",
		func(contentType string) {
			got, err := readWholeForm(strings.NewReader(plainForm()), contentType, 1024)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.fields).To(Equal([][2]string{{"key", "k"}}))
			Expect(got.file).To(Equal("data"))
		},
		Entry("plain", formCT),
		Entry("a quoted boundary", `multipart/form-data; boundary="bnd"`),
		Entry("another parameter first", "multipart/form-data; charset=utf-8; boundary=bnd"),
		Entry("white space around each piece, which radosgw trims", " multipart/form-data ;  boundary = bnd "),
		Entry("a later boundary, which replaces the earlier", "multipart/form-data; boundary=x; boundary=bnd"),
	)

	DescribeTable("refuses another Content-Type with radosgw's message",
		func(contentType, msg string) {
			_, err := newFormReader(strings.NewReader(plainForm()), contentType, 1024)
			Expect(err).To(formError(msg))
		},
		Entry("none", "", "Request Content-Type is not multipart/form-data"),
		Entry("text/plain", "text/plain", "Request Content-Type is not multipart/form-data"),
		Entry("another multipart type", "multipart/mixed; boundary=bnd", "Request Content-Type is not multipart/form-data"),
		Entry("the type in capitals, which radosgw compares exactly", "Multipart/Form-Data; boundary=bnd",
			"Request Content-Type is not multipart/form-data"),
		Entry("no boundary", "multipart/form-data", "Missing multipart boundary specification"),
		Entry("a boundary named in capitals, which radosgw's parameter map does not find",
			"multipart/form-data; Boundary=bnd", "Missing multipart boundary specification"),
	)

	It("keeps a semicolon inside a quoted boundary", func() {
		quoted := strings.ReplaceAll(plainForm(), "--"+formBoundary, "--a;b")
		got, err := readWholeForm(strings.NewReader(quoted), `multipart/form-data; boundary="a;b"`, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.file).To(Equal("data"))
	})
})

var _ = Describe("formReader framing", func() {
	It("reads the fields before the file, the file's name and its data", func() {
		body := formOf(formFieldPart("key", "uploads/${filename}"), formFieldPart("acl", "private"),
			formFilePart("cat.jpg", "meow\r\nmeow"))
		got, err := readWholeForm(strings.NewReader(body), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(formRead{
			fields:   [][2]string{{"acl", "private"}, {"key", "uploads/${filename}"}},
			filename: "cat.jpg",
			file:     "meow\r\nmeow",
		}))
	})

	It("reads the same form whatever the body's reads are, a delimiter split across two of them included", func() {
		body := formOf(formFieldPart("key", "k"), formFieldPart("empty", ""),
			formFilePart("f", "a\r\n--bnd-x\r\n--bn"), formFieldPart("after", "x"))
		want := formRead{fields: [][2]string{{"empty", ""}, {"key", "k"}}, filename: "f", file: "a\r\n--bnd-x\r\n--bn"}
		for name, r := range splitReaders(body) {
			got, err := readWholeForm(r, formCT, 1024)
			Expect(err).NotTo(HaveOccurred(), name)
			Expect(got).To(Equal(want), name)
		}
	})

	It("skips a preamble before the first delimiter", func() {
		body := "a preamble\r\nwith: a colon\r\n" + formOf(formFieldPart("key", "k"), formFilePart("f", "d"))
		got, err := readWholeForm(strings.NewReader(body), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"key", "k"}}))
		Expect(got.file).To(Equal("d"))
	})

	It("reads nothing past the close delimiter", func() {
		body := formOf(formFieldPart("key", "k"), formFilePart("f", "d")) + "an epilogue"
		r := io.MultiReader(strings.NewReader(body), iotest.ErrReader(errors.New("read past the form")))
		got, err := readWholeForm(r, formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.file).To(Equal("d"))
	})

	It("reads the parts after the file in Drain and keeps none of them", func() {
		body := formOf(formFieldPart("key", "first"), formFilePart("f", "d"),
			formFieldPart("key", "second"), formFieldPart("acl", "public-read"))
		got, err := readWholeForm(strings.NewReader(body), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"key", "first"}}))
	})

	It("drains the unread rest of the file before the parts after it", func() {
		body := formOf(formFilePart("f", strings.Repeat("d", 1000)), formFieldPart("after", "x"))
		fr, err := newFormReader(strings.NewReader(body), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		_, file, err := fr.Fields()
		Expect(err).NotTo(HaveOccurred())
		buf := make([]byte, 10)
		Expect(file.Read(buf)).To(Equal(10))
		Expect(fr.Drain()).To(Succeed())
		Expect(fr.Drain()).To(Succeed())
	})

	DescribeTable("answers a malformed part after the file with InvalidArgument and no message, as complete_get_params does",
		func(after string) {
			body := "--bnd\r\n" + formFilePart("f", "d") + "\r\n--bnd\r\n" + after
			_, err := readWholeForm(strings.NewReader(body), formCT, 64)
			Expect(err).To(formError(""))
		},
		Entry("a header line without a colon", "no colon\r\n\r\nx\r\n--bnd--\r\n"),
		Entry("data longer than maxLine", formFieldPart("a", strings.Repeat("x", 65))+"\r\n--bnd--\r\n"),
		Entry("a part delimiter inside a part's headers", "X: y\r\n--bnd\r\n"+formFieldPart("a", "x")+"\r\n--bnd--\r\n"),
		Entry("the body ending in a part's data", formFieldPart("a", "x")),
	)

	It("takes a part with no Content-Disposition for the field with the empty name", func() {
		body := formOf("X-Other: y\r\n\r\nv", formFilePart("f", "d"))
		got, err := readWholeForm(strings.NewReader(body), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"", "v"}}))
	})

	It("names a part by its last Content-Disposition, found in any ASCII case", func() {
		part := "Content-Disposition: form-data; name=\"first\"\r\n" +
			"content-DISPOSITION: form-data; name=\"second\"\r\n\r\nv"
		got, err := readWholeForm(strings.NewReader(formOf(part, formFilePart("f", "d"))), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"second", "v"}}))
	})

	It("takes the filename of the file part's last Content-Disposition, empty when it has none", func() {
		part := "Content-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\n" +
			"Content-Disposition: form-data; name=\"file\"\r\n\r\nd"
		got, err := readWholeForm(strings.NewReader(formOf(part)), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.filename).To(BeEmpty())
		Expect(got.file).To(Equal("d"))
	})

	It("takes the part named file in any ASCII case for the file", func() {
		part := "Content-Disposition: form-data; name=\"FiLe\"\r\n\r\nd"
		got, err := readWholeForm(strings.NewReader(formOf(formFieldPart("key", "k"), part)), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"key", "k"}}))
		Expect(got.file).To(Equal("d"))
	})

	It("keeps a semicolon inside a quoted name or filename", func() {
		file := "Content-Disposition: form-data; name=\"file\"; filename=\"my;cat.jpg\"\r\n\r\nd"
		got, err := readWholeForm(strings.NewReader(formOf(formFieldPart("a;b", "v"), file)), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"a;b", "v"}}))
		Expect(got.filename).To(Equal("my;cat.jpg"))
	})

	It("reads parameters otherwise as radosgw does: trimmed, unquoted, the last of a name", func() {
		part := "Content-Disposition:form-data;name = \" key \" ;  name=  plain  ; filename\r\n\r\nv"
		got, err := readWholeForm(strings.NewReader(formOf(part, formFilePart("f", "d"))), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"plain", "v"}}))
	})
})

// startsLikeDelimiter are lines that begin as a delimiter does, with the
// boundary and then padding or "--", and do not complete one.
func startsLikeDelimiter() []TableEntry {
	return []TableEntry{
		Entry("more padding than rgw-go takes", "--bnd"+strings.Repeat(" ", formMaxPadding+1)+"\r\n"),
		Entry("more padding than rgw-go takes after a close", "--bnd--"+strings.Repeat("\t", formMaxPadding+1)+"\r\n"),
		Entry("the close delimiter followed by other text", "--bnd--x\r\n"),
		Entry("the close delimiter, padding, then other text", "--bnd-- x\r\n"),
		Entry("a close delimiter ended by a bare LF", "--bnd--\n"),
		Entry("a close delimiter followed by a CR and other text", "--bnd--\rx\r\n"),
		Entry("padding, then other text", "--bnd \tx\r\n"),
		Entry("padding, then a bare LF", "--bnd \n"),
	}
}

var _ = Describe("formReader delimiters", func() {
	DescribeTable("reads file data holding the boundary text back byte-identical",
		func(data string) {
			body := formOf(formFieldPart("key", "k"), formFilePart("f", data), formFieldPart("after", "x"))
			for name, r := range splitReaders(body) {
				got, err := readWholeForm(r, formCT, 1024)
				Expect(err).NotTo(HaveOccurred(), name)
				Expect(got.file).To(Equal(data), name)
			}
		},
		Entry("-- and the boundary mid-line, then --", "abc--bnd--def"),
		Entry("-- and the boundary mid-line, then CRLF", "abc--bnd\r\ndef"),
		Entry("the boundary text after a bare LF", "abc\n--bnd\r\ndef"),
		Entry("the boundary text after a bare CR", "abc\r--bnd--def"),
		Entry("the boundary at a line start followed by other text", "abc\r\n--bndx\r\ndef"),
		Entry("the boundary at a line start followed by a bare LF", "abc\r\n--bnd\ndef"),
		Entry("the boundary at a line start followed by a CR and other text", "abc\r\n--bnd\rdef"),
		Entry("the boundary at a line start followed by one dash", "abc\r\n--bnd-\r\ndef"),
		Entry("a prefix of the delimiter at the data's end", "abc\r\n--bn"),
		Entry("the delimiter's CRLF at the data's end", "abc\r\n"),
		Entry("CRLF -- at the data's end", "abc\r\n--"),
		Entry("a run of CRs and LFs", "\r\r\n\r\n\n\r"),
	)

	DescribeTable("ends a part at a delimiter followed by RFC 2046's transport padding",
		func(body string, want formRead) {
			for name, r := range splitReaders(body) {
				got, err := readWholeForm(r, formCT, 1024)
				Expect(err).NotTo(HaveOccurred(), name)
				Expect(got).To(Equal(want), name)
			}
		},
		Entry("a padded delimiter after a field",
			"--bnd\r\n"+formFieldPart("key", "k")+"\r\n--bnd \t \r\n"+formFilePart("f", "d")+"\r\n--bnd--\r\n",
			formRead{fields: [][2]string{{"key", "k"}}, filename: "f", file: "d"}),
		Entry("a padded delimiter after the file",
			"--bnd\r\n"+formFilePart("f", "DATA")+"\r\n--bnd  \r\n"+formFieldPart("after", "x")+"\r\n--bnd--\r\n",
			formRead{filename: "f", file: "DATA"}),
		Entry("a padded close delimiter",
			"--bnd\r\n"+formFilePart("f", "DATA")+"\r\n--bnd--\t \r\nan epilogue",
			formRead{filename: "f", file: "DATA"}),
		Entry("a padded close delimiter at the body's end",
			"--bnd\r\n"+formFilePart("f", "DATA")+"\r\n--bnd-- ",
			formRead{filename: "f", file: "DATA"}),
		Entry("a close delimiter whose CRLF the body's end cuts to a CR",
			"--bnd\r\n"+formFilePart("f", "DATA")+"\r\n--bnd-- \r",
			formRead{filename: "f", file: "DATA"}),
		Entry("a padded first delimiter",
			"--bnd \r\n"+formFieldPart("key", "k")+"\r\n--bnd\r\n"+formFilePart("f", "d")+"\r\n--bnd--\r\n",
			formRead{fields: [][2]string{{"key", "k"}}, filename: "f", file: "d"}),
		Entry("a padded delimiter at the start of a part's data",
			"--bnd\r\n"+formFieldPart("a", "")+"--bnd\t\r\n"+formFilePart("f", "d")+"\r\n--bnd--\r\n",
			formRead{fields: [][2]string{{"a", ""}}, filename: "f", file: "d"}),
		Entry("as much padding as rgw-go takes",
			"--bnd\r\n"+formFilePart("f", "d")+"\r\n--bnd"+strings.Repeat(" ", formMaxPadding)+"\r\n"+
				formFieldPart("after", "x")+"\r\n--bnd--\r\n",
			formRead{filename: "f", file: "d"}),
	)

	DescribeTable("refuses a line that starts like a delimiter but is not one, wherever it falls",
		func(line string) {
			at := map[string]struct {
				body string
				msg  string
			}{
				"in a field before the file": {
					"--bnd\r\n" + formFieldPart("key", "a\r\n"+line+"b") + "\r\n--bnd\r\n" + formFilePart("f", "d") + "\r\n--bnd--\r\n",
					"Malformed request",
				},
				"in the file's data": {
					"--bnd\r\n" + formFilePart("f", "a\r\n"+line+"b") + "\r\n--bnd\r\n" + formFieldPart("after", "x") + "\r\n--bnd--\r\n",
					"Malformed request",
				},
				"in a part after the file": {
					"--bnd\r\n" + formFilePart("f", "d") + "\r\n--bnd\r\n" + formFieldPart("after", "a\r\n"+line+"b") + "\r\n--bnd--\r\n",
					"",
				},
				"at the body's end after the file": {
					"--bnd\r\n" + formFilePart("f", "d") + "\r\n" + line,
					"Malformed request",
				},
			}
			for where, c := range at {
				for name, r := range splitReaders(c.body) {
					_, err := readWholeForm(r, formCT, 1024)
					Expect(err).To(formError(c.msg), "%s, %s", where, name)
				}
			}
		},
		startsLikeDelimiter(),
	)

	DescribeTable("refuses a header line that starts like a delimiter but is not one",
		func(line string) {
			body := "--bnd\r\nContent-Disposition: form-data; name=\"a\"\r\n" + line +
				"\r\nv\r\n--bnd\r\n" + formFilePart("f", "d") + "\r\n--bnd--\r\n"
			_, err := readWholeForm(strings.NewReader(body), formCT, 1024)
			Expect(err).To(formError("Malformed request"))
		},
		startsLikeDelimiter(),
	)

	It("keeps a header line holding the boundary text whole, as radosgw's read_line does not", func() {
		file := "Content-Disposition: form-data; name=\"file\"; filename=\"a--bnd--.txt\"\r\nX-Note: x--bnd\r\n\r\nd"
		got, err := readWholeForm(strings.NewReader(formOf(formFieldPart("key", "k"), file)), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.filename).To(Equal("a--bnd--.txt"))
		Expect(got.file).To(Equal("d"))
	})

	It("reads a field's value holding the boundary text mid-line", func() {
		value := "x--bnd--y\r\nz--bnd\r\n"
		got, err := readWholeForm(strings.NewReader(formOf(formFieldPart("v", value), formFilePart("f", "d"))), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"v", value}}))
	})

	It("ends a part's empty data at a delimiter that starts the line after its headers", func() {
		body := "--bnd\r\n" + formFieldPart("a", "") + "--bnd\r\n" + formFilePart("f", "") + "--bnd--"
		for name, r := range splitReaders(body) {
			got, err := readWholeForm(r, formCT, 1024)
			Expect(err).NotTo(HaveOccurred(), name)
			Expect(got.fields).To(Equal([][2]string{{"a", ""}}), name)
			Expect(got.file).To(BeEmpty(), name)
		}
	})

	It("passes a body read error out of Drain unchanged", func() {
		boom := op.ErrBadDigest.WithMessage("from a reader beneath the form")
		r := io.MultiReader(strings.NewReader("--bnd\r\n"+formFilePart("f", "d")+"\r\n--bnd\r\n"), iotest.ErrReader(boom))
		fr, err := newFormReader(r, formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		_, file, err := fr.Fields()
		Expect(err).NotTo(HaveOccurred())
		Expect(io.ReadAll(file)).To(Equal([]byte("d")))
		Expect(fr.Drain()).To(MatchError(boom))
	})

	It("refuses Fields twice and Drain before the file", func() {
		fr, err := newFormReader(strings.NewReader(formOf(formFieldPart("key", "k"))), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(fr.Drain()).To(MatchError(ContainSubstring("before its file part")))
		_, _, err = fr.Fields()
		Expect(err).To(formError("Malformed request"))
		_, _, err = fr.Fields()
		Expect(err).To(MatchError(ContainSubstring("twice")))
	})
})

var _ = Describe("formReader refusals", func() {
	DescribeTable("answers a form that ends before the file 400 Malformed request",
		func(body string) {
			_, err := readWholeForm(strings.NewReader(body), formCT, 1024)
			Expect(err).To(formError("Malformed request"))
		},
		Entry("an empty form", "--bnd--\r\n"),
		Entry("the close delimiter after a field", "--bnd\r\n"+formFieldPart("key", "k")+"\r\n--bnd--\r\n"),
		Entry("the close delimiter where a part's headers start", "--bnd\r\n"+formFieldPart("key", "k")+"\r\n--bnd\r\n--bnd--\r\n"),
		Entry("the close delimiter inside a part's headers", "--bnd\r\nContent-Disposition: form-data; name=\"a\"\r\n--bnd--\r\n"),
		Entry("a part delimiter inside a part's headers",
			"--bnd\r\nContent-Disposition: form-data; name=\"a\"\r\n--bnd\r\n"+formFilePart("f", "d")+"\r\n--bnd--\r\n"),
		Entry("a padded close delimiter where a part's headers start",
			"--bnd\r\n"+formFieldPart("key", "k")+"\r\n--bnd\r\n--bnd-- \t\r\n"),
		Entry("a padded part delimiter inside a part's headers",
			"--bnd\r\nContent-Disposition: form-data; name=\"a\"\r\n--bnd \t\r\n"+formFilePart("f", "d")+"\r\n--bnd--\r\n"),
	)

	It("ends the form at a padded close delimiter where a part's headers start after the file", func() {
		body := "--bnd\r\n" + formFilePart("f", "d") + "\r\n--bnd\r\n--bnd--  \r\nan epilogue"
		got, err := readWholeForm(strings.NewReader(body), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.file).To(Equal("d"))
	})

	DescribeTable("answers a missing delimiter 400 Couldn't find boundary",
		func(body string) {
			_, err := readWholeForm(strings.NewReader(body), formCT, 64)
			Expect(err).To(formError("Couldn't find boundary"))
		},
		Entry("a body with no delimiter", "no form here"),
		Entry("an empty body", ""),
		Entry("the body ending after a delimiter", "--bnd\r\n"),
		Entry("the body ending in a part's headers", "--bnd\r\nContent-Disposition: form-data; name=\"key\""),
		Entry("the body ending in a field's data", "--bnd\r\n"+formFieldPart("key", "k")),
		Entry("the body ending in the file's data", "--bnd\r\n"+formFilePart("f", "data")),
		Entry("the body ending in a padded part delimiter's padding", "--bnd\r\n"+formFilePart("f", "data")+"\r\n--bnd \t"),
		Entry("the body ending in a padded part delimiter's CR", "--bnd\r\n"+formFilePart("f", "data")+"\r\n--bnd \r"),
		Entry("the body ending in a part delimiter's CR", "--bnd\r\n"+formFilePart("f", "data")+"\r\n--bnd\r"),
		Entry("a field longer than maxLine", formOf(formFieldPart("key", strings.Repeat("k", 65)), formFilePart("f", "d"))),
	)

	It("takes a field of exactly maxLine bytes", func() {
		value := strings.Repeat("k", 64)
		got, err := readWholeForm(strings.NewReader(formOf(formFieldPart("key", value), formFilePart("f", "d"))), formCT, 64)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"key", value}}))
	})

	DescribeTable("answers a bad part header line 400 InvalidArgument with no message, as radosgw's header refusals carry none",
		func(body string) {
			_, err := readWholeForm(strings.NewReader(body), formCT, 64)
			Expect(err).To(formError(""))
		},
		Entry("a line without a colon", formOf("no colon here\r\n\r\nv", formFilePart("f", "d"))),
		Entry("a line longer than maxLine and the boundary's length",
			formOf("X: "+strings.Repeat("h", 64+len("--bnd")-2)+"\r\n\r\nv", formFilePart("f", "d"))),
	)

	It("takes a header line as long as maxLine and the boundary's length, radosgw's widest window", func() {
		line := "X: " + strings.Repeat("h", 64+len("--bnd")-3)
		got, err := readWholeForm(strings.NewReader(formOf(line+"\r\n\r\nv", formFilePart("f", "d"))), formCT, 64)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"", "v"}}))
	})

	manyFields := func(n int, value string) string {
		var b strings.Builder
		for i := range n {
			b.WriteString("--bnd\r\n" + formFieldPart("f"+strconv.Itoa(i), value) + "\r\n")
		}
		return b.String()
	}

	DescribeTable("refuses the parts before the file once they pass maxLine and the fields' allowance",
		func(head func() string) {
			_, err := readWholeForm(strings.NewReader(head()+formOf(formFilePart("f", "d"))), formCT, 64<<10)
			Expect(err).To(formError("Your POST request fields preceding the upload file were too large."))
		},
		Entry("many fields", func() string { return manyFields(20, strings.Repeat("v", 60000)) }),
		Entry("many distinct fields with empty values", func() string { return manyFields(25000, "") }),
		Entry("one part's many header lines", func() string {
			return "--bnd\r\n" + strings.Repeat("X: y\r\n", 200000) + "\r\nv\r\n"
		}),
		Entry("a long preamble", func() string { return strings.Repeat("p", 1200000) + "\r\n" }),
	)

	It("counts every byte before the file's data toward that bound, and no more", func() {
		head := strings.Repeat("p", formFieldsAllowance) + "\r\n--bnd\r\n" + formFieldPart("a", "v") + "\r\n"
		file := "--bnd\r\n" + formFilePart("f", "")
		maxLine := len(head+file) - formFieldsAllowance
		body := head + file + "d\r\n--bnd--\r\n"
		got, err := readWholeForm(strings.NewReader(body), formCT, maxLine)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.file).To(Equal("d"))
		_, err = readWholeForm(strings.NewReader(body), formCT, maxLine-1)
		Expect(err).To(formError("Your POST request fields preceding the upload file were too large."))
	})

	It("does not count the file or the parts after it toward that bound", func() {
		body := "--bnd\r\n" + formFieldPart("a", "v") + "\r\n--bnd\r\n" + formFilePart("f", strings.Repeat("d", 2<<20)) +
			"\r\n" + manyFields(20000, strings.Repeat("v", 60)) + "--bnd--\r\n"
		_, err := readWholeForm(strings.NewReader(body), formCT, 64)
		Expect(err).NotTo(HaveOccurred())
	})

	It("passes the body's read error through", func() {
		boom := errors.New("connection reset")
		r := io.MultiReader(strings.NewReader("--bnd\r\n"+formFieldPart("key", "k")), iotest.ErrReader(boom))
		_, err := readWholeForm(r, formCT, 1024)
		Expect(err).To(MatchError(boom))
	})
})

var _ = Describe("formFields", func() {
	read := func(parts ...string) formFields {
		fr, err := newFormReader(strings.NewReader(formOf(append(parts, formFilePart("f", "d"))...)), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
		fields, _, err := fr.Fields()
		Expect(err).NotTo(HaveOccurred())
		return fields
	}

	It("keeps the later value under the first spelling for names that differ in ASCII case", func() {
		fields := read(formFieldPart("X-Amz-Meta-Color", "red"), formFieldPart("x-amz-meta-COLOR", "blue"))
		Expect(fieldsOf(fields)).To(Equal([][2]string{{"X-Amz-Meta-Color", "blue"}}))
		Expect(rawField(fields, "x-amz-meta-color")).To(Equal("blue"))
		Expect(trimmedField(fields, "X-AMZ-META-COLOR")).To(Equal("blue"))
	})

	It("keeps names that differ in a non-ASCII letter's case apart", func() {
		fields := read(formFieldPart("x-amz-meta-é", "lower"), formFieldPart("x-amz-meta-É", "upper"))
		Expect(rawField(fields, "x-amz-meta-é")).To(Equal("lower"))
		Expect(rawField(fields, "x-amz-meta-É")).To(Equal("upper"))
		Expect(fieldsOf(fields)).To(HaveLen(2))
	})

	It("gives the credentials and the policy the same, last, occurrence of each field", func() {
		fields := read(
			formFieldPart("x-amz-credential", "AKOLD/20261021/us-east-1/s3/aws4_request"),
			formFieldPart("policy", "b2xk"),
			formFieldPart("X-Amz-Credential", " AKNEW/20261021/us-east-1/s3/aws4_request "),
			formFieldPart("POLICY", "bmV3\r\n"),
		)
		Expect(trimmedField(fields, "x-amz-credential")).To(Equal("AKNEW/20261021/us-east-1/s3/aws4_request"))
		Expect(rawField(fields, "x-amz-credential")).To(Equal(" AKNEW/20261021/us-east-1/s3/aws4_request "))
		Expect(rawField(fields, "policy")).To(Equal("bmV3\r\n"))
		Expect(fieldsOf(fields)).To(Equal([][2]string{
			{"policy", "bmV3\r\n"},
			{"x-amz-credential", " AKNEW/20261021/us-east-1/s3/aws4_request "},
		}))
	})

	It("gives the raw value, and the value trimmed of C white space for parameters", func() {
		fields := read(formFieldPart("key", " \t\v\fk e y\r\n "))
		Expect(rawField(fields, "key")).To(Equal(" \t\v\fk e y\r\n "))
		Expect(trimmedField(fields, "key")).To(Equal("k e y"))
	})

	It("passes a NUL in a value through Raw and Trimmed unchanged", func() {
		fields := read(formFieldPart("key", "\x00uploads/a\x00b\x00"))
		Expect(rawField(fields, "key")).To(Equal("\x00uploads/a\x00b\x00"))
		Expect(trimmedField(fields, "key")).To(Equal("\x00uploads/a\x00b\x00"))
	})

	It("reports a field the form does not hold", func() {
		fields := read(formFieldPart("key", "k"))
		Expect(rawField(fields, "acl")).To(Equal("<absent>"))
		Expect(trimmedField(fields, "acl")).To(Equal("<absent>"))
	})

	It("yields the fields in strcasecmp order of their names, as radosgw's parts map holds them", func() {
		fields := read(formFieldPart("b", "1"), formFieldPart("A", "2"), formFieldPart("c", "3"), formFieldPart("_", "4"))
		Expect(fieldsOf(fields)).To(Equal([][2]string{{"_", "4"}, {"A", "2"}, {"b", "1"}, {"c", "3"}}))
	})

	DescribeTable("refuses a part name holding a control byte",
		func(name string) {
			part := "Content-Disposition: form-data; name=\"" + name + "\"\r\n\r\nv"
			_, err := readWholeForm(strings.NewReader(formOf(part, formFilePart("f", "d"))), formCT, 1024)
			Expect(err).To(formError("A form field name holds a control character"))
		},
		Entry("a NUL", "key\x00x"),
		Entry("a CR", "key\rx"),
		Entry("an LF", "key\nx"),
		Entry("a tab", "key\tx"),
		Entry("DEL", "key\x7fx"),
		Entry("0x01", "\x01key"),
		Entry("0x1f", "key\x1f"),
		Entry("Tentacle's acl shape: acl, a NUL, eleven bytes, crc32, a NUL and x-amz-checksum-",
			"acl\x00"+strings.Repeat("a", 11)+"crc32\x00x-amz-checksum-"),
		Entry("Tentacle's meta shape: x-amz-meta-aaaacrc32, a NUL and x-amz-checksum-",
			"x-amz-meta-aaaacrc32\x00x-amz-checksum-"),
	)

	It("refuses a control byte in a name before the file part named in any case", func() {
		part := "Content-Disposition: form-data; name=\"file\x00\"\r\n\r\nd"
		_, err := readWholeForm(strings.NewReader(formOf(part)), formCT, 1024)
		Expect(err).To(formError("A form field name holds a control character"))
	})

	It("checks no name after the file, where nothing is kept", func() {
		after := "Content-Disposition: form-data; name=\"a\x00b\"\r\n\r\nv"
		_, err := readWholeForm(strings.NewReader(formOf(formFilePart("f", "d"), after)), formCT, 1024)
		Expect(err).NotTo(HaveOccurred())
	})
})

// formParseBound is the wall-clock time a maximal header line of
// parameters may take through Fields, at least 100 times what the linear
// parse takes under -race, so host load cannot flake it; a parse quadratic in
// the line's length takes minutes.
const formParseBound = 30 * time.Second

// formLineAllocBound is how many times a header line's length the form reader
// may allocate to read it, the line's own copies included.
const formLineAllocBound = 4

var _ = Describe("formReader's parameter parse", func() {
	// within reads body as a whole form at maxLine, failing the spec once
	// formParseBound passes.
	within := func(body, contentType string, maxLine int) {
		done := make(chan error, 1)
		go func() {
			_, err := readWholeForm(strings.NewReader(body), contentType, maxLine)
			done <- err
		}()
		Eventually(done).WithTimeout(formParseBound).WithPolling(10 * time.Millisecond).Should(Receive(BeNil()))
	}

	It("reads a Content-Disposition line of maxLine bytes of parameters in linear time", func() {
		const maxLine = 4 << 20
		cd := "Content-Disposition: form-data; name=\"key\""
		line := cd + strings.Repeat(";", maxLine-len(cd)-2) + "x="
		within(formOf(line+"\r\n\r\nv", formFilePart("f", "d")), formCT, maxLine)
	})

	It("reads a Content-Type of many parameters in linear time", func() {
		contentType := "multipart/form-data" + strings.Repeat(";", 4<<20) + "boundary=bnd"
		within(formOf(formFieldPart("key", "k"), formFilePart("f", "d")), contentType, 1024)
	})

	It("allocates a small multiple of a Content-Disposition line of maxLine bytes of distinct parameters", func() {
		const maxLine = 4 << 20
		var b strings.Builder
		b.WriteString("Content-Disposition: form-data; name=\"key\"")
		for i := 0; b.Len() < maxLine-16; i++ {
			b.WriteString(";" + strconv.FormatInt(int64(i), 36))
		}
		line := b.String()
		body := formOf(line+"\r\n\r\nv", formFilePart("f", "d"))

		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := readWholeForm(strings.NewReader(body), formCT, maxLine)
		runtime.ReadMemStats(&after)

		Expect(err).NotTo(HaveOccurred())
		Expect(got.fields).To(Equal([][2]string{{"key", "v"}}))
		Expect(after.TotalAlloc-before.TotalAlloc).To(BeNumerically("<", formLineAllocBound*uint64(len(line))),
			"bytes allocated reading a %d-byte line", len(line))
	})
})

// patternReader yields n bytes of block repeated, and the CRC32 of what it
// yielded.
type patternReader struct {
	block []byte
	off   int
	n     int64
	crc   uint32
}

func (p *patternReader) Read(b []byte) (int, error) {
	if p.n == 0 {
		return 0, io.EOF
	}
	if int64(len(b)) > p.n {
		b = b[:p.n]
	}
	w := 0
	for w < len(b) {
		c := copy(b[w:], p.block[p.off:])
		w += c
		p.off = (p.off + c) % len(p.block)
	}
	p.n -= int64(w)
	p.crc = crc32.Update(p.crc, crc32.IEEETable, b[:w])
	return w, nil
}

// crcSink counts and hashes what it is written, and keeps none of it.
type crcSink struct {
	n   int64
	crc uint32
}

func (s *crcSink) Write(b []byte) (int, error) {
	s.n += int64(len(b))
	s.crc = crc32.Update(s.crc, crc32.IEEETable, b)
	return len(b), nil
}

var _ = Describe("formReader on a file larger than memory", func() {
	It("streams a multi-GiB file part with the heap's growth under a fixed multiple of the read size", func() {
		const size = 3<<30 + 7
		const readSize = 64 << 10
		block := []byte(strings.Repeat("lorem --bnd ipsum\r\n--bndx dolor --bnd--\r\n\n--bnd\r", 1000))
		gen := &patternReader{block: block, n: size}
		head := "--bnd\r\n" + formFieldPart("key", "big") + "\r\n--bnd\r\n" + formFilePart("big.bin", "")
		body := io.MultiReader(strings.NewReader(head), gen, strings.NewReader("\r\n--bnd--\r\n"))
		sink := &crcSink{}
		buf := make([]byte, readSize)

		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		fr, err := newFormReader(body, formCT, 4<<20)
		Expect(err).NotTo(HaveOccurred())
		_, file, err := fr.Fields()
		Expect(err).NotTo(HaveOccurred())
		n, err := io.CopyBuffer(sink, file, buf)
		Expect(err).NotTo(HaveOccurred())
		Expect(fr.Drain()).To(Succeed())
		runtime.ReadMemStats(&after)

		Expect(n).To(Equal(int64(size)))
		Expect(sink.crc).To(Equal(gen.crc))
		Expect(after.TotalAlloc-before.TotalAlloc).To(BeNumerically("<", 8*readSize),
			"bytes allocated while streaming %d bytes", size)
	})
})
