package op_test

import (
	"context"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

var _ = Describe("Error", func() {
	It("renders the S3 code and message", func() {
		Expect(op.ErrNoSuchBucket.Error()).To(Equal("NoSuchBucket"), "bare")
		Expect(op.ErrNoSuchBucket.WithMessage("gone").Error()).To(Equal("NoSuchBucket: gone"), "with message")
	})
	It("copies on WithMessage, leaving the sentinel bare", func() {
		e := op.ErrNoSuchBucket.WithMessage("gone")
		Expect(e).NotTo(BeIdenticalTo(op.ErrNoSuchBucket))
		Expect(op.ErrNoSuchBucket.Message).To(BeEmpty())
		Expect(e.Errno).To(Equal(op.ErrNoSuchBucket.Errno))
	})
	It("matches by code and status through errors.Is, message aside", func() {
		err := fmt.Errorf("loading bucket: %w", op.ErrNoSuchBucket.WithMessage("x"))
		Expect(err).To(MatchError(op.ErrNoSuchBucket), "wrapped copy")
		Expect(errors.Is(op.ErrNoSuchKey, op.ErrNoSuchBucket)).To(BeFalse(), "different code")
		Expect(errors.Is(op.ErrRequestTimeout, op.ErrRequestTimedOut)).To(BeFalse(), "same code, different status")
	})
	DescribeTable("carries radosgw's status and rgw_err number",
		func(e *op.Error, code string, status, errno int) {
			Expect(e.Code).To(Equal(code))
			Expect(e.Status).To(Equal(status))
			Expect(e.Errno).To(Equal(errno))
		},
		Entry("NoSuchKey is ENOENT", op.ErrNoSuchKey, "NoSuchKey", 404, 2),
		Entry("NoSuchBucket is ERR_NO_SUCH_BUCKET", op.ErrNoSuchBucket, "NoSuchBucket", 404, 2002),
		Entry("AccessDenied is EACCES", op.ErrAccessDenied, "AccessDenied", 403, 13),
		Entry("SlowDown is ERR_RATE_LIMITED", op.ErrSlowDown, "SlowDown", 503, 2218),
		Entry("NotImplemented", op.ErrNotImplemented, "NotImplemented", 501, 2201),
		Entry("MethodNotAllowed", op.ErrMethodNotAllowed, "MethodNotAllowed", 405, 2003),
		Entry("the 408 RequestTimeout is ETIMEDOUT", op.ErrRequestTimedOut, "RequestTimeout", 408, 110),
		Entry("the 400 RequestTimeout is ERR_REQUEST_TIMEOUT", op.ErrRequestTimeout, "RequestTimeout", 400, 2010),
		Entry("BucketNotEmpty is ENOTEMPTY", op.ErrBucketNotEmpty, "BucketNotEmpty", 409, 39),
		Entry("BucketAlreadyExists is EEXIST, the table's key", op.ErrBucketAlreadyExists, "BucketAlreadyExists", 409, 17),
		Entry("ConcurrentModification is ECANCELED", op.ErrConcurrentModification, "ConcurrentModification", 409, 125),
		Entry("RestoreAlreadyInProgress is Tentacle's ERR_RESTORE_ALREADY_IN_PROGRESS", op.ErrRestoreAlreadyInProgress, "RestoreAlreadyInProgress", 409, 2500),
		Entry("UnknownError is radosgw's fallback", op.ErrUnknown, "UnknownError", 500, 0),
	)
	It("has one sentinel per error row of radosgw's table, less the four it folds, plus Tentacle's row and the fallback", func() {
		// rgw_http_s3_errors at v19.2.6 has 86 error rows (its six success rows
		// aside); EPERM, EBUSY, ERR_ZERO_IN_URL and EDQUOT fold into a sentinel
		// with their code and status.
		Expect(op.Errors()).To(HaveLen(86 - 4 + 2))
	})
	It("shares a code and status only where radosgw's own table has duplicate rows, and never an errno", func() {
		// rgw_http_s3_errors at v19.2.6 (rgw_common.cc:51-144; v20.2.4 :52-146 adds one
		// row) carries the same {status, code} under several ret values: InvalidRequest/400
		// for ERR_INVALID_REQUEST (:62), ERR_INVALID_CORS_RULES_ERROR (:82),
		// ERR_INVALID_WEBSITE_ROUTING_RULES_ERROR (:83) and ERR_ZERO_IN_URL (:136, folded
		// into ErrInvalidRequest here); AccessDenied/403 for EACCES (:88), EPERM (:89,
		// folded into ErrAccessDenied) and ERR_MFA_REQUIRED (:96); NoSuchEntity/404 for
		// ERR_NO_ROLE_FOUND (:105) and ERR_NO_SUCH_ENTITY (:108); NoSuchCORSConfiguration/404
		// for ERR_NO_CORS_FOUND (:106) and ERR_NO_SUCH_CORS_CONFIGURATION (:109).
		// ServiceUnavailable/503 (:133-134) and InsufficientCapacity/507 (:142-143) are
		// doubled there too, but FromRADOS folds EBUSY, EDQUOT and ENOSPC into one
		// sentinel each. Errors.Is matches within a duplicate group (it compares code and
		// status), so a spec that must tell two of them apart compares Errno or identity.
		want := map[string]int{"InvalidRequest/400": 3, "AccessDenied/403": 2, "NoSuchEntity/404": 2, "NoSuchCORSConfiguration/404": 2}
		got := map[string]int{}
		errnos := map[int]string{}
		for _, e := range op.Errors() {
			got[fmt.Sprintf("%s/%d", e.Code, e.Status)]++
			Expect(errnos).NotTo(HaveKey(e.Errno), "%s shares errno %d with %s", e.Code, e.Errno, errnos[e.Errno])
			errnos[e.Errno] = e.Code
		}
		for k, n := range got {
			Expect(n).To(Equal(max(1, want[k])), "%s appears %d times", k, n)
		}
	})
	It("returns a copy of the set", func() {
		set := op.Errors()
		set[0] = nil
		Expect(op.Errors()[0]).To(BeIdenticalTo(op.ErrPermanentRedirect))
	})
})

var _ = Describe("FromRADOS", func() {
	DescribeTable("maps a seam error to the S3 error the scope implies",
		func(in error, scope op.Scope, want *op.Error) {
			got := op.FromRADOS(in, scope)
			Expect(got).To(MatchError(want), "mapped")
			Expect(got).To(MatchError(in), "cause kept")
			Expect(op.AsError(got)).To(BeIdenticalTo(want), "AsError")
		},
		Entry("ENOENT on a bucket", radosclient.ErrNotFound, op.ScopeBucket, op.ErrNoSuchBucket),
		Entry("ENOENT on an object", radosclient.ErrNotFound, op.ScopeObject, op.ErrNoSuchKey),
		Entry("ENOENT on a user", radosclient.ErrNotFound, op.ScopeUser, op.ErrNoSuchUser),
		Entry("ENOENT on an upload", radosclient.ErrNotFound, op.ScopeUpload, op.ErrNoSuchUpload),
		Entry("ENOENT at service scope is radosgw's ENOENT row", radosclient.ErrNotFound, op.ScopeService, op.ErrNoSuchKey),
		Entry("a raw ENOENT on a bucket", &radosclient.Error{Errno: -2, Op: "read"}, op.ScopeBucket, op.ErrNoSuchBucket),
		Entry("EEXIST on a bucket", radosclient.ErrExists, op.ScopeBucket, op.ErrBucketAlreadyExists),
		Entry("EEXIST on a user", radosclient.ErrExists, op.ScopeUser, op.ErrUserAlreadyExists),
		Entry("EPERM", radosclient.ErrPermission, op.ScopeObject, op.ErrAccessDenied),
		Entry("EINVAL", radosclient.ErrInvalid, op.ScopeObject, op.ErrInvalidArgument),
		Entry("ERANGE", radosclient.ErrRange, op.ScopeObject, op.ErrInvalidRange),
		Entry("ETIMEDOUT", radosclient.ErrTimedOut, op.ScopeObject, op.ErrRequestTimedOut),
		Entry("ENOSPC", radosclient.ErrNoSpace, op.ScopeObject, op.ErrInsufficientCapacity),
		Entry("ECANCELED is radosgw's 409 ConcurrentModification row", radosclient.ErrCanceled, op.ScopeObject, op.ErrConcurrentModification),
		Entry("busy resharding is unmapped too", radosclient.ErrBusyResharding, op.ScopeObject, op.ErrUnknown),
		Entry("ENODATA, which the table lacks, is unmapped", radosclient.ErrNoData, op.ScopeObject, op.ErrUnknown),
		Entry("a raw errno the table lacks is unmapped", &radosclient.Error{Errno: 5}, op.ScopeObject, op.ErrUnknown),
		Entry("a raw ENOTEMPTY", &radosclient.Error{Errno: 39, Op: "remove"}, op.ScopeBucket, op.ErrBucketNotEmpty),
		Entry("a raw EBUSY", &radosclient.Error{Errno: 16}, op.ScopeBucket, op.ErrServiceUnavailable),
		Entry("a raw EDQUOT", &radosclient.Error{Errno: 122}, op.ScopeObject, op.ErrInsufficientCapacity),
		Entry("a raw EACCES", &radosclient.Error{Errno: 13}, op.ScopeObject, op.ErrAccessDenied),
		Entry("a closed seam is internal", radosclient.ErrClosed, op.ScopeObject, op.ErrInternalError),
		Entry("an op run before its results is internal", radosclient.ErrIncomplete, op.ScopeObject, op.ErrInternalError),
		Entry("a canceled context", context.Canceled, op.ScopeObject, op.ErrRequestTimedOut),
		Entry("an expired deadline", context.DeadlineExceeded, op.ScopeObject, op.ErrRequestTimedOut),
	)
	It("passes an S3 error through untouched", func() {
		err := fmt.Errorf("x: %w", op.ErrQuotaExceeded)
		Expect(op.FromRADOS(err, op.ScopeObject)).To(BeIdenticalTo(err))
	})
	It("maps nil to nil", func() {
		Expect(op.FromRADOS(nil, op.ScopeObject)).To(Succeed())
	})
	It("maps a foreign error to InternalError", func() {
		Expect(op.AsError(op.FromRADOS(errors.New("boom"), op.ScopeObject))).To(BeIdenticalTo(op.ErrInternalError))
	})
})

var _ = Describe("AsError", func() {
	It("finds the *Error in the chain, message and all", func() {
		e := op.ErrNoSuchKey.WithMessage("k")
		Expect(op.AsError(fmt.Errorf("get: %w", e))).To(BeIdenticalTo(e))
	})
	It("turns an unknown error into InternalError", func() {
		Expect(op.AsError(errors.New("boom"))).To(BeIdenticalTo(op.ErrInternalError))
	})
	It("turns a context error into the 408 RequestTimeout", func() {
		Expect(op.AsError(fmt.Errorf("reading: %w", context.DeadlineExceeded))).To(BeIdenticalTo(op.ErrRequestTimedOut))
	})
})
