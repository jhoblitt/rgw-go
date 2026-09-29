package s3

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// Route is one row of the dispatch table.
type Route struct {
	// Name is radosgw's op name, the handler key and the metrics label.
	Name  string
	Scope op.Scope
	// Payloads is the signed payload forms the op takes at the release
	// Dispatch ran for; the authenticator refuses any other form.
	Payloads op.PayloadForms
}

// Dispatch selects the route for r in radosgw's method, scope and subresource
// precedence at release rel: a table row that exists at one release only is
// skipped on the other, so the request falls through as that release's op_*
// does. A method or subresource radosgw has no op for is ErrMethodNotAllowed.
// The route carries its payload forms at rel.
func Dispatch(r *op.Request, rel denc.Release) (Route, error) {
	scope := r.Scope()
	// RGWRESTMgr_S3::get_handler returns no handler for a bucket request that
	// names an object's subresource, whatever the method.
	if scope == op.ScopeBucket && hasObjectSubresource(r.Query) {
		return Route{}, op.ErrMethodNotAllowed
	}
	m, ok := table[methodKey{scope, r.Method}]
	if !ok {
		return Route{}, op.ErrMethodNotAllowed
	}
	name, matched := m.walk(r.Query, rel)
	if !matched {
		name = m.def
		switch {
		case scope == op.ScopeBucket && r.Method == http.MethodGet && listType(r.Query) == 2:
			// get_obj_op(true) reads list-type as get_int does.
			name = "list_bucket_v2"
		case scope == op.ScopeObject && r.Method == http.MethodPut && copiesObject(r):
			name = "copy_obj"
		}
	}
	if name == "" {
		return Route{}, op.ErrMethodNotAllowed
	}
	return Route{Name: name, Scope: scope, Payloads: payloads(name, rel)}, nil
}

// releaseSet is the releases a table row exists at, zero meaning every one.
type releaseSet uint8

const (
	onSquid    releaseSet = 1 << denc.Squid
	onTentacle releaseSet = 1 << denc.Tentacle
)

func (s releaseSet) admits(rel denc.Release) bool {
	return s == 0 || s&(1<<rel) != 0
}

// row is one branch of an op_* method: the op it returns when its query key
// is present, or "" for a branch that returns nullptr.
type row struct {
	key  string
	name string
	rel  releaseSet
	// valued matches only a non-empty value, where op_* reads the key's value
	// rather than testing its presence.
	valued bool
}

// method is one op_* method: its branches in order and the op it returns
// when none matches, "" when radosgw has no such method.
type method struct {
	rows []row
	def  string
}

type methodKey struct {
	scope  op.Scope
	method string
}

func (m method) walk(q url.Values, rel denc.Release) (string, bool) {
	for _, rw := range m.rows {
		if !rw.rel.admits(rel) {
			continue
		}
		if q.Has(rw.key) && (!rw.valued || q.Get(rw.key) != "") {
			return rw.name, true
		}
	}
	return "", false
}

