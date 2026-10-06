package admin

import (
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// Route is one row of the dispatch table.
type Route struct {
	// Name is the op radosgw runs, by its RGWOp::name(), except
	// get_metadata_myself, which radosgw names get_metadata.
	Name string
	// Payloads are the signed payload forms get_auth_data_v4 accepts for the
	// op's type: RGW_OP_ADMIN_SET_METADATA is the one admin type on its
	// single-chunk list (rgw_rest_s3.cc:5916 at v19.2.6, :6483 at v20.2.4),
	// and no admin type takes aws-chunked.
	Payloads op.PayloadForms
}

// routeNames are every name Dispatch yields.
var routeNames = []string{
	"get_info", "get_zone_config",
	"get_quota_info", "list_user", "get_user_info",
	"create_subuser", "create_access_key", "add_user_caps", "set_quota_info", "create_user",
	"modify_subuser", "modify_user",
	"remove_subuser", "remove_access_key", "remove_user_caps", "remove_user",
	"get_policy", "check_bucket_index", "get_bucket_info",
	"set_bucket_quota", "link_bucket", "unlink_bucket", "remove_object", "remove_bucket",
	"get_metadata_myself", "get_metadata", "list_metadata", "set_metadata", "remove_metadata",
	"get_usage", "trim_usage",
	"create_account", "set_account_quota_info", "modify_account", "get_account", "delete_account",
	"get_ratelimit_info", "put_ratelimit_info",
	"list_realms", "get_realm", "get_period",
}

// capPair is one RGWUserCaps::check_cap an op's check_caps makes.
type capPair struct {
	typ  string
	perm uint32
}

// routeCaps are each route's check_caps (driver/rados/rgw_rest_user.cc,
// driver/rados/rgw_rest_bucket.cc, rgw_rest_metadata.h, rgw_rest_usage.cc,
// rgw_rest_info.cc, rgw_rest_config.h, rgw_rest_account.cc,
// rgw_rest_ratelimit.cc and driver/rados/rgw_rest_realm.cc at v19.2.6 and
// v20.2.4): the op passes when the identity holds any one pair, which only
// get_user_info offers two of (rgw_rest_user.cc:77-83 at v19.2.6).
// get_metadata_myself is an RGWOp_Metadata_Get. Squid's account get and
// delete check a cap named "account", which no caps command can grant
// (rgw_rest_account.cc:173-174 and :195-196 at v19.2.6); accountCaps
// applies that.
var routeCaps = map[string][]capPair{
	"get_info":        {{"info", meta.CapRead}},
	"get_zone_config": {{"zone", meta.CapRead}},

	"get_quota_info":    {{"users", meta.CapRead}},
	"list_user":         {{"users", meta.CapRead}},
	"get_user_info":     {{"user-info-without-keys", meta.CapRead}, {"users", meta.CapRead}},
	"create_subuser":    {{"users", meta.CapWrite}},
	"create_access_key": {{"users", meta.CapWrite}},
	"add_user_caps":     {{"users", meta.CapWrite}},
	"set_quota_info":    {{"users", meta.CapWrite}},
	"create_user":       {{"users", meta.CapWrite}},
	"modify_subuser":    {{"users", meta.CapWrite}},
	"modify_user":       {{"users", meta.CapWrite}},
	"remove_subuser":    {{"users", meta.CapWrite}},
	"remove_access_key": {{"users", meta.CapWrite}},
	"remove_user_caps":  {{"users", meta.CapWrite}},
	"remove_user":       {{"users", meta.CapWrite}},

	"get_policy":         {{"buckets", meta.CapRead}},
	"check_bucket_index": {{"buckets", meta.CapWrite}},
	"get_bucket_info":    {{"buckets", meta.CapRead}},
	"set_bucket_quota":   {{"buckets", meta.CapWrite}},
	"link_bucket":        {{"buckets", meta.CapWrite}},
	"unlink_bucket":      {{"buckets", meta.CapWrite}},
	"remove_object":      {{"buckets", meta.CapWrite}},
	"remove_bucket":      {{"buckets", meta.CapWrite}},

	"get_metadata_myself": {{"metadata", meta.CapRead}},
	"get_metadata":        {{"metadata", meta.CapRead}},
	"list_metadata":       {{"metadata", meta.CapRead}},
	"set_metadata":        {{"metadata", meta.CapWrite}},
	"remove_metadata":     {{"metadata", meta.CapWrite}},

	"get_usage":  {{"usage", meta.CapRead}},
	"trim_usage": {{"usage", meta.CapWrite}},

	"create_account":         {{"accounts", meta.CapWrite}},
	"set_account_quota_info": {{"accounts", meta.CapWrite}},
	"modify_account":         {{"accounts", meta.CapWrite}},
	"get_account":            {{"accounts", meta.CapRead}},
	"delete_account":         {{"accounts", meta.CapWrite}},

	"get_ratelimit_info": {{"ratelimit", meta.CapRead}},
	"put_ratelimit_info": {{"ratelimit", meta.CapWrite}},

	"list_realms": {{"zone", meta.CapRead}},
	"get_realm":   {{"zone", meta.CapRead}},
	"get_period":  {{"zone", meta.CapRead}},
}

// accountCaps is routeCaps for name at rel, with Squid's "account" type for
// the account get and delete.
func accountCaps(name string, rel denc.Release) []capPair {
	caps := routeCaps[name]
	if rel < denc.Tentacle && (name == "get_account" || name == "delete_account") {
		return []capPair{{"account", caps[0].perm}}
	}
	return caps
}

// Dispatch is the op_get, op_put, op_post and op_delete of the admin
// handlers at v19.2.6 and v20.2.4 (driver/rados/rgw_rest_user.cc:1129-1177,
// driver/rados/rgw_rest_bucket.cc:393-427, rgw_rest_metadata.cc:302-317,
// rgw_rest_usage.cc:123-131, rgw_rest_info.cc:46-49,
// rgw_rest_config.cc:49-57, driver/rados/rgw_rest_realm.cc:246-247 and
// :355-358, rgw_rest_ratelimit.cc:344-351, rgw_rest_account.cc:223-241, at
// v19.2.6's lines). A method or sub-resource radosgw has no op for is
// ErrMethodNotAllowed, get_op's null (rgw_process.cc:325-329 at v19.2.6).
// So are the ops docs/exclusions.md excludes with multisite: Sync_Bucket
// (PUT bucket?sync), the log API and the period POST. rel gates the one row
// that differs by release: PUT account?quota, which Squid's op_put has no
// branch for and so runs as modify_account (v20.2.4 :306-311).
func Dispatch(method string, q Request, rel denc.Release) (Route, error) {
	name := opName(method, q, rel)
	if name == "" {
		return Route{}, op.ErrMethodNotAllowed
	}
	r := Route{Name: name}
	if name == "set_metadata" {
		r.Payloads = op.PayloadSigned
	}
	return r, nil
}

// opName is the route method and q select at rel, "" for none.
func opName(method string, q Request, rel denc.Release) string {
	sub := q.Args.SubResource()
	switch q.Resource {
	case "info":
		if method == http.MethodGet {
			return "get_info"
		}
	case "config":
		if t, _ := q.Args.Get("type"); method == http.MethodGet && t == "zone" {
			return "get_zone_config"
		}
	case "user":
		return userOp(method, sub)
	case "bucket":
		return bucketOp(method, sub)
	case "metadata":
		return metadataOp(method, q.Args)
	case "usage":
		switch method {
		case http.MethodGet:
			return "get_usage"
		case http.MethodDelete:
			return "trim_usage"
		}
	case "account":
		return accountOp(method, sub, rel)
	case "ratelimit":
		switch method {
		case http.MethodGet:
			return "get_ratelimit_info"
		case http.MethodPost:
			return "put_ratelimit_info"
		}
	case "realm":
		return realmOp(method, q)
	}
	return ""
}

func userOp(method, sub string) string {
	switch method {
	case http.MethodGet:
		return pick(sub, map[string]string{"quota": "get_quota_info", "list": "list_user"}, "get_user_info")
	case http.MethodPut:
		return pick(sub, map[string]string{
			"subuser": "create_subuser", "key": "create_access_key", "caps": "add_user_caps", "quota": "set_quota_info",
		}, "create_user")
	case http.MethodPost:
		return pick(sub, map[string]string{"subuser": "modify_subuser"}, "modify_user")
	case http.MethodDelete:
		return pick(sub, map[string]string{
			"subuser": "remove_subuser", "key": "remove_access_key", "caps": "remove_user_caps",
		}, "remove_user")
	}
	return ""
}

func bucketOp(method, sub string) string {
	switch method {
	case http.MethodGet:
		return pick(sub, map[string]string{"policy": "get_policy", "index": "check_bucket_index"}, "get_bucket_info")
	case http.MethodPut:
		// RGWOp_Sync_Bucket is excluded with multisite: no route.
		return pick(sub, map[string]string{"quota": "set_bucket_quota", "sync": ""}, "link_bucket")
	case http.MethodPost:
		return "unlink_bucket"
	case http.MethodDelete:
		return pick(sub, map[string]string{"object": "remove_object"}, "remove_bucket")
	}
	return ""
}

// metadataOp tests the arguments themselves, args.exists, not the
// sub-resource (rgw_rest_metadata.cc:302-309).
func metadataOp(method string, a Args) string {
	switch method {
	case http.MethodGet:
		switch {
		case a.Has("myself"):
			return "get_metadata_myself"
		case a.Has("key"):
			return "get_metadata"
		}
		return "list_metadata"
	case http.MethodPut:
		return "set_metadata"
	case http.MethodDelete:
		return "remove_metadata"
	}
	return ""
}

func accountOp(method, sub string, rel denc.Release) string {
	switch method {
	case http.MethodPost:
		return "create_account"
	case http.MethodPut:
		if rel >= denc.Tentacle && sub == "quota" {
			return "set_account_quota_info"
		}
		return "modify_account"
	case http.MethodGet:
		return "get_account"
	case http.MethodDelete:
		return "delete_account"
	}
	return ""
}

// realmOp is RGWHandler_Realm's op_get and, under the period manager
// RGWRESTMgr_Realm registers, RGWHandler_Period's; its POST is excluded
// with multisite.
func realmOp(method string, q Request) string {
	if method != http.MethodGet {
		return ""
	}
	if q.Sub == "period" {
		return "get_period"
	}
	return pick(q.Args.SubResource(), map[string]string{"list": "list_realms"}, "get_realm")
}

// pick is the sub-resource's op, or def when the sub-resource selects none.
func pick(sub string, ops map[string]string, def string) string {
	if name, ok := ops[sub]; ok {
		return name
	}
	return def
}
