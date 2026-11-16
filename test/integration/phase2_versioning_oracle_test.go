package integration_test

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The versioning gate's oracle: what a versions listing and radosgw-admin's
// bi list of one bucket must agree on once the bucket is quiescent, taken
// from cls_rgw's index format rather than from rgw-go's own packages, so
// that it holds whatever order racing writers landed in.

// The flags of a bucket index entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L371-L377 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L371-L377 at v20.2.4).
const (
	dirFlagVer          = 0x1
	dirFlagCurrent      = 0x2
	dirFlagDeleteMarker = 0x4
	dirFlagVerMarker    = 0x8
)

// listedVersion is one Version or DeleteMarker of a ListObjectVersions
// answer.
type listedVersion struct {
	Key, VersionID string
	Latest, Marker bool
	ETag           string
	Size           int64
}

// versionsPage is one ListVersionsResult: its versions and delete markers
// in the order the document gives them, which the SDK's two lists lose, and
// where the next page starts.
type versionsPage struct {
	Versions                           []listedVersion
	Truncated                          bool
	NextKeyMarker, NextVersionIDMarker string
}

// parseVersionsPage decodes a ListVersionsResult.
func parseVersionsPage(body []byte) (versionsPage, error) {
	var doc struct {
		XMLName             xml.Name `xml:"ListVersionsResult"`
		IsTruncated         bool     `xml:"IsTruncated"`
		NextKeyMarker       string   `xml:"NextKeyMarker"`
		NextVersionIDMarker string   `xml:"NextVersionIdMarker"`
		Children            []struct {
			XMLName   xml.Name
			Key       string `xml:"Key"`
			VersionID string `xml:"VersionId"`
			IsLatest  bool   `xml:"IsLatest"`
			ETag      string `xml:"ETag"`
			Size      int64  `xml:"Size"`
		} `xml:",any"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return versionsPage{}, fmt.Errorf("decoding a ListVersionsResult: %w", err)
	}
	p := versionsPage{Truncated: doc.IsTruncated, NextKeyMarker: doc.NextKeyMarker, NextVersionIDMarker: doc.NextVersionIDMarker}
	for _, c := range doc.Children {
		if c.XMLName.Local != "Version" && c.XMLName.Local != "DeleteMarker" {
			continue
		}
		p.Versions = append(p.Versions, listedVersion{
			Key: c.Key, VersionID: c.VersionID, Latest: c.IsLatest, Marker: c.XMLName.Local == "DeleteMarker",
			ETag: strings.Trim(c.ETag, `"`), Size: c.Size,
		})
	}
	return p, nil
}

// indexInstance is the instance a listed version id names in the bucket
// index: the null version's is empty.
func indexInstance(versionID string) string {
	if versionID == "null" {
		return ""
	}
	return versionID
}

// biEntry is one entry radosgw-admin bi list prints, rgw_cls_bi_entry::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.cc#L365-L384 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.cc#L385-L405 at v20.2.4).
type biEntry struct {
	Type  string          `json:"type"`
	Entry json.RawMessage `json:"entry"`
}

// biDir is a plain or an instance entry, rgw_bucket_dir_entry::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.cc#L269-L281 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.cc#L269-L281 at v20.2.4).
type biDir struct {
	Name           string            `json:"name"`
	Instance       string            `json:"instance"`
	Exists         bool              `json:"exists"`
	Flags          uint16            `json:"flags"`
	PendingMap     []json.RawMessage `json:"pending_map"`
	VersionedEpoch uint64            `json:"versioned_epoch"`
	Meta           struct {
		Size  uint64 `json:"size"`
		ETag  string `json:"etag"`
		Owner string `json:"owner"`
	} `json:"meta"`
}

// biOLH is an olh entry, rgw_bucket_olh_entry::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.cc#L434-L443 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.cc#L463-L472 at v20.2.4).
type biOLH struct {
	Key struct {
		Name     string `json:"name"`
		Instance string `json:"instance"`
	} `json:"key"`
	DeleteMarker   bool              `json:"delete_marker"`
	Epoch          uint64            `json:"epoch"`
	PendingLog     []json.RawMessage `json:"pending_log"`
	Exists         bool              `json:"exists"`
	PendingRemoval bool              `json:"pending_removal"`
}

