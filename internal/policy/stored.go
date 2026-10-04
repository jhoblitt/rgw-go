package policy

import (
	"fmt"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// DecodeUserPolicies decodes a user's or group's inline policies,
// RGW_ATTR_USER_POLICY or a group's RGW_ATTR_IAM_POLICY: a bare
// std::map<string, string> of policy name to text, which radosgw parses in
// name order with the user's policy tenant and invalid principals dropped
// (load_inline_policy, src/rgw/rgw_auth.cc:85-95 at v19.2.6 and v20.2.4). A
// policy that does not parse fails the decode, as its exception fails
// radosgw's authentication; the error wraps its *ParseError. Bytes past the
// map fail it too, where radosgw's full-bufferlist decode asserts and
// aborts (src/include/encoding.h:632-637 at v19.2.6, :633-638 at v20.2.4;
// docs/ceph-upstream-bugs.md, "radosgw aborts on a stored IAM policy attr
// with bytes past its encoding").
func DecodeUserPolicies(b []byte, tenant *string, r denc.Release) ([]*Policy, error) {
	d := denc.NewDecoder(b)
	texts := denc.DecodeMap(d, (*denc.Decoder).String, (*denc.Decoder).String)
	if err := checkDecoded(d); err != nil {
		return nil, fmt.Errorf("decoding user policies: %w", err)
	}
	ps := make([]*Policy, 0, len(texts))
	for _, name := range slices.Sorted(maps.Keys(texts)) {
		p, err := Parse(texts[name], ParseOptions{Tenant: tenant, Release: r})
		if err != nil {
			return nil, fmt.Errorf("parsing user policy %q: %w", name, err)
		}
		ps = append(ps, p)
	}
	return ps, nil
}

// DecodeManagedPolicies decodes RGW_ATTR_MANAGED_POLICY, ManagedPolicies
// (src/rgw/rgw_iam_managed_policy.cc:177-189): ENCODE_START(1, 1) around a
// flat_set<string> of ARNs. It returns the managed policy each ARN names, in
// the set's sorted order with repeats dropped, skipping an ARN that names
// none (load_managed_policy, src/rgw/rgw_auth.cc:97-108). Bytes past the
// struct fail the decode, as DecodeUserPolicies's do.
func DecodeManagedPolicies(b []byte, r denc.Release) ([]*Policy, error) {
	d := denc.NewDecoder(b)
	h := d.BeginStruct(1)
	arns := denc.DecodeSlice(d, (*denc.Decoder).String)
	d.EndStruct(h)
	if err := checkDecoded(d); err != nil {
		return nil, fmt.Errorf("decoding managed policies: %w", err)
	}
	slices.Sort(arns)
	var ps []*Policy
	for _, arn := range slices.Compact(arns) {
		if p, ok := ManagedPolicy(arn, r); ok {
			ps = append(ps, p)
		}
	}
	return ps, nil
}

// checkDecoded is d's failure, or denc.ErrMalformed when bytes remain.
func checkDecoded(d *denc.Decoder) error {
	if err := d.Err(); err != nil {
		return err
	}
	if n := d.Remaining(); n > 0 {
		return fmt.Errorf("%w: %d bytes past the end", denc.ErrMalformed, n)
	}
	return nil
}
