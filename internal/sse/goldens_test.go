package sse_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/sse"
)

const configType = "RGWBucketEncryptionConfig"

// The two trees of ceph-dencoder goldens. testdata holds what
// hack/goldens/gen.sh writes from the corpus with the v19.2.7 image;
// running it with IMAGE=quay.io/ceph/ceph:v20.2.4 writes the same bytes, as
// rgw_bucket_encryption.h and .cc are identical at the two tags.
// testdata/carried holds vectors built by hand, the two inner types' corpus
// objects among them, each inside an RGWBucketEncryptionConfig, the only one
// of the three types ceph-dencoder registers
// (https://github.com/ceph/ceph/blob/v19.2.7/src/tools/ceph-dencoder/rgw_types.h#L198,
// https://github.com/ceph/ceph/blob/v20.2.4/src/tools/ceph-dencoder/rgw_types.h#L200),
// with each floor's dencoder output under an archive directory named for
// its tag. The .bin files are committed; the .json and .reenc files beside
// them are rebuilt, as gen.sh runs ceph-dencoder over the corpus, by running
// this in each tag's directory with that tag for <tag>:
//
//	podman run --rm --security-opt label=disable -v "$PWD:/v" quay.io/ceph/ceph:<tag> sh -euc '
//	  for f in /v/*.bin; do
//	    n=${f%.bin}
//	    ceph-dencoder type RGWBucketEncryptionConfig import "$f" decode dump_json > "$n.json"
//	    ceph-dencoder type RGWBucketEncryptionConfig import "$f" decode encode export "$n.reenc"
//	  done'
var goldenDirs = []string{"testdata", filepath.Join("testdata", "carried")}

func encodeConfig(e *denc.Encoder, c sse.BucketEncryptionConfig, r denc.Release) { c.Encode(e, r) }

// dump is RGWBucketEncryptionConfig::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.cc#L42-L49),
// the members ceph-dencoder's dump_json prints.
func dump(c sse.BucketEncryptionConfig) map[string]any {
	m := map[string]any{"rule_exist": c.Rule != nil}
	if c.Rule != nil {
		m["sse_algorithm"] = c.Rule.Apply.SSEAlgorithm
		m["kms_master_key_id"] = c.Rule.Apply.KMSMasterKeyID
		m["bucket_key_enabled"] = c.Rule.BucketKeyEnabled
	}
	return m
}

