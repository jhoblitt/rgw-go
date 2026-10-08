package radosclient

// The kinds of object errors and logs name by their pool and kind, never by
// their id: an index object named by an access key, an email or a Swift
// key, whose id is a credential or an email, and rgw-go's bucket claims,
// named by a bucket id.
const (
	KindUserKeyIndex   = "user key index"
	KindUserEmailIndex = "user email index"
	KindUserSwiftIndex = "user swift index"
	KindBucketClaim    = "bucket claim"
)

// hiddenNamespaces are the namespaces radosgw gives those indexes in a
// zone's metadata pool by default (rgw_zone.cc:586-588 at v19.2.6, :594-596
// at v20.2.4), and the one rgw-go's bucket claims take by default beside the
// domain root (:579; :587).
var hiddenNamespaces = map[string]string{
	"users.keys":                 KindUserKeyIndex,
	"users.email":                KindUserEmailIndex,
	"users.swift":                KindUserSwiftIndex,
	"root" + BucketClaimNSSuffix: KindBucketClaim,
}

// BucketClaimNSSuffix is what rgw-go's bucket claims append to the
// namespace of the zone's domain root, where an object named by a bucket id
// could be an entry point.
const BucketClaimNSSuffix = ".rgw-go-bucket-claims"

// HiddenIDKind returns the kind of the objects in namespace when their ids
// are credentials or emails, and "" otherwise. It knows radosgw's default
// namespaces only; a store that knows where its zone keeps the indexes hides
// them by that too.
func HiddenIDKind(namespace string) string { return hiddenNamespaces[namespace] }
