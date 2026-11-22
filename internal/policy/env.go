package policy

import (
	"maps"
	"slices"
)

// Env is rgw::IAM::Environment
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L326 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L345 at v20.2.4),
// the condition keys of a request and their values. It is a
// std::unordered_multimap, so a key may hold several values, equal ones
// included, and a key with no values is absent, which is what the Null
// operator and IfExists test. The zero value is an empty Env. A copy shares
// its values, as a map does; Clone makes an independent one.
//
// libstdc++ keeps each key's values together in the table's list, and find
// returns the first of them, so which value a typed condition reads depends
// on where each value was linked. v19.2.6 is built with GCC 11.5, whose
// emplace links each value before the key's others (bits/hashtable.h:859-860,
// :1988-2001, :2040-2086). v20.2.4 is built with GCC 13.3, whose emplace
// links a value right after the key's first while the table holds 20 pairs
// or fewer, and before it once the table holds more
// (bits/hashtable.h:867-868, :2118-2159, :2198-2244; 20 is
// __small_size_threshold for std::hash<std::string>,
// bits/hashtable_policy.h:294-299, bits/basic_string.h:4453). Erasing a value
// and rehashing leave the others in place. Env keeps both orders so that Find
// answers as either release does.
type Env struct {
	keys map[string]*envValues
	// unsent are the keys MarkUnsent names.
	unsent []string
	// deny are the keys and values SetForDeny sets.
	deny map[string]string
	// denyOr are the second readings SetForDenyOr and SetForDenyOrAbsent
	// record, by key.
	denyOr map[string][]reading
}

// reading is a reading of a key Amazon's texts leave open: value alone, or no
// value when absent is set.
type reading struct {
	value  string
	absent bool
}

// envValues are one key's values.
type envValues struct {
	// added is in the order the values were added. v19.2.6's list holds them
	// in the reverse order.
	added []string
	// linked is the order v20.2.4's list holds them in.
	linked []string
}

// smallSizeThreshold is libstdc++'s __small_size_threshold for a table keyed
// by std::string, which GCC 13.3 has and GCC 11.5 lacks: up to this many
// pairs, emplace links a value after the first one of its key.
const smallSizeThreshold = 20

// Add is emplace: it adds value to key's values, an empty value included.
func (e *Env) Add(key, value string) {
	held := e.pairs()
	if e.keys == nil {
		e.keys = make(map[string]*envValues)
	}
	v := e.keys[key]
	if v == nil {
		e.keys[key] = &envValues{added: []string{value}, linked: []string{value}}
		return
	}
	v.added = append(v.added, value)
	at := 1
	if held > smallSizeThreshold {
		at = 0
	}
	v.linked = slices.Insert(v.linked, at, value)
}

// pairs is size(): every value of every key.
func (e *Env) pairs() int {
	n := 0
	for _, v := range e.keys {
		n += len(v.added)
	}
	return n
}

// Lookup returns a copy of key's values in the order they were added, or nil
// when key is absent. The operators that read every value of a key, through
// equal_range, do not depend on the order; the typed ones read one value,
// which Find gives.
func (e *Env) Lookup(key string) []string {
	if v := e.keys[key]; v != nil {
		return slices.Clone(v.added)
	}
	return nil
}

// Find is env.find(key)->second, the one value radosgw's typed condition
// operators, Numeric, Date, Bool, BinaryEquals, IpAddress and NotIpAddress,
// read
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L856,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L880 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L875,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L900 at v20.2.4):
// the first of key's values in libstdc++'s linked order, as the type's doc
// describes. Unless sem.FindKeepsFirstValue, that is the value added last.
// With it, that is the first value of v20.2.4's order, where a value added
// while the Env holds 20 pairs or fewer goes after the first and one added
// past 20 pairs goes before it; once the first is removed, the next in that
// order is first. ok is false when key is absent.
func (e *Env) Find(key string, sem Semantics) (value string, ok bool) {
	v := e.keys[key]
	if v == nil {
		return "", false
	}
	if sem.FindKeepsFirstValue {
		return v.linked[0], true
	}
	return v.added[len(v.added)-1], true
}

// keysFolding returns the keys e holds whose names equal name once ASCII
// letters are folded, name itself among them when e holds it, in no
// particular order.
func (e *Env) keysFolding(name string) []string {
	var keys []string
	for k := range e.keys {
		if asciiEqualFold(k, name) {
			keys = append(keys, k)
		}
	}
	return keys
}

