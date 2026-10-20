package driver_test

import (
	"context"
	"encoding/json"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("the metadata section table", func() {
	var s *driver.Store
	BeforeEach(func(ctx SpecContext) {
		st, err := driver.Open(ctx, newIndexCluster(), conf(map[string]string{"rgw_cache_enabled": "false"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		s = st
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: "alice"}
		Expect(s.PutUser(ctx, &op.UserRecord{Info: info}, op.PutUserOptions{Exclusive: true})).To(Succeed())
	})

	// Each verb names the user alice, so a lookup that fell through to
	// another section would find, change or list her.
	DescribeTable("answers a name no section has ErrNoSuchKey in every verb, touching no entry",
		func(ctx SpecContext, verb func(ctx context.Context) error) {
			before, err := s.Get(ctx, "user", "alice")
			Expect(err).NotTo(HaveOccurred())
			Expect(verb(ctx)).To(MatchError(op.ErrNoSuchKey))
			after, err := s.Get(ctx, "user", "alice")
			Expect(err).NotTo(HaveOccurred(), "alice is still there")
			Expect(after.Version).To(Equal(before.Version))
		},
		Entry("a get", func(ctx context.Context) error {
			_, err := s.Get(ctx, "kittens", "alice")
			return err
		}),
		Entry("a get naming a section in another case, as the handlers are found by exact name", func(ctx context.Context) error {
			_, err := s.Get(ctx, "User", "alice")
			return err
		}),
		Entry("a put, before its document is decoded", func(ctx context.Context) error {
			return s.Put(ctx, "kittens", "alice", op.MetadataEntry{Data: json.RawMessage(`not json`)}, op.PutMetadataOptions{})
		}),
		Entry("a remove", func(ctx context.Context) error {
			return s.Remove(ctx, "kittens", "alice")
		}),
		Entry("a listing", func(ctx context.Context) error {
			_, _, _, err := s.List(ctx, "kittens", "", 10)
			return err
		}),
	)

	Describe("through the admin ops", func() {
		run := func(ctx context.Context, o op.Op) error {
			u := meta.NewUserInfo()
			u.UserID = meta.UserID{ID: "admin"}
			u.Caps = meta.Caps{"metadata": meta.CapAll}
			return op.Run(ctx, o, &op.Request{Env: s.Env(), Identity: op.Identity{
				User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: u.Caps,
			}})
		}
		It("lists today's three sections at the top level", func(ctx SpecContext) {
			Expect(s.Sections()).To(ConsistOf("bucket", "bucket.instance", "user"))
			o := &op.ListMetadata{}
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"bucket", "bucket.instance", "user"}))
		})
		It("takes a put to every section it serves, as each has a put rule", func(ctx SpecContext) {
			for _, section := range s.Sections() {
				o := &op.PutMetadata{Section: section, Key: "x", Body: []byte(`not json`)}
				Expect(run(ctx, o)).To(MatchError(op.ErrInvalidArgument), "section %s", section)
			}
		})
		// The sections radosgw serves and the driver does not
		// (docs/exclusions.md, "The admin metadata API serves three
		// sections"): a put rule for one would let its put past the gate.
		It("refuses a put to a section of radosgw's it does not serve before reading the body, as none has a put rule", func(ctx SpecContext) {
			unserved := slices.DeleteFunc([]string{"account", "group", "otp", "roles", "topic"}, func(section string) bool {
				return slices.Contains(s.Sections(), section)
			})
			Expect(unserved).NotTo(BeEmpty(), "every section of radosgw's is served, so this spec checks nothing")
			for _, section := range unserved {
				o := &op.PutMetadata{Section: section, Key: "x", Body: []byte(`not json`)}
				Expect(run(ctx, o)).To(MatchError(op.ErrNoSuchKey), "section %s", section)
			}
		})
	})

	It("refuses a key holding a NUL byte before it looks the section up", func(ctx SpecContext) {
		_, err := s.Get(ctx, "kittens", "al\x00ice")
		Expect(err).To(MatchError(op.ErrInvalidArgument))
		Expect(s.Put(ctx, "kittens", "al\x00ice", op.MetadataEntry{}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
		Expect(s.Remove(ctx, "kittens", "al\x00ice")).To(MatchError(op.ErrInvalidArgument))
	})
})
