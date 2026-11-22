package cephconf_test

import (
	"os/user"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
)

// These pin docs/differences.md, "setuser and setgroup numbers are read
// strictly": radosgw's atoi takes any leading number as the id, where rgw-go
// looks up as a name anything but a whole decimal string.
var _ = DescribeTable("resolving a setuser that atoi reads as a number but is no whole decimal string",
	func(s string) {
		_, _, err := cephconf.ResolveUser(s)
		Expect(err).To(MatchError(user.UnknownUserError(s)), "setuser %q is looked up as a name", s)
		_, err = cephconf.ResolveGroup(s)
		Expect(err).To(MatchError(user.UnknownGroupError(s)), "setgroup %q is looked up as a name", s)
	},
	Entry("trailing letters, which atoi reads as 12", "12abc"),
	Entry("a leading blank, which atoi skips", " 12"),
	Entry("a plus sign, which atoi takes", "+12"),
	Entry("a name that starts with a digit, which atoi reads as 1", "1password-xyz"),
	Entry("2^32+1, which atoi wraps to 1 on glibc x86_64", "4294967297"),
)
