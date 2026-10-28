// Package postpolicy is the policy document of a POST object, a browser
// upload: the JSON a form's policy field carries, read as RGWPolicy::from_json
// reads it, and the form's fields checked against it as RGWPolicy::check
// checks them. The rules are radosgw's at v19.2.7 and v20.2.4, whose
// src/rgw/rgw_policy_s3.cc differ only in Tentacle's exception for checksum
// fields, which rgw-go makes on both releases. The JSON is read as ceph's
// JSONParser reads it, with json_spirit's grammar (internal/jsonspirit), but
// for the forms that reader refuses (docs/exclusions.md, "A POST policy with
// a `\x` or `\X` escape, a number json_spirit takes from the host's libm, or
// nesting past 10000 is refused").
package postpolicy
