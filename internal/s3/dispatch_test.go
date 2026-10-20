package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// sent is the HTTP request a table entry describes; hdr alternates header
// names and values, and a name may repeat. Entries are built before any spec
// runs, so the spec builds the request itself, under its own context.
type sent struct {
	method, target string
	hdr            []string
}

func request(method, target string, hdr ...string) sent {
	return sent{method: method, target: target, hdr: hdr}
}

// dispatch builds s's request, parses it path-style as the handler does
// before it dispatches, and dispatches it at rel with
// rgw_enable_static_website on, so that the website rows are routed. Every
// route carries the request's scope.
func dispatch(ctx context.Context, s sent, rel denc.Release) (s3.Route, error) {
	GinkgoHelper()
	return dispatchWith(ctx, s, rel, &op.Env{Conf: cephconf.NewOptions(cephconf.MapGetter{"rgw_enable_static_website": "true"})})
}

// dispatchWith is dispatch under env.
func dispatchWith(ctx context.Context, s sent, rel denc.Release, env *op.Env) (s3.Route, error) {
	GinkgoHelper()
	req := httptest.NewRequestWithContext(ctx, s.method, s.target, nil)
	for i := 0; i+1 < len(s.hdr); i += 2 {
		req.Header.Add(s.hdr[i], s.hdr[i+1])
	}
	p, err := s3.ParseRequest(req, s3.Config{}, time.Now())
	Expect(err).NotTo(HaveOccurred())
	p.Req.Env = env
	route, err := s3.Dispatch(p.Req, rel)
	if err == nil {
		Expect(route.Scope).To(Equal(p.Req.Scope()), "route scope")
	}
	return route, err
}

func releaseName(rel denc.Release) string {
	switch rel {
	case denc.Squid:
		return "squid"
	case denc.Tentacle:
		return "tentacle"
	default:
		return "unknown release"
	}
}

var (
	squid    = []denc.Release{denc.Squid}
	tentacle = []denc.Release{denc.Tentacle}
	both     = []denc.Release{denc.Squid, denc.Tentacle}
)

// The payload forms get_auth_data_v4 lets an op take: single-chunk only, or
// aws-chunked as well.
const (
	signed  = op.PayloadSigned
	chunked = op.PayloadSigned | op.PayloadChunked
)

// step is one branch of a radosgw op_* method: the query that selects it and
// the route and payload forms it yields; an empty route is a branch that
// returns nullptr, which radosgw answers with MethodNotAllowed.
type step struct {
	query string
	route string
	forms op.PayloadForms
}

