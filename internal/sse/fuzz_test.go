package sse_test

import (
	"bytes"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/sse"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// radosgwRead is the configuration radosgw stores for a document xmltext
// reads as root: RGWBucketEncryptionConfig::decode_xml and its members'
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.cc#L8-L34),
// each a lookup of the first child of its name. ok is false where radosgw
// refuses the document: no ServerSideEncryptionConfiguration root, or a
// BucketKeyEnabled its decode_xml_obj(bool) cannot read
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L392-L406).
func radosgwRead(root *xmltext.Element) (c sse.BucketEncryptionConfig, ok bool) {
	if root.Name != "ServerSideEncryptionConfiguration" {
		return c, false
	}
	r := root.FindFirst("Rule")
	if r == nil {
		return c, true
	}
	c.Rule = &sse.ServerSideEncryptionConfiguration{}
	if a := r.FindFirst("ApplyServerSideEncryptionByDefault"); a != nil {
		if k := a.FindFirst("KMSMasterKeyID"); k != nil {
			c.Rule.Apply.KMSMasterKeyID = k.Text
		}
		if s := a.FindFirst("SSEAlgorithm"); s != nil {
			c.Rule.Apply.SSEAlgorithm = s.Text
		}
	}
	if b := r.FindFirst("BucketKeyEnabled"); b != nil {
		c.Rule.BucketKeyEnabled, ok = radosgwBool(b.Text)
		return c, ok
	}
	return c, true
}

// radosgwBool is decode_xml_obj(bool): true or false in any ASCII case, as
// strncasecmp compares them in the C locale, else an int in range that strtol
// reads in base 10 with only C whitespace around it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L282-L308,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L366-L377).
func radosgwBool(s string) (v, ok bool) {
	switch {
	case asciiFold(s) == "true":
		return true, true
	case asciiFold(s) == "false":
		return false, true
	}
	const cSpace = " \t\n\v\f\r"
	i, err := strconv.ParseInt(strings.Trim(s, cSpace), 10, 32)
	if err != nil {
		return false, false
	}
	return i != 0, true
}

// asciiFold is s with its ASCII upper-case letters lowered, as tolower does
// in the C locale.
func asciiFold(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// FuzzParseXML parses arbitrary bodies. ParseXML must not panic. A refusal
// must be ErrMalformedXML, short, with the zero configuration. An accepted
// body must be one xmltext reads, whose configuration is the one radosgw
// stores for it and passes Validate, and which MarshalS3XML writes back as
// a document that reads as the same configuration. go test runs the seeds;
// go test -fuzz=FuzzParseXML explores from them.
func FuzzParseXML(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		doc("", ""),
		doc(" "+s3NS, algorithm("AES256")),
		doc(" "+s3NS, rule(apply(element("KMSMasterKeyID", "k")+element("SSEAlgorithm", "aws:kms"))+
			element("BucketKeyEnabled", "true"))),
		doc("", rule(element("BucketKeyEnabled", " 1 ")+apply(""))),
		[]byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + string(doc(" "+s3NS, "\n "+algorithm("aws:kms:dsse")+"\n"))),
		doc("", algorithm("&#65;<![CDATA[ES]]>256<!-- c --><?p?>")),
		doc("", algorithm("AES256")+algorithm("aws:kms")),
		doc("", rule(apply(element("SseAlgorithm", "AES256")))),
		doc("", rule(element("BlockedEncryptionTypes", element("EncryptionType", "SSE-C")))),
		[]byte(`<!DOCTYPE x [<!ENTITY a "AES256">]>` + string(doc("", algorithm("&a;")))),
		doc(` xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/"`, `<s3:Rule/>`),
		append([]byte("\xef\xbb\xbf"), doc("", "<Rule/>")...),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		c, err := sse.ParseXML(body)
		if err != nil {
			if !errors.Is(err, sse.ErrMalformedXML) {
				t.Fatalf("%q: %v is not ErrMalformedXML", body, err)
			}
			if len(err.Error()) >= 512 {
				t.Fatalf("%q: a %d-byte message", body, len(err.Error()))
			}
			if !reflect.DeepEqual(c, sse.BucketEncryptionConfig{}) {
				t.Fatalf("%q: refused with %+v", body, c)
			}
			return
		}
		root, perr := xmltext.Parse(body)
		if perr != nil {
			t.Fatalf("%q: accepted, but xmltext refuses it: %v", body, perr)
		}
		want, ok := radosgwRead(root)
		if !ok {
			t.Fatalf("%q: accepted, but radosgw refuses it", body)
		}
		if !reflect.DeepEqual(c, want) {
			t.Fatalf("%q: read as %+v, radosgw stores %+v", body, c, want)
		}
		if verr := c.Validate(); verr != nil {
			t.Fatalf("%q: accepted, but Validate refuses %+v: %v", body, c, verr)
		}
		again, err := sse.ParseXML(c.MarshalS3XML())
		if err != nil {
			t.Fatalf("%q: %+v renders as %q, which does not parse: %v", body, c, c.MarshalS3XML(), err)
		}
		if !reflect.DeepEqual(again, c) {
			t.Fatalf("%q: %+v renders as a document that reads as %+v", body, c, again)
		}
	})
}

