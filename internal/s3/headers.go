package s3

import (
	"net/http"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// SetCommonHeaders writes x-amz-request-id on w, and x-amz-request-charged:
// requester when r.BucketRec is requester-pays and the identity does not own
// it (end_header, rgw_rest.cc:597-601 at v19.2.6, :602-606 at v20.2.4;
// WriteError never adds it, end_header's !is_err()). The Server header is
// not r's to know: the Handler sends it on every response.
func SetCommonHeaders(w http.ResponseWriter, r *op.Request) {
	h := w.Header()
	// dump_trans_id skips an empty id (rgw_rest.cc:579-587 at v19.2.6,
	// :584-592 at v20.2.4).
	if r.ID != "" {
		SetHeader(h, "x-amz-request-id", r.ID)
	}
	if b := r.BucketRec; b != nil && b.Info.RequesterPays && !isOwnerOf(r.Identity, b.Info.Owner) {
		SetHeader(h, "x-amz-request-charged", "requester")
	}
}

// SetHeader sets name to v under name's own spelling. radosgw's dump_header
// hands the name to the frontend, which writes its bytes as they are
// (rgw_rest.cc:347-357, rgw_asio_client.cc:167-178 at v19.2.6 and v20.2.4),
// and an SDK reads a user metadata key back case and all. http.Header.Set
// would send net/http's canonical form instead; net/http writes a key set
// directly as it stands.
func SetHeader(h http.Header, name, v string) { h[name] = []string{v} }

// addHeader is SetHeader that keeps the values already under name.
func addHeader(h http.Header, name, v string) { h[name] = append(h[name], v) }

// isOwnerOf is LocalApplier::is_owner_of's match_owner (rgw_auth.cc:67-75 at
// v19.2.6 and v20.2.4): a user owner is the identity's user, an account
// owner the account the user belongs to.
func isOwnerOf(id op.Identity, owner meta.Owner) bool {
	if owner.User != nil {
		return id.User != nil && owner.User.String() == id.User.UserID.String()
	}
	return id.Account != nil && owner.Account == id.Account.ID
}

// SetContentLength is dump_content_length (rgw_rest.cc:388-397 at v19.2.6
// and v20.2.4): the length and Accept-Ranges: bytes. radosgw calls it for
// every error document and for a success whose op names its length: the GET
// and HEAD of an object (rgw_rest_s3.cc:459 at v19.2.6, :495 at v20.2.4) and
// the PUT of an object or part without a copy source (:2747, :2911). Every
// other Content-Length radosgw sends is the one its frontend adds when the
// response completes, which carries no Accept-Ranges.
func SetContentLength(h http.Header, n uint64) {
	h.Set("Content-Length", strconv.FormatUint(n, 10))
	h.Set("Accept-Ranges", "bytes")
}
