package op_test

import (
	"context"
	"errors"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("deleteMarkerVersion", func() {
	var (
		authz *opfakes.FakeAuthorizer
		r     *op.Request
	)
	BeforeEach(func() {
		authz = &opfakes.FakeAuthorizer{}
		key := meta.ObjKey{Name: "k", Instance: "m1"}
		r = &op.Request{
			Object: key, ObjState: &op.ObjectState{Key: key, DeleteMarker: true},
			BucketRec: &op.BucketRecord{}, Env: &op.Env{Authz: authz},
		}
	})
	// markerCase is the actions VerifyMarkerIn refuses, whether the marker
	// is answered 405, and the actions it is asked for, in order.
	type markerCase struct {
		refused  []policy.Action
		answered bool
		asked    []policy.Action
	}
	DescribeTable("answers the marker only to a requester holding every action it is given, asking each in turn until one is refused",
		func(ctx SpecContext, c markerCase) {
			authz.VerifyMarkerInCalls(func(_ context.Context, _ *op.Request, a policy.Action, _ acl.Permission, _ *op.BucketRecord, _ meta.ObjKey) error {
				if slices.Contains(c.refused, a) {
					return op.ErrAccessDenied
				}
				return nil
			})
			err := op.DeleteMarkerVersionForTest(ctx, r, op.BeforeVerify(op.ErrNoSuchKey), false,
				policy.S3GetObjectVersion, policy.S3GetObjectVersionAttributes)
			Expect(op.IsBeforeVerify(err)).To(BeTrue())
			if c.answered {
				Expect(err).To(MatchError(op.ErrMethodNotAllowed))
				Expect(errors.As(err, new(*op.MarkerVersionError))).To(BeTrue())
			} else {
				Expect(err).To(MatchError(op.ErrNoSuchKey), "the read's own refusal")
				Expect(errors.As(err, new(*op.MarkerVersionError))).To(BeFalse())
			}
			var asked []policy.Action
			for i := range authz.VerifyMarkerInCallCount() {
				_, _, a, perm, _, key := authz.VerifyMarkerInArgsForCall(i)
				Expect([]any{perm, key}).To(Equal([]any{acl.PermFor(a), r.Object}))
				asked = append(asked, a)
			}
			Expect(asked).To(Equal(c.asked))
		},
		Entry("both held", markerCase{answered: true, asked: []policy.Action{policy.S3GetObjectVersion, policy.S3GetObjectVersionAttributes}}),
		Entry("the first refused, the second never asked", markerCase{
			refused: []policy.Action{policy.S3GetObjectVersion}, asked: []policy.Action{policy.S3GetObjectVersion},
		}),
		Entry("the second refused", markerCase{
			refused: []policy.Action{policy.S3GetObjectVersionAttributes},
			asked:   []policy.Action{policy.S3GetObjectVersion, policy.S3GetObjectVersionAttributes},
		}),
		Entry("both refused", markerCase{
			refused: []policy.Action{policy.S3GetObjectVersion, policy.S3GetObjectVersionAttributes},
			asked:   []policy.Action{policy.S3GetObjectVersion},
		}),
	)
})