// bucketIndex is a bucket's bi list by entry type; Other holds the type of
// each entry of another, such as Tentacle's reshard log.
type bucketIndex struct {
	Plain, Instance []biDir
	OLH             []biOLH
	Other           []string
}

// decodeIndex sorts bi list's entries by type and decodes each.
func decodeIndex(entries []biEntry) (bucketIndex, error) {
	var ix bucketIndex
	for _, e := range entries {
		var err error
		switch e.Type {
		case "plain":
			var d biDir
			err = json.Unmarshal(e.Entry, &d)
			ix.Plain = append(ix.Plain, d)
		case "instance":
			var d biDir
			err = json.Unmarshal(e.Entry, &d)
			ix.Instance = append(ix.Instance, d)
		case "olh":
			var o biOLH
			err = json.Unmarshal(e.Entry, &o)
			ix.OLH = append(ix.OLH, o)
		default:
			ix.Other = append(ix.Other, e.Type)
		}
		if err != nil {
			return bucketIndex{}, fmt.Errorf("decoding bi list's %s entry %s: %w", e.Type, e.Entry, err)
		}
	}
	return ix, nil
}

// byKey groups ls by key, each key's versions in listing order, and returns
// the keys in listing order.
func byKey(ls []listedVersion) ([]string, map[string][]listedVersion) {
	var keys []string
	m := map[string][]listedVersion{}
	for _, v := range ls {
		if _, ok := m[v.Key]; !ok {
			keys = append(keys, v.Key)
		}
		m[v.Key] = append(m[v.Key], v)
	}
	return keys, m
}

// wantFlags is the flags cls_rgw gives a listed version's list and instance
// entries: FLAG_VER, FLAG_CURRENT on the version linked last, and
// FLAG_DELETE_MARKER on a marker (init_as_delete_marker and
// BIVerObjEntry::write,
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1350-L1356 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1410-L1427 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1476-L1482 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1537-L1554 at v20.2.4).
func wantFlags(v listedVersion) uint16 {
	f := uint16(dirFlagVer)
	if v.Latest {
		f |= dirFlagCurrent
	}
	if v.Marker {
		f |= dirFlagDeleteMarker
	}
	return f
}

// entryOf is the entry of dirs for v: its instance and whether it is a
// delete marker, which tells a null delete marker from the null version.
func entryOf(dirs []biDir, v listedVersion) []biDir {
	var out []biDir
	for _, d := range dirs {
		if d.Name == v.Key && d.Instance == indexInstance(v.VersionID) && d.Flags&dirFlagVer != 0 &&
			(d.Flags&dirFlagDeleteMarker != 0) == v.Marker {
			out = append(out, d)
		}
	}
	return out
}