// Remove erases every pair equal to (key, value), as rgw_iam_remove_objtags
// erases a copy source's tags
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L651-L699 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L681-L729 at v20.2.4);
// a key left with no values is removed.
func (e *Env) Remove(key, value string) {
	v := e.keys[key]
	if v == nil {
		return
	}
	equal := func(s string) bool { return s == value }
	v.added = slices.DeleteFunc(v.added, equal)
	v.linked = slices.DeleteFunc(v.linked, equal)
	if len(v.added) == 0 {
		delete(e.keys, key)
	}
}

// Clone returns a copy of e that shares no storage with it.
func (e *Env) Clone() Env {
	c := Env{
		keys:   make(map[string]*envValues, len(e.keys)),
		unsent: slices.Clone(e.unsent),
		deny:   maps.Clone(e.deny),
		denyOr: make(map[string][]reading, len(e.denyOr)),
	}
	for key, v := range e.keys {
		c.keys[key] = &envValues{added: slices.Clone(v.added), linked: slices.Clone(v.linked)}
	}
	for key, rs := range e.denyOr {
		c.denyOr[key] = slices.Clone(rs)
	}
	return c
}

// MarkUnsent records that key holds only the value radosgw adds for a header
// the request does not send, or sends setting nothing, as it adds
// s3:x-amz-acl empty. A statement that allows is judged with the key as
// radosgw holds it; one that denies is judged with it and without it, and
// holds where either holds (Statement.EvalConditions).
func (e *Env) MarkUnsent(key string) {
	if !slices.Contains(e.unsent, key) {
		e.unsent = append(e.unsent, key)
	}
}

// withoutUnsent is a copy of e with the keys MarkUnsent names absent, and
// whether it names any.
func (e *Env) withoutUnsent() (Env, bool) {
	if len(e.unsent) == 0 {
		return Env{}, false
	}
	c := e.Clone()
	for _, key := range c.unsent {
		delete(c.keys, key)
	}
	c.unsent = nil
	return c, true
}

// SetForDeny records that Amazon S3's request context holds key with value
// alone, where radosgw's holds no value for it or another one. A statement
// that allows never reads it. One that denies reads it in place of key's
// values here, beside radosgw's reading of the environment as built
// (Statement.EvalConditions).
func (e *Env) SetForDeny(key, value string) {
	if e.deny == nil {
		e.deny = make(map[string]string)
	}
	e.deny[key] = value
}

// forDeny is e with each key SetForDeny names holding the value it was set
// to, alone: the request context a Deny's conditions read. It shares the
// other keys' values with e, which no evaluation changes.
func (e *Env) forDeny() Env {
	if len(e.deny) == 0 {
		return *e
	}
	keys := make(map[string]*envValues, len(e.keys)+len(e.deny))
	maps.Copy(keys, e.keys)
	for key, value := range e.deny {
		keys[key] = &envValues{added: []string{value}, linked: []string{value}}
	}
	return Env{keys: keys}
}

// SetForDenyOr records that Amazon's texts leave key open between the value
// SetForDeny sets and other: a Deny's condition also holds where it holds on
// Amazon S3's request context with key holding other alone. A statement that
// allows never reads it.
func (e *Env) SetForDenyOr(key, other string) {
	e.addDenyOr(key, reading{value: other})
}

// SetForDenyOrAbsent records that Amazon's texts leave key open between the
// value SetForDeny sets and none: a Deny's condition also holds where it
// holds on Amazon S3's request context without key. A statement that allows
// never reads it.
func (e *Env) SetForDenyOrAbsent(key string) {
	e.addDenyOr(key, reading{absent: true})
}

func (e *Env) addDenyOr(key string, r reading) {
	if e.denyOr == nil {
		e.denyOr = make(map[string][]reading)
	}
	e.denyOr[key] = append(e.denyOr[key], r)
}

// forDenyOr is one request context for each second reading SetForDenyOr and
// SetForDenyOrAbsent record: forDeny with that reading's key holding its
// value alone, or absent. Each differs from forDeny in that one key.
func (e *Env) forDenyOr() []Env {
	if len(e.denyOr) == 0 {
		return nil
	}
	amazon := e.forDeny()
	var ctxs []Env
	for key, rs := range e.denyOr {
		for _, r := range rs {
			keys := maps.Clone(amazon.keys)
			if keys == nil {
				keys = make(map[string]*envValues)
			}
			if r.absent {
				delete(keys, key)
			} else {
				keys[key] = &envValues{added: []string{r.value}, linked: []string{r.value}}
			}
			ctxs = append(ctxs, Env{keys: keys})
		}
	}
	return ctxs
}