var _ = Describe("Dispatch", func() {
	DescribeTable("selects radosgw's op at both releases",
		func(ctx SpecContext, req sent, want string) {
			for _, rel := range both {
				route, err := dispatch(ctx, req, rel)
				Expect(err).NotTo(HaveOccurred(), releaseName(rel))
				Expect(route.Name).To(Equal(want), releaseName(rel))
			}
		},
		Entry("GET / lists buckets", request("GET", "/"), "list_buckets"),
		Entry("HEAD / lists buckets", request("HEAD", "/"), "list_buckets"),
		Entry("GET /?usage", request("GET", "/?usage"), "get_usage"),
		Entry("GET bucket lists v1", request("GET", "/b"), "list_bucket"),
		Entry("GET bucket list-type=2", request("GET", "/b?list-type=2"), "list_bucket_v2"),
		Entry("GET bucket list-type=3 falls back to v1", request("GET", "/b?list-type=3"), "list_bucket"),
		Entry("list-type is read as strict_strtol reads it", request("GET", "/b?list-type=%2B02"), "list_bucket_v2"),
		Entry("list-type skips leading white space", request("GET", "/b?list-type=+2"), "list_bucket_v2"),
		Entry("list-type with trailing white space is v1", request("GET", "/b?list-type=2+"), "list_bucket"),
		Entry("list-type is read up to a NUL, as a C string", request("GET", "/b?list-type=2%00x"), "list_bucket_v2"),
		Entry("the last list-type wins", request("GET", "/b?list-type=2&list-type=1"), "list_bucket"),
		Entry("GET bucket ?location", request("GET", "/b?location"), "get_bucket_location"),
		Entry("GET bucket ?acl", request("GET", "/b?acl"), "get_acls"),
		Entry("GET bucket ?uploads", request("GET", "/b?uploads"), "list_bucket_multiparts"),
		Entry("GET bucket ?policy", request("GET", "/b?policy"), "get_bucket_policy"),
		Entry("GET bucket ?tagging", request("GET", "/b?tagging"), "get_bucket_tags"),
		Entry("GET bucket ?encryption reaches the encryption op", request("GET", "/b?encryption"), "get_bucket_encryption"),
		Entry("location beats acl on GET", request("GET", "/b?acl&location"), "get_bucket_location"),
		Entry("HEAD bucket stats", request("HEAD", "/b"), "stat_bucket"),
		Entry("HEAD bucket ?acl", request("HEAD", "/b?acl"), "get_acls"),
		Entry("PUT bucket creates", request("PUT", "/b"), "create_bucket"),
		Entry("PUT bucket ?acl", request("PUT", "/b?acl"), "put_acls"),
		Entry("PUT bucket ?tagging beats acl", request("PUT", "/b?acl&tagging"), "put_bucket_tags"),
		Entry("PUT bucket ?policy", request("PUT", "/b?policy"), "put_bucket_policy"),
		Entry("DELETE bucket", request("DELETE", "/b"), "delete_bucket"),
		Entry("DELETE bucket ?policy", request("DELETE", "/b?policy"), "delete_bucket_policy"),
		Entry("DELETE bucket ?tagging", request("DELETE", "/b?tagging"), "delete_bucket_tags"),
		Entry("POST bucket ?delete", request("POST", "/b?delete"), "multi_object_delete"),
		Entry("POST bucket is a form upload", request("POST", "/b"), "post_obj"),
		Entry("OPTIONS bucket", request("OPTIONS", "/b"), "options_cors"),
		Entry("GET object", request("GET", "/b/k"), "get_obj"),
		Entry("HEAD object", request("HEAD", "/b/k"), "get_obj"),
		Entry("GET object ?acl", request("GET", "/b/k?acl"), "get_acls"),
		Entry("GET object ?uploadId lists parts", request("GET", "/b/k?uploadId=u"), "list_multipart"),
		Entry("GET object ?tagging", request("GET", "/b/k?tagging"), "get_obj_tags"),
		Entry("PUT object", request("PUT", "/b/k"), "put_obj"),
		Entry("PUT object with copy source copies", request("PUT", "/b/k", "x-amz-copy-source", "/a/b"), "copy_obj"),
		Entry("PUT part copy is put_obj", request("PUT", "/b/k?uploadId=u&partNumber=1", "x-amz-copy-source", "/a/b"), "put_obj"),
		Entry("PUT with a copy range is put_obj", request("PUT", "/b/k", "x-amz-copy-source", "/a/b", "x-amz-copy-source-range", "bytes=0-1"), "put_obj"),
		Entry("a copy source with an empty bucket is a plain put", request("PUT", "/b/k", "x-amz-copy-source", "//src"), "put_obj"),
		Entry("an encoded slash empties the copy source's bucket too", request("PUT", "/b/k", "x-amz-copy-source", "/%2Fsrc"), "put_obj"),
		Entry("the last copy source header decides", request("PUT", "/b/k", "x-amz-copy-source", "/a/b", "x-amz-copy-source", "//src"), "put_obj"),
		Entry("a copy source parse_copy_location refuses stays with copy_obj", request("PUT", "/b/k", "x-amz-copy-source", "/src"), "copy_obj"),
		Entry("PUT object ?acl with a copy source sets the ACL", request("PUT", "/b/k?acl", "x-amz-copy-source", "/a/b"), "put_acls"),
		Entry("PUT part", request("PUT", "/b/k?uploadId=u&partNumber=1"), "put_obj"),
		Entry("PUT object ?acl", request("PUT", "/b/k?acl"), "put_acls"),
		Entry("PUT object ?tagging", request("PUT", "/b/k?tagging"), "put_obj_tags"),
		Entry("DELETE object", request("DELETE", "/b/k"), "delete_obj"),
		Entry("DELETE object ?uploadId aborts", request("DELETE", "/b/k?uploadId=u"), "abort_multipart"),
		Entry("DELETE object with an empty uploadId deletes the object", request("DELETE", "/b/k?uploadId"), "delete_obj"),
		Entry("DELETE object ?tagging", request("DELETE", "/b/k?tagging"), "delete_obj_tags"),
		Entry("POST object ?uploads initiates", request("POST", "/b/k?uploads"), "init_multipart"),
		Entry("POST object ?uploadId completes", request("POST", "/b/k?uploadId=u"), "complete_multipart"),
		Entry("POST object with both prefers uploadId", request("POST", "/b/k?uploads&uploadId=u"), "complete_multipart"),
		Entry("OPTIONS object", request("OPTIONS", "/b/k"), "options_cors"),
		// radosgw checks names only after its MethodNotAllowed answers and
		// authentication, so these reach an op, and auth, first.
		Entry("a 1025-byte key reaches GetObject", request("GET", "/b/"+strings.Repeat("k", 1025)), "get_obj"),
		Entry("a token with nothing after its colon keeps object scope", request("PUT", "/t1:/k"), "put_obj"),
		Entry("a token with nothing after its colon keeps bucket scope", request("GET", "/t1:"), "list_bucket"),
	)
	DescribeTable("follows the release's op_* tables where they differ",
		func(ctx SpecContext, req sent, rel denc.Release, want string) {
			route, err := dispatch(ctx, req, rel)
			Expect(err).NotTo(HaveOccurred())
			Expect(route.Name).To(Equal(want))
		},
		Entry("PUT bucket ?logging on Tentacle", request("PUT", "/b?logging"), denc.Tentacle, "put_bucket_logging"),
		Entry("DELETE bucket ?logging on Tentacle falls through to delete_bucket", request("DELETE", "/b?logging"), denc.Tentacle, "delete_bucket"),
		Entry("POST bucket ?logging on Tentacle", request("POST", "/b?logging"), denc.Tentacle, "post_bucket_logging"),
		Entry("POST bucket ?logging on Squid is a form upload", request("POST", "/b?logging"), denc.Squid, "post_obj"),
		Entry("POST object ?restore on Tentacle", request("POST", "/b/k?restore"), denc.Tentacle, "restore_obj"),
		Entry("POST object ?restore on Squid is a form upload", request("POST", "/b/k?restore"), denc.Squid, "post_obj"),
		Entry("GET object ?attributes on Tentacle", request("GET", "/b/k?attributes"), denc.Tentacle, "get_obj_attrs"),
		// A Squid radosgw answers ?attributes as GetObject (RGWGetObjAttrs exists
		// only at v20.2.4); rgw-go serves GetObjectAttributes on both releases.
		Entry("GET object ?attributes on Squid is GetObjectAttributes too", request("GET", "/b/k?attributes"), denc.Squid, "get_obj_attrs"),
	)
	DescribeTable("routes ?ownershipControls as v21.1.2 does, on both releases",
		func(ctx SpecContext, req sent, want string, forms op.PayloadForms) {
			for _, rel := range both {
				route, err := dispatch(ctx, req, rel)
				Expect(err).NotTo(HaveOccurred(), releaseName(rel))
				Expect(route.Name).To(Equal(want), releaseName(rel))
				Expect(route.Payloads).To(Equal(forms), releaseName(rel))
			}
		},
		Entry("GET reaches GetBucketOwnershipControls, taking neither payload form", request("GET", "/b?ownershipControls"), "get_bucket_ownership_controls", op.PayloadForms(0)),
		Entry("GET with a value, which the presence test ignores", request("GET", "/b?ownershipControls=x"), "get_bucket_ownership_controls", op.PayloadForms(0)),
		Entry("GET with list-type=2, which only the fall-through reads", request("GET", "/b?ownershipControls&list-type=2"), "get_bucket_ownership_controls", op.PayloadForms(0)),
		Entry("GET behind ?encryption", request("GET", "/b?ownershipControls&encryption"), "get_bucket_encryption", signed),
		Entry("GET behind ?acl", request("GET", "/b?ownershipControls&acl"), "get_acls", op.PayloadForms(0)),
		Entry("GET with the key in another case lists", request("GET", "/b?ownershipcontrols"), "list_bucket", op.PayloadForms(0)),
		Entry("PUT reaches PutBucketOwnershipControls, taking a single chunk", request("PUT", "/b?ownershipControls"), "put_bucket_ownership_controls", signed),
		Entry("PUT behind ?encryption", request("PUT", "/b?ownershipControls&encryption"), "put_bucket_encryption", signed),
		Entry("PUT behind ?acl", request("PUT", "/b?ownershipControls&acl"), "put_acls", signed),
		Entry("DELETE reaches DeleteBucketOwnershipControls, taking neither payload form", request("DELETE", "/b?ownershipControls"), "delete_bucket_ownership_controls", op.PayloadForms(0)),
		Entry("DELETE behind ?encryption", request("DELETE", "/b?ownershipControls&encryption"), "delete_bucket_encryption", signed),
		Entry("DELETE behind ?tagging", request("DELETE", "/b?ownershipControls&tagging"), "delete_bucket_tags", op.PayloadForms(0)),
		Entry("DELETE ahead of ?website", request("DELETE", "/b?website&ownershipControls"), "delete_bucket_ownership_controls", op.PayloadForms(0)),
		Entry("DELETE ahead of ?mdsearch", request("DELETE", "/b?mdsearch&ownershipControls"), "delete_bucket_ownership_controls", op.PayloadForms(0)),
		Entry("HEAD, which has no such branch, stats", request("HEAD", "/b?ownershipControls"), "stat_bucket", op.PayloadForms(0)),
		Entry("POST, which has no such branch, is a form upload", request("POST", "/b?ownershipControls"), "post_obj", op.PayloadForms(0)),
		Entry("an object GET ignores it", request("GET", "/b/k?ownershipControls"), "get_obj", signed),
		Entry("an object PUT ignores it", request("PUT", "/b/k?ownershipControls"), "put_obj", chunked),
		Entry("an object DELETE ignores it", request("DELETE", "/b/k?ownershipControls"), "delete_obj", op.PayloadForms(0)),
	)
	It("routes DELETE ?ownershipControls ahead of ?website whatever rgw_enable_static_website says, and GET behind it", func(ctx SpecContext) {
		for _, rel := range both {
			route, err := dispatchWith(ctx, request("DELETE", "/b?website&ownershipControls"), rel, &op.Env{})
			Expect(err).NotTo(HaveOccurred(), releaseName(rel))
			Expect(route.Name).To(Equal("delete_bucket_ownership_controls"), releaseName(rel))
			_, err = dispatchWith(ctx, request("GET", "/b?website&ownershipControls"), rel, &op.Env{})
			Expect(err).To(MatchError(op.ErrMethodNotAllowed), "%s: op_get's website branch returns nullptr first", releaseName(rel))
		}
	})
	It("refuses ?ownershipControls beside an object subresource on a bucket, as get_handler does", func(ctx SpecContext) {
		for _, rel := range both {
			_, err := dispatch(ctx, request("DELETE", "/b?ownershipControls&uploadId=u"), rel)
			Expect(err).To(MatchError(op.ErrMethodNotAllowed), releaseName(rel))
		}
	})
	// The walk below pins each refusal's position; these pin the requests as
	// Amazon S3's SDK sends them, and that a refusal comes before the
	// fall-through's list-type, copy source and empty uploadId readings.
	DescribeTable("routes the configurations radosgw has no op for to a route of their own, on both releases",
		func(ctx SpecContext, req sent, want string) {
			for _, rel := range both {
				route, err := dispatch(ctx, req, rel)
				Expect(err).NotTo(HaveOccurred(), releaseName(rel))
				Expect(route.Name).To(Equal(want), releaseName(rel))
				Expect(route.Payloads).To(Equal(op.PayloadForms(0)), releaseName(rel))
			}
		},
		Entry("GetBucketAnalyticsConfiguration", request("GET", "/b?analytics&x-id=GetBucketAnalyticsConfiguration&id=a"), "get_bucket_analytics"),
		Entry("ListBucketMetricsConfigurations with list-type=2, which only the fall-through reads", request("GET", "/b?metrics&x-id=ListBucketMetricsConfigurations&list-type=2"), "get_bucket_metrics"),
		Entry("DeleteBucketInventoryConfiguration", request("DELETE", "/b?inventory&id=a"), "delete_bucket_inventory"),
		Entry("PutObjectAnnotation naming a version", request("PUT", "/b/k?annotation&annotationName=n&versionId=v"), "put_obj_annotation"),
		Entry("an annotation PUT with a copy source, ahead of copy_obj", request("PUT", "/b/k?annotation", "x-amz-copy-source", "/a/b"), "put_obj_annotation"),
		Entry("DeleteObjectAnnotation naming a version", request("DELETE", "/b/k?annotation&annotationName=n&versionId=v"), "delete_obj_annotation"),
		Entry("an annotation DELETE with an empty uploadId, which aborts nothing", request("DELETE", "/b/k?annotation&uploadId"), "delete_obj_annotation"),
		Entry("GetObjectAnnotation", request("GET", "/b/k?annotation&x-id=GetObjectAnnotation&annotationName=n"), "get_obj_annotation"),
		Entry("RenameObject", request("PUT", "/b/new?renameObject", "x-amz-rename-source", "/b/k"), "rename_obj"),
		Entry("UpdateObjectEncryption naming a version", request("PUT", "/b/k?encryption&versionId=v"), "update_obj_encryption"),
		Entry("an encryption PUT with a copy source, ahead of copy_obj", request("PUT", "/b/k?encryption", "x-amz-copy-source", "/a/b"), "update_obj_encryption"),
	)
	// Each entry lists one op_* method's branches in radosgw's order, ending
	// with the op it returns when none matches. The first request carries
	// every branch's key and must select the first branch; each later one
	// drops the key before it, so a branch out of order, a key that does not
	// select its branch, or a mistyped route or payload form fails here. The
	// bucket GET, PUT and DELETE carry v21.1.2's ownershipControls branch on
	// both releases, where v21.1.2 puts it, and they and the object GET, PUT
	// and DELETE end with rgw-go's refusals, just before the fall-through.
	DescribeTable("walks each op_* method's branches in radosgw's order",
		func(ctx SpecContext, method, path string, rels []denc.Release, steps []step) {
			for _, rel := range rels {
				for i, want := range steps {
					var keys []string
					for _, s := range steps[i:] {
						if s.query != "" {
							keys = append(keys, s.query)
						}
					}
					target := path
					if len(keys) > 0 {
						target += "?" + strings.Join(keys, "&")
					}
					desc := releaseName(rel) + " " + method + " " + target
					route, err := dispatch(ctx, request(method, target), rel)
					if want.route == "" {
						Expect(err).To(MatchError(op.ErrMethodNotAllowed), desc)
						continue
					}
					Expect(err).NotTo(HaveOccurred(), desc)
					Expect(route.Name).To(Equal(want.route), desc)
					Expect(route.Payloads).To(Equal(want.forms), desc)
				}
			}
		},
		Entry("service GET", "GET", "/", both, []step{
			{"usage", "get_usage", 0},
			{"", "list_buckets", 0},
		}),
		Entry("service HEAD", "HEAD", "/", both, []step{
			{"", "list_buckets", 0},
		}),
		Entry("bucket GET on Squid", "GET", "/b", squid, []step{
			{"logging", "get_bucket_logging", 0},
			{"location", "get_bucket_location", 0},
			{"versioning", "get_bucket_versioning", 0},
			{"website", "get_bucket_website", 0},
			{"mdsearch", "get_bucket_meta_search", 0},
			{"acl", "get_acls", 0},
			{"cors", "get_cors", 0},
			{"requestPayment", "get_request_payment", 0},
			{"uploads", "list_bucket_multiparts", 0},
			{"lifecycle", "get_lifecycle", 0},
			{"policy", "get_bucket_policy", 0},
			{"tagging", "get_bucket_tags", 0},
			{"object-lock", "get_bucket_object_lock", 0},
			{"notification", "get_bucket_notification", signed},
			{"replication", "get_bucket_replication", 0},
			{"policyStatus", "get_bucket_policy_status", 0},
			{"publicAccessBlock", "get_bucket_public_access_block", signed},
			{"encryption", "get_bucket_encryption", signed},
			{"ownershipControls", "get_bucket_ownership_controls", 0},
			{"analytics", "get_bucket_analytics", 0},
			{"intelligent-tiering", "get_bucket_intelligent_tiering", 0},
			{"inventory", "get_bucket_inventory", 0},
			{"metrics", "get_bucket_metrics", 0},
			{"metadataConfiguration", "get_bucket_metadata_configuration", 0},
			{"metadataTable", "get_bucket_metadata_table", 0},
			{"", "list_bucket", 0},
		}),
		Entry("bucket GET on Tentacle", "GET", "/b", tentacle, []step{
			{"logging", "get_bucket_logging", signed},
			{"location", "get_bucket_location", 0},
			{"versioning", "get_bucket_versioning", 0},
			{"website", "get_bucket_website", 0},
			{"mdsearch", "get_bucket_meta_search", 0},
			{"acl", "get_acls", 0},
			{"cors", "get_cors", 0},
			{"requestPayment", "get_request_payment", 0},
			{"uploads", "list_bucket_multiparts", 0},
			{"lifecycle", "get_lifecycle", 0},
			{"policy", "get_bucket_policy", 0},
			{"tagging", "get_bucket_tags", 0},
			{"object-lock", "get_bucket_object_lock", 0},
			{"notification", "get_bucket_notification", signed},
			{"replication", "get_bucket_replication", 0},
			{"policyStatus", "get_bucket_policy_status", 0},
			{"publicAccessBlock", "get_bucket_public_access_block", signed},
			{"encryption", "get_bucket_encryption", signed},
			{"ownershipControls", "get_bucket_ownership_controls", 0},
			{"analytics", "get_bucket_analytics", 0},
			{"intelligent-tiering", "get_bucket_intelligent_tiering", 0},
			{"inventory", "get_bucket_inventory", 0},
			{"metrics", "get_bucket_metrics", 0},
			{"metadataConfiguration", "get_bucket_metadata_configuration", 0},
			{"metadataTable", "get_bucket_metadata_table", 0},
			{"", "list_bucket", 0},
		}),
		Entry("bucket HEAD", "HEAD", "/b", both, []step{
			{"acl", "get_acls", 0},
			{"uploads", "list_bucket_multiparts", 0},
			{"", "stat_bucket", 0},
		}),
		Entry("bucket PUT on Squid", "PUT", "/b", squid, []step{
			{"logging", "", 0},
			{"versioning", "set_bucket_versioning", signed},
			{"website", "set_bucket_website", signed},
			{"tagging", "put_bucket_tags", signed},
			{"acl", "put_acls", signed},
			{"cors", "put_cors", signed},
			{"requestPayment", "set_request_payment", signed},
			{"lifecycle", "put_lifecycle", signed},
			{"policy", "put_bucket_policy", signed},
			{"object-lock", "put_bucket_object_lock", signed},
			{"notification", "put_bucket_notification", signed},
			{"replication", "put_bucket_replication", signed},
			{"publicAccessBlock", "put_bucket_public_access_block", signed},
			{"encryption", "put_bucket_encryption", signed},
			{"ownershipControls", "put_bucket_ownership_controls", signed},
			{"analytics", "put_bucket_analytics", 0},
			{"intelligent-tiering", "put_bucket_intelligent_tiering", 0},
			{"inventory", "put_bucket_inventory", 0},
			{"metrics", "put_bucket_metrics", 0},
			{"metadataConfiguration", "put_bucket_metadata_configuration", 0},
			{"metadataTable", "put_bucket_metadata_table", 0},
			{"", "create_bucket", signed},
		}),
		Entry("bucket PUT on Tentacle", "PUT", "/b", tentacle, []step{
			{"logging", "put_bucket_logging", signed},
			{"versioning", "set_bucket_versioning", signed},
			{"website", "set_bucket_website", signed},
			{"tagging", "put_bucket_tags", signed},
			{"acl", "put_acls", signed},
			{"cors", "put_cors", signed},
			{"requestPayment", "set_request_payment", signed},
			{"lifecycle", "put_lifecycle", signed},
			{"policy", "put_bucket_policy", signed},
			{"object-lock", "put_bucket_object_lock", signed},
			{"notification", "put_bucket_notification", signed},
			{"replication", "put_bucket_replication", signed},
			{"publicAccessBlock", "put_bucket_public_access_block", signed},
			{"encryption", "put_bucket_encryption", signed},
			{"ownershipControls", "put_bucket_ownership_controls", signed},
			{"analytics", "put_bucket_analytics", 0},
			{"intelligent-tiering", "put_bucket_intelligent_tiering", 0},
			{"inventory", "put_bucket_inventory", 0},
			{"metrics", "put_bucket_metrics", 0},
			{"metadataConfiguration", "put_bucket_metadata_configuration", 0},
			{"metadataTable", "put_bucket_metadata_table", 0},
			{"", "create_bucket", signed},
		}),
		Entry("bucket DELETE on Squid", "DELETE", "/b", squid, []step{
			{"logging", "", 0},
			{"tagging", "delete_bucket_tags", 0},
			{"cors", "delete_cors", 0},
			{"lifecycle", "delete_lifecycle", 0},
			{"policy", "delete_bucket_policy", 0},
			{"notification", "delete_bucket_notification", signed},
			{"replication", "delete_bucket_replication", 0},
			{"publicAccessBlock", "delete_bucket_public_access_block", signed},
			{"encryption", "delete_bucket_encryption", signed},
			{"ownershipControls", "delete_bucket_ownership_controls", 0},
			{"website", "delete_bucket_website", signed},
			{"mdsearch", "delete_bucket_meta_search", 0},
			{"analytics", "delete_bucket_analytics", 0},
			{"intelligent-tiering", "delete_bucket_intelligent_tiering", 0},
			{"inventory", "delete_bucket_inventory", 0},
			{"metrics", "delete_bucket_metrics", 0},
			{"metadataConfiguration", "delete_bucket_metadata_configuration", 0},
			{"metadataTable", "delete_bucket_metadata_table", 0},
			{"", "delete_bucket", 0},
		}),
		Entry("bucket DELETE on Tentacle", "DELETE", "/b", tentacle, []step{
			{"tagging", "delete_bucket_tags", 0},
			{"cors", "delete_cors", 0},
			{"lifecycle", "delete_lifecycle", 0},
			{"policy", "delete_bucket_policy", 0},
			{"notification", "delete_bucket_notification", signed},
			{"replication", "delete_bucket_replication", 0},
			{"publicAccessBlock", "delete_bucket_public_access_block", signed},
			{"encryption", "delete_bucket_encryption", signed},
			{"ownershipControls", "delete_bucket_ownership_controls", 0},
			{"website", "delete_bucket_website", signed},
			{"mdsearch", "delete_bucket_meta_search", 0},
			{"analytics", "delete_bucket_analytics", 0},
			{"intelligent-tiering", "delete_bucket_intelligent_tiering", 0},
			{"inventory", "delete_bucket_inventory", 0},
			{"metrics", "delete_bucket_metrics", 0},
			{"metadataConfiguration", "delete_bucket_metadata_configuration", 0},
			{"metadataTable", "delete_bucket_metadata_table", 0},
			{"", "delete_bucket", 0},
		}),
		Entry("bucket POST on Squid", "POST", "/b", squid, []step{
			{"delete", "multi_object_delete", signed},
			{"mdsearch", "config_bucket_meta_search", 0},
			{"", "post_obj", 0},
		}),
		Entry("bucket POST on Tentacle", "POST", "/b", tentacle, []step{
			{"delete", "multi_object_delete", signed},
			{"logging", "post_bucket_logging", signed},
			{"mdsearch", "config_bucket_meta_search", 0},
			{"", "post_obj", 0},
		}),
		Entry("bucket OPTIONS", "OPTIONS", "/b", both, []step{
			{"", "options_cors", 0},
		}),
		Entry("object GET on Squid", "GET", "/b/k", squid, []step{
			{"acl", "get_acls", 0},
			{"uploadId=u", "list_multipart", 0},
			{"layout", "get_obj_layout", 0},
			{"tagging", "get_obj_tags", 0},
			{"attributes", "get_obj_attrs", signed},
			{"retention", "get_obj_retention", 0},
			{"legal-hold", "get_obj_legal_hold", 0},
			{"annotation", "get_obj_annotation", 0},
			{"", "get_obj", signed},
		}),
		Entry("object GET on Tentacle", "GET", "/b/k", tentacle, []step{
			{"acl", "get_acls", 0},
			{"uploadId=u", "list_multipart", 0},
			{"layout", "get_obj_layout", 0},
			{"tagging", "get_obj_tags", 0},
			{"attributes", "get_obj_attrs", 0},
			{"retention", "get_obj_retention", 0},
			{"legal-hold", "get_obj_legal_hold", 0},
			{"annotation", "get_obj_annotation", 0},
			{"", "get_obj", signed},
		}),
		Entry("object HEAD", "HEAD", "/b/k", both, []step{
			{"acl", "get_acls", 0},
			{"uploadId=u", "list_multipart", 0},
			{"", "get_obj", signed},
		}),
		Entry("object PUT", "PUT", "/b/k", both, []step{
			{"acl", "put_acls", signed},
			{"tagging", "put_obj_tags", signed},
			{"retention", "put_obj_retention", signed},
			{"legal-hold", "put_obj_legal_hold", signed},
			{"annotation", "put_obj_annotation", 0},
			{"renameObject", "rename_obj", 0},
			{"encryption", "update_obj_encryption", 0},
			{"", "put_obj", chunked},
		}),
		Entry("object DELETE", "DELETE", "/b/k", both, []step{
			{"tagging", "delete_obj_tags", 0},
			{"uploadId=u", "abort_multipart", 0},
			{"annotation", "delete_obj_annotation", 0},
			{"", "delete_obj", 0},
		}),
		Entry("object POST on Squid", "POST", "/b/k", squid, []step{
			{"uploadId=u", "complete_multipart", signed},
			{"uploads", "init_multipart", signed},
			{"select-type=2", "select_obj", signed},
			{"", "post_obj", 0},
		}),
		Entry("object POST on Tentacle", "POST", "/b/k", tentacle, []step{
			{"uploadId=u", "complete_multipart", signed},
			{"uploads", "init_multipart", signed},
			{"restore", "restore_obj", signed},
			{"select-type=2", "select_obj", signed},
			{"", "post_obj", 0},
		}),
		Entry("object OPTIONS", "OPTIONS", "/b/k", both, []step{
			{"", "options_cors", 0},
		}),
	)
	// radosgw refuses a signed payload form by op type inside authentication:
	// the single-chunk whitelist and the streamed one of get_auth_data_v4
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5899-L5969 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6466-L6540 at v20.2.4).
	DescribeTable("carries the payload forms radosgw's SigV4 completer takes for the op",
		func(ctx SpecContext, req sent, rel denc.Release, want op.PayloadForms) {
			route, err := dispatch(ctx, req, rel)
			Expect(err).NotTo(HaveOccurred())
			Expect(route.Payloads).To(Equal(want), route.Name)
		},
		Entry("PutObject takes both", request("PUT", "/b/k"), denc.Squid, chunked),
		Entry("UploadPart takes both", request("PUT", "/b/k?uploadId=u&partNumber=1"), denc.Tentacle, chunked),
		Entry("UploadPartCopy is put_obj and takes both", request("PUT", "/b/k?uploadId=u&partNumber=1", "x-amz-copy-source", "/a/b"), denc.Squid, chunked),
		Entry("CopyObject takes neither", request("PUT", "/b/k", "x-amz-copy-source", "/a/b"), denc.Squid, op.PayloadForms(0)),
		Entry("CreateBucket takes a single chunk", request("PUT", "/b"), denc.Squid, signed),
		Entry("PutObjectAcl takes a single chunk", request("PUT", "/b/k?acl"), denc.Tentacle, signed),
		Entry("DeleteObjects takes a single chunk", request("POST", "/b?delete"), denc.Squid, signed),
		Entry("CreateMultipartUpload takes a single chunk", request("POST", "/b/k?uploads"), denc.Squid, signed),
		Entry("CompleteMultipartUpload takes a single chunk", request("POST", "/b/k?uploadId=u"), denc.Tentacle, signed),
		Entry("GetObject takes a single chunk", request("GET", "/b/k"), denc.Squid, signed),
		Entry("HeadObject is get_obj too", request("HEAD", "/b/k"), denc.Tentacle, signed),
		Entry("DELETE ?website is typed SET_BUCKET_WEBSITE", request("DELETE", "/b?website"), denc.Squid, signed),
		Entry("GET ?encryption takes a single chunk", request("GET", "/b?encryption"), denc.Squid, signed),
		Entry("GET ?notification takes a single chunk", request("GET", "/b?notification"), denc.Tentacle, signed),
		Entry("S3 Select keeps get_obj's op type", request("POST", "/b/k?select-type=2"), denc.Squid, signed),
		Entry("ListObjects takes neither", request("GET", "/b"), denc.Squid, op.PayloadForms(0)),
		Entry("ListObjectsV2 takes neither", request("GET", "/b?list-type=2"), denc.Tentacle, op.PayloadForms(0)),
		Entry("DeleteObject takes neither", request("DELETE", "/b/k"), denc.Tentacle, op.PayloadForms(0)),
		Entry("AbortMultipartUpload takes neither", request("DELETE", "/b/k?uploadId=u"), denc.Squid, op.PayloadForms(0)),
		Entry("GetObjectAttributes takes neither on Tentacle", request("GET", "/b/k?attributes"), denc.Tentacle, op.PayloadForms(0)),
		Entry("?attributes keeps get_obj's single chunk on Squid", request("GET", "/b/k?attributes"), denc.Squid, signed),
		Entry("a form upload takes neither", request("POST", "/b"), denc.Squid, op.PayloadForms(0)),
		Entry("GET ?logging takes neither on Squid", request("GET", "/b?logging"), denc.Squid, op.PayloadForms(0)),
		Entry("GET ?logging takes a single chunk on Tentacle", request("GET", "/b?logging"), denc.Tentacle, signed),
		Entry("PUT ?logging takes a single chunk on Tentacle", request("PUT", "/b?logging"), denc.Tentacle, signed),
		Entry("POST ?logging is a form upload on Squid", request("POST", "/b?logging"), denc.Squid, op.PayloadForms(0)),
		Entry("POST ?logging takes a single chunk on Tentacle", request("POST", "/b?logging"), denc.Tentacle, signed),
		Entry("POST ?restore is a form upload on Squid", request("POST", "/b/k?restore"), denc.Squid, op.PayloadForms(0)),
		Entry("POST ?restore takes a single chunk on Tentacle", request("POST", "/b/k?restore"), denc.Tentacle, signed),
	)
	DescribeTable("answers MethodNotAllowed where radosgw has no handler",
		func(ctx SpecContext, req sent) {
			_, err := dispatch(ctx, req, denc.Squid)
			Expect(err).To(MatchError(op.ErrMethodNotAllowed))
		},
		Entry("POST at service scope", request("POST", "/")),
		Entry("PUT at service scope", request("PUT", "/")),
		Entry("DELETE at service scope", request("DELETE", "/")),
		Entry("OPTIONS at service scope", request("OPTIONS", "/")),
		Entry("an object-only subresource on a bucket", request("GET", "/b?uploadId=u")),
		Entry("versionId on a bucket", request("GET", "/b?versionId=v")),
		Entry("partNumber on a bucket, whatever the method", request("OPTIONS", "/b?partNumber=1")),
		Entry("append on a bucket", request("PUT", "/b?append")),
		Entry("torrent on a bucket", request("HEAD", "/b?torrent")),
		Entry("PUT bucket ?logging on Squid", request("PUT", "/b?logging")),
		Entry("DELETE bucket ?logging on Squid", request("DELETE", "/b?logging")),
		Entry("an object subresource on a bucket, before its bad tenant", request("GET", "/t-1:b?uploadId=u")),
		Entry("PUT bucket ?logging on Squid, before the bad tenant", request("PUT", "/t-1:b?logging")),
	)
	// op_get, op_put and op_delete return nullptr for ?website while
	// rgw_enable_static_website is off
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4674-L4679 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5186-L5191 at v20.2.4),
	// and each website branch returns there, so nothing falls through to a
	// later branch.
	DescribeTable("answers MethodNotAllowed for the website configuration routes while rgw_enable_static_website is off",
		func(ctx SpecContext, req sent, rel denc.Release, env *op.Env) {
			_, err := dispatchWith(ctx, req, rel, env)
			Expect(err).To(MatchError(op.ErrMethodNotAllowed))
		},
		Entry("GET with the option false, before ?acl", request("GET", "/b?website&acl"), denc.Squid,
			&op.Env{Conf: cephconf.NewOptions(cephconf.MapGetter{"rgw_enable_static_website": "false"})}),
		Entry("PUT with the option unset, its default", request("PUT", "/b?website"), denc.Tentacle, &op.Env{}),
		Entry("DELETE with no environment", request("DELETE", "/b?website"), denc.Squid, nil),
	)
	It("reads a key without values in a hand-built query as an empty one", func() {
		del := &op.Request{Method: "DELETE", Bucket: "b", Object: meta.ObjKey{Name: "k"}, Query: url.Values{"uploadId": {}}}
		route, err := s3.Dispatch(del, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(route.Name).To(Equal("delete_obj"), "DELETE ?uploadId")
		list := &op.Request{Method: "GET", Bucket: "b", Query: url.Values{"list-type": {}}}
		route, err = s3.Dispatch(list, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(route.Name).To(Equal("list_bucket"), "GET ?list-type")
	})
	It("answers MethodNotAllowed for a method no op_* serves", func() {
		r := &op.Request{Method: "PATCH", Bucket: "b"}
		_, err := s3.Dispatch(r, denc.Tentacle)
		Expect(err).To(MatchError(op.ErrMethodNotAllowed))
	})
})

