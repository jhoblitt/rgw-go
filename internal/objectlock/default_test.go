package objectlock_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/testutil/locktest"
)

// lockedInfo is a bucket whose flags enable object lock, with lock l.
func lockedInfo(l meta.ObjectLock) *meta.BucketInfo {
	info := meta.NewBucketInfo()
	info.Bucket = meta.BucketID{Name: "b"}
	info.Flags |= meta.BucketVersioned | meta.BucketObjLockEnabled
	info.SetObjectLock(l)
	return &info
}

func governanceRule(days int32) meta.ObjectLock {
	return meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: "GOVERNANCE", Days: days}}}
}

var _ = Describe("DefaultRetention", func() {
	It("is the rule's mode retained until LockUntil of the rule and mtime", func() {
		r, err := objectlock.DefaultRetention(lockedInfo(governanceRule(3)), map[string][]byte{meta.AttrETag: []byte("e")}, mtime)
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(Equal(&meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: mtime.Add(3 * 24 * time.Hour)}))
	})
	It("takes a rule whose lock is not enabled, as has_rule reads rule_exist alone", func() {
		l := governanceRule(1)
		l.Enabled = false
		r, err := objectlock.DefaultRetention(lockedInfo(l), nil, mtime)
		Expect(err).NotTo(HaveOccurred())
		Expect(r).NotTo(BeNil())
	})
	It("takes the rule over an empty retention attr, which a head write skips", func() {
		r, err := objectlock.DefaultRetention(lockedInfo(governanceRule(1)), map[string][]byte{meta.AttrObjectRetention: {}}, mtime)
		Expect(err).NotTo(HaveOccurred())
		Expect(r).NotTo(BeNil())
	})
	DescribeTable("is nil",
		func(info func() *meta.BucketInfo, attrs map[string][]byte) {
			Expect(objectlock.DefaultRetention(info(), attrs, mtime)).To(BeNil())
		},
		Entry("for a lock without a rule, RGWObjectLock()", func() *meta.BucketInfo {
			info := lockedInfo(governanceRule(1))
			info.ObjLock = nil
			return info
		}, nil),
		Entry("for a rule on a record whose flags do not enable object lock", func() *meta.BucketInfo {
			info := lockedInfo(governanceRule(1))
			info.Flags &^= meta.BucketObjLockEnabled
			return info
		}, nil),
		Entry("for a write that carries a retention", func() *meta.BucketInfo { return lockedInfo(governanceRule(1)) },
			map[string][]byte{meta.AttrObjectRetention: locktest.RetentionAttr("COMPLIANCE", mtime)}),
	)
	It("fails on a lock configuration that does not decode, whatever the write carries", func() {
		info := lockedInfo(governanceRule(1))
		info.ObjLock = meta.RawStruct{1, 1, 1, 0, 0, 0, 1}
		_, err := objectlock.DefaultRetention(info, map[string][]byte{meta.AttrObjectRetention: locktest.RetentionAttr("COMPLIANCE", mtime)}, mtime)
		Expect(err).To(MatchError(ContainSubstring("the default retention of bucket b")))
	})
})

var _ = Describe("the lock attrs' encoders", func() {
	It("EncodeRetention writes RGWObjectRetention", func() {
		until := time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)
		Expect(objectlock.EncodeRetention(meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: until})).To(Equal(locktest.RetentionAttr("GOVERNANCE", until)))
	})
	It("EncodeLegalHold writes RGWObjectLegalHold", func() {
		Expect(objectlock.EncodeLegalHold(meta.ObjectLegalHold{Status: "ON"})).To(Equal(locktest.LegalHoldAttr("ON")))
	})
})