// table is RGWHandler_REST_{Service,Bucket,Obj}_S3::op_* (rgw_rest_s3.cc
// :4596-4881 at v19.2.6, :5137-5441 at v20.2.4) and the is_*_op predicates
// they call (rgw_rest_s3.h:670-775 at v19.2.6, :694-805 at v20.2.4), each a
// test of its key's presence. A method missing from the table, the service
// PUT, DELETE and OPTIONS among them, is the base class's nullptr. The
// service POST's IAM, STS and topic actions are not dispatched here.
//
// The bucket op_get, op_put and op_delete open by returning nullptr for an
// "encryption" subresource, which never exists because RGWHTTPArgs::append
// does not list the key (rgw_common.cc:918-976 at v19.2.6), so ?encryption
// reaches its own branch. is_notification_op is false unless rgw_enable_apis
// names notifications or pubsub, as its default does; the table routes
// ?notification regardless (docs/exclusions.md). The website branches (both
// releases) and the mdsearch ones (v20.2.4 only) return nullptr when
// rgw_enable_static_website or rgw_enable_mdsearch is off, and PUT
// ?replication when the bucket's sync policy is legacy; those routes'
// handlers apply the options.
var table = map[methodKey]method{
	{op.ScopeService, http.MethodGet}: {
		rows: []row{{key: "usage", name: "get_usage"}},
		def:  "list_buckets",
	},
	{op.ScopeService, http.MethodHead}: {def: "list_buckets"},

	{op.ScopeBucket, http.MethodGet}: {
		rows: []row{
			{key: "logging", name: "get_bucket_logging"},
			{key: "location", name: "get_bucket_location"},
			{key: "versioning", name: "get_bucket_versioning"},
			{key: "website", name: "get_bucket_website"},
			{key: "mdsearch", name: "get_bucket_meta_search"},
			{key: "acl", name: "get_acls"},
			{key: "cors", name: "get_cors"},
			{key: "requestPayment", name: "get_request_payment"},
			{key: "uploads", name: "list_bucket_multiparts"},
			{key: "lifecycle", name: "get_lifecycle"},
			{key: "policy", name: "get_bucket_policy"},
			{key: "tagging", name: "get_bucket_tags"},
			{key: "object-lock", name: "get_bucket_object_lock"},
			{key: "notification", name: "get_bucket_notification"},
			{key: "replication", name: "get_bucket_replication"},
			{key: "policyStatus", name: "get_bucket_policy_status"},
			{key: "publicAccessBlock", name: "get_bucket_public_access_block"},
			{key: "encryption", name: "get_bucket_encryption"},
		},
		def: "list_bucket",
	},
	{op.ScopeBucket, http.MethodHead}: {
		rows: []row{
			{key: "acl", name: "get_acls"},
			{key: "uploads", name: "list_bucket_multiparts"},
		},
		def: "stat_bucket",
	},
	{op.ScopeBucket, http.MethodPut}: {
		rows: []row{
			{key: "logging", rel: onSquid},
			{key: "logging", name: "put_bucket_logging", rel: onTentacle},
			{key: "versioning", name: "set_bucket_versioning"},
			{key: "website", name: "set_bucket_website"},
			{key: "tagging", name: "put_bucket_tags"},
			{key: "acl", name: "put_acls"},
			{key: "cors", name: "put_cors"},
			{key: "requestPayment", name: "set_request_payment"},
			{key: "lifecycle", name: "put_lifecycle"},
			{key: "policy", name: "put_bucket_policy"},
			{key: "object-lock", name: "put_bucket_object_lock"},
			{key: "notification", name: "put_bucket_notification"},
			{key: "replication", name: "put_bucket_replication"},
			{key: "publicAccessBlock", name: "put_bucket_public_access_block"},
			{key: "encryption", name: "put_bucket_encryption"},
		},
		def: "create_bucket",
	},
	{op.ScopeBucket, http.MethodDelete}: {
		rows: []row{
			{key: "logging", rel: onSquid},
			{key: "tagging", name: "delete_bucket_tags"},
			{key: "cors", name: "delete_cors"},
			{key: "lifecycle", name: "delete_lifecycle"},
			{key: "policy", name: "delete_bucket_policy"},
			{key: "notification", name: "delete_bucket_notification"},
			{key: "replication", name: "delete_bucket_replication"},
			{key: "publicAccessBlock", name: "delete_bucket_public_access_block"},
			{key: "encryption", name: "delete_bucket_encryption"},
			{key: "website", name: "delete_bucket_website"},
			{key: "mdsearch", name: "delete_bucket_meta_search"},
		},
		def: "delete_bucket",
	},
	{op.ScopeBucket, http.MethodPost}: {
		rows: []row{
			{key: "delete", name: "multi_object_delete"},
			{key: "logging", name: "post_bucket_logging", rel: onTentacle},
			{key: "mdsearch", name: "config_bucket_meta_search"},
		},
		def: "post_obj",
	},
	{op.ScopeBucket, http.MethodOptions}: {def: "options_cors"},

	{op.ScopeObject, http.MethodGet}: {
		rows: []row{
			{key: "acl", name: "get_acls"},
			{key: "uploadId", name: "list_multipart"},
			{key: "layout", name: "get_obj_layout"},
			{key: "tagging", name: "get_obj_tags"},
			// radosgw has this branch at v20.2.4 only; rgw-go serves
			// GetObjectAttributes on both releases.
			{key: "attributes", name: "get_obj_attrs"},
			{key: "retention", name: "get_obj_retention"},
			{key: "legal-hold", name: "get_obj_legal_hold"},
		},
		def: "get_obj",
	},
	{op.ScopeObject, http.MethodHead}: {
		rows: []row{
			{key: "acl", name: "get_acls"},
			{key: "uploadId", name: "list_multipart"},
		},
		def: "get_obj",
	},
	{op.ScopeObject, http.MethodPut}: {
		rows: []row{
			{key: "acl", name: "put_acls"},
			{key: "tagging", name: "put_obj_tags"},
			{key: "retention", name: "put_obj_retention"},
			{key: "legal-hold", name: "put_obj_legal_hold"},
		},
		def: "put_obj",
	},
	{op.ScopeObject, http.MethodDelete}: {
		rows: []row{
			{key: "tagging", name: "delete_obj_tags"},
			{key: "uploadId", name: "abort_multipart", valued: true},
		},
		def: "delete_obj",
	},
	{op.ScopeObject, http.MethodPost}: {
		rows: []row{
			{key: "uploadId", name: "complete_multipart"},
			{key: "uploads", name: "init_multipart"},
			{key: "restore", name: "restore_obj", rel: onTentacle},
			{key: "select-type", name: "select_obj"},
		},
		def: "post_obj",
	},
	{op.ScopeObject, http.MethodOptions}: {def: "options_cors"},
}

// hasObjectSubresource is RGWHTTPArgs::exist_obj_excl_sub_resource
// (rgw_common.h:408-415 at v19.2.6).
func hasObjectSubresource(q url.Values) bool {
	for _, key := range [...]string{"append", "torrent", "uploadId", "partNumber", "versionId"} {
		if q.Has(key) {
			return true
		}
	}
	return false
}

