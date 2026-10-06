package authz_test

import (
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

var _ = Describe("ErrorFor", func() {
	// header stands for a request header's value that an error's text may
	// carry and the mapped error must not.
	const header = `id="header-value"`

	DescribeTable("maps a sentinel to radosgw's S3 error and message, keeping no text of the cause",
		func(err error, want *op.Error, message string) {
			got := authz.ErrorFor(err)
			Expect(got).To(MatchError(want))
			e := op.AsError(got)
			Expect(e.Code).To(Equal(want.Code))
			Expect(e.Status).To(Equal(want.Status))
			Expect(e.Message).To(Equal(message))
			Expect(got.Error()).NotTo(ContainSubstring(header))
		},
		Entry("an unresolvable email in an ACL document, with parse_policy's message",
			&acl.ParseError{Message: "The e-mail address you provided does not match any account on record.", Err: acl.ErrUnresolvableEmail},
			op.ErrUnresolvableGrantByEmail, "The e-mail address you provided does not match any account on record."),
		Entry("a bare unresolvable email, with no message",
			fmt.Errorf("%s: %w", header, acl.ErrUnresolvableEmail), op.ErrUnresolvableGrantByEmail, ""),
		Entry("an ACL document radosgw refuses, with its message",
			&acl.ParseError{Message: "Invalid Owner ID", Err: acl.ErrInvalid}, op.ErrInvalidArgument, "Invalid Owner ID"),
		Entry("an ACL document radosgw refuses without a message",
			&acl.ParseError{Err: acl.ErrInvalid}, op.ErrInvalidArgument, ""),
		Entry("an invalid canned ACL or grant header, whose text names the header",
			fmt.Errorf("grant %q: %w", header, acl.ErrInvalid), op.ErrInvalidArgument, ""),
		Entry("a missing owner", fmt.Errorf("%s: %w", header, acl.ErrNoSuchOwner), op.ErrInvalidArgument, ""),
		Entry("a grant header naming no user or account, radosgw's ENOENT",
			fmt.Errorf("grant %q: %w", header, acl.ErrGranteeNotFound), op.ErrNoSuchKey, ""),
		Entry("a new owner, with RGWPutACLs' message", acl.ErrOwnerMismatch, op.ErrAccessDenied, "Cannot modify ACL Owner"),
		Entry("too many grants, with RGWPutACLs' message",
			&acl.ParseError{Message: "The request is rejected, because the acl grants number you requested is larger than the maximum 3 grants allowed in an acl.", Err: acl.ErrTooManyGrants},
			op.ErrLimitExceeded, "The request is rejected, because the acl grants number you requested is larger than the maximum 3 grants allowed in an acl."),
		Entry("an invalid tag", fmt.Errorf("%w: key %s", tags.ErrInvalidTag, header), op.ErrInvalidTag, ""),
		Entry("a malformed Tagging document", fmt.Errorf("%w: %s", tags.ErrMalformedXML, header), op.ErrMalformedXML, ""),
	)

	It("maps a policy that does not parse to InvalidArgument with radosgw's message", func() {
		_, err := policy.Parse(`{"Version": "2012-10-17", "Statement": [{"Effect": "Maybe"}]}`, policy.ParseOptions{Release: denc.Squid})
		var pe *policy.ParseError
		Expect(errors.As(err, &pe)).To(BeTrue(), "got %v", err)
		got := op.AsError(authz.ErrorFor(fmt.Errorf("parsing: %w", err)))
		Expect(got.Code).To(Equal("InvalidArgument"))
		Expect(got.Message).To(Equal(pe.Error()), "RGWPutBucketPolicy::execute, rgw_op.cc:8116-8120 at v19.2.6")
		Expect(got.Message).To(HavePrefix("At character offset "))
	})

	DescribeTable("passes on an error that names no sentinel of acl, policy or tags",
		func(err error) {
			Expect(authz.ErrorFor(err)).To(BeIdenticalTo(err))
		},
		Entry("an S3 error", op.ErrNoSuchBucket),
		Entry("an S3 error with a message", op.ErrInvalidArgument.WithMessage("x")),
		Entry("an S3 error already chosen for an acl sentinel", fmt.Errorf("%w: %w", op.ErrInvalidRequest, acl.ErrInvalid)),
		Entry("a plain error", errors.New("plain")),
	)

	It("returns nil for nil", func() {
		Expect(authz.ErrorFor(nil)).To(Succeed())
	})
})
