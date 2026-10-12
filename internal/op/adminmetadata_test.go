package op_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// listTimeout bounds a listing spec, so a listing that never ends fails
// it rather than the suite.
const listTimeout = 10 * time.Second

var _ = Describe("admin metadata ops", func() {
	var (
		ctx   context.Context
		store *memstore.Store
		env   *op.Env
		clock time.Time
		inst  string
	)
	BeforeEach(func() {
		ctx = context.Background()
		clock = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
		pools := meta.NewZonePlacementInfo()
		store = memstore.New(memstore.Config{
			Now: func() time.Time { return clock },
			Params: meta.ZoneParams{
				Name: "z1", PlacementPools: map[string]meta.ZonePlacementInfo{"default-placement": pools},
			},
		})
		for _, u := range []struct{ id, email, key string }{
			{"admin", "", ""}, {"leseb", "leseb@example.com", "AKIALESEB"}, {"carol", "carol@example.com", "AKIACAROL"},
		} {
			info := meta.NewUserInfo()
			info.UserID = meta.UserID{ID: u.id}
			info.DisplayName = u.id
			info.Email = u.email
			if u.key != "" {
				info.AccessKeys = map[string]meta.AccessKey{u.key: {ID: u.key, Secret: u.key + "-secret", Active: true}}
			}
			store.AddUser(info)
		}
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{
			Name: "test", Owner: meta.UserOwner(meta.UserID{ID: "leseb"}), Placement: meta.PlacementRule{Name: "default-placement"},
		})
		Expect(err).NotTo(HaveOccurred())
		inst = "test:" + rec.Info.Bucket.ID
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, BucketAdmin: store, Metadata: store, Usage: store, Metrics: op.NopMetrics{},
		}
	})
	runWith := func(c context.Context, o op.Op, caps meta.Caps) error {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return op.Run(c, o, &op.Request{Env: env, Identity: op.Identity{
			User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps,
		}})
	}
	runAs := func(o op.Op, caps meta.Caps) error { return runWith(ctx, o, caps) }
	run := func(o op.Op) error { return runAs(o, meta.Caps{"metadata": meta.CapAll}) }
	// runIn is run under c, a listing spec's context.
	runIn := func(c context.Context, o op.Op) error { return runWith(c, o, meta.Caps{"metadata": meta.CapAll}) }
	get := func(section, key string) *op.GetMetadata {
		GinkgoHelper()
		o := &op.GetMetadata{Section: section, Key: key}
		Expect(run(o)).To(Succeed())
		return o
	}
	user := func(id string) *op.UserRecord {
		GinkgoHelper()
		rec, err := store.GetUser(ctx, meta.UserID{ID: id})
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	// envelope is the document a metadata get renders, as a put reads it.
	envelope := func(ver meta.ObjVersion, mtime time.Time, data any) []byte {
		GinkgoHelper()
		d, err := json.Marshal(data)
		Expect(err).NotTo(HaveOccurred())
		b, err := json.Marshal(map[string]any{
			"key": "ignored", "ver": ver, "mtime": meta.Time{Time: mtime}, "data": json.RawMessage(d),
		})
		Expect(err).NotTo(HaveOccurred())
		return b
	}
	// renamed is user id's document with its display name changed.
	renamed := func(id, name string) meta.UserCompleteInfo {
		GinkgoHelper()
		rec := user(id)
		info := rec.Info
		info.DisplayName = name
		return meta.UserCompleteInfo{Info: info, Attrs: rec.Attrs, HasAttrs: true}
	}
	put := func(section, key string, sync op.SyncType, body []byte) (*op.PutMetadata, error) {
		o := &op.PutMetadata{Section: section, Key: key, Sync: sync, Body: body}
		return o, run(o)
	}

	Describe("ParseSyncType", func() {
		DescribeTable("maps update-type as string_to_sync_type does",
			func(s string, want op.SyncType, ok bool) {
				got, parsed := op.ParseSyncType(s)
				Expect(parsed).To(Equal(ok), "update-type %q", s)
				if ok {
					Expect(got).To(Equal(want), "update-type %q", s)
				}
			},
			Entry("update-by-version", "update-by-version", op.SyncByVersion, true),
			Entry("update-by-timestamp", "update-by-timestamp", op.SyncByTimestamp, true),
			Entry("always", "always", op.SyncAlways, true),
			Entry("compared exactly", "Always", op.SyncAlways, false),
			Entry("empty", "", op.SyncAlways, false),
		)
	})

	Describe("GetMetadata", func() {
		It("gets a user with its version, mtime and document", func() {
			o := get("user", "leseb")
			rec := user("leseb")
			Expect(o.Result.Version).To(Equal(rec.Version))
			Expect(o.Result.Version.Tag).NotTo(BeEmpty())
			Expect(o.Result.Mtime).To(BeTemporally("==", clock))
			doc, ok := o.Result.Doc.(meta.UserCompleteInfo)
			Expect(ok).To(BeTrue(), "Doc is %T", o.Result.Doc)
			Expect(doc.Info.UserID).To(Equal(meta.UserID{ID: "leseb"}))
			var data meta.UserCompleteInfo
			Expect(json.Unmarshal(o.Result.Data, &data)).To(Succeed())
			Expect(data.Info.Email).To(Equal("leseb@example.com"))
		})
		It("gets a bucket entry point and a bucket instance", func() {
			ep, ok := get("bucket", "test").Result.Doc.(meta.BucketEntryPoint)
			Expect(ok).To(BeTrue())
			Expect(ep.Bucket.Name).To(Equal("test"))
			o := get("bucket.instance", inst)
			bci, ok := o.Result.Doc.(meta.BucketCompleteInfo)
			Expect(ok).To(BeTrue(), "Doc is %T", o.Result.Doc)
			Expect(bci.Info.Bucket.Name).To(Equal("test"))
			Expect(o.Result.Version.Ver).NotTo(BeZero())
			Expect(o.Result.Mtime).NotTo(BeZero())
		})
		It("answers NoSuchKey for a missing entry or section, and UnknownError for the top level", func() {
			Expect(run(&op.GetMetadata{Section: "user", Key: "nosuch"})).To(MatchError(op.ErrNoSuchKey))
			Expect(run(&op.GetMetadata{Section: "kittens", Key: "x"})).To(MatchError(op.ErrNoSuchKey))
			err := run(&op.GetMetadata{})
			Expect(op.AsError(err).Code).To(Equal("UnknownError"))
			Expect(op.AsError(err).Status).To(Equal(500))
		})
	})

	Describe("ListMetadata", func() {
		It("lists every key without max-entries", func(sctx SpecContext) {
			o := &op.ListMetadata{Section: "user"}
			Expect(runIn(sctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"admin", "carol", "leseb"}))
			Expect(o.Truncated).To(BeFalse())
			Expect(o.Count).To(Equal(uint64(3)))
		}, SpecTimeout(listTimeout))
		It("pages with max-entries, and a marker continues from the base64 marker", func(sctx SpecContext) {
			o := &op.ListMetadata{Section: "user", Extended: true, MaxEntries: 1}
			Expect(runIn(sctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"admin"}))
			Expect(o.Truncated).To(BeTrue())
			Expect(o.Count).To(Equal(uint64(1)))
			Expect(o.NextMarker).To(Equal("admin"))
			next := &op.ListMetadata{Section: "user", Extended: true, MaxEntries: 5, Marker: base64.StdEncoding.EncodeToString([]byte(o.NextMarker))}
			Expect(runIn(sctx, next)).To(Succeed())
			Expect(next.Keys).To(Equal([]string{"carol", "leseb"}))
			Expect(next.Truncated).To(BeFalse())
			Expect(next.NextMarker).To(BeEmpty())
		}, SpecTimeout(listTimeout))
		DescribeTable("lists from the beginning for a marker from_base64 does not decode",
			func(marker string) {
				o := &op.ListMetadata{Section: "user", Marker: marker}
				Expect(run(o)).To(Succeed())
				Expect(o.Keys).To(Equal([]string{"admin", "carol", "leseb"}))
			},
			Entry("a byte outside the alphabet", "!!!"),
			Entry("one character into a group", "Y"),
			Entry("all '='", "===="),
		)
		It("asks the store for 1000 at a time until it reports no more, without max-entries", func(sctx SpecContext) {
			fake := new(opfakes.FakeMetadataStore)
			fake.ListReturnsOnCall(0, []string{"a"}, "a", true, nil)
			fake.ListReturnsOnCall(1, []string{"b"}, "b", true, nil)
			fake.ListReturnsOnCall(2, []string{"c"}, "c", false, nil)
			env.Metadata = fake
			o := &op.ListMetadata{Section: "user"}
			Expect(runIn(sctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"a", "b", "c"}))
			Expect(fake.ListCallCount()).To(Equal(3))
			for i, marker := range []string{"", "a", "b"} {
				_, section, m, maxEntries := fake.ListArgsForCall(i)
				Expect(section).To(Equal("user"))
				Expect(m).To(Equal(marker), "call %d", i)
				Expect(maxEntries).To(Equal(1000), "call %d", i)
			}
		}, SpecTimeout(listTimeout))
		It("asks for what max-entries leaves, a last time for none, and reports the truncation", func(sctx SpecContext) {
			fake := new(opfakes.FakeMetadataStore)
			fake.ListReturnsOnCall(0, []string{"a", "b"}, "b", true, nil)
			fake.ListReturnsOnCall(1, []string{"c"}, "c", true, nil)
			fake.ListReturnsOnCall(2, nil, "c", true, nil)
			env.Metadata = fake
			o := &op.ListMetadata{Section: "user", Extended: true, MaxEntries: 3}
			Expect(runIn(sctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"a", "b", "c"}))
			Expect(o.Truncated).To(BeTrue())
			Expect(o.Count).To(Equal(uint64(3)))
			Expect(o.NextMarker).To(Equal("c"))
			Expect(fake.ListCallCount()).To(Equal(3))
			for i, want := range []int{3, 1, 0} {
				_, _, _, maxEntries := fake.ListArgsForCall(i)
				Expect(maxEntries).To(Equal(want), "call %d", i)
			}
		}, SpecTimeout(listTimeout))
		It("answers NoSuchKey for an unknown section and passes on a store failure", func(sctx SpecContext) {
			Expect(runIn(sctx, &op.ListMetadata{Section: "kittens"})).To(MatchError(op.ErrNoSuchKey))
			fake := new(opfakes.FakeMetadataStore)
			boom := errors.New("boom")
			fake.ListReturns(nil, "", false, boom)
			env.Metadata = fake
			Expect(runIn(sctx, &op.ListMetadata{Section: "user"})).To(MatchError(boom))
		}, SpecTimeout(listTimeout))
		It("stops when the request's context ends", func() {
			fake := new(opfakes.FakeMetadataStore)
			canceled, cancel := context.WithCancel(ctx)
			DeferCleanup(cancel)
			// The lister reports more for three pages, so a listing that
			// ignores its context ends rather than hangs.
			fake.ListCalls(func(context.Context, string, string, int) ([]string, string, bool, error) {
				cancel()
				return []string{"a"}, "a", fake.ListCallCount() < 3, nil
			})
			env.Metadata = fake
			Expect(runIn(canceled, &op.ListMetadata{Section: "user"})).To(MatchError(context.Canceled))
			Expect(fake.ListCallCount()).To(Equal(1))
		})
		It("lists the sections at the top level, the marker naming the next one", func(sctx SpecContext) {
			o := &op.ListMetadata{}
			Expect(runIn(sctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"bucket", "bucket.instance", "user"}))
			Expect(o.Truncated).To(BeFalse())
			o = &op.ListMetadata{Extended: true, MaxEntries: 1}
			Expect(runIn(sctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"bucket"}))
			Expect(o.Truncated).To(BeTrue())
			Expect(o.NextMarker).To(Equal("bucket.instance"))
			o = &op.ListMetadata{Extended: true, MaxEntries: 1, Marker: base64.StdEncoding.EncodeToString([]byte("bucket.instance"))}
			Expect(runIn(sctx, o)).To(Succeed())
			Expect(o.Keys).To(Equal([]string{"bucket.instance"}), "lower_bound: the marker's own section comes first")
			Expect(o.NextMarker).To(Equal("user"))
		}, SpecTimeout(listTimeout))
	})

	Describe("PutMetadata", func() {
		It("applies a higher version of the same tag, writing it as given", func() {
			old := user("leseb").Version
			incoming := meta.ObjVersion{Ver: old.Ver + 1, Tag: old.Tag}
			o, err := put("user", "leseb", op.SyncByVersion, envelope(incoming, clock, renamed("leseb", "Leseb Two")))
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Applied).To(BeTrue())
			Expect(o.OnDisk).To(Equal(old))
			Expect(o.UpdateStatus()).To(Equal("applied"))
			rec := user("leseb")
			Expect(rec.Info.DisplayName).To(Equal("Leseb Two"))
			Expect(rec.Version).To(Equal(incoming))
		})
		DescribeTable("skips by version, reporting no version read",
			func(ver func(meta.ObjVersion) meta.ObjVersion) {
				old := user("leseb").Version
				o, err := put("user", "leseb", op.SyncByVersion, envelope(ver(old), clock, renamed("leseb", "Changed")))
				Expect(err).NotTo(HaveOccurred())
				Expect(o.Applied).To(BeFalse())
				Expect(o.OnDisk).To(Equal(meta.ObjVersion{}), "put_pre returns before it records the version read")
				Expect(o.UpdateStatus()).To(Equal("skipped"))
				Expect(user("leseb").Info.DisplayName).To(Equal("leseb"))
			},
			Entry("the same version", func(v meta.ObjVersion) meta.ObjVersion { return v }),
			Entry("a lower version", func(v meta.ObjVersion) meta.ObjVersion { return meta.ObjVersion{Ver: v.Ver - 1, Tag: v.Tag} }),
			Entry("a higher version of another tag", func(v meta.ObjVersion) meta.ObjVersion { return meta.ObjVersion{Ver: v.Ver + 5, Tag: "_other"} }),
		)
		DescribeTable("applies by timestamp only a newer mtime",
			func(delta time.Duration, applied bool) {
				o, err := put("user", "leseb", op.SyncByTimestamp, envelope(meta.ObjVersion{}, clock.Add(delta), renamed("leseb", "Changed")))
				Expect(err).NotTo(HaveOccurred())
				Expect(o.Applied).To(Equal(applied))
				want := "leseb"
				if applied {
					want = "Changed"
				}
				Expect(user("leseb").Info.DisplayName).To(Equal(want))
			},
			Entry("newer", time.Second, true),
			Entry("a microsecond newer", time.Microsecond, true),
			Entry("the same", time.Duration(0), false),
			Entry("older", -time.Second, false),
		)
		It("applies always whatever the version and mtime", func() {
			o, err := put("user", "leseb", op.SyncAlways, envelope(meta.ObjVersion{}, clock.Add(-time.Hour), renamed("leseb", "Always")))
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Applied).To(BeTrue())
			Expect(user("leseb").Info.DisplayName).To(Equal("Always"))
		})
		It("writes the entry the request frames, whatever key the body names", func() {
			_, err := put("user", "leseb", op.SyncAlways, func() []byte {
				d, err := json.Marshal(renamed("leseb", "Framed"))
				Expect(err).NotTo(HaveOccurred())
				b, err := json.Marshal(map[string]any{"key": "user:carol", "data": json.RawMessage(d)})
				Expect(err).NotTo(HaveOccurred())
				return b
			}())
			Expect(err).NotTo(HaveOccurred())
			Expect(user("leseb").Info.DisplayName).To(Equal("Framed"))
			Expect(user("carol").Info.DisplayName).To(Equal("carol"))
		})
		DescribeTable("refuses a body RGWMetadataManager::put cannot read with InvalidArgument",
			func(body string) {
				o, err := put("user", "leseb", op.SyncAlways, []byte(body))
				Expect(err).To(MatchError(op.ErrInvalidArgument))
				Expect(o.OnDisk).To(Equal(meta.ObjVersion{}))
				Expect(user("leseb").Info.DisplayName).To(Equal("leseb"))
			},
			Entry("not JSON", `{"data":`),
			Entry("no data", `{"key":"user:leseb","ver":{"tag":"t","ver":2}}`),
			Entry("an array", `[{"data":{}}]`),
			Entry("a ver that is no number", `{"ver":{"tag":"t","ver":"two"},"data":{}}`),
			Entry("a ver with text after its number", `{"ver":{"tag":"t","ver":"2x"},"data":{}}`),
			Entry("a ver tag that is an object", `{"ver":{"tag":{},"ver":2},"data":{}}`),
			Entry("a ver that is no object", `{"ver":2,"data":{}}`),
			Entry("an mtime that is no time", `{"mtime":"yesterday","data":{}}`),
			Entry("an mtime of bare seconds", `{"mtime":"5","data":{}}`),
		)
		It("reads an mtime in either form the get writes, a number included", func() {
			d, err := json.Marshal(renamed("leseb", "Dated"))
			Expect(err).NotTo(HaveOccurred())
			for _, mt := range []string{`"2026-10-11T12:00:01.000000Z"`, strconv.FormatInt(clock.Unix()+1, 10) + `.5`} {
				o, err := put("user", "leseb", op.SyncByTimestamp, []byte(`{"mtime":`+mt+`,"data":`+string(d)+`}`))
				Expect(err).NotTo(HaveOccurred(), "mtime %s", mt)
				Expect(o.Applied).To(BeTrue(), "mtime %s", mt)
			}
		})
		DescribeTable("answers NoSuchKey for a section it does not serve before it reads the body",
			func(body string) {
				fake := new(opfakes.FakeMetadataStore)
				env.Metadata = fake
				o, err := put("kittens", "x", op.SyncByVersion, []byte(body))
				Expect(err).To(MatchError(op.ErrNoSuchKey))
				Expect(o.Applied).To(BeFalse())
				Expect(o.OnDisk).To(Equal(meta.ObjVersion{}))
				Expect(fake.GetCallCount()).To(BeZero())
				Expect(fake.PutCallCount()).To(BeZero())
			},
			Entry("a document update-by-version would skip", `{"data":{}}`),
			Entry("a body that is not JSON", `not json`),
		)
		It("refuses a put on the top level with UnknownError once the body reads", func() {
			_, err := put("", "", op.SyncAlways, []byte(`{"data":{}}`))
			Expect(op.AsError(err).Code).To(Equal("UnknownError"))
			_, err = put("", "", op.SyncAlways, []byte(`{}`))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		})
		It("puts through the store's claims: a held key or email is refused and nothing is written", func() {
			doc := renamed("leseb", "Thief")
			doc.Info.AccessKeys["AKIACAROL"] = meta.AccessKey{ID: "AKIACAROL", Secret: "s", Active: true}
			o, err := put("user", "leseb", op.SyncAlways, envelope(meta.ObjVersion{}, clock, doc))
			Expect(err).To(MatchError(op.ErrKeyExists))
			Expect(o.OnDisk).To(Equal(user("leseb").Version), "the version read before the write")
			Expect(user("leseb").Info.DisplayName).To(Equal("leseb"))

			doc = renamed("leseb", "Thief")
			doc.Info.Email = "CAROL@example.com"
			_, err = put("user", "leseb", op.SyncAlways, envelope(meta.ObjVersion{}, clock, doc))
			Expect(err).To(MatchError(op.ErrEmailExists))
			Expect(user("leseb").Info.Email).To(Equal("leseb@example.com"))
		})
		It("puts under the version it read, with the body's version and mtime", func() {
			fake := new(opfakes.FakeMetadataStore)
			ondisk := meta.ObjVersion{Ver: 7, Tag: "_t"}
			fake.GetReturns(op.MetadataEntry{Version: ondisk, Mtime: clock}, nil)
			env.Metadata = fake
			incoming := meta.ObjVersion{Ver: 8, Tag: "_t"}
			o, err := put("bucket", "test", op.SyncByVersion, envelope(incoming, clock.Add(time.Second), map[string]any{"x": 1}))
			Expect(err).NotTo(HaveOccurred())
			Expect(o.OnDisk).To(Equal(ondisk))
			Expect(fake.PutCallCount()).To(Equal(1))
			_, section, key, e, opts := fake.PutArgsForCall(0)
			Expect(section).To(Equal("bucket"))
			Expect(key).To(Equal("test"))
			Expect(e.Key).To(Equal("test"))
			Expect(e.Version).To(Equal(incoming))
			Expect(e.Mtime).To(BeTemporally("==", clock.Add(time.Second)))
			Expect(string(e.Data)).To(MatchJSON(`{"x":1}`))
			Expect(opts.IfVersion).To(Equal(&ondisk))
		})
		It("creates under the zero version over a missing entry, and passes on any other read failure", func() {
			fake := new(opfakes.FakeMetadataStore)
			fake.GetReturns(op.MetadataEntry{}, op.ErrNoSuchKey)
			env.Metadata = fake
			o, err := put("user", "new", op.SyncAlways, []byte(`{"data":{}}`))
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Applied).To(BeTrue())
			_, _, _, _, opts := fake.PutArgsForCall(0)
			Expect(opts.IfVersion).To(Equal(&meta.ObjVersion{}))

			boom := errors.New("boom")
			fake.GetReturns(op.MetadataEntry{}, boom)
			_, err = put("user", "new", op.SyncAlways, []byte(`{"data":{}}`))
			Expect(err).To(MatchError(boom))
			Expect(fake.PutCallCount()).To(Equal(1))
		})
		It("applies a bucket entry point without an update status, as its handler answers 0", func() {
			e := get("bucket", "test").Result
			o, err := put("bucket", "test", op.SyncAlways, envelope(e.Version, clock, e.Doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Applied).To(BeTrue())
			Expect(o.UpdateStatus()).To(BeEmpty())
			o, err = put("bucket", "test", op.SyncByVersion, envelope(e.Version, clock, e.Doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(o.UpdateStatus()).To(Equal("skipped"))
			bi := get("bucket.instance", inst).Result
			o, err = put("bucket.instance", inst, op.SyncAlways, envelope(meta.ObjVersion{}, clock, bi.Doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(o.UpdateStatus()).To(Equal("applied"))
		})
	})

	Describe("RemoveMetadata", func() {
		It("removes an entry, then the get answers NoSuchKey", func() {
			Expect(run(&op.RemoveMetadata{Section: "user", Key: "carol"})).To(Succeed())
			Expect(run(&op.GetMetadata{Section: "user", Key: "carol"})).To(MatchError(op.ErrNoSuchKey))
			Expect(run(&op.RemoveMetadata{Section: "user", Key: "carol"})).To(MatchError(op.ErrNoSuchKey))
			err := run(&op.RemoveMetadata{})
			Expect(op.AsError(err).Code).To(Equal("UnknownError"))
		})
	})

	DescribeTable("checks radosgw's caps",
		func(o func() op.Op, caps meta.Caps, allowed bool) {
			err := runAs(o(), caps)
			if allowed {
				Expect(errors.Is(err, op.ErrAccessDenied)).To(BeFalse(), "err %v", err)
				return
			}
			Expect(err).To(MatchError(op.ErrAccessDenied))
		},
		Entry("get with metadata=read", func() op.Op { return &op.GetMetadata{Section: "user", Key: "admin"} }, meta.Caps{"metadata": meta.CapRead}, true),
		Entry("get without caps", func() op.Op { return &op.GetMetadata{Section: "user", Key: "admin"} }, meta.Caps{}, false),
		Entry("get with metadata=write", func() op.Op { return &op.GetMetadata{Section: "user", Key: "admin"} }, meta.Caps{"metadata": meta.CapWrite}, false),
		Entry("get with users=*", func() op.Op { return &op.GetMetadata{Section: "user", Key: "admin"} }, meta.Caps{"users": meta.CapAll}, false),
		Entry("list with metadata=read", func() op.Op { return &op.ListMetadata{Section: "user"} }, meta.Caps{"metadata": meta.CapRead}, true),
		Entry("list without caps", func() op.Op { return &op.ListMetadata{Section: "user"} }, meta.Caps{}, false),
		Entry("list with users=*", func() op.Op { return &op.ListMetadata{Section: "user"} }, meta.Caps{"users": meta.CapAll}, false),
		Entry("put with metadata=write", func() op.Op { return &op.PutMetadata{Section: "user", Key: "x", Body: []byte(`{}`)} }, meta.Caps{"metadata": meta.CapWrite}, true),
		Entry("put with metadata=read", func() op.Op { return &op.PutMetadata{Section: "user", Key: "x", Body: []byte(`{}`)} }, meta.Caps{"metadata": meta.CapRead}, false),
		Entry("put without caps", func() op.Op { return &op.PutMetadata{Section: "user", Key: "x", Body: []byte(`{}`)} }, meta.Caps{}, false),
		Entry("put with users=*", func() op.Op { return &op.PutMetadata{Section: "user", Key: "x", Body: []byte(`{}`)} }, meta.Caps{"users": meta.CapAll}, false),
		Entry("remove with metadata=write", func() op.Op { return &op.RemoveMetadata{Section: "user", Key: "x"} }, meta.Caps{"metadata": meta.CapWrite}, true),
		Entry("remove with metadata=read", func() op.Op { return &op.RemoveMetadata{Section: "user", Key: "x"} }, meta.Caps{"metadata": meta.CapRead}, false),
		Entry("remove with buckets=*", func() op.Op { return &op.RemoveMetadata{Section: "user", Key: "x"} }, meta.Caps{"buckets": meta.CapAll}, false),
	)

	It("names each op as radosgw does", func() {
		Expect((&op.GetMetadata{}).Name()).To(Equal("get_metadata"))
		Expect((&op.ListMetadata{}).Name()).To(Equal("list_metadata"))
		Expect((&op.PutMetadata{}).Name()).To(Equal("set_metadata"))
		Expect((&op.RemoveMetadata{}).Name()).To(Equal("remove_metadata"))
	})
})
