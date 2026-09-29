package gate_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/test/gate"
)

// populated is a manifest in the shape hack/rooket/populate.sh writes.
const populated = `{
  "release": "squid",
  "ceph_version": "19.2.6",
  "rooket_name": "rgw-go-squid",
  "realm": "ceph-objectstore",
  "zonegroup": "ceph-objectstore",
  "zone": "ceph-objectstore",
  "pools": {"root": ".rgw.root", "meta": "ceph-objectstore.rgw.meta",
    "control": "ceph-objectstore.rgw.control", "log": "ceph-objectstore.rgw.log",
    "index": "ceph-objectstore.rgw.buckets.index", "data": "ceph-objectstore.rgw.buckets.data",
    "nonec": "ceph-objectstore.rgw.buckets.non-ec"},
  "storage_classes": {"COMP_ZLIB": "zlib", "COMP_LZ4": "lz4"},
  "users": [
    {"uid": "alice", "tenant": "", "access_key": "AK1", "secret_key": "SK1"},
    {"uid": "bob", "tenant": "t1", "access_key": "AK2", "secret_key": "SK2"}
  ],
  "buckets": [
    {"name": "plain", "owner": "alice", "id": "z.1", "marker": "z.1", "num_shards": 11},
    {"name": "tenanted", "owner": "t1$bob", "id": "z.2", "marker": "z.2", "num_shards": 11}
  ],
  "objects": [
    {"bucket": "plain", "key": "small.bin", "size": 1024},
    {"bucket": "plain", "key": "multipart.bin", "size": 20971520, "multipart": true},
    {"bucket": "plain", "key": "meta.bin", "size": 64, "content_type": "text/plain",
      "metadata": {"x-amz-meta-color": "blue"}},
    {"bucket": "plain", "key": "comp-zlib.bin", "size": 1048576, "storage_class": "COMP_ZLIB",
      "compression": "zlib"}
  ]
}`

var _ = Describe("Manifest", func() {
	write := func(content string) string {
		path := filepath.Join(GinkgoT().TempDir(), "manifest.json")
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		return path
	}

	It("decodes the populated manifest", func() {
		m, err := gate.LoadManifest(write(populated))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Release).To(Equal("squid"))
		Expect(m.CephVersion).To(Equal("19.2.6"))
		Expect(m.RooketName).To(Equal("rgw-go-squid"))
		Expect(m.Pools.Data).To(Equal("ceph-objectstore.rgw.buckets.data"))
		Expect(m.Pools.NonEC).To(Equal("ceph-objectstore.rgw.buckets.non-ec"))
		Expect([]string{m.Realm, m.ZoneGroup, m.Zone}).To(HaveEach("ceph-objectstore"))
		Expect(m.StorageClasses).To(Equal(map[string]string{"COMP_ZLIB": "zlib", "COMP_LZ4": "lz4"}))
		Expect(m.Users[0].ID()).To(Equal("alice"))
		Expect(m.Users[1].ID()).To(Equal("t1$bob"))

		plain, ok := m.Bucket("plain")
		Expect(ok).To(BeTrue())
		Expect(plain.Tenant()).To(BeEmpty())
		Expect(plain.EntryPointKey()).To(Equal("plain"))
		Expect(plain.NumShards).To(Equal(uint32(11)))
		tenanted, ok := m.Bucket("tenanted")
		Expect(ok).To(BeTrue())
		Expect(tenanted.Tenant()).To(Equal("t1"))
		Expect(tenanted.EntryPointKey()).To(Equal("t1/tenanted"))
		_, ok = m.Bucket("missing")
		Expect(ok).To(BeFalse())

		objs := m.ObjectsIn("plain")
		Expect(objs).To(HaveLen(4))
		Expect(objs[1].Multipart).To(BeTrue())
		Expect(objs[2].ContentType).To(Equal("text/plain"))
		Expect(objs[2].Metadata).To(HaveKeyWithValue("x-amz-meta-color", "blue"))
		Expect(objs[2].StorageClass).To(BeEmpty())
		Expect(objs[2].Compression).To(BeEmpty())
		Expect(objs[3]).To(Equal(gate.Object{
			Bucket: "plain", Key: "comp-zlib.bin", Size: 1 << 20, StorageClass: "COMP_ZLIB", Compression: "zlib",
		}))
		Expect(m.ObjectsIn("tenanted")).To(BeEmpty())
	})

	It("names the populated site in radosgw-admin's options", func() {
		m := gate.Manifest{Realm: "r1", ZoneGroup: "zg1", Zone: "z1"}
		Expect(m.AdminFlags()).To(Equal([]string{"--rgw-realm=r1", "--rgw-zonegroup=zg1", "--rgw-zone=z1"}))
	})

	It("reports a missing or malformed manifest", func() {
		_, err := gate.LoadManifest(filepath.Join(GinkgoT().TempDir(), "absent.json"))
		Expect(err).To(MatchError(os.ErrNotExist))
		_, err = gate.LoadManifest(write("{"))
		Expect(err).To(MatchError(ContainSubstring("decoding")))
	})
})
