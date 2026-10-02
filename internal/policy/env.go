package policy

import "slices"

// Env is rgw::IAM::Environment (src/rgw/rgw_iam_policy.h:326 at v19.2.6, :345
// at v20.2.4), the condition keys of a request and their values. It is a
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
// read (src/rgw/rgw_iam_policy.cc:856, :880 at v19.2.6; :875, :900 at
// v20.2.4): the first of key's values in libstdc++'s linked order, as the
// type's doc describes. Unless sem.FindKeepsFirstValue, that is the value
// added last. With it, that is the first value of v20.2.4's order, where a
// value added while the Env holds 20 pairs or fewer goes after the first and
// one added past 20 pairs goes before it; once the first is removed, the
// next in that order is first. ok is false when key is absent.
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

// Remove erases every pair equal to (key, value), as rgw_iam_remove_objtags
// erases a copy source's tags (src/rgw/rgw_op.cc:651-699 at v19.2.6,
// :681-729 at v20.2.4); a key left with no values is removed.
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
	c := Env{keys: make(map[string]*envValues, len(e.keys))}
	for key, v := range e.keys {
		c.keys[key] = &envValues{added: slices.Clone(v.added), linked: slices.Clone(v.linked)}
	}
	return c
}