// A floor radosgw lists, creates or deletes the bucket for these requests;
// rgw-go's ownership controls routes answer as every route without a
// handler does, after authentication.
var _ = Describe("An ownershipControls request through the handler", func() {
	const document = `<OwnershipControls xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<Rule><ObjectOwnership>BucketOwnerEnforced</ObjectOwnership></Rule></OwnershipControls>`
	// newStore holds alice's empty bucket "plain" at rel.
	newStore := func(ctx context.Context, rel denc.Release) (*memstore.Store, *op.UserRecord) {
		store := memstore.New(memstore.Config{Release: rel})
		alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(alice.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
		return store, alice
	}

	DescribeTable("answers 501 NotImplemented once authenticated, and lists, creates and deletes nothing",
		func(ctx SpecContext, rel denc.Release, method, target, body string, forms op.PayloadForms) {
			store, alice := newStore(ctx, rel)
			var authed []op.PayloadForms
			as := s3.AuthenticatorFunc(func(_ context.Context, _ *http.Request, got op.PayloadForms) (*op.AuthResult, error) {
				authed = append(authed, got)
				return &op.AuthResult{Identity: op.Identity{User: &alice.Info, Owner: meta.UserOwner(alice.Info.UserID), OpMask: alice.Info.OpMask}}, nil
			})
			rec := serveReq(newHandler(store, as, s3.Config{}), method, target, strings.NewReader(body))
			Expect(rec.Code).To(Equal(http.StatusNotImplemented), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NotImplemented</Code>"))
			Expect(rec.Body.String()).NotTo(ContainSubstring("ListBucketResult"))
			Expect(authed).To(Equal([]op.PayloadForms{forms}), "authenticated once, with the route's payload forms")
			_, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred(), "the empty bucket is still there")
			_, err = store.GetBucket(ctx, "", "fresh")
			Expect(err).To(MatchError(op.ErrNoSuchBucket), "no bucket was created")
		},
		Entry("GET on Squid", denc.Squid, "GET", "/plain?ownershipControls", "", op.PayloadForms(0)),
		Entry("GET with list-type=2 on Tentacle", denc.Tentacle, "GET", "/plain?ownershipControls&list-type=2", "", op.PayloadForms(0)),
		Entry("PUT of an OwnershipControls document on Squid", denc.Squid, "PUT", "/plain?ownershipControls", document, signed),
		Entry("PUT with no body naming a free bucket name on Tentacle", denc.Tentacle, "PUT", "/fresh?ownershipControls", "", signed),
		Entry("DELETE of an empty bucket on Squid", denc.Squid, "DELETE", "/plain?ownershipControls", "", op.PayloadForms(0)),
		Entry("DELETE of an empty bucket on Tentacle", denc.Tentacle, "DELETE", "/plain?ownershipControls", "", op.PayloadForms(0)),
	)
	It("answers a refused authentication with its own error rather than 501", func(ctx SpecContext) {
		store, _ := newStore(ctx, denc.Tentacle)
		refusing := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return nil, op.ErrAccessDenied
		})
		rec := serveReq(newHandler(store, refusing, s3.Config{}), http.MethodDelete, "/plain?ownershipControls", nil)
		Expect(rec.Code).To(Equal(http.StatusForbidden), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"))
	})
})

