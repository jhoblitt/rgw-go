package sse_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/sse"
)

var releases = []denc.Release{denc.Squid, denc.Tentacle}

// framed writes ENCODE_START(v, compat) around body, then appended, as a later
// version's encoder would add fields the decoder skips.
func framed(v, compat uint8, body func(*denc.Encoder), appended ...byte) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(v, compat)
	body(e)
	e.Raw(appended)
	e.EndStruct(f)
	return e.Bytes()
}

// applyBytes is ApplyServerSideEncryptionByDefault::encode's body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.h#L28-L33):
// kmsMasterKeyID, then sseAlgorithm.
func applyBytes(v, compat uint8, keyID, algorithm string, appended ...byte) []byte {
	return framed(v, compat, func(e *denc.Encoder) {
		e.String(keyID)
		e.String(algorithm)
	}, appended...)
}

// ruleBytes is ServerSideEncryptionConfiguration::encode's body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.h#L72-L77):
// the default, then bucketKeyEnabled as one byte.
func ruleBytes(v, compat uint8, apply []byte, bucketKey byte, appended ...byte) []byte {
	return framed(v, compat, func(e *denc.Encoder) {
		e.Raw(apply)
		e.U8(bucketKey)
	}, appended...)
}

// configBytes is RGWBucketEncryptionConfig::encode's body
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.h#L119-L126):
// rule_exist as one byte, then the rule, which the caller leaves nil when
// rule_exist is zero.
func configBytes(v, compat uint8, ruleExist byte, rule []byte, appended ...byte) []byte {
	return framed(v, compat, func(e *denc.Encoder) {
		e.U8(ruleExist)
		e.Raw(rule)
	}, appended...)
}

func decodeWhole[T any](b []byte, decode func(*denc.Decoder) T) T {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := decode(d)
	Expect(d.Err()).NotTo(HaveOccurred(), "decoding %x", b)
	Expect(d.Remaining()).To(BeZero(), "decoding %x", b)
	return v
}

func decodeErr[T any](b []byte, decode func(*denc.Decoder) T) error {
	d := denc.NewDecoder(b)
	decode(d)
	return d.Err()
}

func encodeAt(r denc.Release, enc func(*denc.Encoder, denc.Release)) []byte {
	e := denc.NewEncoder()
	enc(e, r)
	return e.Bytes()
}

// kmsRule is a configuration whose rule names aws:kms, key id k and a bucket key.
func kmsRule() sse.BucketEncryptionConfig {
	return sse.BucketEncryptionConfig{Rule: &sse.ServerSideEncryptionConfiguration{
		Apply:            sse.ApplyServerSideEncryptionByDefault{KMSMasterKeyID: "k", SSEAlgorithm: "aws:kms"},
		BucketKeyEnabled: true,
	}}
}

var _ = Describe("ApplyServerSideEncryptionByDefault", func() {
	It("writes kmsMasterKeyID, then sseAlgorithm, in ENCODE_START(1, 1), and reads them back", func() {
		a := sse.ApplyServerSideEncryptionByDefault{KMSMasterKeyID: "key-1", SSEAlgorithm: "aws:kms"}
		b := applyBytes(1, 1, "key-1", "aws:kms")
		for _, r := range releases {
			Expect(encodeAt(r, a.Encode)).To(Equal(b), "release %v", r)
		}
		Expect(decodeWhole(b, sse.DecodeApplyServerSideEncryptionByDefault)).To(Equal(a))
	})
	It("skips what a later version appends, as DECODE_START(1) does", func() {
		b := applyBytes(2, 1, "k", "AES256", 0xAA, 0xBB)
		Expect(decodeWhole(b, sse.DecodeApplyServerSideEncryptionByDefault)).
			To(Equal(sse.ApplyServerSideEncryptionByDefault{KMSMasterKeyID: "k", SSEAlgorithm: "AES256"}))
	})
	It("refuses a compat version above 1", func() {
		Expect(decodeErr(applyBytes(2, 2, "", ""), sse.DecodeApplyServerSideEncryptionByDefault)).
			To(MatchError(denc.ErrIncompatible))
	})
	It("refuses a key id whose length runs past the buffer, before reading it", func() {
		b := framed(1, 1, func(e *denc.Encoder) { e.U32(0xffffffff) })
		Expect(decodeErr(b, sse.DecodeApplyServerSideEncryptionByDefault)).To(MatchError(denc.ErrShortBuffer))
	})
})

var _ = Describe("ServerSideEncryptionConfiguration", func() {
	It("writes the default, then bucketKeyEnabled, in ENCODE_START(1, 1), and reads them back", func() {
		c := sse.ServerSideEncryptionConfiguration{
			Apply:            sse.ApplyServerSideEncryptionByDefault{KMSMasterKeyID: "k", SSEAlgorithm: "aws:kms:dsse"},
			BucketKeyEnabled: true,
		}
		b := ruleBytes(1, 1, applyBytes(1, 1, "k", "aws:kms:dsse"), 1)
		for _, r := range releases {
			Expect(encodeAt(r, c.Encode)).To(Equal(b), "release %v", r)
		}
		Expect(decodeWhole(b, sse.DecodeServerSideEncryptionConfiguration)).To(Equal(c))
	})
	DescribeTable("reads bucketKeyEnabled as Ceph's decode(bool) does, any non-zero byte true, and writes it as 0 or 1",
		func(stored byte, want bool) {
			c := decodeWhole(ruleBytes(1, 1, applyBytes(1, 1, "", ""), stored), sse.DecodeServerSideEncryptionConfiguration)
			Expect(c.BucketKeyEnabled).To(Equal(want), "byte %#x", stored)
			canonical := byte(0)
			if want {
				canonical = 1
			}
			Expect(encodeAt(denc.Squid, c.Encode)).To(Equal(ruleBytes(1, 1, applyBytes(1, 1, "", ""), canonical)))
		},
		Entry("0 is false", byte(0), false),
		Entry("1 is true", byte(1), true),
		Entry("2 is true", byte(2), true),
		Entry("0xff is true", byte(0xff), true),
	)
	It("skips what a later version appends, at its own level and its default's", func() {
		b := ruleBytes(3, 1, applyBytes(2, 1, "k", "aws:kms", 0x01), 1, 0x07, 0x08)
		Expect(decodeWhole(b, sse.DecodeServerSideEncryptionConfiguration)).To(Equal(*kmsRule().Rule))
	})
	It("refuses a compat version above 1", func() {
		Expect(decodeErr(ruleBytes(2, 2, applyBytes(1, 1, "", ""), 0), sse.DecodeServerSideEncryptionConfiguration)).
			To(MatchError(denc.ErrIncompatible))
	})
})