var _ = Describe("ceph-dencoder goldens", func() {
	for _, dir := range goldenDirs {
		It("re-encodes every RGWBucketEncryptionConfig under "+dir+" as ceph-dencoder does, on both releases", func() {
			for _, r := range releases {
				goldentest.RoundTrip(dir, configType, goldentest.Options{Release: r, SkipJSON: true},
					sse.DecodeBucketEncryptionConfig, encodeConfig)
			}
		})
		It("decodes every RGWBucketEncryptionConfig under "+dir+" to ceph-dencoder's dump", func() {
			cs, err := goldentest.Load(dir, configType)
			Expect(err).NotTo(HaveOccurred())
			Expect(cs).NotTo(BeEmpty())
			for _, c := range cs {
				id := c.Archive + "/" + c.Name
				var want map[string]any
				Expect(json.Unmarshal(c.JSON, &want)).To(Succeed(), id)
				Expect(dump(decodeWhole(c.Bin, sse.DecodeBucketEncryptionConfig))).To(Equal(want), id)
			}
		})
	}
	It("holds each corpus archive's three objects, generate_test_instances' configurations", func() {
		cs, err := goldentest.Load("testdata", configType)
		Expect(err).NotTo(HaveOccurred())
		got := map[string][]sse.BucketEncryptionConfig{}
		for _, c := range cs {
			got[c.Archive] = append(got[c.Archive], decodeWhole(c.Bin, sse.DecodeBucketEncryptionConfig))
		}
		instances := []sse.BucketEncryptionConfig{
			{Rule: &sse.ServerSideEncryptionConfiguration{
				Apply:            sse.ApplyServerSideEncryptionByDefault{KMSMasterKeyID: "some:key", SSEAlgorithm: "aws:kms"},
				BucketKeyEnabled: true,
			}},
			{Rule: &sse.ServerSideEncryptionConfiguration{Apply: sse.ApplyServerSideEncryptionByDefault{SSEAlgorithm: "AES256"}}},
			{},
		}
		Expect(got).To(HaveLen(2))
		for _, a := range []string{"18.2.0", "19.2.0-404-g78ddc7f9027"} {
			Expect(got).To(HaveKeyWithValue(a, ConsistOf(instances)), "archive %s", a)
		}
	})
	It("holds every carried vector from both floors' dencoders, which agree byte for byte", func() {
		cs, err := goldentest.Load(filepath.Join("testdata", "carried"), configType)
		Expect(err).NotTo(HaveOccurred())
		byTag := map[string]map[string]goldentest.Case{}
		for _, c := range cs {
			if byTag[c.Archive] == nil {
				byTag[c.Archive] = map[string]goldentest.Case{}
			}
			byTag[c.Archive][c.Name] = c
		}
		Expect(byTag).To(HaveLen(2))
		squid, tentacle := byTag["v19.2.7"], byTag["v20.2.4"]
		Expect(squid).NotTo(BeEmpty())
		Expect(tentacle).To(HaveLen(len(squid)))
		for name, s := range squid {
			Expect(tentacle).To(HaveKey(name), "vector %s has no v20.2.4 output", name)
			t := tentacle[name]
			Expect(t.Bin).To(Equal(s.Bin), name)
			Expect(t.JSON).To(Equal(s.JSON), name)
			Expect(t.ReEnc).To(Equal(s.ReEnc), name)
		}
	})
})

// rawCase is a corpus object of a type ceph-dencoder does not register.
type rawCase struct {
	id  string
	bin []byte
}

// loadRaw reads testdata/corpus/<typ>/<archive>/<object>, copied verbatim
// from the ceph-object-corpus. A (1,1) struct's corpus bytes are themselves
// its expected encoding.
func loadRaw(typ string) []rawCase {
	GinkgoHelper()
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", typ, "*", "*"))
	Expect(err).NotTo(HaveOccurred())
	Expect(paths).NotTo(BeEmpty(), "no corpus objects for %s", typ)
	cs := make([]rawCase, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p) //nolint:gosec // paths come from the test's own testdata
		Expect(err).NotTo(HaveOccurred())
		cs = append(cs, rawCase{id: p, bin: b})
	}
	return cs
}

// corpusDefault is the default both inner types' corpus objects hold.
func corpusDefault() sse.ApplyServerSideEncryptionByDefault {
	return sse.ApplyServerSideEncryptionByDefault{KMSMasterKeyID: "testkey-1", SSEAlgorithm: "aws:kms"}
}

var _ = Describe("corpus objects ceph-dencoder does not register", func() {
	It("ApplyServerSideEncryptionByDefault round-trips byte for byte", func() {
		for _, c := range loadRaw("ApplyServerSideEncryptionByDefault") {
			v := decodeWhole(c.bin, sse.DecodeApplyServerSideEncryptionByDefault)
			Expect(v).To(Equal(corpusDefault()), c.id)
			for _, r := range releases {
				Expect(encodeAt(r, v.Encode)).To(Equal(c.bin), c.id)
			}
		}
	})
	It("ServerSideEncryptionConfiguration round-trips byte for byte", func() {
		for _, c := range loadRaw("ServerSideEncryptionConfiguration") {
			v := decodeWhole(c.bin, sse.DecodeServerSideEncryptionConfiguration)
			Expect(v).To(Equal(sse.ServerSideEncryptionConfiguration{Apply: corpusDefault()}), c.id)
			for _, r := range releases {
				Expect(encodeAt(r, v.Encode)).To(Equal(c.bin), c.id)
			}
		}
	})
})
