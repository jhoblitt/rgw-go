// Package sse is a bucket's default server-side encryption: radosgw's
// RGWBucketEncryptionConfig, its stored form under the user.rgw.sse-s3.policy
// attr, and the ServerSideEncryptionConfiguration document PutBucketEncryption
// reads and GetBucketEncryption writes. The stored form is radosgw's at
// v19.2.7 and v20.2.4, whose src/rgw/rgw_bucket_encryption.{h,cc} are
// identical. The document is read as Amazon S3 reads it, held to S3's schema
// and AWS's values, where radosgw reads it leniently and checks no value
// (docs/exclusions.md, "A PutBucketEncryption document is held to S3's schema
// and AWS's values").
package sse
