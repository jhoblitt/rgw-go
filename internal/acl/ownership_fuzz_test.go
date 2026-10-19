package acl_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// v21Ownership is the setting v21 stores for a document xmltext reads as
// root, or the message it refuses the document with: get_params' root lookup
// and OwnershipControls::decode_xml's, each the first child of its name, and
// parse's comparison with the three names as written
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4570-L4577,
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L36-L69).
func v21Ownership(root *xmltext.Element) (o acl.Ownership, message string) {
	if root.Name != "OwnershipControls" {
		return acl.ObjectWriter, msgNoOwnershipControls
	}
	r := root.FindFirst("Rule")
	if r == nil {
		return acl.ObjectWriter, msgNoRule
	}
	v := r.FindFirst("ObjectOwnership")
	if v == nil {
		return acl.ObjectWriter, msgNoObjectOwnership
	}
	switch v.Text {
	case "BucketOwnerEnforced":
		return acl.BucketOwnerEnforced, ""
	case "BucketOwnerPreferred":
		return acl.BucketOwnerPreferred, ""
	case "ObjectWriter":
		return acl.ObjectWriter, ""
	}
	return acl.ObjectWriter, msgBadObjectOwnership
}

// FuzzParseOwnershipXML parses arbitrary bodies. ParseOwnershipXML must not
// panic. A refusal must be a short *acl.ParseError wrapping ErrMalformedXML,
// with ObjectWriter. When xmltext reads the body, its message, if it has one,
// must be the one v21 refuses the body with; when xmltext refuses it, the only
// message it may carry is the root's, which the scan sends before reading
// the rest. An accepted body must be one
// xmltext reads and v21 stores as the same setting, and MarshalS3XML must write
// that setting back as a document that reads as it. go test runs the seeds;
// go test -fuzz=FuzzParseOwnershipXML explores from them.
func FuzzParseOwnershipXML(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		ownershipDoc("", ""),
		ownershipDoc(" "+s3NS, ownershipRule("BucketOwnerEnforced")),
		[]byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + string(ownershipDoc(" "+s3NS, "\n "+ownershipRule("ObjectWriter")+"\n"))),
		ownershipDoc("", ownershipRule("&#66;<![CDATA[ucketOwner]]>Preferred<!-- c --><?p?>")),
		ownershipDoc("", "<Rule/>"),
		ownershipDoc("", ownershipRule(" ObjectWriter")),
		ownershipDoc("", ownershipRule("ObjectWriter")+ownershipRule("BucketOwnerEnforced")),
		ownershipDoc("", element("Rule", "<Foo/>"+element("ObjectOwnership", "ObjectWriter"))),
		[]byte(`<!DOCTYPE x [<!ENTITY a "ObjectWriter">]>` + string(ownershipDoc("", ownershipRule("&a;")))),
		ownershipDoc(` xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/"`, `<s3:Rule/>`),
		[]byte(`<Ownership>` + ownershipRule("ObjectWriter") + `</Ownership>`),
		[]byte(`<OwnershipControls>` + ownershipRule("Nobody")),
		append([]byte("\xef\xbb\xbf"), ownershipDoc("", ownershipRule("BucketOwnerEnforced"))...),
		ownershipDoc("", ownershipRule(strings.Repeat("\u0085", 64))),
		ownershipDoc("", ownershipRule(strings.Repeat("\U000E0001", 64))),
		ownershipDoc(` xmlns="`+strings.Repeat("\U000E0001", 64)+`"`, ownershipRule("ObjectWriter")),
		[]byte(` <?xml version="1.0"?><Foo/>`),
		[]byte(`<Foo a="1" a="1"/>`),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		o, err := acl.ParseOwnershipXML(body)
		root, perr := xmltext.Parse(body)
		if err != nil {
			pe, ok := errors.AsType[*acl.ParseError](err)
			switch {
			case !ok || !errors.Is(err, acl.ErrMalformedXML):
				t.Fatalf("%q: %v is not an *acl.ParseError wrapping ErrMalformedXML", body, err)
			case len(err.Error()) >= 512:
				t.Fatalf("%q: a %d-byte refusal", body, len(err.Error()))
			case o != acl.ObjectWriter:
				t.Fatalf("%q: refused with %v", body, o)
			case perr != nil && pe.Message != "" && pe.Message != msgNoOwnershipControls:
				t.Fatalf("%q: xmltext refuses it, yet the refusal carries %q", body, pe.Message)
			case perr == nil && pe.Message != "":
				if _, want := v21Ownership(root); pe.Message != want {
					t.Fatalf("%q: refused with %q, where v21 refuses with %q", body, pe.Message, want)
				}
			}
			return
		}
		if perr != nil {
			t.Fatalf("%q: accepted, but xmltext refuses it: %v", body, perr)
		}
		if want, message := v21Ownership(root); message != "" || o != want {
			t.Fatalf("%q: read as %v, where v21 stores %v or refuses with %q", body, o, want, message)
		}
		if again, err := acl.ParseOwnershipXML(o.MarshalS3XML()); err != nil || again != o {
			t.Fatalf("%q: %v renders as %q, which reads as %v, %v", body, o, o.MarshalS3XML(), again, err)
		}
	})
}

// FuzzDecodeOwnership decodes arbitrary bytes as the
// user.rgw.ownership-controls attr. A decode must not panic and must read no
// more than it was given. One that fails must be ObjectWriter, as OwnershipOf
// must be; one that succeeds must re-encode, on both releases, to bytes that
// decode to the same setting, and OwnershipOf must read it, or ObjectWriter
// for a byte outside the three. go test runs the seeds; go test
// -fuzz=FuzzDecodeOwnership explores from them.
func FuzzDecodeOwnership(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		ownershipBytes(1, 1, 0),
		ownershipBytes(1, 1, 2),
		ownershipBytes(1, 1, 3),
		ownershipBytes(2, 1, 1, 7, 7),
		ownershipBytes(2, 2, 0),
		{1, 1, 0, 0, 0, 0, 0},
		{1, 1, 1, 0, 0, 0},
		append(ownershipBytes(1, 1, 1), 'x'),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		d := denc.NewDecoder(b)
		o := acl.DecodeOwnership(d)
		of := acl.OwnershipOf(map[string][]byte{acl.AttrOwnershipControls: b})
		if d.Offset() > len(b) {
			t.Fatalf("read %d of %d bytes", d.Offset(), len(b))
		}
		if d.Err() != nil {
			if o != acl.ObjectWriter || of != acl.ObjectWriter {
				t.Fatalf("%x: a failed decode read as %v, and OwnershipOf as %v", b, o, of)
			}
			return
		}
		switch {
		case o <= acl.ObjectWriter && of != o:
			t.Fatalf("%x: decoded as %v, OwnershipOf %v", b, o, of)
		case o > acl.ObjectWriter && of != acl.ObjectWriter:
			t.Fatalf("%x: a byte outside the three, OwnershipOf %v", b, of)
		}
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			e := denc.NewEncoder()
			o.Encode(e, r)
			want := []byte{1, 1, 1, 0, 0, 0, byte(o)}
			if !bytes.Equal(e.Bytes(), want) {
				t.Fatalf("on %s: %v encodes as %x, not %x", r, o, e.Bytes(), want)
			}
			d2 := denc.NewDecoder(e.Bytes())
			if again := acl.DecodeOwnership(d2); d2.Err() != nil || d2.Remaining() != 0 || again != o {
				t.Fatalf("on %s: %x decodes again as %v, %d bytes left, %v", r, e.Bytes(), again, d2.Remaining(), d2.Err())
			}
		}
	})
}
