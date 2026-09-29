package op

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// MetadataEntry is one entry of a metadata section as the admin API shows it.
type MetadataEntry struct {
	Key     string
	Data    json.RawMessage
	Version meta.ObjVersion
	Mtime   time.Time
}

// PutMetadataOptions guards a metadata Put.
type PutMetadataOptions struct {
	// IfVersion, when non-nil, fails with ErrConcurrentModification unless the
	// stored version matches.
	IfVersion *meta.ObjVersion
}

//counterfeiter:generate . MetadataStore

// MetadataStore is the admin API's metadata sections: user, bucket and
// bucket.instance.
type MetadataStore interface {
	Get(ctx context.Context, section, key string) (MetadataEntry, error)
	Put(ctx context.Context, section, key string, e MetadataEntry, opts PutMetadataOptions) error
	Remove(ctx context.Context, section, key string) error
	List(ctx context.Context, section, marker string, maxEntries int) (keys []string, next string, more bool, err error)
}
