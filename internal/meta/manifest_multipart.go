package meta

import (
	"cmp"
	"errors"
	"maps"
)

// ErrExplicitManifest is Append's refusal of an explicit manifest, or of a
// part without rules after the first, both of which RGWObjManifest::append
// hands to append_explicit to convert. radosgw writes neither for a part.
var ErrExplicitManifest = errors.New("meta: cannot append an explicit manifest")

// NewPartManifest is the manifest MultipartObjectProcessor::prepare,
// prepare_head and generator::create_begin leave for part partNum, before
// any data (driver/rados/rgw_putobj_processor.cc:441-489 at v19.2.6,
// :475-523 at v20.2.4; driver/rados/rgw_obj_manifest.h:265-270 and
// driver/rados/rgw_obj_manifest.cc:221-272 at both): prefix names the
// part's objects; one rule keyed at 0 numbers the part partNum with no part
// size and stripes of stripeSize; no head data, as the part head is the
// rule's first stripe; Obj is target, the upload's object rather than the
// part head; and the tails go in target's bucket under its instance, placed
// by tailRule with what it leaves empty taken from headRule.
func NewPartManifest(target Obj, headRule, tailRule PlacementRule, prefix string, partNum uint32, stripeSize uint64) Manifest {
	m := NewManifest()
	m.Obj = target
	m.HeadPlacementRule = headRule
	m.Prefix = prefix
	m.Rules = map[uint64]ManifestRule{0: {StartPartNum: partNum, StripeMaxSize: stripeSize}}
	m.TailPlacement = BucketPlacement{Bucket: target.Bucket, PlacementRule: tailRule.InheritFrom(headRule)}
	m.TailInstance = target.Key.Instance
	return m
}

// StripeObj is get_implicit_location for stripe of part under the rule at
// offset 0, the object generator::create_next names for it: what TailObj
// names when part is 0; "<prefix>.<part>" in the multipart namespace for a
// later part's stripe 0, its head; and "<prefix>.<part>_<stripe>" in the
// shadow namespace after it. The rule's override prefix replaces the
// manifest's. Like radosgw it prints part and stripe as ints.
func (m Manifest) StripeObj(part uint32, stripe uint64) Obj {
	//nolint:gosec // get_implicit_location prints (int)cur_part_id and (int)cur_stripe
	return implicitLocation(&m, int32(part), int32(stripe), m.MaxHeadSize, m.Rules[0].OverridePrefix).obj
}

// Append is RGWObjManifest::append for rule-based manifests
// (driver/rados/rgw_obj_manifest.cc:44-132 at v19.2.6 and v20.2.4), which
// RadosMultipartUpload::complete calls for each part in turn. A manifest
// without rules adopts the part whole. Otherwise each of the part's rules,
// in order, is absorbed into m's last rule while its part size, stripe size
// and prefix match and its part number is the one m's last rule reaches
// next. At the first that is not, it and the rest are added shifted by m's
// size; where its size or prefix differed, they all take its prefix as
// their override when that is not m's. A rule without a part size is given
// the rest of its manifest when it is compared, and m's last rule keeps it.
// m's size then grows by the part's. Append fails with ErrExplicitManifest,
// leaving m unchanged, where radosgw would convert both manifests to
// explicit ones.
func (m *Manifest) Append(part Manifest) error {
	if m.ExplicitObjs || part.ExplicitObjs {
		return ErrExplicitManifest
	}
	if len(m.Rules) == 0 {
		*m = part
		m.Objs = maps.Clone(part.Objs)
		m.Rules = maps.Clone(part.Rules)
		return nil
	}
	keys := sortedKeys(part.Rules)
	if len(keys) == 0 {
		return ErrExplicitManifest
	}
	if m.Prefix == "" {
		m.Prefix = part.Prefix
	}
	// The C++ fills a part rule's missing part size in the part's own map,
	// which append_rules then copies from.
	rules := maps.Clone(part.Rules)
	for i, k := range keys {
		lastKey := lastRuleKey(m.Rules)
		last := m.Rules[lastKey]
		if last.PartSize == 0 {
			last.PartSize = m.ObjSize - last.StartOfs
			m.Rules[lastKey] = last
		}
		next := rules[k]
		if next.PartSize == 0 {
			next.PartSize = part.ObjSize - next.StartOfs
			rules[k] = next
		}
		lastPrefix := cmp.Or(last.OverridePrefix, m.Prefix)
		nextPrefix := cmp.Or(next.OverridePrefix, part.Prefix)
		if last.PartSize != next.PartSize || last.StripeMaxSize != next.StripeMaxSize || lastPrefix != nextPrefix {
			if nextPrefix != m.Prefix {
				m.appendRules(rules, keys[i:], &nextPrefix)
			} else {
				m.appendRules(rules, keys[i:], nil)
			}
			break
		}
		expected := uint64(last.StartPartNum) + 1
		if last.PartSize > 0 {
			expected = uint64(last.StartPartNum) + (m.ObjSize+next.StartOfs-last.StartOfs)/last.PartSize
		}
		if expected != uint64(next.StartPartNum) {
			m.appendRules(rules, keys[i:], nil)
			break
		}
	}
	m.ObjSize += part.ObjSize
	return nil
}

// appendRules is RGWObjManifest::append_rules: the rules at keys, shifted by
// m's size, each with the override prefix when one is given.
func (m *Manifest) appendRules(rules map[uint64]ManifestRule, keys []uint64, override *string) {
	for _, k := range keys {
		r := rules[k]
		r.StartOfs += m.ObjSize
		if override != nil {
			r.OverridePrefix = *override
		}
		m.Rules[r.StartOfs] = r
	}
}

// lastRuleKey is the key of rules.rbegin(), the rule at the highest offset.
func lastRuleKey(rules map[uint64]ManifestRule) uint64 {
	var last uint64
	for k := range rules {
		last = max(last, k)
	}
	return last
}