var _ = Describe("BucketEncryptionConfig", func() {
	It("writes rule_exist false and no rule for a configuration without one", func() {
		for _, r := range releases {
			Expect(encodeAt(r, sse.BucketEncryptionConfig{}.Encode)).To(Equal([]byte{1, 1, 1, 0, 0, 0, 0}), "release %v", r)
		}
		Expect(decodeWhole([]byte{1, 1, 1, 0, 0, 0, 0}, sse.DecodeBucketEncryptionConfig)).To(Equal(sse.BucketEncryptionConfig{}))
	})
	It("writes rule_exist true, then the rule, and reads them back", func() {
		b := configBytes(1, 1, 1, ruleBytes(1, 1, applyBytes(1, 1, "k", "aws:kms"), 1))
		for _, r := range releases {
			Expect(encodeAt(r, kmsRule().Encode)).To(Equal(b), "release %v", r)
		}
		Expect(decodeWhole(b, sse.DecodeBucketEncryptionConfig)).To(Equal(kmsRule()))
	})
	It("writes a rule that names nothing, which an empty Rule element stores", func() {
		c := sse.BucketEncryptionConfig{Rule: &sse.ServerSideEncryptionConfiguration{}}
		b := configBytes(1, 1, 1, ruleBytes(1, 1, applyBytes(1, 1, "", ""), 0))
		Expect(encodeAt(denc.Squid, c.Encode)).To(Equal(b))
		Expect(decodeWhole(b, sse.DecodeBucketEncryptionConfig)).To(Equal(c))
	})
	It("reads no rule when rule_exist is zero, skipping the rest of the struct", func() {
		b := configBytes(1, 1, 0, nil, 0x01, 0x02, 0x03)
		Expect(decodeWhole(b, sse.DecodeBucketEncryptionConfig)).To(Equal(sse.BucketEncryptionConfig{}))
	})
	It("reads a rule_exist byte of 2 as true, as decode(bool) does", func() {
		b := configBytes(1, 1, 2, ruleBytes(1, 1, applyBytes(1, 1, "k", "aws:kms"), 1))
		Expect(decodeWhole(b, sse.DecodeBucketEncryptionConfig)).To(Equal(kmsRule()))
	})
	It("skips what a later version appends after the rule", func() {
		b := configBytes(2, 1, 1, ruleBytes(1, 1, applyBytes(1, 1, "k", "aws:kms"), 1), 0x09)
		Expect(decodeWhole(b, sse.DecodeBucketEncryptionConfig)).To(Equal(kmsRule()))
	})
	DescribeTable("refuses a compat version above 1 at any level",
		func(b []byte) {
			Expect(decodeErr(b, sse.DecodeBucketEncryptionConfig)).To(MatchError(denc.ErrIncompatible))
		},
		Entry("the configuration", configBytes(2, 2, 0, nil)),
		Entry("its rule", configBytes(1, 1, 1, ruleBytes(2, 2, applyBytes(1, 1, "", ""), 0))),
		Entry("the rule's default", configBytes(1, 1, 1, ruleBytes(1, 1, applyBytes(2, 2, "", ""), 0))),
	)
	It("fails a rule that runs past the configuration's struct_len, as DECODE_FINISH does", func() {
		rule := ruleBytes(1, 1, applyBytes(1, 1, "k", "aws:kms"), 1)
		b := append([]byte{1, 1, 1, 0, 0, 0, 1}, rule...)
		Expect(decodeErr(b, sse.DecodeBucketEncryptionConfig)).To(MatchError(denc.ErrOverread))
	})
	It("fails every truncation of an encoding, reading no more than it was given", func() {
		b := configBytes(1, 1, 1, ruleBytes(1, 1, applyBytes(1, 1, "key", "aws:kms"), 1))
		for n := range b {
			d := denc.NewDecoder(b[:n])
			sse.DecodeBucketEncryptionConfig(d)
			Expect(d.Err()).To(HaveOccurred(), "%d of %d bytes", n, len(b))
			Expect(d.Offset()).To(BeNumerically("<=", n), "%d of %d bytes", n, len(b))
		}
	})
	It("leaves the bytes after the encoding unread, as radosgw's readers of the attr ignore them", func() {
		b := append(configBytes(1, 1, 0, nil), 'x', 'y', 'z')
		d := denc.NewDecoder(b)
		Expect(sse.DecodeBucketEncryptionConfig(d)).To(Equal(sse.BucketEncryptionConfig{}))
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(d.Remaining()).To(Equal(3))
	})
})
