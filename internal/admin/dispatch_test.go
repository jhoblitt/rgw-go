package admin_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// The rows are the op_get, op_put, op_post and op_delete of each admin
// handler at v19.2.6 and v20.2.4: driver/rados/rgw_rest_user.cc:1129-1177 and
// rgw_rest_bucket.cc:393-427, rgw_rest_metadata.cc:302-317,
// rgw_rest_usage.cc:123-131, rgw_rest_info.cc:46-49,
// rgw_rest_config.cc:49-57, driver/rados/rgw_rest_realm.cc:246-247 and
// :355-358, rgw_rest_ratelimit.cc:344-351 and rgw_rest_account.cc:223-241
// (v20.2.4 :301-321), at v19.2.6's line numbers unless given.
var _ = Describe("Dispatch", func() {
	dispatch := func(method, path, query string, rel denc.Release) (admin.Route, error) {
		GinkgoHelper()
		q, err := admin.ParseRequest(path, "admin", query)
		Expect(err).NotTo(HaveOccurred())
		return admin.Dispatch(method, q, rel)
	}

	DescribeTable("routes each method and sub-resource to radosgw's op",
		func(method, path, query, want string) {
			for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
				r, err := dispatch(method, path, query, rel)
				Expect(err).NotTo(HaveOccurred(), "%s %s?%s at %s", method, path, query, rel)
				Expect(r.Name).To(Equal(want), "%s %s?%s at %s", method, path, query, rel)
				if r.Name != "set_metadata" {
					Expect(r.Payloads).To(Equal(op.PayloadForms(0)), "only RGW_OP_ADMIN_SET_METADATA is on get_auth_data_v4's single-chunk list")
				}
			}
		},
		Entry(nil, "GET", "/admin/info", "", "get_info"),
		Entry(nil, "GET", "/admin/config", "type=zone", "get_zone_config"),
		Entry(nil, "GET", "/admin/user", "quota&uid=u", "get_quota_info"),
		Entry(nil, "GET", "/admin/user", "list", "list_user"),
		Entry(nil, "GET", "/admin/user", "uid=u", "get_user_info"),
		Entry(nil, "GET", "/admin/user", "key&quota", "get_user_info"),
		Entry(nil, "PUT", "/admin/user", "subuser", "create_subuser"),
		Entry(nil, "PUT", "/admin/user", "key", "create_access_key"),
		Entry(nil, "PUT", "/admin/user", "caps", "add_user_caps"),
		Entry(nil, "PUT", "/admin/user", "quota", "set_quota_info"),
		Entry(nil, "PUT", "/admin/user", "uid=u", "create_user"),
		Entry(nil, "PUT", "/admin/user", "quota&key", "set_quota_info"),
		Entry(nil, "POST", "/admin/user", "subuser", "modify_subuser"),
		Entry(nil, "POST", "/admin/user", "key", "modify_user"),
		Entry(nil, "DELETE", "/admin/user", "subuser", "remove_subuser"),
		Entry(nil, "DELETE", "/admin/user", "key", "remove_access_key"),
		Entry(nil, "DELETE", "/admin/user", "caps", "remove_user_caps"),
		Entry(nil, "DELETE", "/admin/user", "uid=u", "remove_user"),
		Entry(nil, "GET", "/admin/bucket", "policy", "get_policy"),
		Entry(nil, "GET", "/admin/bucket", "index", "check_bucket_index"),
		Entry(nil, "GET", "/admin/bucket", "bucket=b", "get_bucket_info"),
		Entry(nil, "PUT", "/admin/bucket", "quota", "set_bucket_quota"),
		Entry(nil, "PUT", "/admin/bucket", "bucket=b", "link_bucket"),
		Entry(nil, "POST", "/admin/bucket", "bucket=b", "unlink_bucket"),
		Entry(nil, "DELETE", "/admin/bucket", "object", "remove_object"),
		Entry(nil, "DELETE", "/admin/bucket", "bucket=b", "remove_bucket"),
		Entry(nil, "GET", "/admin/metadata/user", "myself&key=u", "get_metadata_myself"),
		Entry(nil, "GET", "/admin/metadata/user", "key=u", "get_metadata"),
		Entry(nil, "GET", "/admin/metadata/user", "", "list_metadata"),
		Entry(nil, "GET", "/admin/metadata", "", "list_metadata"),
		Entry(nil, "DELETE", "/admin/metadata/user", "key=u", "remove_metadata"),
		Entry(nil, "GET", "/admin/usage", "", "get_usage"),
		Entry(nil, "DELETE", "/admin/usage", "", "trim_usage"),
		Entry(nil, "POST", "/admin/account", "", "create_account"),
		Entry(nil, "PUT", "/admin/account", "id=a", "modify_account"),
		Entry(nil, "GET", "/admin/account", "id=a", "get_account"),
		Entry(nil, "DELETE", "/admin/account", "id=a", "delete_account"),
		Entry(nil, "GET", "/admin/ratelimit", "", "get_ratelimit_info"),
		Entry(nil, "POST", "/admin/ratelimit", "", "put_ratelimit_info"),
		Entry(nil, "GET", "/admin/realm", "list", "list_realms"),
		Entry(nil, "GET", "/admin/realm", "id=r", "get_realm"),
		Entry(nil, "GET", "/admin/realm/periodx", "", "get_realm"),
		Entry(nil, "GET", "/admin/realm/period", "", "get_period"),
	)

	It("routes PUT account?quota by release: Squid's op_put has no quota branch", func() {
		r, err := dispatch("PUT", "/admin/account", "quota&id=a", denc.Tentacle)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Name).To(Equal("set_account_quota_info"))
		r, err = dispatch("PUT", "/admin/account", "quota&id=a", denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Name).To(Equal("modify_account"))
	})

	It("hands set_metadata the signed single-chunk payload form", func() {
		r, err := dispatch("PUT", "/admin/metadata/user", "key=u", denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(Equal(admin.Route{Name: "set_metadata", Payloads: op.PayloadSigned}))
	})

	DescribeTable("answers 405 where radosgw's handler has no op, or rgw-go excludes the op",
		func(method, path, query string) {
			_, err := dispatch(method, path, query, denc.Tentacle)
			Expect(err).To(MatchError(op.ErrMethodNotAllowed))
		},
		Entry("POST info", "POST", "/admin/info", ""),
		Entry("HEAD info", "HEAD", "/admin/info", ""),
		Entry("an unknown method", "PATCH", "/admin/user", ""),
		Entry("config without a type", "GET", "/admin/config", ""),
		Entry("config of another type", "GET", "/admin/config", "type=zonegroup"),
		Entry("config's type compared exactly", "GET", "/admin/config", "type=Zone"),
		Entry("PUT config", "PUT", "/admin/config", "type=zone"),
		Entry("Sync_Bucket, excluded", "PUT", "/admin/bucket", "sync&bucket=b"),
		Entry("POST metadata", "POST", "/admin/metadata/user", ""),
		Entry("PUT usage", "PUT", "/admin/usage", ""),
		Entry("the log API, excluded", "GET", "/admin/log", "type=metadata"),
		Entry("the period POST, excluded", "POST", "/admin/realm/period", ""),
		Entry("PUT period", "PUT", "/admin/realm/period", ""),
		Entry("POST realm", "POST", "/admin/realm", ""),
		Entry("PUT ratelimit", "PUT", "/admin/ratelimit", ""),
		Entry("DELETE ratelimit", "DELETE", "/admin/ratelimit", ""),
		Entry("DELETE info", "DELETE", "/admin/info", ""),
	)
})
