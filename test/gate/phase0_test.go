//go:build integration

package gate_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
	"github.com/jhoblitt/rgw-go/test/gate"
)

// rgwContainer is the disposable cluster's radosgw container, which holds the
// keyring radosgw-admin needs; monContainer is its mon, whose image carries
// ceph-dencoder.
const (
	rgwContainer = "rgw-go-rgw"
	monContainer = "rgw-go-mon"
)

// omapPage is how many omap values or listed entries one read asks for; the
// populated shards and user bucket lists hold far fewer.
const omapPage = 1000

var (
	// plainETag is an MD5 etag; multipartETag is the "<md5>-<parts>" form a
	// completed multipart upload gets.
	plainETag     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	multipartETag = regexp.MustCompile(`^[0-9a-f]{32}-[0-9]+$`)
)

// roundTrip decodes raw whole with decode, re-encodes the value for release r
// and requires the bytes radosgw wrote.
func roundTrip[T any](what string, raw []byte, r denc.Release, decode func(*denc.Decoder) T,
	encode func(T, *denc.Encoder, denc.Release),
) T {
	GinkgoHelper()
	v, got := reencode(what, raw, r, decode, encode)
	expectSameBytes(got, raw, "re-encoding "+what)
	return v
}

// reencode decodes raw whole and returns the value and its encoding for r.
func reencode[T any](what string, raw []byte, r denc.Release, decode func(*denc.Decoder) T,
	encode func(T, *denc.Encoder, denc.Release),
) (T, []byte) {
	GinkgoHelper()
	d := denc.NewDecoder(raw)
	v := decode(d)
	Expect(d.Err()).NotTo(HaveOccurred(), "decoding %s", what)
	Expect(d.Remaining()).To(BeZero(), "trailing bytes in %s", what)
	e := denc.NewEncoder()
	encode(v, e, r)
	return v, e.Bytes()
}

// cxxNormalizes lists the C++ types whose decoder changes what it read, so
// that radosgw itself cannot re-encode a stored object byte for byte, with
// the reason. rgw-go mirrors those decoders.
var cxxNormalizes = map[string]string{
	"RGWZoneGroup": "RGWZoneGroupPlacementTarget::decode fills an empty storage_classes with STANDARD",
}

// roundTripAs is roundTrip for an object of the C++ type cxxType. When
// cxxType is one of cxxNormalizes and the re-encoding differs from raw, it
// requires instead the bytes the cluster's own ceph-dencoder writes after
// decoding raw, the most radosgw could write back.
func roundTripAs[T any](ctx context.Context, what, cxxType string, raw []byte, r denc.Release,
	decode func(*denc.Decoder) T, encode func(T, *denc.Encoder, denc.Release),
) T {
	GinkgoHelper()
	v, got := reencode(what, raw, r, decode, encode)
	reason, normalizes := cxxNormalizes[cxxType]
	if normalizes && !bytes.Equal(got, raw) {
		note("%s: radosgw's own decode re-encodes it differently (%s); comparing with ceph-dencoder", what, reason)
		expectSameBytes(got, cxxReencode(ctx, cxxType, raw), "re-encoding "+what+" against ceph-dencoder")
		return v
	}
	expectSameBytes(got, raw, "re-encoding "+what)
	return v
}

// cxxReencode runs raw through decode and encode in the cluster's
// ceph-dencoder, which is the running release's.
func cxxReencode(ctx context.Context, cxxType string, raw []byte) []byte {
	GinkgoHelper()
	script := `out=$(mktemp) && ceph-dencoder type "$1" import - decode encode export "$out" && cat "$out"; rc=$?; rm -f "$out"; exit $rc`
	cmd := exec.CommandContext(ctx, "podman", "exec", "-i", monContainer, "sh", "-c", script, "sh", cxxType)
	cmd.Stdin = bytes.NewReader(raw)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred(), "ceph-dencoder %s: %s", cxxType, stderr.String())
	return out
}

// expectSameBytes fails with the first differing offset and the bytes around
// it, which a plain Equal on two long slices buries.
func expectSameBytes(got, want []byte, what string) {
	GinkgoHelper()
	if bytes.Equal(got, want) {
		return
	}
	at := 0
	for at < len(got) && at < len(want) && got[at] == want[at] {
		at++
	}
	window := func(b []byte) []byte {
		lo, hi := max(at-16, 0), min(at+32, len(b))
		if lo > len(b) {
			return nil
		}
		return b[lo:hi]
	}
	Fail(fmt.Sprintf("%s: %d bytes, want %d; first difference at offset %d\n got [%d:]: % x\nwant [%d:]: % x",
		what, len(got), len(want), at, max(at-16, 0), window(got), max(at-16, 0), window(want)))
}

// tally counts what a spec round-tripped, per pool and kind, and prints the
// counts when the spec ends.
type tally map[string]int

func newTally(spec string) tally {
	t := tally{}
	DeferCleanup(func() {
		GinkgoWriter.Printf("%s:\n", spec)
		for _, k := range slices.Sorted(maps.Keys(t)) {
			GinkgoWriter.Printf("  %-56s %d\n", k, t[k])
		}
	})
	return t
}

// note records a fact about the population the gate reports rather than
// asserts, such as an xattr a real object does not carry.
func note(format string, args ...any) {
	GinkgoWriter.Printf("NOTE: "+format+"\n", args...)
}

// object is one RADOS object read whole with its xattrs.
type object struct {
	data   []byte
	xattrs map[string][]byte
}

// statObject returns an object's size, and false when it does not exist.
func statObject(ctx context.Context, p radosclient.Pool, oid string) (uint64, bool) {
	GinkgoHelper()
	op := radosclient.NewReadOp()
	st := op.Stat()
	_, err := p.Read(ctx, oid, op, radosclient.OpFlagNone)
	if err != nil {
		Expect(err).To(MatchError(radosclient.ErrNotFound), "stat %s/%s", p.Name(), oid)
		return 0, false
	}
	Expect(st.Err).NotTo(HaveOccurred())
	return st.Size, true
}

// readObject reads oid's data and xattrs in one operation, after a stat
// sizes the read.
func readObject(ctx context.Context, p radosclient.Pool, oid string) object {
	GinkgoHelper()
	size, ok := statObject(ctx, p, oid)
	Expect(ok).To(BeTrue(), "%s/%s:%s does not exist", p.Name(), p.Namespace(), oid)
	op := radosclient.NewReadOp()
	xs := op.GetXattrs()
	var data *radosclient.ReadResult
	if size > 0 {
		data = op.Read(0, size)
	}
	Expect(p.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed(), "reading %s/%s", p.Name(), oid)
	Expect(xs.Err).NotTo(HaveOccurred())
	o := object{xattrs: xs.Xattrs}
	if data != nil {
		Expect(data.Err).NotTo(HaveOccurred())
		Expect(uint64(data.N)).To(Equal(size), "short read of %s", oid)
		o.data = data.Data[:data.N]
	}
	return o
}

// listObjects returns the names in a pool's namespace, sorted.
func listObjects(ctx context.Context, p radosclient.Pool) []string {
	GinkgoHelper()
	var oids []string
	Expect(p.ListObjects(ctx, func(oid, _ string) error {
		oids = append(oids, oid)
		return nil
	})).To(Succeed())
	slices.Sort(oids)
	return oids
}

