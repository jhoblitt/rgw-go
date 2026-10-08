package radosclient

// The kinds of object whose id is a credential or an email: an index object
// named by an access key, an email or a Swift key. Errors and logs name such
// an object by its pool and kind, never by its id.
const (
	KindUserKeyIndex   = "user key index"
	KindUserEmailIndex = "user email index"
	KindUserSwiftIndex = "user swift index"
)

// hiddenNamespaces are the namespaces radosgw gives those indexes in a
// zone's metadata pool by default (rgw_zone.cc:586-588 at v19.2.6, :594-596
// at v20.2.4).
var hiddenNamespaces = map[string]string{
	"users.keys":  KindUserKeyIndex,
	"users.email": KindUserEmailIndex,
	"users.swift": KindUserSwiftIndex,
}

// HiddenIDKind returns the kind of the objects in namespace when their ids
// are credentials or emails, and "" otherwise. It knows radosgw's default
// namespaces only; a store that knows where its zone keeps the indexes hides
// them by that too.
func HiddenIDKind(namespace string) string { return hiddenNamespaces[namespace] }