// FuzzDecodeBucketEncryptionConfig decodes arbitrary bytes as the
// user.rgw.sse-s3.policy attr. A decode must not panic and must read no more
// than it was given; one that succeeds must re-encode, on both releases, to
// bytes that decode to the same value and encode to themselves. go test runs
// the seeds; go test -fuzz=FuzzDecodeBucketEncryptionConfig explores from
// them.
func FuzzDecodeBucketEncryptionConfig(f *testing.F) {
	kms := configBytes(1, 1, 1, ruleBytes(1, 1, applyBytes(1, 1, "k", "aws:kms"), 1))
	for _, seed := range [][]byte{
		nil,
		configBytes(1, 1, 0, nil),
		kms,
		kms[:len(kms)-1],
		configBytes(2, 1, 2, ruleBytes(3, 1, applyBytes(2, 1, "k", "AES256", 9), 2, 7), 8),
		configBytes(1, 1, 0, nil, 1, 2, 3),
		append(configBytes(1, 1, 0, nil), 'x'),
		configBytes(1, 1, 1, ruleBytes(1, 1, framed(1, 1, func(e *denc.Encoder) { e.U32(0xffffffff) }), 0)),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		d := denc.NewDecoder(b)
		c := sse.DecodeBucketEncryptionConfig(d)
		if d.Offset() > len(b) {
			t.Fatalf("read %d of %d bytes", d.Offset(), len(b))
		}
		if d.Err() != nil {
			return
		}
		var first []byte
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			e := denc.NewEncoder()
			c.Encode(e, r)
			enc := e.Bytes()
			if first == nil {
				first = enc
			} else if !bytes.Equal(enc, first) {
				t.Fatalf("on %s: encoding %x differs from %x", r, enc, first)
			}
			d2 := denc.NewDecoder(enc)
			again := sse.DecodeBucketEncryptionConfig(d2)
			if err := d2.Err(); err != nil {
				t.Fatalf("on %s: re-encoding %x does not decode: %v", r, enc, err)
			}
			if d2.Remaining() != 0 {
				t.Fatalf("on %s: re-encoding %x leaves %d bytes", r, enc, d2.Remaining())
			}
			if !reflect.DeepEqual(again, c) {
				t.Fatalf("on %s: %+v decoded again as %+v", r, c, again)
			}
			e2 := denc.NewEncoder()
			again.Encode(e2, r)
			if !bytes.Equal(e2.Bytes(), enc) {
				t.Fatalf("on %s: re-encoding is %x, then %x", r, enc, e2.Bytes())
			}
		}
	})
}