// omapValues reads every omap value of oid.
func omapValues(ctx context.Context, p radosclient.Pool, oid string) map[string][]byte {
	GinkgoHelper()
	op := radosclient.NewReadOp()
	vals := op.OmapGetVals("", "", omapPage)
	Expect(p.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed(), "omap of %s", oid)
	Expect(vals.Err).NotTo(HaveOccurred())
	Expect(vals.More).To(BeFalse(), "%s has more than %d omap values", oid, omapPage)
	return vals.Values
}

// encodeRequest encodes a class request for release r.
func encodeRequest(enc func(*denc.Encoder, denc.Release), r denc.Release) []byte {
	e := denc.NewEncoder()
	enc(e, r)
	return e.Bytes()
}

// replyBytes returns a class method's output once its op has run.
func replyBytes(res *radosclient.ExecResult, what string) []byte {
	GinkgoHelper()
	b, err := res.Bytes()
	Expect(err).NotTo(HaveOccurred(), what)
	return b
}

// expectXattrNames requires that every xattr name is one of allowed.
func expectXattrNames(what string, xattrs map[string][]byte, allowed ...string) {
	GinkgoHelper()
	for _, name := range slices.Sorted(maps.Keys(xattrs)) {
		Expect(allowed).To(ContainElement(name), "unexpected xattr %s on %s", name, what)
	}
}

// objVersion round-trips the cls_version xattr when the object has one.
func objVersion(t tally, kind, what string, xattrs map[string][]byte, r denc.Release) (version.ObjVersion, bool) {
	GinkgoHelper()
	raw, ok := xattrs[meta.AttrObjVersion]
	if !ok {
		return version.ObjVersion{}, false
	}
	v := roundTrip(what+" "+meta.AttrObjVersion, raw, r, version.DecodeObjVersion, version.ObjVersion.Encode)
	Expect(v.Ver).To(BeNumerically(">=", 1), "%s version", what)
	Expect(v.Tag).NotTo(BeEmpty(), "%s version tag", what)
	t[kind+" "+meta.AttrObjVersion]++
	return v, true
}

// containerRelease is a disposable cluster container's rgw-go.release label.
func containerRelease(ctx context.Context, name string) string {
	GinkgoHelper()
	out, err := exec.CommandContext(ctx, "podman", "inspect", name,
		"--format", `{{index .Config.Labels "rgw-go.release"}}`).Output()
	Expect(err).NotTo(HaveOccurred(), "inspecting %s", name)
	return strings.TrimSpace(string(out))
}

// rgwMaxChunkSize is the default rgw_max_chunk_size, the most data a head
// object holds.
const rgwMaxChunkSize = 4 << 20

// expectPopulated is the floor under every per-object check: a manifest that
// lists nothing, or lacks the shapes the gate exists to cover, would let the
// specs pass without looking at anything.
func expectPopulated(m gate.Manifest) {
	GinkgoHelper()
	Expect(m.Users).NotTo(BeEmpty(), "the manifest lists no users")
	Expect(m.Buckets).NotTo(BeEmpty(), "the manifest lists no buckets")
	Expect(m.Objects).NotTo(BeEmpty(), "the manifest lists no objects")
	Expect(m.Objects).To(ContainElement(HaveField("Multipart", BeTrue())), "no multipart object")
	Expect(m.Objects).To(ContainElement(HaveField("Size", BeNumerically(">", rgwMaxChunkSize))),
		"no object larger than a head")
	Expect(m.Objects).To(ContainElement(HaveField("Size", BeZero())), "no empty object")
	tenanted := false
	for _, b := range m.Buckets {
		Expect(b.NumShards).To(BeNumerically(">", 0), "bucket %s has no index shards", b.Name)
		tenanted = tenanted || b.Tenant() != ""
	}
	Expect(tenanted).To(BeTrue(), "no tenanted bucket")
}

