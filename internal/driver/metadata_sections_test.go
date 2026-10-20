package driver_test

import (
	"context"
	"encoding/json"

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

	It("refuses a key holding a NUL byte before it looks the section up", func(ctx SpecContext) {
		_, err := s.Get(ctx, "kittens", "al\x00ice")
		Expect(err).To(MatchError(op.ErrInvalidArgument))
		Expect(s.Put(ctx, "kittens", "al\x00ice", op.MetadataEntry{}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
		Expect(s.Remove(ctx, "kittens", "al\x00ice")).To(MatchError(op.ErrInvalidArgument))
	})
})
