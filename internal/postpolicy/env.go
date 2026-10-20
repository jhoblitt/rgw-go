package postpolicy

import (
	"strings"

	"github.com/jhoblitt/rgw-go/internal/cksum"
)

// Env is RGWPolicyEnv, the form's fields as the policy reads them, with the
// mark RGWPolicy's checked_vars keeps for each field a condition, or the
// caller, has checked. Names match in any ASCII case, as radosgw's
// ltstr_nocase maps match them, but over all their bytes, where strcasecmp
// ends a name at a NUL (docs/exclusions.md, "POST policy names are compared
// over all their bytes"). The zero Env is empty and ready to use.
type Env struct {
	vars    map[string]*envVar
	checked map[string]bool
}

type envVar struct{ name, value string }

// Add is RGWPolicyEnv::add_var
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L75-L78 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L79-L82 at v20.2.4):
// a field already present under the name takes value and keeps the
// spelling it was first added with, which a refusal names.
func (e *Env) Add(name, value string) {
	k := foldASCII(name)
	if v, ok := e.vars[k]; ok {
		v.value = value
		return
	}
	if e.vars == nil {
		e.vars = map[string]*envVar{}
	}
	e.vars[k] = &envVar{name: name, value: value}
}

// Get is RGWPolicyEnv::get_var: the field's value and whether the form has
// it.
func (e *Env) Get(name string) (string, bool) {
	v, ok := e.vars[foldASCII(name)]
	if !ok {
		return "", false
	}
	return v.value, true
}

// MarkChecked is RGWPolicy::set_var_checked, which get_policy calls for
// x-amz-signature on a SigV4 upload, AWSAccessKeyId and signature on a SigV2
// one, and policy on both, leaving SigV4's x-amz-algorithm, x-amz-credential
// and x-amz-date for the policy's conditions to name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3219-L3227 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3355-L3363 at v20.2.4).
// A name may be marked before its field is added.
func (e *Env) MarkChecked(name string) {
	if e.checked == nil {
		e.checked = map[string]bool{}
	}
	e.checked[foldASCII(name)] = true
}

// operand is RGWPolicyEnv::get_value
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L91-L102 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L95-L106 at v20.2.4):
// s itself, unless it starts with $, when the rest names a field, which is
// marked checked whether or not the form has it, and read as "" if not.
func (e *Env) operand(s string) string {
	name, ok := strings.CutPrefix(s, "$")
	if !ok {
		return s
	}
	e.MarkChecked(name)
	v, _ := e.Get(name)
	return v
}

// unchecked is RGWPolicyEnv::match_policy_vars
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L105-L121 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L108-L130 at v20.2.4):
// the first field in name order no mark covers, skipping an x-ignore- name
// in any case and, as v20.2.4 skips them, a checksum field.
func (e *Env) unchecked() (string, bool) {
	var key, name string
	found := false
	for k, v := range e.vars {
		if e.checked[k] || strings.HasPrefix(k, "x-ignore-") || checksumField(v.name) {
			continue
		}
		if !found || k < key {
			key, name, found = k, v.name, true
		}
	}
	return name, found
}

// checksumField is rgw::cksum::is_checksum_hdr
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L318-L334 at v20.2.4),
// whose fields Tentacle's match_policy_vars excepts and rgw-go excepts on
// both releases (docs/exclusions.md, "A POST policy needs no condition for a
// checksum field"): x-amz-checksum-algorithm, or x-amz-checksum- followed
// by an algorithm's name in any case, each prefix in lower case as Tentacle
// matches it. The names are cksum.ParseName's, Tentacle's and main's
// xxhash3 for Tentacle's xxh3, and the name is all that follows the prefix.
// Tentacle finds the prefix anywhere in the field's name and reads what
// follows its first fifteen bytes as a C string, so it also excepts a name
// with a NUL after an algorithm's (docs/ceph-upstream-bugs.md, "Tentacle's
// POST policy excepts a field whose name holds a NUL after a checksum
// algorithm's").
func checksumField(name string) bool {
	if name == "x-amz-checksum-algorithm" {
		return true
	}
	algo, ok := strings.CutPrefix(name, "x-amz-checksum-")
	if !ok {
		return false
	}
	_, known := cksum.ParseName(algo)
	return known
}

// foldASCII is s with its ASCII capitals in lower case and every other byte
// as it is, as strcasecmp folds in the C locale.
func foldASCII(s string) string {
	i := strings.IndexFunc(s, func(r rune) bool { return 'A' <= r && r <= 'Z' })
	if i < 0 {
		return s
	}
	b := []byte(s)
	for j := i; j < len(b); j++ {
		if 'A' <= b[j] && b[j] <= 'Z' {
			b[j] += 'a' - 'A'
		}
	}
	return string(b)
}
