package radosclient_test

import (
	"context"
	"errors"
	"math/bits"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// uidNS is the namespace the paging specs list.
const uidNS = "users.uid"

// uidListing is a users.uid namespace in the order RADOS lists it, by the
// bit-reversed placement hash ceph_str_hash_rjenkins gives each name, then
// by name:
//
//	dave 0x0694dfdd, alice 0x06a04cf5, alice.buckets 0x1f5ca3c3,
//	user-318489 and user-326802 0x259305f8, bob 0x954708d3,
//	carol 0xbcfb659a, user-155108 and user-327421 0xc4707780,
//	erin 0xf72c4da3.
//
// With 4 placement groups, group 0 holds the first five, group 1 bob and
// carol, and group 3 the last three.
var uidListing = []string{
	"dave", "alice", "alice.buckets", "user-318489", "user-326802",
	"bob", "carol", "user-155108", "user-327421", "erin",
}

// groupSource lists oids, which are in RADOS's order, and seeks as
// librados does on a fresh listing: to the start of the placement group
// holding the hash, in a pool of pgNum groups, a power of two.
type groupSource struct {
	oids  []string
	pgNum uint32
	err   error // what Err reports once the listing ends
	pos   int
	seeks []uint32
	read  []string // every name Next handed out
}

func newGroupSource(pgNum uint32, oids ...string) *groupSource {
	return &groupSource{oids: oids, pgNum: pgNum}
}

func (s *groupSource) Seek(h uint32) {
	s.seeks = append(s.seeks, h)
	// A group is the hashes sharing its low bits, one run in bit-reversed
	// order that starts at the reversed group number.
	start := bits.Reverse32(h & (s.pgNum - 1))
	s.pos = slices.IndexFunc(s.oids, func(oid string) bool {
		return bits.Reverse32(radosclient.PlacementHash(uidNS, oid)) >= start
	})
	if s.pos < 0 {
		s.pos = len(s.oids)
	}
}

func (s *groupSource) Next() (oid, locator string, ok bool) {
	if s.pos == len(s.oids) {
		return "", "", false
	}
	oid = s.oids[s.pos]
	s.pos++
	s.read = append(s.read, oid)
	return oid, "", true
}

func (s *groupSource) Err() error { return s.err }

// page runs one ListPage over src and returns what it delivered.
func page(ctx context.Context, src radosclient.ListSource, token string, limit int) (oids []string, next string, more bool) {
	GinkgoHelper()
	next, more, err := radosclient.ListPage(ctx, src, uidNS, token, limit, func(oid, locator string) error {
		Expect(locator).To(BeEmpty())
		oids = append(oids, oid)
		return nil
	})
	Expect(err).NotTo(HaveOccurred())
	return oids, next, more
}

// tokenAfter is the token a page ending at oid hands out.
func tokenAfter(oid string) string { return radosclient.EncodeListToken(oid) }

var _ = Describe("ListPage", func() {
	It("lists the same-hash pairs of the fixture under one hash", func() {
		Expect(radosclient.PlacementHash(uidNS, "user-155108")).To(Equal(uint32(0x01ee0e23)))
		Expect(radosclient.PlacementHash(uidNS, "user-327421")).To(Equal(uint32(0x01ee0e23)))
		Expect(radosclient.PlacementHash(uidNS, "user-318489")).To(Equal(uint32(0x1fa0c9a4)))
		Expect(radosclient.PlacementHash(uidNS, "user-326802")).To(Equal(uint32(0x1fa0c9a4)))
	})

	It("pages into the full listing at every limit, wherever in a group the seek lands", func(ctx SpecContext) {
		for _, pgNum := range []uint32{1, 2, 4, 8, 32} {
			for limit := 1; limit <= len(uidListing)+1; limit++ {
				var paged []string
				token := ""
				for pages := 1; ; pages++ {
					Expect(pages).To(BeNumerically("<=", len(uidListing)), "pg_num %d, limit %d ends", pgNum, limit)
					oids, next, more := page(ctx, newGroupSource(pgNum, uidListing...), token, limit)
					paged = append(paged, oids...)
					if !more {
						Expect(next).To(BeEmpty())
						break
					}
					Expect(oids).To(HaveLen(limit), "pg_num %d, limit %d: a page before the last is full", pgNum, limit)
					Expect(next).To(Equal(tokenAfter(oids[len(oids)-1])))
					token = next
				}
				Expect(paged).To(Equal(uidListing), "pg_num %d, limit %d", pgNum, limit)
			}
		}
	})

	It("skips what the seek replays from the start of the group, other hashes included", func(ctx SpecContext) {
		src := newGroupSource(4, uidListing...)
		oids, next, more := page(ctx, src, tokenAfter("alice.buckets"), 2)
		Expect(src.read[:3]).To(Equal([]string{"dave", "alice", "alice.buckets"}), "the replay this spec needs")
		Expect(oids).To(Equal([]string{"user-318489", "user-326802"}))
		Expect([]any{next, more}).To(Equal([]any{tokenAfter("user-326802"), true}))
	})

	It("splits a same-hash pair across pages by name", func(ctx SpecContext) {
		src := newGroupSource(4, uidListing...)
		oids, _, more := page(ctx, src, "", 4)
		Expect(oids).To(Equal(uidListing[:4]))
		Expect(more).To(BeTrue())
		oids, _, more = page(ctx, newGroupSource(4, uidListing...), tokenAfter("user-318489"), 4)
		Expect(oids).To(Equal([]string{"user-326802", "bob", "carol", "user-155108"}))
		Expect(more).To(BeTrue())
		src = newGroupSource(4, uidListing...)
		oids, next, more := page(ctx, src, tokenAfter("user-155108"), 4)
		Expect(src.read[0]).To(Equal("user-155108"), "the replay hands back the pair's first name")
		Expect(oids).To(Equal([]string{"user-327421", "erin"}))
		Expect([]any{next, more}).To(Equal([]any{"", false}))
	})

	It("says nothing follows a page the listing fills exactly", func(ctx SpecContext) {
		oids, next, more := page(ctx, newGroupSource(4, uidListing...), tokenAfter("carol"), 3)
		Expect(oids).To(Equal([]string{"user-155108", "user-327421", "erin"}))
		Expect([]any{next, more}).To(Equal([]any{"", false}))
		oids, next, more = page(ctx, newGroupSource(4, uidListing...), "", len(uidListing))
		Expect(oids).To(Equal(uidListing))
		Expect([]any{next, more}).To(Equal([]any{"", false}))
	})

	It("delivers an empty last page for a token at the last object", func(ctx SpecContext) {
		oids, next, more := page(ctx, newGroupSource(4, uidListing...), tokenAfter("erin"), 3)
		Expect(oids).To(BeEmpty())
		Expect([]any{next, more}).To(Equal([]any{"", false}))
	})

	It("resumes after a removed last object at the object that followed it", func(ctx SpecContext) {
		without := slices.DeleteFunc(slices.Clone(uidListing), func(oid string) bool { return oid == "alice.buckets" })
		oids, _, _ := page(ctx, newGroupSource(4, without...), tokenAfter("alice.buckets"), 0)
		Expect(oids).To(Equal(uidListing[3:]))
	})

	DescribeTable("lists everything after the token when limit is not positive",
		func(ctx SpecContext, limit int) {
			oids, next, more := page(ctx, newGroupSource(4, uidListing...), tokenAfter("alice"), limit)
			Expect(oids).To(Equal(uidListing[2:]))
			Expect([]any{next, more}).To(Equal([]any{"", false}))
		},
		Entry("limit 0", 0),
		Entry("a negative limit", -1),
	)

	It("seeks to the last object's placement hash in the namespace, and not at all without a token", func(ctx SpecContext) {
		src := newGroupSource(4, uidListing...)
		page(ctx, src, tokenAfter("alice.buckets"), 1)
		Expect(src.seeks).To(Equal([]uint32{0xc3c53af8}), "rjenkins of users.uid, 0x1f and alice.buckets")
		src = newGroupSource(4, uidListing...)
		page(ctx, src, "", 1)
		Expect(src.seeks).To(BeEmpty())
	})

	It("refuses a token it did not make before reading the listing", func(ctx SpecContext) {
		src := newGroupSource(4, uidListing...)
		_, _, err := radosclient.ListPage(ctx, src, uidNS, "3:b55a9110:root::bu_9:head", 1, func(string, string) error { return nil })
		Expect(err).To(MatchError(radosclient.ErrBadOp))
		Expect(src.seeks).To(BeEmpty())
		Expect(src.read).To(BeEmpty())
	})

	It("stops at the callback's error without reading further", func(ctx SpecContext) {
		src := newGroupSource(4, uidListing...)
		stop := errors.New("stop")
		_, _, err := radosclient.ListPage(ctx, src, uidNS, "", 0, func(string, string) error { return stop })
		Expect(err).To(MatchError(stop))
		Expect(src.read).To(Equal(uidListing[:1]))
	})

	It("stops at the context's end", func(ctx SpecContext) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		called := false
		_, _, err := radosclient.ListPage(canceled, newGroupSource(4, uidListing...), uidNS, "", 0, func(string, string) error {
			called = true
			return nil
		})
		Expect(err).To(MatchError(context.Canceled))
		Expect(called).To(BeFalse())
	})

	It("returns the error that ended the listing", func(ctx SpecContext) {
		src := newGroupSource(4, uidListing...)
		src.err = radosclient.ErrNotSupported
		var oids []string
		_, _, err := radosclient.ListPage(ctx, src, uidNS, "", 0, func(oid, _ string) error {
			oids = append(oids, oid)
			return nil
		})
		Expect(err).To(MatchError(radosclient.ErrNotSupported))
		Expect(oids).To(Equal(uidListing))
	})
})