// indexFindings is what ix contradicts of ls, the bucket's whole versions
// listing, once no write is in flight: each listed version has one list
// entry and one instance entry, with its flags and one versioned epoch,
// which falls along each key's listing; each key has its version marker and
// one OLH entry, naming the version listed latest at an epoch no lower than
// its, with no pending log; no entry has a pending operation; and nothing
// else is in the index. An empty result is an index consistent with ls.
func indexFindings(ix bucketIndex, ls []listedVersion) []string {
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
	for _, typ := range ix.Other {
		add("an entry of type %q", typ)
	}
	for _, d := range slices.Concat(ix.Plain, ix.Instance) {
		if len(d.PendingMap) > 0 {
			add("%s[%s] has pending operations", d.Name, d.Instance)
		}
	}
	keys, versions := byKey(ls)
	for _, k := range keys {
		vs := versions[k]
		var markers, lists, insts int
		for _, d := range ix.Plain {
			switch {
			case d.Name != k:
			case d.Flags == dirFlagVerMarker && d.Instance == "":
				markers++
			case d.Flags&dirFlagVer != 0:
				lists++
			default:
				add("%s has a plain entry %q with flags %#x", k, d.Instance, d.Flags)
			}
		}
		for _, d := range ix.Instance {
			if d.Name == k {
				insts++
			}
		}
		if markers != 1 {
			add("%s has %d version markers", k, markers)
		}
		if lists != len(vs) || insts != len(vs) {
			add("%s has %d list and %d instance entries for %d listed versions", k, lists, insts, len(vs))
		}
		var prev uint64
		for i, v := range vs {
			if v.Latest != (i == 0) {
				add("%s's version %s is listed %d with IsLatest %t", k, v.VersionID, i, v.Latest)
			}
			l, in := entryOf(ix.Plain, v), entryOf(ix.Instance, v)
			if len(l) != 1 || len(in) != 1 {
				add("%s's version %s has %d list and %d instance entries", k, v.VersionID, len(l), len(in))
				continue
			}
			if l[0].Flags != wantFlags(v) || in[0].Flags != wantFlags(v) {
				add("%s's version %s has flags %#x listed and %#x on its instance, not %#x", k, v.VersionID, l[0].Flags, in[0].Flags, wantFlags(v))
			}
			if l[0].VersionedEpoch != in[0].VersionedEpoch {
				add("%s's version %s has epoch %d listed and %d on its instance", k, v.VersionID, l[0].VersionedEpoch, in[0].VersionedEpoch)
			}
			if i > 0 && l[0].VersionedEpoch >= prev {
				add("%s's version %s at epoch %d is listed after epoch %d", k, v.VersionID, l[0].VersionedEpoch, prev)
			}
			prev = l[0].VersionedEpoch
		}
		var olh []biOLH
		for _, o := range ix.OLH {
			if o.Key.Name == k {
				olh = append(olh, o)
			}
		}
		if len(olh) != 1 {
			add("%s has %d OLH entries", k, len(olh))
			continue
		}
		o, latest := olh[0], vs[0]
		if !o.Exists || o.PendingRemoval || len(o.PendingLog) > 0 {
			add("%s's OLH entry: exists %t, pending removal %t, %d pending log epochs", k, o.Exists, o.PendingRemoval, len(o.PendingLog))
		}
		if o.Key.Instance != indexInstance(latest.VersionID) || o.DeleteMarker != latest.Marker {
			add("%s's OLH entry names %q (marker %t), not the latest %s (marker %t)", k, o.Key.Instance, o.DeleteMarker, latest.VersionID, latest.Marker)
		}
		if l := entryOf(ix.Plain, latest); len(l) == 1 && o.Epoch < l[0].VersionedEpoch {
			add("%s's OLH entry is at epoch %d, below its latest version's %d", k, o.Epoch, l[0].VersionedEpoch)
		}
	}
	for _, d := range slices.Concat(ix.Plain, ix.Instance) {
		if !slices.Contains(keys, d.Name) {
			add("an entry %s[%s] of no listed key", d.Name, d.Instance)
		}
	}
	for _, o := range ix.OLH {
		if !slices.Contains(keys, o.Key.Name) {
			add("an OLH entry %s of no listed key", o.Key.Name)
		}
	}
	return out
}

// versionLabel names v by its key and its place in the key's listing, so
// that two buckets written alike compare though their version ids differ.
func versionLabel(versions map[string][]listedVersion, name, instance string, marker bool) string {
	for i, v := range versions[name] {
		if indexInstance(v.VersionID) == instance && v.Marker == marker {
			return fmt.Sprintf("%s#%d", name, i)
		}
	}
	return name + "#unlisted"
}

