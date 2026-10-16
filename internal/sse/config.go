package sse

import "github.com/jhoblitt/rgw-go/internal/denc"

// ApplyServerSideEncryptionByDefault is ApplyServerSideEncryptionByDefault
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.h#L9-L45 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_bucket_encryption.h#L9-L45 at v20.2.4):
// the algorithm a write that names no encryption of its own is encrypted
// with, and the KMS key for an algorithm that takes one.
type ApplyServerSideEncryptionByDefault struct {
	KMSMasterKeyID string
	SSEAlgorithm   string
}

// Encode mirrors ApplyServerSideEncryptionByDefault::encode, ENCODE_START(1, 1):
// the key id, then the algorithm.
func (a ApplyServerSideEncryptionByDefault) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(a.KMSMasterKeyID)
	e.String(a.SSEAlgorithm)
	e.EndStruct(f)
}

// DecodeApplyServerSideEncryptionByDefault mirrors
// ApplyServerSideEncryptionByDefault::decode, DECODE_START(1).
func DecodeApplyServerSideEncryptionByDefault(d *denc.Decoder) ApplyServerSideEncryptionByDefault {
	h := d.BeginStruct(1)
	var a ApplyServerSideEncryptionByDefault
	a.KMSMasterKeyID = d.String()
	a.SSEAlgorithm = d.String()
	d.EndStruct(h)
	return a
}

// ServerSideEncryptionConfiguration is ServerSideEncryptionConfiguration
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.h#L47-L89 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_bucket_encryption.h#L47-L89 at v20.2.4),
// the document's Rule: its default, and whether SSE-KMS uses an S3 Bucket Key.
type ServerSideEncryptionConfiguration struct {
	Apply            ApplyServerSideEncryptionByDefault
	BucketKeyEnabled bool
}

// Encode mirrors ServerSideEncryptionConfiguration::encode, ENCODE_START(1, 1):
// the default, then the bucket key flag.
func (c ServerSideEncryptionConfiguration) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	c.Apply.Encode(e, r)
	e.Bool(c.BucketKeyEnabled)
	e.EndStruct(f)
}

// DecodeServerSideEncryptionConfiguration mirrors
// ServerSideEncryptionConfiguration::decode, DECODE_START(1).
func DecodeServerSideEncryptionConfiguration(d *denc.Decoder) ServerSideEncryptionConfiguration {
	h := d.BeginStruct(1)
	var c ServerSideEncryptionConfiguration
	c.Apply = DecodeApplyServerSideEncryptionByDefault(d)
	c.BucketKeyEnabled = d.Bool()
	d.EndStruct(h)
	return c
}

// BucketEncryptionConfig is RGWBucketEncryptionConfig
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.h#L91-L141 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_bucket_encryption.h#L91-L141 at v20.2.4),
// what radosgw stores in a bucket's user.rgw.sse-s3.policy attr. Rule is nil
// when rule_exist is false, as a document without a Rule stores it.
type BucketEncryptionConfig struct {
	Rule *ServerSideEncryptionConfiguration
}

// Encode mirrors RGWBucketEncryptionConfig::encode, ENCODE_START(1, 1):
// rule_exist, then the rule when there is one.
func (c BucketEncryptionConfig) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.Bool(c.Rule != nil)
	if c.Rule != nil {
		c.Rule.Encode(e, r)
	}
	e.EndStruct(f)
}

// DecodeBucketEncryptionConfig mirrors RGWBucketEncryptionConfig::decode,
// DECODE_START(1). It leaves d after the configuration and reads no further,
// as GetBucketEncryption and get_encryption_defaults read the attr
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8832-L8839
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L183-L192 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9800-L9807
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L180-L189 at v20.2.4),
// neither of which looks for bytes past it.
func DecodeBucketEncryptionConfig(d *denc.Decoder) BucketEncryptionConfig {
	h := d.BeginStruct(1)
	var c BucketEncryptionConfig
	if d.Bool() {
		r := DecodeServerSideEncryptionConfiguration(d)
		c.Rule = &r
	}
	d.EndStruct(h)
	return c
}
