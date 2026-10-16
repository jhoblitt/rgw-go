package op_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// preflightRecorder is a recorder named as the CORS preflight.
type preflightRecorder struct{ *recorder }

func (preflightRecorder) Name() string { return op.OptionsCORSName }

// unverifiedAlice is alice's identity as the verifier grants a preflight:
// on her key's id alone.
func unverifiedAlice() op.Identity {
	u := meta.NewUserInfo()
	u.UserID = meta.UserID{ID: "alice"}
	return op.Identity{
		User: &u, Owner: meta.UserOwner(u.UserID), AccessKey: "AKALICE", OpMask: op.OpTypeAll,
		Admin: true, System: true, Unverified: true,
	}
}

var _ = Describe("Run with an unverified identity", func() {
	var req *op.Request
	BeforeEach(func() {
		req = &op.Request{Identity: unverifiedAlice(), Env: &op.Env{}}
	})

	It("runs the CORS preflight", func(ctx SpecContext) {
		rec := &recorder{mask: op.OpTypeRead}
		Expect(op.Run(ctx, preflightRecorder{rec}, req)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
	})

	It("refuses any other op before it reads anything", func(ctx SpecContext) {
		rec := &recorder{mask: op.OpTypeRead}
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrInternalError))
		Expect(rec.calls).To(BeEmpty())
	})

	// Each op is its zero value: the refusal comes before Init, which a zero
	// op could not survive.
	DescribeTable("refuses every op but the preflight, naming it and nothing of the credentials",
		func(ctx SpecContext, o op.Op) {
			err := op.Run(ctx, o, req)
			Expect(err).To(MatchError(op.ErrInternalError), o.Name())
			Expect(err.Error()).To(ContainSubstring(o.Name()))
			Expect(err.Error()).NotTo(ContainSubstring("AKALICE"), o.Name())
			Expect(op.AsError(err)).To(BeIdenticalTo(op.ErrInternalError), "the client sees no text of it")
		},
		Entry("abort_multipart", &op.AbortMultipart{}),
		Entry("add_user_caps", &op.AddCaps{}),
		Entry("check_bucket_index", &op.CheckBucketIndex{}),
		Entry("complete_multipart", &op.CompleteMultipart{}),
		Entry("copy_obj", &op.CopyObject{}),
		Entry("create_access_key", &op.CreateKey{}),
		Entry("create_account", &op.CreateAccount{}),
		Entry("create_bucket", &op.CreateBucket{}),
		Entry("create_subuser", &op.CreateSubuser{}),
		Entry("create_user", &op.CreateUser{}),
		Entry("delete_account", &op.RemoveAccount{}),
		Entry("delete_bucket", &op.DeleteBucket{}),
		Entry("delete_bucket_policy", &op.DeleteBucketPolicy{}),
		Entry("delete_bucket_tags", &op.DeleteBucketTagging{}),
		Entry("delete_obj", &op.DeleteObject{}),
		Entry("delete_obj_tags", &op.DeleteObjectTagging{}),
		Entry("get_account", &op.GetAccountInfo{}),
		Entry("get_acls, a bucket's", &op.GetBucketACL{}),
		Entry("get_acls, an object's", &op.GetObjectACL{}),
		Entry("get_bucket_info", &op.BucketInfo{}),
		Entry("get_bucket_location", &op.GetBucketLocation{}),
		Entry("get_bucket_policy", &op.GetBucketPolicy{}),
		Entry("get_bucket_tags", &op.GetBucketTagging{}),
		Entry("get_info", &op.GetInfo{}),
		Entry("get_lifecycle", &op.GetBucketLifecycle{}),
		Entry("get_metadata", &op.GetMetadata{}),
		Entry("get_obj", &op.GetObject{}),
		Entry("get_obj_attrs", &op.GetObjectAttributes{}),
		Entry("get_obj_tags", &op.GetObjectTagging{}),
		Entry("get_period", &op.GetPeriod{}),
		Entry("get_policy", &op.GetBucketPolicyAdmin{}),
		Entry("get_quota_info", &op.GetUserQuota{}),
		Entry("get_ratelimit_info", &op.GetRateLimit{}),
		Entry("get_realm", &op.GetRealm{}),
		Entry("get_usage", &op.ShowUsage{}),
		Entry("get_user_info", &op.GetUserInfo{}),
		Entry("get_zone_config", &op.GetZoneConfig{}),
		Entry("init_multipart", &op.InitMultipart{}),
		Entry("link_bucket", &op.LinkBucket{}),
		Entry("list_bucket", &op.ListObjects{}),
		Entry("list_bucket_multiparts", &op.ListMultipartUploads{}),
		Entry("list_buckets", &op.ListBuckets{}),
		Entry("list_metadata", &op.ListMetadata{}),
		Entry("list_multipart", &op.ListParts{}),
		Entry("list_realms", &op.ListRealms{}),
		Entry("list_user", &op.ListUsers{}),
		Entry("modify_account", &op.ModifyAccount{}),
		Entry("modify_subuser", &op.ModifySubuser{}),
		Entry("modify_user", &op.ModifyUser{}),
		Entry("multi_object_delete", &op.DeleteObjects{}),
		Entry("put_acls, a bucket's", &op.PutBucketACL{}),
		Entry("put_acls, an object's", &op.PutObjectACL{}),
		Entry("put_bucket_policy", &op.PutBucketPolicy{}),
		Entry("put_bucket_tags", &op.PutBucketTagging{}),
		Entry("put_obj", &op.PutObject{}),
		Entry("put_obj, an upload part", &op.UploadPart{}),
		Entry("put_obj_tags", &op.PutObjectTagging{}),
		Entry("put_ratelimit_info", &op.SetRateLimit{}),
		Entry("remove_access_key", &op.RemoveKey{}),
		Entry("remove_bucket", &op.RemoveBucketAdmin{}),
		Entry("remove_metadata", &op.RemoveMetadata{}),
		Entry("remove_object", &op.RemoveObjectAdmin{}),
		Entry("remove_subuser", &op.RemoveSubuser{}),
		Entry("remove_user", &op.RemoveUser{}),
		Entry("remove_user_caps", &op.RemoveCaps{}),
		Entry("set_bucket_quota", &op.SetBucketQuota{}),
		Entry("set_metadata", &op.PutMetadata{}),
		Entry("set_quota_info", &op.SetUserQuota{}),
		Entry("stat_bucket", &op.StatBucket{}),
		Entry("trim_usage", &op.TrimUsage{}),
		Entry("unlink_bucket", &op.UnlinkBucket{}),
	)
})

var _ = Describe("RefuseUnverified", func() {
	It("passes an identity whose signature was compared, for any name", func() {
		id := unverifiedAlice()
		id.Unverified = false
		for _, name := range []string{op.OptionsCORSName, "get_acls", "put_obj", ""} {
			Expect(op.RefuseUnverified(id, name)).To(Succeed(), name)
		}
		Expect(op.RefuseUnverified(op.Anonymous(), "get_acls")).To(Succeed())
	})
	It("passes an unverified identity for the preflight alone", func() {
		Expect(op.RefuseUnverified(unverifiedAlice(), op.OptionsCORSName)).To(Succeed())
		for _, name := range []string{"OPTIONS_CORS", "options_cors ", "", "get_acls"} {
			Expect(op.RefuseUnverified(unverifiedAlice(), name)).To(MatchError(op.ErrInternalError), "%q", name)
		}
	})
})
