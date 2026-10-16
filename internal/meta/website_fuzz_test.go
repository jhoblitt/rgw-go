package meta_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// FuzzDecodeWebsiteConf decodes arbitrary bytes as an RGWBucketWebsiteConf,
// which a bucket instance read from RADOS carries. A decode must not panic and
// must read no more than it was given; one that succeeds must re-encode, on
// both releases, to bytes that decode to the same value and encode to
// themselves. go test runs the seeds; go test -fuzz=FuzzDecodeWebsiteConf
// explores from them.
func FuzzDecodeWebsiteConf(f *testing.F) {
	v1 := websiteV1Bytes("index.html", "error.html", rulesBytes(docsRuleBytes(), imagesRuleBytes()), redirectBytes("", "", 0))
	for _, seed := range [][]byte{
		nil,
		indexSiteV2(),
		redirectSiteV2(),
		swiftSiteV2(),
		v1,
		reframe(swiftSiteV2(), 3, []byte("later")),
		indexSiteV2()[:len(indexSiteV2())-1],
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		d := denc.NewDecoder(b)
		w := meta.DecodeWebsiteConf(d)
		if d.Offset() > len(b) {
			t.Fatalf("read %d of %d bytes", d.Offset(), len(b))
		}
		if d.Err() != nil {
			return
		}
		var first []byte
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			e := denc.NewEncoder()
			w.Encode(e, r)
			enc := e.Bytes()
			if first == nil {
				first = enc
			} else if !bytes.Equal(enc, first) {
				t.Fatalf("on %s: encoding %x differs from %x", r, enc, first)
			}
			d2 := denc.NewDecoder(enc)
			again := meta.DecodeWebsiteConf(d2)
			if err := d2.Err(); err != nil {
				t.Fatalf("on %s: re-encoding %x does not decode: %v", r, enc, err)
			}
			if d2.Remaining() != 0 {
				t.Fatalf("on %s: re-encoding %x leaves %d bytes", r, enc, d2.Remaining())
			}
			if !reflect.DeepEqual(again, w) {
				t.Fatalf("on %s: %+v decoded again as %+v", r, w, again)
			}
			e2 := denc.NewEncoder()
			again.Encode(e2, r)
			if !bytes.Equal(e2.Bytes(), enc) {
				t.Fatalf("on %s: re-encoding is %x, then %x", r, enc, e2.Bytes())
			}
		}
	})
}
