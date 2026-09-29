// Package gate holds the phase 0 acceptance gate: specs that read every
// metadata object a radosgw wrote into a populated disposable cluster and
// prove rgw-go decodes and re-encodes it byte for byte. This file decodes the
// manifest hack/rooket/populate.sh records of what it wrote.
package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Manifest is hack/rooket/out/<release>/manifest.json.
type Manifest struct {
	Release string `json:"release"`
	// CephVersion is the Ceph version the release's cluster pins, e.g.
	// "19.2.6", as the daemons report it.
	CephVersion string `json:"ceph_version"`
	// RooketName is the rooket cluster holding the population, whose Rook
	// toolbox runs radosgw-admin and ceph-dencoder for the gate.
	RooketName string `json:"rooket_name"`
	// Realm, ZoneGroup and Zone name the site the radosgw serves.
	Realm     string `json:"realm"`
	ZoneGroup string `json:"zonegroup"`
	Zone      string `json:"zone"`
	Pools     Pools  `json:"pools"`
	// StorageClasses maps each storage class populate.sh adds to the
	// default placement to the codec radosgw compresses its objects with.
	StorageClasses map[string]string `json:"storage_classes"`
	Users          []User            `json:"users"`
	Buckets        []Bucket          `json:"buckets"`
	Objects        []Object          `json:"objects"`
}

// AdminFlags are the radosgw-admin options that select the populated site.
// Without them radosgw-admin works in a zone named default, which it creates
// on first use and the radosgw never serves.
func (m Manifest) AdminFlags() []string {
	return []string{"--rgw-realm=" + m.Realm, "--rgw-zonegroup=" + m.ZoneGroup, "--rgw-zone=" + m.Zone}
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

// Object is one object populate.sh uploaded. Size is its size as a client
// writes and reads it, before any compression.
type Object struct {
	Bucket      string            `json:"bucket"`
	Key         string            `json:"key"`
	Size        uint64            `json:"size"`
	Multipart   bool              `json:"multipart"`
	ContentType string            `json:"content_type"`
	Metadata    map[string]string `json:"metadata"`
	// StorageClass is the storage class the object was written to, empty
	// for the placement's STANDARD class.
	StorageClass string `json:"storage_class,omitempty"`
	// Compression is the codec radosgw compressed the object with, empty
	// when it stored the object as written.
	Compression string `json:"compression,omitempty"`
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
