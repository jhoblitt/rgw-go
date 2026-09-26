package acl_test

import (
	. "github.com/onsi/ginkgo/v2"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
)

// squid checks re-encodings against goldens from the v19 dencoder image.
var squid = goldentest.Options{Release: denc.Squid}

var _ = Describe("corpus goldens", func() {
	const dir = "testdata"
	It("RGWAccessControlPolicy", func() {
		goldentest.RoundTrip(dir, "RGWAccessControlPolicy", squid, acl.DecodePolicy,
			func(e *denc.Encoder, v acl.Policy, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWAccessControlList", func() {
		goldentest.RoundTrip(dir, "RGWAccessControlList", squid, acl.DecodeList,
			func(e *denc.Encoder, v acl.List, r denc.Release) { v.Encode(e, r) })
	})
	It("ACLOwner", func() {
		goldentest.RoundTrip(dir, "ACLOwner", squid, acl.DecodeOwner,
			func(e *denc.Encoder, v acl.Owner, r denc.Release) { v.Encode(e, r) })
	})
	It("ACLGrant", func() {
		goldentest.RoundTrip(dir, "ACLGrant", squid, acl.DecodeGrant,
			func(e *denc.Encoder, v acl.Grant, r denc.Release) { v.Encode(e, r) })
	})
	It("ACLPermission", func() {
		goldentest.RoundTrip(dir, "ACLPermission", squid, acl.DecodePermission,
			func(e *denc.Encoder, v acl.Permission, r denc.Release) { v.Encode(e, r) })
	})
	It("ACLGranteeType", func() {
		goldentest.RoundTrip(dir, "ACLGranteeType", squid, acl.DecodeGranteeType,
			func(e *denc.Encoder, v acl.GranteeType, r denc.Release) { v.Encode(e, r) })
	})
})