// indexShape is ix with each instance named by versionLabel against ls. It
// leaves out write tags, mtimes and index versions, which two buckets
// written alike need not share. It also leaves out owners' display names,
// which biDir does not read. Two buckets that ran the same requests,
// as one user and with the same bodies, have the same shape: epochs, flags,
// sizes, ETags and owners included.
func indexShape(ix bucketIndex, ls []listedVersion) []string {
	_, versions := byKey(ls)
	var out []string
	dir := func(typ string, d biDir) {
		label := d.Name + "#version-marker"
		if d.Flags != dirFlagVerMarker {
			label = versionLabel(versions, d.Name, d.Instance, d.Flags&dirFlagDeleteMarker != 0)
		}
		out = append(out, fmt.Sprintf("%s %s flags=%#x epoch=%d exists=%t size=%d etag=%s owner=%s pending=%d",
			typ, label, d.Flags, d.VersionedEpoch, d.Exists, d.Meta.Size, d.Meta.ETag, d.Meta.Owner, len(d.PendingMap)))
	}
	for _, d := range ix.Plain {
		dir("plain", d)
	}
	for _, d := range ix.Instance {
		dir("instance", d)
	}
	for _, o := range ix.OLH {
		out = append(out, fmt.Sprintf("olh %s -> %s marker=%t epoch=%d exists=%t pending-removal=%t log=%d",
			o.Key.Name, versionLabel(versions, o.Key.Name, o.Key.Instance, o.DeleteMarker), o.DeleteMarker, o.Epoch,
			o.Exists, o.PendingRemoval, len(o.PendingLog)))
	}
	slices.Sort(out)
	return out
}

// listingShape is ls with each version named by its place in its key's
// listing. It keeps ETags and sizes, so two buckets compare only when they
// were written with the same bodies.
func listingShape(ls []listedVersion) []string {
	_, versions := byKey(ls)
	out := make([]string, 0, len(ls))
	for _, v := range ls {
		out = append(out, fmt.Sprintf("%s latest=%t marker=%t etag=%s size=%d",
			versionLabel(versions, v.Key, indexInstance(v.VersionID), v.Marker), v.Latest, v.Marker, v.ETag, v.Size))
	}
	return out
}

// epochs is each listed version's versioned epoch by its listingShape
// label, and each key's OLH epoch under "olh <key>".
func epochs(ix bucketIndex, ls []listedVersion) map[string]uint64 {
	_, versions := byKey(ls)
	out := map[string]uint64{}
	for _, v := range ls {
		if l := entryOf(ix.Plain, v); len(l) == 1 {
			out[versionLabel(versions, v.Key, indexInstance(v.VersionID), v.Marker)] = l[0].VersionedEpoch
		}
	}
	for _, o := range ix.OLH {
		out["olh "+o.Key.Name] = o.Epoch
	}
	return out
}

// olhHead is a key's OLH head as RADOS holds it: the version olh.info
// names, olh.ver, and the names of its pending attrs.
type olhHead struct {
	info    meta.OLHInfo
	ver     string
	pending []string
}

// olhShape is h with the version olh.info names labeled as indexShape
// labels it, and the null version's own instance string kept, which two
// gateways must write alike. It leaves out the target's bucket, which is
// each bucket's own. It counts the pending attrs instead of naming them,
// since each name holds a random op tag.
func olhShape(h olhHead, ls []listedVersion) string {
	_, versions := byKey(ls)
	t := h.info.Target.Key
	label := versionLabel(versions, t.Name, indexInstance(t.Instance), h.info.Removed)
	if indexInstance(t.Instance) == "" {
		label += fmt.Sprintf(" instance=%q", t.Instance)
	}
	return fmt.Sprintf("%s removed=%t ver=%s pending=%d", label, h.info.Removed, h.ver, len(h.pending))
}

