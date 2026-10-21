package op

// BrowserUploadCredentials are a POST object form's credentials, the fields
// RGWPostObj_ObjStore_S3::get_policy reads into s3_postobj_creds
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3120-L3185 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3256-L3321 at v20.2.4).
// Every field but Policy is the field's value trimmed of white space, as
// part_str reads it. The caller refuses, before it authenticates, a form that
// lacks a field its algorithm reads and one whose x-amz-security-token is
// present but empty, as get_policy does with 400. An empty AccessKey,
// Credential or Signature is then one the form sent empty, which radosgw
// passes on to be refused with 403, and an empty SecurityToken one the form
// does not carry.
type BrowserUploadCredentials struct {
	// Algorithm is x-amz-algorithm. AWS4-HMAC-SHA256, compared exactly,
	// selects SigV4; anything else, its absence included, SigV2.
	Algorithm string
	// AccessKey is AWSAccessKeyId, which SigV2 alone reads.
	AccessKey string
	// Credential is x-amz-credential, which SigV4 alone reads.
	Credential string
	// Signature is SigV2's signature field or SigV4's x-amz-signature.
	Signature string
	// SecurityToken is x-amz-security-token.
	SecurityToken string
	// Policy is the policy field's bytes as sent, untrimmed, as part_bl reads
	// them: what the signature covers, and so what the caller must
	// base64-decode into the policy document it checks.
	Policy string
}
