package meta_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("ObjVersion", func() {
	It("round-trips cls_version's obj_version corpus", func() {
		goldentest.RoundTrip("../cls/version/testdata", "obj_version", squid, meta.DecodeObjVersion,
			func(e *denc.Encoder, v meta.ObjVersion, r denc.Release) { v.Encode(e, r) })
	})
	It("converts to and from cls/version's ObjVersion", func() {
		Expect(version.ObjVersion(meta.ObjVersion{Ver: 3, Tag: "t"})).To(Equal(version.ObjVersion{Ver: 3, Tag: "t"}))
		Expect(meta.ObjVersion(version.ObjVersion{Ver: 4, Tag: "u"})).To(Equal(meta.ObjVersion{Ver: 4, Tag: "u"}))
	})
})