var _ = Describe("the versioning gate's oracle", func() {
	It("keeps a ListVersionsResult's versions and delete markers in document order", func() {
		doc := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>b</Name><Prefix></Prefix><KeyMarker></KeyMarker>` +
			`<VersionIdMarker></VersionIdMarker><NextKeyMarker>obj</NextKeyMarker><NextVersionIdMarker>v2</NextVersionIdMarker>` +
			`<MaxKeys>3</MaxKeys><IsTruncated>true</IsTruncated>` +
			`<Version><Key>gone</Key><VersionId>g1</VersionId><IsLatest>true</IsLatest><ETag>&quot;e1&quot;</ETag><Size>4</Size></Version>` +
			`<DeleteMarker><Key>obj</Key><VersionId>m1</VersionId><IsLatest>true</IsLatest></DeleteMarker>` +
			`<Version><Key>obj</Key><VersionId>v2</VersionId><IsLatest>false</IsLatest><ETag>"e2"</ETag><Size>7</Size></Version>` +
			`</ListVersionsResult>`)
		Expect(parseVersionsPage(doc)).To(Equal(versionsPage{
			Versions: []listedVersion{
				{Key: "gone", VersionID: "g1", Latest: true, ETag: "e1", Size: 4},
				{Key: "obj", VersionID: "m1", Latest: true, Marker: true},
				{Key: "obj", VersionID: "v2", ETag: "e2", Size: 7},
			},
			Truncated: true, NextKeyMarker: "obj", NextVersionIDMarker: "v2",
		}))
		_, err := parseVersionsPage([]byte(`<Error><Code>NoSuchBucket</Code></Error>`))
		Expect(err).To(HaveOccurred(), "an error document")
	})

	Describe("indexFindings and indexShape", func() {
		// obj: the null version current, a delete marker and v2 under it;
		// gone: g1 alone, current again at its own epoch once a null marker
		// over it was linked and unlinked; removed: the null delete marker
		// current over r1, whose entries of instance "" only the marker flag
		// tells from the null version's. The epochs are what cls_rgw gives a
		// key's links and unlinks in turn from 2.
		listing := func() []listedVersion {
			return []listedVersion{
				{Key: "gone", VersionID: "g1", Latest: true, ETag: "e4", Size: 4},
				{Key: "obj", VersionID: "null", Latest: true, ETag: "e3", Size: 3},
				{Key: "obj", VersionID: "m1", Marker: true},
				{Key: "obj", VersionID: "v2", ETag: "e2", Size: 2},
				{Key: "removed", VersionID: "null", Latest: true, Marker: true},
				{Key: "removed", VersionID: "r1", ETag: "e5", Size: 5},
			}
		}
		dir := func(name, instance string, flags uint16, epoch uint64) biDir {
			return biDir{Name: name, Instance: instance, Flags: flags, VersionedEpoch: epoch, Exists: flags&dirFlagDeleteMarker == 0}
		}
		index := func() bucketIndex {
			ix := bucketIndex{
				Plain: []biDir{
					dir("gone", "", dirFlagVerMarker, 0),
					dir("gone", "g1", dirFlagVer|dirFlagCurrent, 2),
					dir("obj", "", dirFlagVerMarker, 0),
					dir("obj", "", dirFlagVer|dirFlagCurrent, 5),
					dir("obj", "m1", dirFlagVer|dirFlagDeleteMarker, 4),
					dir("obj", "v2", dirFlagVer, 3),
					dir("removed", "", dirFlagVerMarker, 0),
					dir("removed", "", dirFlagVer|dirFlagCurrent|dirFlagDeleteMarker, 3),
					dir("removed", "r1", dirFlagVer, 2),
				},
				Instance: []biDir{
					dir("gone", "g1", dirFlagVer|dirFlagCurrent, 2),
					dir("obj", "", dirFlagVer|dirFlagCurrent, 5),
					dir("obj", "m1", dirFlagVer|dirFlagDeleteMarker, 4),
					dir("obj", "v2", dirFlagVer, 3),
					dir("removed", "", dirFlagVer|dirFlagCurrent|dirFlagDeleteMarker, 3),
					dir("removed", "r1", dirFlagVer, 2),
				},
				OLH: []biOLH{{Epoch: 4, Exists: true}, {Epoch: 6, Exists: true}, {Epoch: 3, DeleteMarker: true, Exists: true}},
			}
			ix.OLH[0].Key.Name, ix.OLH[0].Key.Instance = "gone", "g1"
			ix.OLH[1].Key.Name = "obj"
			ix.OLH[2].Key.Name = "removed"
			return ix
		}

		It("finds nothing in an index that matches the listing", func() {
			Expect(indexFindings(index(), listing())).To(BeEmpty())
		})

		DescribeTable("finds each contradiction",
			func(change func(*bucketIndex, *[]listedVersion), want string) {
				ix, ls := index(), listing()
				change(&ix, &ls)
				Expect(indexFindings(ix, ls)).To(ContainElement(ContainSubstring(want)))
			},
			Entry("a listed version with no list entry",
				func(ix *bucketIndex, _ *[]listedVersion) { ix.Plain = slices.Delete(ix.Plain, 5, 6) },
				"obj's version v2 has 0 list and 1 instance entries"),
			Entry("a noncurrent version flagged current",
				func(ix *bucketIndex, _ *[]listedVersion) { ix.Instance[3].Flags |= dirFlagCurrent },
				"obj's version v2 has flags 0x1 listed and 0x3 on its instance"),
			Entry("a delete marker listed as a version",
				func(_ *bucketIndex, ls *[]listedVersion) { (*ls)[2].Marker = false },
				"obj's version m1 has 0 list and 0 instance entries"),
			Entry("epochs that do not fall along the listing",
				func(ix *bucketIndex, _ *[]listedVersion) {
					ix.Plain[5].VersionedEpoch, ix.Instance[3].VersionedEpoch = 9, 9
				},
				"obj's version v2 at epoch 9 is listed after epoch 4"),
			Entry("list and instance entries at different epochs",
				func(ix *bucketIndex, _ *[]listedVersion) { ix.Instance[2].VersionedEpoch = 3 },
				"obj's version m1 has epoch 4 listed and 3 on its instance"),
			Entry("an OLH entry naming a version that is not the latest",
				func(ix *bucketIndex, _ *[]listedVersion) { ix.OLH[1].Key.Instance = "v2" },
				`obj's OLH entry names "v2" (marker false), not the latest null`),
			Entry("an OLH entry below its latest version's epoch",
				func(ix *bucketIndex, _ *[]listedVersion) { ix.OLH[1].Epoch = 4 },
				"obj's OLH entry is at epoch 4, below its latest version's 5"),
			Entry("an OLH entry with a pending log",
				func(ix *bucketIndex, _ *[]listedVersion) {
					ix.OLH[0].PendingLog = []json.RawMessage{json.RawMessage(`{"key":5}`)}
				},
				"gone's OLH entry: exists true, pending removal false, 1 pending log epochs"),
			Entry("an entry with a pending operation",
				func(ix *bucketIndex, _ *[]listedVersion) {
					ix.Instance[0].PendingMap = []json.RawMessage{json.RawMessage(`{"key":"t"}`)}
				},
				"gone[g1] has pending operations"),
			Entry("a key without its version marker",
				func(ix *bucketIndex, _ *[]listedVersion) { ix.Plain = slices.Delete(ix.Plain, 0, 1) },
				"gone has 0 version markers"),
			Entry("an instance entry no listing shows, as a failed write leaves",
				func(ix *bucketIndex, _ *[]listedVersion) {
					ix.Instance = append(ix.Instance, dir("obj", "orphan", dirFlagVer, 0))
				},
				"obj has 3 list and 4 instance entries for 3 listed versions"),
			Entry("an entry of a key the listing does not show",
				func(ix *bucketIndex, _ *[]listedVersion) {
					ix.Instance = append(ix.Instance, dir("other", "x", dirFlagVer, 2))
				},
				"an entry other[x] of no listed key"),
			Entry("two latest versions",
				func(_ *bucketIndex, ls *[]listedVersion) { (*ls)[3].Latest = true },
				"obj's version v2 is listed 2 with IsLatest true"),
			Entry("an entry of another type, as a reshard log's",
				func(ix *bucketIndex, _ *[]listedVersion) { ix.Other = append(ix.Other, "resharddeleted") },
				`an entry of type "resharddeleted"`),
			Entry("a null delete marker listed as the null version",
				func(_ *bucketIndex, ls *[]listedVersion) { (*ls)[4].Marker = false },
				"removed's version null has 0 list and 0 instance entries"),
		)

		It("gives each listed version's epoch, and each key's OLH epoch", func() {
			Expect(epochs(index(), listing())).To(Equal(map[string]uint64{
				"gone#0": 2, "obj#0": 5, "obj#1": 4, "obj#2": 3, "removed#0": 3, "removed#1": 2,
				"olh gone": 4, "olh obj": 6, "olh removed": 3,
			}))
		})

		It("labels the version olh.info names, and keeps the null version's own instance string", func() {
			head := func(name, instance string, removed bool) olhHead {
				return olhHead{
					info: meta.OLHInfo{Target: meta.Obj{Key: meta.ObjKey{Name: name, Instance: instance}}, Removed: removed},
					ver:  "6", pending: []string{meta.AttrOLHPendingPrefix + "t"},
				}
			}
			Expect(olhShape(head("obj", "", false), listing())).To(Equal(`obj#0 instance="" removed=false ver=6 pending=1`))
			Expect(olhShape(head("obj", "null", false), listing())).To(Equal(`obj#0 instance="null" removed=false ver=6 pending=1`))
			Expect(olhShape(head("removed", "", true), listing())).To(Equal(`removed#0 instance="" removed=true ver=6 pending=1`))
			Expect(olhShape(head("obj", "v2", false), listing())).To(Equal("obj#2 removed=false ver=6 pending=1"))
		})

		It("decodes bi list's entries by their type", func() {
			ix, err := decodeIndex([]biEntry{
				{Type: "plain", Entry: json.RawMessage(`{"name":"obj","instance":"v2","ver":{"pool":7,"epoch":9},"locator":"",` +
					`"exists":true,"meta":{"category":1,"size":2,"etag":"e2"},"tag":"t","flags":1,"pending_map":[],"versioned_epoch":3}`)},
				{Type: "olh", Entry: json.RawMessage(`{"key":{"name":"obj","instance":""},"delete_marker":false,"epoch":6,` +
					`"pending_log":[{"key":7,"val":[]}],"tag":"o","exists":true,"pending_removal":false}`)},
				{Type: "resharddeleted", Entry: json.RawMessage(`{}`)},
			})
			Expect(err).NotTo(HaveOccurred())
			want := biDir{Name: "obj", Instance: "v2", Exists: true, Flags: dirFlagVer, PendingMap: []json.RawMessage{}, VersionedEpoch: 3}
			want.Meta.Size, want.Meta.ETag = 2, "e2"
			Expect(ix.Plain).To(Equal([]biDir{want}))
			Expect(ix.OLH).To(HaveExactElements(And(HaveField("Epoch", BeEquivalentTo(6)), HaveField("PendingLog", HaveLen(1)))))
			Expect(ix.Other).To(Equal([]string{"resharddeleted"}))
			_, err = decodeIndex([]biEntry{{Type: "instance", Entry: json.RawMessage(`{"flags":"x"}`)}})
			Expect(err).To(MatchError(ContainSubstring("decoding bi list's instance entry")))
		})

		It("gives two buckets written alike one shape, whatever their version ids", func() {
			ix, ls := index(), listing()
			rename := map[string]string{"g1": "G9", "m1": "M9", "v2": "V9"}
			ix2, ls2 := index(), listing()
			for i := range ls2 {
				if r, ok := rename[ls2[i].VersionID]; ok {
					ls2[i].VersionID = r
				}
			}
			for _, ds := range [][]biDir{ix2.Plain, ix2.Instance} {
				for i := range ds {
					if r, ok := rename[ds[i].Instance]; ok {
						ds[i].Instance = r
					}
				}
			}
			ix2.OLH[0].Key.Instance = "G9"
			Expect(indexShape(ix2, ls2)).To(Equal(indexShape(ix, ls)))
			Expect(listingShape(ls2)).To(Equal(listingShape(ls)))
			ix2.Plain[5].VersionedEpoch = 2
			Expect(indexShape(ix2, ls2)).NotTo(Equal(indexShape(ix, ls)), "an epoch that differs")
			Expect(indexShape(ix, ls)).To(ContainElements(
				"olh obj -> obj#0 marker=false epoch=6 exists=true pending-removal=false log=0",
				"instance obj#1 flags=0x5 epoch=4 exists=false size=0 etag= owner= pending=0",
				"plain gone#version-marker flags=0x8 epoch=0 exists=true size=0 etag= owner= pending=0",
			))
		})
	})
})