// listType is list-type as RGWHTTPArgs::get_int reads it through
// strict_strtol: strtoll's leading white space and sign, then only digits,
// within int's range; 1 when absent or malformed. strict_strtol reads the
// value as a C string, so it stops at a NUL.
func listType(q url.Values) int {
	s := q.Get("list-type")
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.ParseInt(strings.TrimLeft(s, " \t\n\v\f\r"), 10, 32)
	if err != nil {
		return 1
	}
	return int(n)
}

// copiesObject is op_put's choice of RGWCopyObj, made when init has set
// init_state.src_bucket. With x-amz-copy-source-range or uploadId, init
// parses no copy source and op_put returns RGWPutObj, which serves
// UploadPartCopy; so it does for a source whose bucket part is empty. A
// source parse_copy_location cannot parse stays with copy_obj here, though
// radosgw never dispatches one: RGWHandler_REST_S3::init answers it with 400
// before get_op and authentication, for every method and scope
// (rgw_rest_s3.cc:5026-5041 at v19.2.6, :5586-5601 at v20.2.4), so that
// check belongs in the handler lifecycle, not in copy_obj's handler.
func copiesObject(r *op.Request) bool {
	v, ok := copySource(r)
	if !ok {
		return false
	}
	bucket, parsed := copySourceBucket(v)
	return !parsed || bucket != ""
}

// payloadRow is the signed payload forms an op takes at the releases in rel,
// zero meaning both.
type payloadRow struct {
	forms op.PayloadForms
	rel   releaseSet
}

// payloadTable is radosgw's per-op completer whitelist, keyed by route name
// through each route's RGWOp::get_type(): get_auth_data_v4's single-chunk
// switch (rgw_rest_s3.cc:5899-5944 at v19.2.6, :6466-6515 at v20.2.4) and
// its streamed switch, RGW_OP_PUT_OBJ alone (:5957-5969, :6528-6540). A
// route missing from it takes neither form.
var payloadTable = map[string]payloadRow{
	"put_obj":                           {op.PayloadSigned | op.PayloadChunked, 0},
	"create_bucket":                     {op.PayloadSigned, 0},
	"put_acls":                          {op.PayloadSigned, 0},
	"put_cors":                          {op.PayloadSigned, 0},
	"put_bucket_encryption":             {op.PayloadSigned, 0},
	"get_bucket_encryption":             {op.PayloadSigned, 0},
	"delete_bucket_encryption":          {op.PayloadSigned, 0},
	"init_multipart":                    {op.PayloadSigned, 0},
	"complete_multipart":                {op.PayloadSigned, 0},
	"set_bucket_versioning":             {op.PayloadSigned, 0},
	"multi_object_delete":               {op.PayloadSigned, 0},
	"set_bucket_website":                {op.PayloadSigned, 0},
	"delete_bucket_website":             {op.PayloadSigned, 0}, // typed RGW_OP_SET_BUCKET_WEBSITE (rgw_op.h:1090 at v19.2.6)
	"put_bucket_policy":                 {op.PayloadSigned, 0},
	"put_obj_tags":                      {op.PayloadSigned, 0},
	"put_bucket_tags":                   {op.PayloadSigned, 0},
	"put_bucket_replication":            {op.PayloadSigned, 0},
	"put_lifecycle":                     {op.PayloadSigned, 0},
	"set_request_payment":               {op.PayloadSigned, 0},
	"put_bucket_notification":           {op.PayloadSigned, 0}, // RGW_OP_PUBSUB_NOTIF_CREATE
	"delete_bucket_notification":        {op.PayloadSigned, 0}, // RGW_OP_PUBSUB_NOTIF_DELETE
	"get_bucket_notification":           {op.PayloadSigned, 0}, // RGW_OP_PUBSUB_NOTIF_LIST
	"put_bucket_object_lock":            {op.PayloadSigned, 0},
	"put_obj_retention":                 {op.PayloadSigned, 0},
	"put_obj_legal_hold":                {op.PayloadSigned, 0},
	"put_bucket_public_access_block":    {op.PayloadSigned, 0},
	"get_bucket_public_access_block":    {op.PayloadSigned, 0},
	"delete_bucket_public_access_block": {op.PayloadSigned, 0},
	"get_obj":                           {op.PayloadSigned, 0},
	"select_obj":                        {op.PayloadSigned, 0},       // S3 Select's op keeps RGWGetObj's RGW_OP_GET_OBJ
	"get_obj_attrs":                     {op.PayloadSigned, onSquid}, // a Squid radosgw runs ?attributes as get_obj; RGW_OP_GET_OBJ_ATTRS (v20.2.4) is not listed
	"get_bucket_logging":                {op.PayloadSigned, onTentacle},
	"put_bucket_logging":                {op.PayloadSigned, onTentacle},
	"post_bucket_logging":               {op.PayloadSigned, onTentacle},
	"restore_obj":                       {op.PayloadSigned, onTentacle},
}

func payloads(name string, rel denc.Release) op.PayloadForms {
	if p, ok := payloadTable[name]; ok && p.rel.admits(rel) {
		return p.forms
	}
	return 0
}
