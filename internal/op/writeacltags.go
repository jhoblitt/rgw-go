package op

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// grantHeaderPrefix is the prefix of s->has_acl_header's headers,
// exists_prefix("HTTP_X_AMZ_GRANT")
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5054 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5584 at v20.2.4).
const grantHeaderPrefix = "X-Amz-Grant"

// HasGrantHeader reports whether h holds a header whose name starts with
// x-amz-grant in any case, which has create_s3_policy build the ACL from the
// grant headers.
func HasGrantHeader(h http.Header) bool {
	for k := range h {
		if len(k) >= len(grantHeaderPrefix) && strings.EqualFold(k[:len(grantHeaderPrefix)], grantHeaderPrefix) {
			return true
		}
	}
	return false
}

// NamesNonPrivateACL reports whether a request's headers name an ACL other
// than the private one every new bucket and object gets without any: an
// x-amz-acl that is neither empty nor private, or an x-amz-grant-* header,
// the headers create_s3_policy builds the ACL from. An x-amz-acl of private,
// or an empty one, builds the ACL a request without the header gets, so it
// changes nothing, as CreateBucket's reference says of a bucket's. CreateBucket
// takes its ACL headers by this predicate.
func NamesNonPrivateACL(h http.Header) bool {
	canned, _ := HeaderValue(h, "X-Amz-Acl")
	return canned != "" && canned != "private" || HasGrantHeader(h)
}

// namesACL reports whether a write's headers name an ACL that needs
// s3:PutObjectAcl: NamesNonPrivateACL's, but for x-amz-acl:
// bucket-owner-full-control. That canned ACL gives the bucket's owner control
// of the object and no one else access, and Amazon documents and ships
// policies that allow s3:PutObject alone under an s3:x-amz-acl condition on
// it (docs/differences.md, "A write's ACL and tag headers need
// s3:PutObjectAcl and s3:PutObjectTagging"), so a write naming it needs
// s3:PutObject alone. CreateBucket still counts it, as its reference says of
// any ACL it does not name. A grant header beside it is counted: exempting
// the grant that names the bucket's owner would need the owner's id parsed
// from the header.
func namesACL(h http.Header) bool {
	if canned, _ := HeaderValue(h, "X-Amz-Acl"); canned == "bucket-owner-full-control" && !HasGrantHeader(h) {
		return false
	}
	return NamesNonPrivateACL(h)
}

// namesTags reports whether a write's headers name a tag set, an
// x-amz-tagging header with any value, empty included. The request's attrs
// hold the header as sent under the tag attr's name
// (rgw_get_request_metadata,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L2175-L2237 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L2338-L2408 at v20.2.4),
// where a write that stores them leaves it unless a tag set replaces it. A
// copy that replaces its metadata keeps any value as sent, and RGWObjTags
// reads that text as the tags older objects stored
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_tag.h#L32-L62 at v19.2.7 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_tag.h#L32-L62 at v20.2.4;
// tags.Decode), so the header sets the copy's tags. The other writes leave
// an empty one, which no reader decodes, and still change what is stored.
func namesTags(h http.Header) bool {
	_, ok := HeaderValue(h, "X-Amz-Tagging")
	return ok
}

// verifyACLAndTags is the permission an object write needs beside s3:PutObject
// for each of the two things it sets, evaluated on the object's ARN:
// s3:PutObjectAcl when setsACL, an ACL namesACL counts, and
// s3:PutObjectTagging when setsTags. Amazon S3's permissions table requires
// both for the headers that set them, and radosgw checks neither for any
// write (docs/ceph-upstream-bugs.md, "radosgw's PutObject sets the object
// ACL and tags its headers name under s3:PutObject alone";
// docs/differences.md, "A write's ACL and tag headers need s3:PutObjectAcl
// and s3:PutObjectTagging").
//
// The identity's and the bucket's policies decide. Where neither does, no ACL
// permission is asked. That leaves the policies alone to refuse an account's
// user and a role in an account, whom they must allow as they must every
// action, and another account's identity, who needs its own identity policy's
// Allow; the account's root passes, as it does every action. A requester
// outside an account, a role outside one and the anonymous user are refused
// only by a Deny, as CreateBucket's s3:PutBucketAcl refuses a user outside an
// account (VerifyUser). The evaluator gives each action the condition keys of
// the request's headers, as it gives them to the action's own op. The write's
// own permission check calls it once s3:PutObject has passed, so Run lets an
// admin through its refusal as through that check's.
//
// The headers of a write name what it sets (namesACL, namesTags); a write
// that takes them from a form passes what its fields name.
func verifyACLAndTags(ctx context.Context, r *Request, setsACL, setsTags bool) error {
	for _, c := range []struct {
		sets bool
		a    policy.Action
		what string
	}{{setsACL, policy.S3PutObjectAcl, "the request's ACL needs"}, {setsTags, policy.S3PutObjectTagging, "the request's tags need"}} {
		if !c.sets {
			continue
		}
		if err := VerifyBucketPermission(ctx, r, c.a, acl.PermNone); err != nil {
			return fmt.Errorf("%w: %s %s", err, c.what, c.a)
		}
	}
	return nil
}

// verifyWriteHeaders is what a write's headers need beside s3:PutObject: the
// permissions its object-lock headers need (verifyLockHeaders), then those
// its ACL and tag headers need (verifyACLAndTags).
func verifyWriteHeaders(ctx context.Context, r *Request) error {
	if err := verifyLockHeaders(ctx, r); err != nil {
		return err
	}
	return verifyACLAndTags(ctx, r, namesACL(r.Header), namesTags(r.Header))
}
