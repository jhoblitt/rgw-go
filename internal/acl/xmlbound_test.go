package acl_test

import (
	"fmt"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

var startTag = regexp.MustCompile(`<[A-Za-z]`)

// units is how many elements and attributes the generated document doc
// starts, each attribute counting as one as the reader counts it. Every
// attribute the specs write is double-quoted.
func units(doc string) int {
	return len(startTag.FindAllStringIndex(doc, -1)) + strings.Count(doc, `="`)
}

// maxACLGrants is rgw_acl_grants_max_num's default.
const maxACLGrants = 100

// grantTo is a grant to user u with every element S3 gives one.
func grantTo(u int) string {
	return xmlGrant("CanonicalUser", fmt.Sprintf("<ID>u%d</ID><DisplayName>U%d</DisplayName>", u, u), "READ")
}

// usersResolver knows alice and the users u0 to u(n-1).
func usersResolver(n int) mapResolver {
	r := newResolver()
	for u := range n {
		r.names[fmt.Sprintf("u%d", u)] = fmt.Sprintf("U%d", u)
	}
	return r
}

// grantsDoc is a policy of n grants, each to a user of its own, or as many
// as fit in rgw_max_put_param_size, 1 MiB, when n is 0.
func grantsDoc(n int) (doc string, grants int) {
	var parts []string
	size := len(aclDoc())
	for u := 0; n == 0 || u < n; u++ {
		g := grantTo(u)
		if size+len(g) > 1<<20 {
			break
		}
		size += len(g)
		parts = append(parts, g)
	}
	return aclDoc(parts...), len(parts)
}

var _ = Describe("ParseS3XML against the bounds on a document's elements and depth", func() {
	// radosgw checks the grant count once the policy is parsed, so the largest
	// body is the grants that fit in rgw_max_put_param_size.
	DescribeTable("reads the largest document radosgw's limits allow",
		func(ctx SpecContext, n int) {
			doc, grants := grantsDoc(n)
			p, err := acl.ParseS3XML(ctx, usersResolver(grants), []byte(doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.ACL.Grants).To(HaveLen(grants))
			elements := units(doc)
			Expect(elements * 3 / 2).To(BeNumerically("<=", xmltext.MaxElements))
		},
		Entry("rgw_acl_grants_max_num grants", maxACLGrants),
		Entry("the most grants 1 MiB holds", 0),
	)

	It("answers its own error for a document past the bound, as it does for one that does not parse", func(ctx SpecContext) {
		_, err := acl.ParseS3XML(ctx, newResolver(), []byte(aclDoc(strings.Repeat("<x/>", xmltext.MaxElements))))
		Expect(err).To(MatchError(acl.ErrInvalid))

		nest := strings.Repeat("<x>", xmltext.MaxDepth) + strings.Repeat("</x>", xmltext.MaxDepth)
		_, err = acl.ParseS3XML(ctx, newResolver(), []byte(aclDoc(nest)))
		Expect(err).To(MatchError(acl.ErrInvalid))
	})
})