// admin runs radosgw-admin in the radosgw container and returns its stdout.
func admin(ctx context.Context, args ...string) []byte {
	GinkgoHelper()
	cmd := exec.CommandContext(ctx, "podman", append([]string{"exec", rgwContainer, "radosgw-admin"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred(), "radosgw-admin %s: %s", strings.Join(args, " "), stderr.String())
	return out
}

// expectSameJSON requires that rgw-go's JSON for v equals radosgw-admin's,
// once the keys at the paths in absent, which the running release's dump
// does not write, are dropped from rgw-go's. A path is dot-separated keys, a
// "[]" segment standing for every element of an array.
func expectSameJSON(what string, v, want any, absent ...string) {
	GinkgoHelper()
	b, err := json.Marshal(v)
	Expect(err).NotTo(HaveOccurred(), "marshaling %s", what)
	got := canonical(b)
	for _, path := range absent {
		Expect(dropPath(got, strings.Split(path, "."))).To(BeNumerically(">", 0),
			"%s lacks %s, which only an older dump omits", what, path)
	}
	diff := jsonDiff(what, got, want)
	Expect(diff).To(BeEmpty(), "%s differs from radosgw-admin", what)
}

// dropPath deletes the key at path from every object it reaches and returns
// how many it deleted.
func dropPath(v any, path []string) int {
	if len(path) == 0 {
		return 0
	}
	if path[0] == "[]" {
		arr, ok := v.([]any)
		if !ok {
			return 0
		}
		n := 0
		for _, e := range arr {
			n += dropPath(e, path[1:])
		}
		return n
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	if len(path) == 1 {
		if _, ok := obj[path[0]]; !ok {
			return 0
		}
		delete(obj, path[0])
		return 1
	}
	return dropPath(obj[path[0]], path[1:])
}

// squidZoneDumpLacks are the RGWZoneParams::dump keys v20.2.4 added:
// v19.2.6's dump in src/rgw/rgw_zone.cc writes none of these pools.
var squidZoneDumpLacks = []string{"dedup_pool", "bucket_logging_pool", "restore_pool"}

// squidZoneGroupDumpLacks are the RGWZoneGroupPlacementTier::dump keys
// v20.2.4 added: v19.2.6's dump writes only tier_type, storage_class,
// retain_head_object and s3.
var squidZoneGroupDumpLacks = []string{
	"placement_targets.[].tier_targets.[].val.allow_read_through",
	"placement_targets.[].tier_targets.[].val.read_through_restore_days",
	"placement_targets.[].tier_targets.[].val.restore_storage_class",
}

// xattrU64 and xattrU32 decode an xattr holding one little-endian integer
// and require that it holds nothing else.
func xattrU64(what string, raw []byte) uint64 {
	GinkgoHelper()
	return roundTrip(what, raw, denc.Squid, (*denc.Decoder).U64,
		func(v uint64, e *denc.Encoder, _ denc.Release) { e.U64(v) })
}

func xattrU32(what string, raw []byte) uint32 {
	GinkgoHelper()
	return roundTrip(what, raw, denc.Squid, (*denc.Decoder).U32,
		func(v uint32, e *denc.Encoder, _ denc.Release) { e.U32(v) })
}

// etagString is an etag xattr with at most one trailing NUL stripped, as
// get_obj_state strips it: radosgw stores most etags without one, but some
// writers (a second append, NFS, Swift bulk upload) include it.
func etagString(raw []byte) string {
	return string(bytes.TrimSuffix(raw, []byte{0}))
}

// cString requires that an xattr is a NUL-terminated string, as radosgw
// stores most string attrs, and returns the string.
func cString(what string, raw []byte) string {
	GinkgoHelper()
	Expect(raw).NotTo(BeEmpty(), "%s is empty", what)
	Expect(raw[len(raw)-1]).To(BeZero(), "%s does not end in NUL: %q", what, raw)
	s := string(raw[:len(raw)-1])
	Expect(s).NotTo(ContainSubstring("\x00"), "%s holds an inner NUL", what)
	return s
}

// headOID is the head object's name and locator for key in a bucket, as
// get_obj_bucket_and_oid_loc derives them.
func headOID(marker, key string) (oid, locator string) {
	k := meta.ObjKey{Name: key}
	oid = marker + "_" + k.OID()
	if loc := k.Locator(); loc != "" {
		locator = marker + "_" + loc
	}
	return oid, locator
}

var _ = Describe("phase 0 gate", Label("integration"), func() {
	var (
		cluster radosclient.Cluster
		release denc.Release
		m       gate.Manifest
		zone    meta.ZoneParams
		pools   map[meta.Pool]radosclient.Pool
	)

	// open returns a pool handle for p's pool and namespace, closed when the
	// spec ends.
	open := func(ctx context.Context, p meta.Pool) radosclient.Pool {
		GinkgoHelper()
		if h, ok := pools[p]; ok {
			return h
		}
		h, err := cluster.Pool(ctx, p.Name, p.NS)
		Expect(err).NotTo(HaveOccurred(), "opening %s", p)
		DeferCleanup(func() { Expect(h.Close()).To(Succeed()) })
		pools[p] = h
		return h
	}

	// bucketInfo reads and decodes a bucket's instance.
	bucketInfo := func(ctx context.Context, b gate.Bucket) meta.BucketInfo {
		GinkgoHelper()
		id := meta.BucketID{Tenant: b.Tenant(), Name: b.Name, ID: b.ID}
		o := readObject(ctx, open(ctx, zone.DomainRoot), id.InstanceOID())
		d := denc.NewDecoder(o.data)
		info := meta.DecodeBucketInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return info
	}

	// head reads an object's head and decodes its manifest.
	head := func(ctx context.Context, b gate.Bucket, key string) (object, meta.Manifest, radosclient.Pool, string) {
		GinkgoHelper()
		oid, loc := headOID(b.Marker, key)
		data := open(ctx, meta.Pool{Name: m.Pools.Data})
		if loc != "" {
			data = data.WithLocator(loc)
		}
		o := readObject(ctx, data, oid)
		raw, ok := o.xattrs[meta.AttrManifest]
		Expect(ok).To(BeTrue(), "%s has no manifest", oid)
		d := denc.NewDecoder(raw)
		man := meta.DecodeManifest(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return o, man, data, oid
	}

	// zoneGroupOf reads the zonegroup whose zones include the populated zone.
	zoneGroupOf := func(ctx context.Context) meta.ZoneGroup {
		GinkgoHelper()
		root := open(ctx, meta.Pool{Name: m.Pools.Root})
		var found []meta.ZoneGroup
		for _, oid := range listObjects(ctx, root) {
			if !strings.HasPrefix(oid, "zonegroup_info.") {
				continue
			}
			d := denc.NewDecoder(readObject(ctx, root, oid).data)
			g := meta.DecodeZoneGroup(d)
			Expect(d.Err()).NotTo(HaveOccurred(), oid)
			if _, ok := g.Zones[zone.ID]; ok {
				found = append(found, g)
			}
		}
		Expect(found).To(HaveLen(1), "zonegroups holding zone %s", zone.ID)
		return found[0]
	}

	// partHeads returns a multipart object's part count, the N of its
	// "<md5>-N" etag, and the names of its part heads: the first object of
	// each part, which its manifest names in the multipart namespace.
	partHeads := func(ctx context.Context, b gate.Bucket, key string) (int, []string) {
		GinkgoHelper()
		o, man, _, _ := head(ctx, b, key)
		etag := etagString(o.xattrs[meta.AttrETag])
		Expect(etag).To(MatchRegexp(multipartETag.String()), "%s etag", key)
		_, count, _ := strings.Cut(etag, "-")
		n, err := strconv.Atoi(count)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeNumerically(">", 0), "%s part count", key)
		stripes, err := man.Stripes()
		Expect(err).NotTo(HaveOccurred(), key)
		var names []string
		for _, s := range stripes {
			if s.Obj.Key.NS == meta.NSMultipart && !slices.Contains(names, s.OID()) {
				names = append(names, s.OID())
			}
		}
		Expect(names).To(HaveLen(n), "%s names %d part heads for %d parts", key, len(names), n)
		return n, names
	}

	BeforeEach(func(ctx SpecContext) {
		// Built with the integration tag, the gate runs or fails: it never
		// skips for want of a cluster.
		conf := cephtest.Conf()
		manifestPath := cephtest.ModuleRelative(os.Getenv("RGW_GO_TEST_MANIFEST"))
		Expect(manifestPath).NotTo(BeEmpty(), "RGW_GO_TEST_MANIFEST is not set; run make gate RELEASE=squid|tentacle")
		var err error
		m, err = gate.LoadManifest(manifestPath)
		Expect(err).NotTo(HaveOccurred(), "the cluster must be populated")
		expectPopulated(m)
		for _, c := range []string{rgwContainer, monContainer} {
			Expect(containerRelease(ctx, c)).To(Equal(m.Release), "%s runs another release than the manifest's", c)
		}

		cluster, err = goceph.Connect(ctx, goceph.Config{ConfigFile: conf, Mode: goceph.ModeCallback})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(cluster.Close()).To(Succeed()) })
		pools = map[meta.Pool]radosclient.Pool{}

		name, err := cluster.RequiredOSDRelease(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal(m.Release), "the running cluster is not the populated one")
		release, err = goceph.Release(ctx, cluster)
		Expect(err).NotTo(HaveOccurred())
		want, ok := denc.ParseRelease(m.Release)
		Expect(ok).To(BeTrue())
		Expect(release).To(Equal(want))

		root := open(ctx, meta.Pool{Name: m.Pools.Root})
		d := denc.NewDecoder(readObject(ctx, root, meta.ZoneNameOID(m.Zone)).data)
		zoneID := meta.DecodeNameToID(d).ObjID
		Expect(d.Err()).NotTo(HaveOccurred())
		d = denc.NewDecoder(readObject(ctx, root, meta.ZoneInfoOID(zoneID)).data)
		zone = meta.DecodeZoneParams(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(zone.Name).To(Equal(m.Zone))
	})

	It("round-trips every root pool object", func(ctx SpecContext) {
		t := newTally("root pool " + m.Pools.Root)
		root := open(ctx, meta.Pool{Name: m.Pools.Root})
		var zoneIDs, zoneGroupIDs, defaultZones, defaultZoneGroups, zoneNames, zoneGroupNames []string
		// The disposable clusters run without a realm, so they hold no realm
		// or period objects; those cases decode them should a realm appear.
		for _, oid := range listObjects(ctx, root) {
			o := readObject(ctx, root, oid)
			var kind string
			switch {
			case strings.HasPrefix(oid, "zone_info."):
				kind = "zone_info"
				z := roundTrip(oid, o.data, release, meta.DecodeZoneParams, meta.ZoneParams.Encode)
				Expect(meta.ZoneInfoOID(z.ID)).To(Equal(oid))
				zoneIDs = append(zoneIDs, z.ID)
			case strings.HasPrefix(oid, "zone_names."):
				kind = "zone_names"
				zoneNames = append(zoneNames, roundTrip(oid, o.data, release, meta.DecodeNameToID, meta.NameToID.Encode).ObjID)
			case strings.HasPrefix(oid, "zonegroup_info."):
				kind = "zonegroup_info"
				g := roundTripAs(ctx, oid, "RGWZoneGroup", o.data, release, meta.DecodeZoneGroup, meta.ZoneGroup.Encode)
				Expect(meta.ZoneGroupInfoOID(g.ID)).To(Equal(oid))
				zoneGroupIDs = append(zoneGroupIDs, g.ID)
			case strings.HasPrefix(oid, "zonegroups_names."):
				kind = "zonegroups_names"
				zoneGroupNames = append(zoneGroupNames, roundTrip(oid, o.data, release, meta.DecodeNameToID, meta.NameToID.Encode).ObjID)
			case strings.HasPrefix(oid, "default.zonegroup."):
				kind = "default.zonegroup"
				defaultZoneGroups = append(defaultZoneGroups, roundTrip(oid, o.data, release,
					meta.DecodeDefaultSystemMetaObjInfo, meta.DefaultSystemMetaObjInfo.Encode).DefaultID)
			case strings.HasPrefix(oid, "default.zone."):
				kind = "default.zone"
				defaultZones = append(defaultZones, roundTrip(oid, o.data, release,
					meta.DecodeDefaultSystemMetaObjInfo, meta.DefaultSystemMetaObjInfo.Encode).DefaultID)
			case oid == meta.DefaultRealmOID():
				kind = "default.realm"
				roundTrip(oid, o.data, release, meta.DecodeDefaultSystemMetaObjInfo, meta.DefaultSystemMetaObjInfo.Encode)
			case strings.HasPrefix(oid, "realms_names."):
				kind = "realms_names"
				roundTrip(oid, o.data, release, meta.DecodeNameToID, meta.NameToID.Encode)
			case strings.HasPrefix(oid, "realms."):
				kind = "realms"
				realm := roundTrip(oid, o.data, release, meta.DecodeRealm, meta.Realm.Encode)
				Expect(meta.RealmOID(realm.ID)).To(Equal(oid))
			case strings.HasPrefix(oid, "periods.") && strings.HasSuffix(oid, ".latest_epoch"):
				kind = "periods latest_epoch"
				roundTrip(oid, o.data, release, meta.DecodePeriodLatestEpochInfo, meta.PeriodLatestEpochInfo.Encode)
			case strings.HasPrefix(oid, "periods."):
				kind = "periods"
				p := roundTrip(oid, o.data, release, meta.DecodePeriod, meta.Period.Encode)
				Expect(meta.PeriodOID(p.ID, p.Epoch)).To(Equal(oid))
			default:
				Fail("no decoder for root pool object " + oid)
			}
			t[kind]++
			objVersion(t, kind, oid, o.xattrs, release)
			expectXattrNames(oid, o.xattrs, meta.AttrObjVersion)
		}

		Expect(zoneIDs).To(ConsistOf(zone.ID))
		Expect(zoneNames).To(ConsistOf(zone.ID))
		Expect(defaultZones).To(ConsistOf(zone.ID))
		Expect(zoneGroupIDs).To(HaveLen(1))
		Expect(zoneGroupNames).To(ConsistOf(zoneGroupIDs))
		Expect(defaultZoneGroups).To(ConsistOf(zoneGroupIDs))

		// populate.sh adds a cloud-s3 storage class, so the round trip above
		// covered a placement tier, which Squid and Tentacle encode at
		// different versions.
		zg := zoneGroupOf(ctx)
		Expect(zoneGroupIDs).To(ConsistOf(zg.ID))
		var tiers []string
		for _, name := range slices.Sorted(maps.Keys(zg.PlacementTargets)) {
			for class, tier := range zg.PlacementTargets[name].TierTargets {
				Expect(tier.TierType).To(Equal(meta.TierTypeCloudS3), "%s/%s tier type", name, class)
				tiers = append(tiers, name+"/"+class)
			}
		}
		Expect(tiers).NotTo(BeEmpty(), "zonegroup %s has no tier_targets; populate.sh adds one", zg.Name)
		note("zonegroup %s tier targets: %v", zg.Name, tiers)
		t["zonegroup tier target"] += len(tiers)
	})

	It("round-trips every user object and its xattrs", func(ctx SpecContext) {
		t := newTally("users")
		uids := open(ctx, zone.UserUIDPool)
		wantUsers := map[string]gate.User{}
		for _, u := range m.Users {
			wantUsers[u.ID()] = u
		}
		var gotUsers, gotBucketLists []string
		for _, oid := range listObjects(ctx, uids) {
			o := readObject(ctx, uids, oid)
			if owner, ok := strings.CutSuffix(oid, ".buckets"); ok {
				expectXattrNames(oid, o.xattrs)
				Expect(o.data).To(BeEmpty(), "%s holds data", oid)
				gotBucketLists = append(gotBucketLists, owner)
				continue
			}
			uo := roundTrip(zone.UserUIDPool.NS+"/"+oid, o.data, release, meta.DecodeUserObject, meta.UserObject.Encode)
			t[zone.UserUIDPool.String()+" user"]++
			Expect(string(uo.UID)).To(Equal(oid))
			Expect(uo.Info.UserID.String()).To(Equal(oid))
			objVersion(t, zone.UserUIDPool.String()+" user", oid, o.xattrs, release)
			expectXattrNames(oid, o.xattrs, meta.AttrObjVersion)
			gotUsers = append(gotUsers, oid)
			if u, ok := wantUsers[oid]; ok {
				Expect(uo.Info.AccessKeys).To(HaveKey(u.AccessKey))
				Expect(uo.Info.AccessKeys[u.AccessKey].Secret).To(Equal(u.SecretKey))
			}
		}
		Expect(gotUsers).To(ConsistOf(slices.Collect(maps.Keys(wantUsers))))
		Expect(gotBucketLists).To(ConsistOf(slices.Collect(maps.Keys(wantUsers))))

		keys := open(ctx, zone.UserKeysPool)
		var gotKeys []string
		for _, oid := range listObjects(ctx, keys) {
			o := readObject(ctx, keys, oid)
			uid := roundTrip(zone.UserKeysPool.NS+"/"+oid, o.data, release, meta.DecodeUID, meta.UID.Encode)
			t[zone.UserKeysPool.String()+" uid"]++
			expectXattrNames(oid, o.xattrs)
			gotKeys = append(gotKeys, oid)
			for _, u := range m.Users {
				if u.AccessKey == oid {
					Expect(string(uid)).To(Equal(u.ID()), "the user access key %s names", oid)
				}
			}
		}
		var wantKeys []string
		for _, u := range m.Users {
			wantKeys = append(wantKeys, u.AccessKey)
		}
		Expect(gotKeys).To(ConsistOf(wantKeys))

		for _, p := range []meta.Pool{zone.UserEmailPool, zone.UserSwiftPool} {
			h := open(ctx, p)
			oids := listObjects(ctx, h)
			if len(oids) == 0 {
				note("%s is empty: populate.sh sets no email or swift key", p)
			}
			for _, oid := range oids {
				o := readObject(ctx, h, oid)
				roundTrip(p.NS+"/"+oid, o.data, release, meta.DecodeUID, meta.UID.Encode)
				t[p.String()+" uid"]++
				expectXattrNames(oid, o.xattrs)
			}
		}
	})

	It("round-trips every user's bucket list through cls_user", func(ctx SpecContext) {
		t := newTally("user bucket lists")
		uids := open(ctx, zone.UserUIDPool)
		var counted int
		for _, u := range m.Users {
			oid := u.ID() + ".buckets"
			op := radosclient.NewReadOp()
			hdr := op.Exec("user", "get_header", encodeRequest(user.GetHeaderOp{}.Encode, release))
			lst := op.Exec("user", "list_buckets",
				encodeRequest(user.ListBucketsOp{MaxEntries: omapPage}.Encode, release))
			Expect(uids.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed(), oid)

			// The class encodes its replies with the OSD's own encoders, so
			// the reply bytes are what the release's encoder writes.
			h := roundTrip(oid+" get_header reply", replyBytes(hdr, oid), release,
				user.DecodeGetHeaderRet, user.GetHeaderRet.Encode).Header
			t["cls_user get_header reply"]++
			ret := roundTrip(oid+" list_buckets reply", replyBytes(lst, oid), release,
				user.DecodeListBucketsRet, user.ListBucketsRet.Encode)
			t["cls_user list_buckets reply"]++
			Expect(ret.Truncated).To(BeFalse())

			// The omap values are what the class stored. The omap header is
			// checked only through the get_header reply above: radosclient
			// has no omap_get_header step.
			vals := omapValues(ctx, uids, oid)
			Expect(slices.Sorted(maps.Keys(vals))).To(HaveLen(len(ret.Entries)))
			var entries, objects, bytesTotal uint64
			var names []string
			for i, k := range slices.Sorted(maps.Keys(vals)) {
				be := roundTrip(oid+" omap "+k, vals[k], release, user.DecodeBucketEntry, user.BucketEntry.Encode)
				t[zone.UserUIDPool.String()+" cls_user_bucket_entry"]++
				Expect(be.Bucket.Name).To(Equal(k))
				Expect(ret.Entries[i]).To(Equal(be), "listed entry %d of %s", i, oid)
				Expect(be.UserStatsSync).To(BeTrue(), "%s: %s was not synced", oid, k)
				names = append(names, k)
				b, ok := m.Bucket(k)
				Expect(ok).To(BeTrue(), "%s lists unknown bucket %s", oid, k)
				Expect(be.Bucket.BucketID).To(Equal(b.ID))
				Expect(be.Bucket.Marker).To(Equal(b.Marker))
				var count, size uint64
				for _, obj := range m.ObjectsIn(k) {
					count++
					size += obj.Size
					counted++
				}
				Expect(be.Count).To(Equal(count), "%s object count", k)
				Expect(be.Size).To(Equal(size), "%s size", k)
				entries++
				objects += count
				bytesTotal += size
			}
			var owned []string
			for _, b := range m.Buckets {
				if b.Owner == u.ID() {
					owned = append(owned, b.Name)
				}
			}
			Expect(names).To(ConsistOf(owned))
			Expect(entries).To(BeNumerically("==", len(owned)))
			Expect(h.Stats.TotalEntries).To(Equal(objects), "%s header entries", oid)
			Expect(h.Stats.TotalBytes).To(Equal(bytesTotal), "%s header bytes", oid)
			Expect(h.LastStatsSync.IsZero()).To(BeFalse(), "%s was never synced", oid)
		}
		Expect(counted).To(Equal(len(m.Objects)), "objects counted through the users' bucket lists")
	})

	It("round-trips bucket entry points and instances with their xattrs", func(ctx SpecContext) {
		t := newTally("buckets")
		root := open(ctx, zone.DomainRoot)
		entryPoints := map[string]meta.BucketEntryPoint{}
		instances := map[string]meta.BucketInfo{}
		for _, oid := range listObjects(ctx, root) {
			o := readObject(ctx, root, oid)
			if strings.HasPrefix(oid, ".bucket.meta.") {
				kind := zone.DomainRoot.String() + " bucket instance"
				bi := roundTrip(oid, o.data, release, meta.DecodeBucketInfo, meta.BucketInfo.Encode)
				t[kind]++
				Expect(bi.Bucket.InstanceOID()).To(Equal(oid))
				instances[bi.Bucket.ID] = bi
				objVersion(t, kind, oid, o.xattrs, release)
				raw, ok := o.xattrs[meta.AttrACL]
				Expect(ok).To(BeTrue(), "%s has no ACL", oid)
				p := roundTrip(oid+" "+meta.AttrACL, raw, release, acl.DecodePolicy, acl.Policy.Encode)
				t[kind+" "+meta.AttrACL]++
				Expect(p.Owner.ID).To(Equal(bi.Owner.String()))
				expectXattrNames(oid, o.xattrs, meta.AttrObjVersion, meta.AttrACL)
				continue
			}
			kind := zone.DomainRoot.String() + " bucket entry point"
			ep := roundTrip(oid, o.data, release, meta.DecodeBucketEntryPoint, meta.BucketEntryPoint.Encode)
			t[kind]++
			Expect(ep.Bucket.EntryPointOID()).To(Equal(oid))
			Expect(ep.HasBucketInfo).To(BeFalse())
			entryPoints[oid] = ep
			objVersion(t, kind, oid, o.xattrs, release)
			expectXattrNames(oid, o.xattrs, meta.AttrObjVersion)
		}

		Expect(entryPoints).To(HaveLen(len(m.Buckets)))
		Expect(instances).To(HaveLen(len(m.Buckets)))
		for _, b := range m.Buckets {
			ep, ok := entryPoints[b.EntryPointKey()]
			Expect(ok).To(BeTrue(), "no entry point for %s", b.EntryPointKey())
			Expect(ep.Bucket.ID).To(Equal(b.ID))
			Expect(ep.Bucket.Marker).To(Equal(b.Marker))
			Expect(ep.Owner.String()).To(Equal(b.Owner))
			Expect(ep.Linked).To(BeTrue())
			bi, ok := instances[b.ID]
			Expect(ok).To(BeTrue(), "no instance for %s", b.ID)
			Expect(bi.Bucket.Tenant).To(Equal(b.Tenant()))
			Expect(bi.Owner.String()).To(Equal(b.Owner))
			Expect(bi.Layout.Current.Layout.Normal.NumShards).To(Equal(b.NumShards))
		}
	})

	It("decodes every index shard header and entry", func(ctx SpecContext) {
		t := newTally("bucket index " + m.Pools.Index)
		index := open(ctx, meta.Pool{Name: m.Pools.Index})
		data := open(ctx, meta.Pool{Name: m.Pools.Data})
		var indexed int
		for _, b := range m.Buckets {
			bi := bucketInfo(ctx, b)
			merged := map[string]rgw.DirEntry{}
			for shard := range b.NumShards {
				oid := bi.IndexShardOID(bi.Layout.Current, shard)
				op := radosclient.NewReadOp()
				hdr := op.Exec(rgw.Class, "bucket_list", encodeRequest(rgw.ListOp{}.Encode, release))
				lst := op.Exec(rgw.Class, "bucket_list",
					encodeRequest(rgw.ListOp{NumEntries: omapPage}.Encode, release))
				Expect(index.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed(), oid)

				// The class re-encodes the header and entries into its reply
				// with the OSD's encoders, so the reply is byte-exact for the
				// release.
				header := roundTrip(oid+" header reply", replyBytes(hdr, oid), release, rgw.DecodeListRet, rgw.ListRet.Encode)
				t["rgw_cls_list_ret header reply"]++
				Expect(header.Dir.Entries).To(BeEmpty())
				listing := roundTrip(oid+" bucket_list reply", replyBytes(lst, oid), release, rgw.DecodeListRet, rgw.ListRet.Encode)
				t["rgw_cls_list_ret listing reply"]++
				Expect(listing.IsTruncated).To(BeFalse())
				Expect(listing.Dir.Header).To(Equal(header.Dir.Header))
				Expect(header.Dir.Header.NewInstance.ReshardStatus).To(Equal(rgw.ReshardNone))

				// The omap values are what the class stored, byte for byte. The
				// shard's omap header is checked only through the class
				// replies above: radosclient has no omap_get_header step.
				vals := omapValues(ctx, index, oid)
				Expect(slices.Sorted(maps.Keys(vals))).To(Equal(slices.Sorted(maps.Keys(listing.Dir.Entries))),
					"%s omap keys against its listing", oid)
				stats := map[uint8]rgw.CategoryStats{}
				for k, raw := range vals {
					en := roundTrip(oid+" omap "+k, raw, release, rgw.DecodeDirEntry, rgw.DirEntry.Encode)
					t["rgw_bucket_dir_entry omap value"]++
					Expect(listing.Dir.Entries[k]).To(Equal(en), "%s entry %s", oid, k)
					Expect(merged).NotTo(HaveKey(k), "index key %q on two shards", k)
					merged[k] = en
					if en.Exists {
						s := stats[en.Meta.Category]
						s.NumEntries++
						s.TotalSize += en.Meta.AccountedSize
						s.TotalSizeRounded += (en.Meta.AccountedSize + 4095) &^ 4095
						s.ActualSize += en.Meta.Size
						stats[en.Meta.Category] = s
					}
				}
				for c, s := range header.Dir.Header.Stats {
					if s == (rgw.CategoryStats{}) {
						continue
					}
					Expect(stats).To(HaveKeyWithValue(c, s), "%s category %d stats against its entries", oid, c)
				}
				for c, s := range stats {
					Expect(header.Dir.Header.Stats).To(HaveKeyWithValue(c, s), "%s category %d stats", oid, c)
				}
				t["index shard"]++
			}

			want := map[string]gate.Object{}
			for _, o := range m.ObjectsIn(b.Name) {
				want[meta.ObjKey{Name: o.Key}.IndexKeyName()] = o
			}
			Expect(slices.Sorted(maps.Keys(merged))).To(Equal(slices.Sorted(maps.Keys(want))), "%s index keys", b.Name)
			indexed += len(merged)
			for k, en := range merged {
				o := want[k]
				Expect(en.Key.Name).To(Equal(k), "the entry under %s names the index key", k)
				Expect(en.Key.Instance).To(BeEmpty())
				Expect(en.Exists).To(BeTrue(), "%s exists", o.Key)
				Expect(en.Meta.Size).To(Equal(o.Size), "%s size", o.Key)
				Expect(en.Meta.AccountedSize).To(Equal(o.Size), "%s accounted size", o.Key)
				Expect(en.Meta.Owner).To(Equal(b.Owner))
				Expect(en.PendingMap).To(BeEmpty(), "%s has pending ops", o.Key)
				if o.ContentType != "" {
					Expect(en.Meta.ContentType).To(Equal(o.ContentType))
				}
				if o.Multipart {
					Expect(en.Meta.ETag).To(MatchRegexp(multipartETag.String()), "%s etag", o.Key)
				} else {
					Expect(en.Meta.ETag).To(MatchRegexp(plainETag.String()), "%s etag", o.Key)
				}
				oid, loc := headOID(b.Marker, o.Key)
				p := data
				if loc != "" {
					p = data.WithLocator(loc)
				}
				Expect(en.Locator).To(Equal(strings.TrimPrefix(loc, b.Marker+"_")), "%s locator", o.Key)
				xs := readObject(ctx, p, oid).xattrs
				Expect(etagString(xs[meta.AttrETag])).To(Equal(en.Meta.ETag), "%s head etag against its index entry", o.Key)
			}
		}
		Expect(indexed).To(Equal(len(m.Objects)), "index entries across every bucket")
	})

	It("round-trips head object xattrs for every populated object", func(ctx SpecContext) {
		t := newTally("head objects " + m.Pools.Data)
		var checked int
		for _, obj := range m.Objects {
			b, ok := m.Bucket(obj.Bucket)
			Expect(ok).To(BeTrue())
			o, _, _, oid := head(ctx, b, obj.Key)
			x := o.xattrs
			note("%s head xattrs: %v", obj.Key, slices.Sorted(maps.Keys(x)))

			man := roundTrip(oid+" "+meta.AttrManifest, x[meta.AttrManifest], release, meta.DecodeManifest, meta.Manifest.Encode)
			t[meta.AttrManifest]++
			Expect(man.ObjSize).To(Equal(obj.Size), "%s manifest size", obj.Key)
			Expect(man.Obj.Key.Name).To(Equal(obj.Key))
			Expect(man.Obj.Bucket.Marker).To(Equal(b.Marker))
			Expect(uint64(len(o.data))).To(Equal(min(man.HeadSize, obj.Size)), "%s head data", obj.Key)

			raw, ok := x[meta.AttrACL]
			Expect(ok).To(BeTrue(), "%s has no ACL", obj.Key)
			p := roundTrip(oid+" "+meta.AttrACL, raw, release, acl.DecodePolicy, acl.Policy.Encode)
			t[meta.AttrACL]++
			Expect(p.Owner.ID).To(Equal(b.Owner))

			Expect(x).To(HaveKey(meta.AttrETag))
			etag := etagString(x[meta.AttrETag])
			if obj.Multipart {
				Expect(etag).To(MatchRegexp(multipartETag.String()), "%s etag", obj.Key)
			} else {
				Expect(etag).To(MatchRegexp(plainETag.String()), "%s etag", obj.Key)
			}
			t[meta.AttrETag]++

			Expect(x).To(HaveKey(meta.AttrIDTag))
			Expect(cString(obj.Key+" idtag", x[meta.AttrIDTag])).NotTo(BeEmpty())
			t[meta.AttrIDTag]++
			Expect(x).To(HaveKey(meta.AttrTailTag))
			Expect(x[meta.AttrTailTag]).To(Equal(x[meta.AttrIDTag]), "%s tail_tag", obj.Key)
			t[meta.AttrTailTag]++

			Expect(x).To(HaveKey(meta.AttrPGVer))
			pgVer := xattrU64(obj.Key+" pg_ver", x[meta.AttrPGVer])
			t[meta.AttrPGVer]++
			Expect(x).To(HaveKey(meta.AttrSourceZone))
			sourceZone := xattrU32(obj.Key+" source_zone", x[meta.AttrSourceZone])
			t[meta.AttrSourceZone]++
			note("%s pg_ver %d, source_zone %d", obj.Key, pgVer, sourceZone)

			if obj.ContentType != "" {
				Expect(x).To(HaveKey(meta.AttrContentType), "%s lost its content type", obj.Key)
				Expect(cString(obj.Key+" content_type", x[meta.AttrContentType])).To(Equal(obj.ContentType))
				t[meta.AttrContentType]++
			}
			for k, v := range obj.Metadata {
				Expect(cString(obj.Key+" "+k, x[meta.AttrPrefix+k])).To(Equal(v))
				t["user metadata"]++
			}
			for name := range x {
				if k, ok := strings.CutPrefix(name, meta.AttrPrefix); ok && strings.HasPrefix(name, meta.AttrMetaPrefix) {
					Expect(obj.Metadata).To(HaveKey(k), "%s carries metadata it was not given", obj.Key)
				}
			}
			t["head object"]++
			checked++
		}
		Expect(checked).To(Equal(len(m.Objects)), "heads checked")
	})

	It("classifies every data pool object and checks the non-heads", func(ctx SpecContext) {
		t := newTally("data pool classification")
		heads := map[string]bool{}
		// Every multipart upload's part heads carry the attrs radosgw wrote
		// with each part. The only other objects with xattrs a populated
		// cluster may hold are part heads of an earlier upload of the same
		// key, which a populate rerun leaves until gc removes them.
		currentParts := map[string]bool{}
		var staleMultipart []string
		wantParts := 0
		for _, obj := range m.Objects {
			b, ok := m.Bucket(obj.Bucket)
			Expect(ok).To(BeTrue())
			oid, _ := headOID(b.Marker, obj.Key)
			heads[oid] = false
			if obj.Multipart {
				n, parts := partHeads(ctx, b, obj.Key)
				wantParts += n
				for _, p := range parts {
					currentParts[p] = false
				}
				staleMultipart = append(staleMultipart, b.Marker+"__"+meta.NSMultipart+"_"+obj.Key+".")
			}
		}
		Expect(heads).To(HaveLen(len(m.Objects)), "distinct head names")
		Expect(currentParts).To(HaveLen(wantParts), "distinct part head names")
		for _, name := range []string{m.Pools.Data, m.Pools.NonEC} {
			pool := open(ctx, meta.Pool{Name: name})
			type listed struct{ oid, locator string }
			var objs []listed
			Expect(pool.ListObjects(ctx, func(oid, locator string) error {
				objs = append(objs, listed{oid, locator})
				return nil
			})).To(Succeed())
			slices.SortFunc(objs, func(a, b listed) int { return strings.Compare(a.oid, b.oid) })
			for _, l := range objs {
				p := pool
				if l.locator != "" {
					p = p.WithLocator(l.locator)
				}
				x := readObject(ctx, p, l.oid).xattrs
				names := slices.Sorted(maps.Keys(x))
				_, olhInfo := x[meta.AttrPrefix+"olh.info"]
				_, olhIDTag := x[meta.AttrPrefix+"olh.idtag"]
				_, isHead := heads[l.oid]
				_, isPart := currentParts[l.oid]
				switch {
				case olhInfo || olhIDTag:
					Fail(fmt.Sprintf("%s/%s is an OLH object in an unversioned bucket: %v", name, l.oid, names))
				case isHead && name == m.Pools.Data:
					heads[l.oid] = true
					t[name+" head"]++
					continue
				}
				Expect(x).NotTo(HaveKey(meta.AttrManifest), "non-head %s/%s has a manifest", name, l.oid)
				Expect(x).NotTo(HaveKey(meta.AttrIDTag), "non-head %s/%s has an idtag", name, l.oid)
				switch {
				case len(x) == 0:
					t[name+" tail without xattrs"]++
				case isPart && name == m.Pools.Data:
					currentParts[l.oid] = true
					t[name+" part head of the current upload"]++
					note("%s/%s xattrs: %v", name, l.oid, names)
				default:
					stale := slices.ContainsFunc(staleMultipart, func(prefix string) bool { return strings.HasPrefix(l.oid, prefix) })
					Expect(stale && name == m.Pools.Data).To(BeTrue(),
						"%s/%s has xattrs %v but is neither a head nor a part head", name, l.oid, names)
					t[name+" part head of an earlier upload, awaiting gc"]++
					note("%s/%s is a part head of an earlier upload: %v", name, l.oid, names)
				}
			}
		}
		for oid, seen := range heads {
			Expect(seen).To(BeTrue(), "head %s is not in %s", oid, m.Pools.Data)
		}
		for oid, seen := range currentParts {
			Expect(seen).To(BeTrue(), "part head %s is not in %s", oid, m.Pools.Data)
		}
	})

	It("finds every tail object the manifest names", func(ctx SpecContext) {
		t := newTally("tail objects " + m.Pools.Data)
		var walked int
		for _, obj := range m.Objects {
			b, ok := m.Bucket(obj.Bucket)
			Expect(ok).To(BeTrue())
			_, man, data, oid := head(ctx, b, obj.Key)
			stripes, err := man.Stripes()
			Expect(err).NotTo(HaveOccurred(), obj.Key)
			walked++
			if obj.Size == 0 {
				Expect(stripes).To(BeEmpty())
				continue
			}
			parts := map[string]bool{}
			wantParts := 0
			if obj.Multipart {
				var names []string
				wantParts, names = partHeads(ctx, b, obj.Key)
				for _, n := range names {
					parts[n] = false
				}
			}
			var sum uint64
			tails := 0
			for i, s := range stripes {
				Expect(s.Ofs).To(Equal(sum), "%s stripe %d offset", obj.Key, i)
				sum += s.Size
				Expect(s.Obj.Bucket.Marker).To(Equal(b.Marker))
				// The populated buckets use the default placement, whose
				// STANDARD class keeps heads and tails in the data pool.
				p := open(ctx, meta.Pool{Name: m.Pools.Data})
				if loc := s.Locator(); loc != "" {
					p = p.WithLocator(loc)
				}
				if s.InHead {
					Expect(s.OID()).To(Equal(oid))
					p = data
				}
				size, ok := statObject(ctx, p, s.OID())
				Expect(ok).To(BeTrue(), "%s stripe %d: %s does not exist", obj.Key, i, s.OID())
				Expect(size).To(Equal(s.LocOfs+s.Size), "%s stripe %d: %s size", obj.Key, i, s.OID())
				if s.InHead {
					continue
				}
				tails++
				t[s.Obj.Key.NS+" tail"]++
				xs := readObject(ctx, p, s.OID()).xattrs
				if _, ok := xs["refcount"]; ok {
					note("%s tail %s carries a refcount", obj.Key, s.OID())
				}
				// A multipart part's first object is the part's head and
				// carries the attrs radosgw wrote with the part.
				if _, ok := parts[s.OID()]; !ok {
					Expect(xs).NotTo(HaveKey(meta.AttrACL), "%s tail %s is not a part head but has an ACL", obj.Key, s.OID())
					continue
				}
				parts[s.OID()] = true
				Expect(xs).To(HaveKey(meta.AttrACL), s.OID())
				roundTrip(s.OID()+" "+meta.AttrACL, xs[meta.AttrACL], release, acl.DecodePolicy, acl.Policy.Encode)
				Expect(xs).To(HaveKey(meta.AttrETag), s.OID())
				Expect(etagString(xs[meta.AttrETag])).To(MatchRegexp(plainETag.String()), "%s etag", s.OID())
				Expect(xs).To(HaveKey(meta.AttrPGVer), s.OID())
				xattrU64(s.OID()+" pg_ver", xs[meta.AttrPGVer])
				Expect(xs).To(HaveKey(meta.AttrSourceZone), s.OID())
				xattrU32(s.OID()+" source_zone", xs[meta.AttrSourceZone])
				t["part head"]++
			}
			checkedParts := 0
			for _, seen := range parts {
				if seen {
					checkedParts++
				}
			}
			Expect(checkedParts).To(Equal(wantParts), "%s part heads checked against its etag's part count", obj.Key)
			Expect(sum).To(Equal(obj.Size), "%s stripe sizes", obj.Key)
			note("%s: %d stripes, %d outside the head", obj.Key, len(stripes), tails)
		}
		Expect(walked).To(Equal(len(m.Objects)), "manifests walked")
	})

	It("agrees with radosgw-admin", func(ctx SpecContext) {
		t := newTally("radosgw-admin")

		// metadata get prints the object version, then the data, which for
		// users and instances carries the xattrs as {key, base64 val}.
		type metadataGet struct {
			Ver struct {
				Tag string `json:"tag"`
				Ver uint64 `json:"ver"`
			} `json:"ver"`
			Data json.RawMessage `json:"data"`
		}
		get := func(key string) (metadataGet, map[string]any) {
			var out metadataGet
			Expect(json.Unmarshal(admin(ctx, "metadata", "get", key), &out)).To(Succeed())
			data, ok := canonical(out.Data).(map[string]any)
			Expect(ok).To(BeTrue())
			return out, data
		}
		expectVer := func(key string, got metadataGet, xattrs map[string][]byte) {
			GinkgoHelper()
			d := denc.NewDecoder(xattrs[meta.AttrObjVersion])
			v := version.DecodeObjVersion(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(got.Ver.Ver).To(Equal(v.Ver), "%s ver", key)
			Expect(got.Ver.Tag).To(Equal(v.Tag), "%s tag", key)
		}
		// attrs is the xattrs as metadata get prints them: every xattr but
		// the version, which it prints as ver.
		attrs := func(xattrs map[string][]byte) any {
			out := []map[string]string{}
			for _, k := range slices.Sorted(maps.Keys(xattrs)) {
				if k != meta.AttrObjVersion {
					out = append(out, map[string]string{"key": k, "val": base64.StdEncoding.EncodeToString(xattrs[k])})
				}
			}
			b, err := json.Marshal(out)
			Expect(err).NotTo(HaveOccurred())
			return canonical(b)
		}

		uids := open(ctx, zone.UserUIDPool)
		for _, u := range m.Users {
			key := "user:" + u.ID()
			o := readObject(ctx, uids, u.ID())
			d := denc.NewDecoder(o.data)
			info := meta.DecodeUserObject(d).Info
			Expect(d.Err()).NotTo(HaveOccurred())
			got, data := get(key)
			expectVer(key, got, o.xattrs)
			Expect(jsonDiff(key+" attrs", attrs(o.xattrs), data["attrs"])).To(BeEmpty())
			delete(data, "attrs")
			expectSameJSON(key, info, data)
			t["metadata get user"]++

			// user stats prints the cls_user header as RGWStorageStats.
			args := []string{"user", "stats", "--uid", u.UID}
			if u.Tenant != "" {
				args = append(args, "--tenant", u.Tenant)
			}
			var stats struct {
				Stats struct {
					Size       uint64 `json:"size"`
					SizeActual uint64 `json:"size_actual"`
					NumObjects uint64 `json:"num_objects"`
				} `json:"stats"`
				LastStatsSync   meta.Time `json:"last_stats_sync"`
				LastStatsUpdate meta.Time `json:"last_stats_update"`
			}
			Expect(json.Unmarshal(admin(ctx, args...), &stats)).To(Succeed())
			op := radosclient.NewReadOp()
			hres := user.GetHeader(op, release)
			Expect(uids.Read(ctx, u.ID()+".buckets", op, radosclient.OpFlagNone)).Error().To(Succeed())
			h, err := hres.Header()
			Expect(err).NotTo(HaveOccurred())
			Expect(stats.Stats.Size).To(Equal(h.Stats.TotalBytes), "%s size", u.ID())
			Expect(stats.Stats.SizeActual).To(Equal(h.Stats.TotalBytesRounded), "%s size_actual", u.ID())
			Expect(stats.Stats.NumObjects).To(Equal(h.Stats.TotalEntries), "%s num_objects", u.ID())
			Expect(stats.LastStatsSync.Time.Equal(h.LastStatsSync.Truncate(1000))).To(BeTrue(),
				"%s last_stats_sync %s, header %s", u.ID(), stats.LastStatsSync, h.LastStatsSync)
			Expect(stats.LastStatsUpdate.Time.Equal(h.LastStatsUpdate.Truncate(1000))).To(BeTrue(),
				"%s last_stats_update %s, header %s", u.ID(), stats.LastStatsUpdate, h.LastStatsUpdate)
			t["user stats"]++
		}

		root := open(ctx, zone.DomainRoot)
		index := open(ctx, meta.Pool{Name: m.Pools.Index})
		for _, b := range m.Buckets {
			key := "bucket:" + b.EntryPointKey()
			o := readObject(ctx, root, b.EntryPointKey())
			d := denc.NewDecoder(o.data)
			ep := meta.DecodeBucketEntryPoint(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			got, data := get(key)
			expectVer(key, got, o.xattrs)
			expectSameJSON(key, ep, data)
			t["metadata get bucket"]++

			id := meta.BucketID{Tenant: b.Tenant(), Name: b.Name, ID: b.ID}
			key = "bucket.instance:" + b.EntryPointKey() + ":" + b.ID
			o = readObject(ctx, root, id.InstanceOID())
			d = denc.NewDecoder(o.data)
			bi := meta.DecodeBucketInfo(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			got, data = get(key)
			expectVer(key, got, o.xattrs)
			Expect(jsonDiff(key+" attrs", attrs(o.xattrs), data["attrs"])).To(BeEmpty())
			expectSameJSON(key+" bucket_info", bi, data["bucket_info"])
			t["metadata get bucket.instance"]++

			// bi list prints every index entry as it decodes it.
			var listed []struct {
				Type  string          `json:"type"`
				Idx   string          `json:"idx"`
				Entry json.RawMessage `json:"entry"`
			}
			Expect(json.Unmarshal(admin(ctx, "bi", "list", "--bucket", b.EntryPointKey()), &listed)).To(Succeed())
			entries := map[string]rgw.DirEntry{}
			for shard := range b.NumShards {
				for k, raw := range omapValues(ctx, index, bi.IndexShardOID(bi.Layout.Current, shard)) {
					d := denc.NewDecoder(raw)
					entries[k] = rgw.DecodeDirEntry(d)
					Expect(d.Err()).NotTo(HaveOccurred())
				}
			}
			Expect(listed).To(HaveLen(len(entries)), "bi list %s", b.Name)
			for _, l := range listed {
				Expect(l.Type).To(Equal("plain"))
				en, ok := entries[l.Idx]
				Expect(ok).To(BeTrue(), "bi list %s names %s", b.Name, l.Idx)
				expectSameJSON("bi list "+b.Name+" "+l.Idx, en, canonical(l.Entry))
				t["bi list entry"]++
			}
		}

		// zone get and zonegroup get print the root pool objects.
		var zoneAbsent, zoneGroupAbsent []string
		if release == denc.Squid {
			zoneAbsent, zoneGroupAbsent = squidZoneDumpLacks, squidZoneGroupDumpLacks
		}
		expectSameJSON("zone get", zone, canonical(admin(ctx, "zone", "get")), zoneAbsent...)
		t["zone get"]++
		zg := zoneGroupOf(ctx)
		root = open(ctx, meta.Pool{Name: m.Pools.Root})
		d := denc.NewDecoder(readObject(ctx, root, meta.ZoneGroupNameOID(zg.Name)).data)
		Expect(meta.DecodeNameToID(d).ObjID).To(Equal(zg.ID), "zonegroups_names.%s", zg.Name)
		Expect(d.Err()).NotTo(HaveOccurred())
		expectSameJSON("zonegroup get", zg, canonical(admin(ctx, "zonegroup", "get", "--rgw-zonegroup", zg.Name)),
			zoneGroupAbsent...)
		t["zonegroup get"]++
	})
})
