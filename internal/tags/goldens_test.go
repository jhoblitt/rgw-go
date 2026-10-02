package tags_test

import (
	. "github.com/onsi/ginkgo/v2"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// squid checks re-encodings against goldens from the v19 dencoder image.
var squid = goldentest.Options{Release: denc.Squid}

var _ = Describe("corpus goldens", func() {
	It("RGWObjTags", func() {
		goldentest.RoundTrip("testdata", "RGWObjTags", squid, tags.Decode,
			func(e *denc.Encoder, v tags.Set, r denc.Release) { v.Encode(e, r) })
	})
})