// radosgw lists, creates or deletes the bucket, or reads, overwrites or
// deletes the object, for each of these requests.
var _ = Describe("A request for a configuration radosgw has no op for, through the handler", func() {
	const data = "the object's data"
	// newStore holds alice's empty bucket "plain" and her bucket "full", whose
	// object "k" holds data.
	newStore := func(ctx context.Context, rel denc.Release) (*memstore.Store, s3.Authenticator) {
		store := memstore.New(memstore.Config{Release: rel})
		alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(alice.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
		full, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "full", Owner: meta.UserOwner(alice.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
		_, err = store.PutObject(ctx, full, meta.ObjKey{Name: "k"}, strings.NewReader(data), op.PutParams{Size: int64(len(data))})
		Expect(err).NotTo(HaveOccurred())
		return store, authAs(alice)
	}

	DescribeTable("answers 501 NotImplemented and writes nothing, on both releases",
		func(ctx SpecContext, method, target, body string, hdr ...string) {
			for _, rel := range both {
				store, as := newStore(ctx, rel)
				rec := serveReq(newHandler(store, as, s3.Config{}), method, target, strings.NewReader(body), hdr...)
				Expect(rec.Code).To(Equal(http.StatusNotImplemented), "%s: %s", releaseName(rel), rec.Body.String())
				Expect(rec.Body.String()).To(ContainSubstring("<Code>NotImplemented</Code>"), releaseName(rel))
				Expect(rec.Body.String()).NotTo(ContainSubstring(data), releaseName(rel))
				_, err := store.GetBucket(ctx, "", "plain")
				Expect(err).NotTo(HaveOccurred(), "%s: the empty bucket is still there", releaseName(rel))
				_, err = store.GetBucket(ctx, "", "fresh")
				Expect(err).To(MatchError(op.ErrNoSuchBucket), "%s: no bucket was created", releaseName(rel))
				full, err := store.GetBucket(ctx, "", "full")
				Expect(err).NotTo(HaveOccurred(), releaseName(rel))
				st, err := store.PrefetchObject(ctx, full, meta.ObjKey{Name: "k"})
				Expect(err).NotTo(HaveOccurred(), releaseName(rel))
				Expect(string(st.Head)).To(Equal(data), "%s: the object is still there with its data", releaseName(rel))
			}
		},
		refusedRequests(),
	)
})

// refusedRequests is a GET, a PUT and a DELETE of each bucket configuration
// radosgw has no op for, against the empty bucket or a free name, and of an
// object's annotation, against the object that holds data, with a PUT of a
// RenameObject and an UpdateObjectEncryption onto that object.
func refusedRequests() []TableEntry {
	var entries []TableEntry
	for _, key := range []string{"analytics", "intelligent-tiering", "inventory", "metrics", "metadataConfiguration", "metadataTable"} {
		entries = append(entries,
			Entry("GET ?"+key+" lists nothing", "GET", "/plain?"+key, ""),
			Entry("PUT ?"+key+" with no body creates no bucket under a free name", "PUT", "/fresh?"+key, ""),
			Entry("DELETE ?"+key+" keeps the empty bucket", "DELETE", "/plain?"+key, ""))
	}
	return append(entries,
		Entry("GET ?annotation sends none of the object's data", "GET", "/full/k?annotation&annotationName=n", ""),
		Entry("PUT ?annotation keeps the object's data", "PUT", "/full/k?annotation&annotationName=n", "an annotation"),
		Entry("DELETE ?annotation keeps the object", "DELETE", "/full/k?annotation&annotationName=n", ""),
		Entry("PUT ?renameObject with no body keeps the data of the object under the new name", "PUT", "/full/k?renameObject", "",
			"Content-Length", "0", "x-amz-rename-source", "/full/other"),
		Entry("PUT ?encryption keeps the object's data", "PUT", "/full/k?encryption", "<ObjectEncryption/>"))
}
