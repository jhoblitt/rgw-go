// Package gate holds the phase 0 acceptance gate: specs that read every
// metadata object a radosgw wrote into a populated disposable cluster and
// prove rgw-go decodes and re-encodes it byte for byte. This file decodes the
// manifest hack/cluster/populate.sh records of what it wrote.
package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Manifest is hack/cluster/out/<release>/manifest.json.
type Manifest struct {
	Release string   `json:"release"`
	Zone    string   `json:"zone"`
	Pools   Pools    `json:"pools"`
	Users   []User   `json:"users"`
	Buckets []Bucket `json:"buckets"`
	Objects []Object `json:"objects"`
}

// Pools names the zone's RADOS pools.
type Pools struct {
	Root    string `json:"root"`
	Meta    string `json:"meta"`
	Control string `json:"control"`
	Log     string `json:"log"`
	Index   string `json:"index"`
	Data    string `json:"data"`
	NonEC   string `json:"nonec"`
}

// User is one user populate.sh created, with its first S3 key.
type User struct {
	UID       string `json:"uid"`
	Tenant    string `json:"tenant"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// ID is the user's rgw_user string form, "<tenant>$<uid>" or "<uid>".
func (u User) ID() string {
	if u.Tenant == "" {
		return u.UID
	}
	return u.Tenant + "$" + u.UID
}

// Bucket is one bucket populate.sh created, as radosgw-admin bucket stats
// reported it.
type Bucket struct {
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	ID        string `json:"id"`
	Marker    string `json:"marker"`
	NumShards uint32 `json:"num_shards"`
}

// Tenant is the tenant of the bucket's owner, which the bucket shares.
func (b Bucket) Tenant() string {
	tenant, _, ok := strings.Cut(b.Owner, "$")
	if !ok {
		return ""
	}
	return tenant
}

// EntryPointKey is the bucket's metadata key, "<tenant>/<name>" or "<name>".
func (b Bucket) EntryPointKey() string {
	if t := b.Tenant(); t != "" {
		return t + "/" + b.Name
	}
	return b.Name
}

// Object is one object populate.sh uploaded.
type Object struct {
	Bucket      string            `json:"bucket"`
	Key         string            `json:"key"`
	Size        uint64            `json:"size"`
	Multipart   bool              `json:"multipart"`
	ContentType string            `json:"content_type"`
	Metadata    map[string]string `json:"metadata"`
}

// LoadManifest reads and decodes the manifest at path.
func LoadManifest(path string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Manifest{}, fmt.Errorf("gate: reading the manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("gate: decoding %s: %w", path, err)
	}
	return m, nil
}

// Bucket returns the bucket named name.
func (m Manifest) Bucket(name string) (Bucket, bool) {
	for _, b := range m.Buckets {
		if b.Name == name {
			return b, true
		}
	}
	return Bucket{}, false
}

// ObjectsIn returns the objects uploaded to the bucket named name.
func (m Manifest) ObjectsIn(name string) []Object {
	var out []Object
	for _, o := range m.Objects {
		if o.Bucket == name {
			out = append(out, o)
		}
	}
	return out
}
