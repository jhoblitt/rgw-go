//go:build integration

package integration_test

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/conformance"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/tags"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
	"github.com/jhoblitt/rgw-go/test/gate"
)

// The suite's fixtures, set once by BeforeSuite.
var (
	conf     string
	manifest gate.Manifest
	cluster  radosclient.Cluster
	// store is the driver every spec reads through, its workers running,
	// the control watches among them.
	store *driver.Store
	// runDone receives store.Run's result once it returns.
	runDone chan error
	// usageStore is a second driver with rgw_enable_usage_log set and a
	// flush threshold of 0, so that each logged entry is flushed at once.
	usageStore *driver.Store
	// endpoint is the coexisting radosgw's URL.
	endpoint string
)

// connect connects to the cluster with the site's names, and args, on the
// command line, as Rook starts a radosgw, and closes it when the suite ends.
func connect(ctx context.Context, args ...string) radosclient.Cluster {
	GinkgoHelper()
	site := []string{"--rgw-realm=" + manifest.Realm, "--rgw-zonegroup=" + manifest.ZoneGroup, "--rgw-zone=" + manifest.Zone}
	c, err := goceph.Connect(ctx, goceph.Config{ConfigFile: conf, Args: slices.Concat(site, args)})
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(c.Close()).To(Succeed()) })
	return c
}

// open opens the driver over c and closes it when the suite ends.
func open(ctx context.Context, c radosclient.Cluster) *driver.Store {
	GinkgoHelper()
	s, err := driver.Open(ctx, c, cephconf.NewOptions(c), driver.Options{})
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(s.Close()).To(Succeed()) })
	return s
}

// Built with the integration tag, the suite runs against a cluster or
// fails: it never skips for want of one.
var _ = BeforeSuite(func(ctx SpecContext) {
	conf = cephtest.Conf()
	cephtest.ReadManifest(conf, &manifest)
	// hack/rooket names each release's cluster; any other name is not one
	// the harness brought up.
	Expect(manifest.RooketName).To(Equal("rgw-go-"+manifest.Release), "the manifest's rooket cluster")
	Expect(manifest.Users).NotTo(BeEmpty(), "the manifest lists no users")
	Expect(manifest.Buckets).NotTo(BeEmpty(), "the manifest lists no buckets")

	cluster = connect(ctx)
	store = open(ctx, cluster)
	// The workers outlive BeforeSuite's context, which ends with it.
	runCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	runDone = make(chan error, 1)
	go func() { runDone <- store.Run(runCtx) }()
	DeferCleanup(func() {
		stop()
		Eventually(runDone).WithTimeout(30 * time.Second).WithPolling(100 * time.Millisecond).Should(Receive(Succeed()))
	})

	usageStore = open(ctx, connect(ctx, "--rgw-enable-usage-log=true", "--rgw-usage-log-flush-threshold=0"))

	var err error
	endpoint, err = cephtest.Endpoint(ctx, conf)
	Expect(err).NotTo(HaveOccurred())
})

// admin runs radosgw-admin on the populated site and returns its stdout.
func admin(ctx context.Context, args ...string) []byte {
	GinkgoHelper()
	out, err := cephtest.RadosgwAdmin(ctx, conf, args...)
	Expect(err).NotTo(HaveOccurred())
	return out
}

// adminJSON runs radosgw-admin and decodes its output into v.
func adminJSON(ctx context.Context, v any, args ...string) {
	GinkgoHelper()
	out := admin(ctx, args...)
	Expect(json.Unmarshal(out, v)).To(Succeed(), "decoding radosgw-admin %s: %s", strings.Join(args, " "), out)
}

// canonical is gate.Canonical failing the spec on a document that does not
// decode.
func canonical(b []byte) any {
	GinkgoHelper()
	v, err := gate.Canonical(b)
	Expect(err).NotTo(HaveOccurred())
	return v
}

// expectSameJSON requires that rgw-go's JSON for v equals radosgw-admin's,
// once the keys at the paths in absent, which the running release's dump
// does not write, are dropped from rgw-go's.
func expectSameJSON(what string, v, want any, absent ...string) {
	GinkgoHelper()
	diff, err := gate.DiffJSON(what, v, want, absent...)
	Expect(err).NotTo(HaveOccurred())
	Expect(diff).To(BeEmpty(), "%s differs from radosgw-admin", what)
}

// userArgs names u to radosgw-admin.
func userArgs(u gate.User) []string {
	args := []string{"--uid", u.UID}
	if u.Tenant != "" {
		args = append(args, "--tenant", u.Tenant)
	}
	return args
}

func userID(u gate.User) meta.UserID { return meta.UserID{Tenant: u.Tenant, ID: u.UID} }

// alice is the manifest's untenanted user, who owns plain.
func alice() gate.User {
	GinkgoHelper()
	for _, u := range manifest.Users {
		if u.ID() == "alice" {
			return u
		}
	}
	Fail("the manifest lists no user alice")
	return gate.User{}
}

// s3Client is an S3 client of u's against the coexisting radosgw.
func s3Client(u gate.User) *awss3.Client {
	return awss3.New(awss3.Options{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(endpoint),
		UsePathStyle:     true,
		Credentials:      credentials.NewStaticCredentialsProvider(u.AccessKey, u.SecretKey, ""),
		RetryMaxAttempts: 1,
	})
}

// pool opens p and closes it when the spec ends.
func pool(ctx context.Context, p meta.Pool) radosclient.Pool {
	GinkgoHelper()
	h, err := cluster.Pool(ctx, p.Name, p.NS)
	Expect(err).NotTo(HaveOccurred(), "opening %s", p)
	DeferCleanup(func() { Expect(h.Close()).To(Succeed()) })
	return h
}

// listOIDs lists the names in p's namespace, as rados ls does.
func listOIDs(ctx context.Context, p meta.Pool) []string {
	GinkgoHelper()
	var oids []string
	Expect(pool(ctx, p).ListObjects(ctx, func(oid, _ string) error {
		oids = append(oids, oid)
		return nil
	})).To(Succeed())
	slices.Sort(oids)
	return oids
}

// kubectl runs kubectl against the manifest's rooket cluster, and only it.
func kubectl(ctx context.Context, args ...string) []byte {
	GinkgoHelper()
	cmd := exec.CommandContext(ctx, cmp.Or(os.Getenv("RGW_GO_TEST_ROOKET"), "rooket"), append([]string{"kubectl"}, args...)...)
	cmd.Env = append(os.Environ(), "ROOKET_NAME="+manifest.RooketName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred(), "kubectl %s: %s", strings.Join(args, " "), stderr.String())
	return out
}

// randName is a bucket name no other spec or run uses.
func randName(what string) string { return "rgwgo-" + what + "-" + strings.ToLower(rand.Text()[:10]) }

// radosgwBucket creates a bucket of u's through the coexisting radosgw and
// deletes it through radosgw when the spec ends. The specs that write to a
// bucket write to one of these, so that the population's buckets keep the
// versions radosgw gave them, which other integration specs read.
func radosgwBucket(ctx context.Context, u gate.User, what string) string {
	GinkgoHelper()
	name := randName(what)
	s3c := s3Client(u)
	_, err := s3c.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(name)})
	Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", name)
	DeferCleanup(func(ctx context.Context) {
		_, err := s3c.DeleteBucket(ctx, &awss3.DeleteBucketInput{Bucket: aws.String(name)})
		Expect(err).NotTo(HaveOccurred(), "deleting %s through radosgw", name)
	})
	return name
}

// encodeAt encodes v at the store's release.
func encodeAt(v interface {
	Encode(*denc.Encoder, denc.Release)
},
) []byte {
	e := denc.NewEncoder()
	v.Encode(e, store.Release())
	return e.Bytes()
}

// usageOID is usage_log_hash of index 0: the usage object a flush writes
// its first user's entries to.
func usageOID(user string) (string, error) {
	shards, err := configUint(cluster, "rgw_usage_max_shards")
	if err != nil {
		return "", err
	}
	userShards, err := configUint(cluster, "rgw_usage_max_user_shards")
	if err != nil {
		return "", err
	}
	return "usage." + strconv.FormatUint(uint64((0%userShards+meta.StrHashLinux(user))%shards), 10), nil
}

// usagePool opens the zone's usage log pool and closes it when the spec
// ends.
func usagePool(ctx context.Context) radosclient.Pool {
	GinkgoHelper()
	return pool(ctx, store.ZoneParams().UsageLogPool)
}

// trimUsage removes user's usage records of bucket with
// user_usage_log_trim, which radosgw-admin usage trim cannot do for a
// bucket that does not exist. A trim removes a bounded batch and answers
// ENODATA once nothing is left; a few rounds clear a spec's records.
func trimUsage(ctx context.Context, user, bucket string) {
	GinkgoHelper()
	oid, err := usageOID(user)
	Expect(err).NotTo(HaveOccurred())
	// A cleanup cannot defer the pool's close to the end of the spec.
	p := store.ZoneParams().UsageLogPool
	h, err := cluster.Pool(ctx, p.Name, p.NS)
	Expect(err).NotTo(HaveOccurred(), "opening %s", p)
	defer func() { Expect(h.Close()).To(Succeed()) }()
	for range 4 {
		wop := radosclient.NewWriteOp()
		rgwcls.UsageLogTrim(wop, rgwcls.UsageTrimOp{EndEpoch: math.MaxUint64, User: user, Bucket: bucket}, store.Release())
		_, err := h.Write(ctx, oid, wop, radosclient.OpFlagNone)
		if errors.Is(err, radosclient.ErrNoData) {
			return
		}
		Expect(err).NotTo(HaveOccurred(), "trimming %s's usage of %s in %s", user, bucket, oid)
	}
}

// usageLog is the driver's conformance.UsageLog: usageStore, whose flush
// threshold of 0 flushes each entry as it is logged, and a read of the
// entry's usage object through the pool with user_usage_log_read. The
// records it reads are trimmed when the spec ends.
type usageLog struct{ *driver.Store }

func (u usageLog) Logged(ctx context.Context, user, bucket string) ([]op.UsageRecord, error) {
	DeferCleanup(trimUsage, user, bucket)
	oid, err := usageOID(user)
	if err != nil {
		return nil, err
	}
	rop := radosclient.NewReadOp()
	res := rgwcls.UsageLogRead(rop, rgwcls.UsageReadOp{EndEpoch: math.MaxUint64, Owner: user, Bucket: bucket, MaxEntries: 1000}, u.Release())
	if _, err = usagePool(ctx).Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
		return nil, fmt.Errorf("reading %s: %w", oid, err)
	}
	ret, err := res.Result()
	if err != nil {
		return nil, err
	}
	var out []op.UsageRecord
	for k, e := range ret.Usage {
		r := op.UsageRecord{
			User: k.User, Owner: e.Owner, Payer: e.Payer, Bucket: k.Bucket, Epoch: e.Epoch,
			Total: op.UsageData(e.TotalUsage), Categories: map[string]op.UsageData{},
		}
		for c, d := range e.UsageMap {
			r.Categories[c] = op.UsageData(d)
		}
		out = append(out, r)
	}
	return out, nil
}

// configUint reads a positive integer option through c.
func configUint(c radosclient.Cluster, name string) (uint32, error) {
	s, err := c.ConfigGet(name)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", name, err)
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("%s is %q, not a positive integer", name, s)
	}
	return uint32(n), nil
}

var _ = Describe("the driver against the coexisting radosgw", Label("integration"), func() {
	Describe("on the shared store", func() {
		conformance.Run(func(context.Context) (*op.Env, func()) {
			env := store.Env()
			env.Usage = usageLog{usageStore}
			return env, func() {}
		})
	})

	It("resolves Rook's zoned site as radosgw-admin reads it", func(ctx SpecContext) {
		Expect(store.Zone().Name).To(Equal(manifest.Zone), "zone")
		Expect(store.ZoneGroup().Name).To(Equal(manifest.ZoneGroup), "zonegroup")
		Expect(store.Realm().Name).To(Equal(manifest.Realm), "realm")
		Expect(store.Period().ID).NotTo(BeEmpty(), "period")
		var absent []string
		if store.Release() == denc.Squid {
			absent = gate.SquidZoneDumpLacks
		}
		expectSameJSON("zone get", store.ZoneParams(), canonical(admin(ctx, "zone", "get")), absent...)

		pl, err := store.Placement(meta.PlacementRule{})
		Expect(err).NotTo(HaveOccurred())
		Expect(pl.Rule.Name).To(Equal(store.ZoneGroup().DefaultPlacement.Name), "the default rule")
		Expect(pl.IndexPool).To(Equal(meta.Pool{Name: manifest.Pools.Index}), "the index pool")
		Expect(pl.DataPool).To(Equal(meta.Pool{Name: manifest.Pools.Data}), "the data pool")
		Expect(pl.DataExtraPool).To(Equal(meta.Pool{Name: manifest.Pools.NonEC}), "the data extra pool")
	})

	It("reads the users populate created as radosgw-admin user info prints them", func(ctx SpecContext) {
		for _, u := range manifest.Users {
			want := canonical(admin(ctx, slices.Concat([]string{"user", "info"}, userArgs(u))...))
			byID, err := store.GetUser(ctx, userID(u))
			Expect(err).NotTo(HaveOccurred(), "GetUser %s", u.ID())
			expectSameJSON("GetUser "+u.ID(), byID.Info, want)
			byKey, err := store.GetUserByAccessKey(ctx, u.AccessKey)
			Expect(err).NotTo(HaveOccurred(), "GetUserByAccessKey %s", u.ID())
			expectSameJSON("GetUserByAccessKey "+u.ID(), byKey.Info, want)
		}
	})

	It("finds a user by the email radosgw-admin gives it, in any case", func(ctx SpecContext) {
		u := alice()
		email := "Alice.RGWGO@example.com"
		admin(ctx, slices.Concat([]string{"user", "modify", "--email", email}, userArgs(u))...)
		DeferCleanup(func(ctx context.Context) {
			admin(ctx, slices.Concat([]string{"user", "modify", "--email="}, userArgs(u))...)
		})
		want := canonical(admin(ctx, slices.Concat([]string{"user", "info"}, userArgs(u))...))
		var got *op.UserRecord
		Eventually(func(g Gomega) {
			var err error
			got, err = store.GetUserByEmail(ctx, strings.ToUpper(email))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(got.Info.Email).To(Equal(email))
		}).WithContext(ctx).WithTimeout(10 * time.Second).WithPolling(200 * time.Millisecond).Should(Succeed())
		expectSameJSON("GetUserByEmail "+u.ID(), got.Info, want)
	})

	It("reads the buckets radosgw created as radosgw-admin bucket stats prints them", func(ctx SpecContext) {
		for _, b := range manifest.Buckets {
			var stats struct {
				ID            string `json:"id"`
				Marker        string `json:"marker"`
				Owner         string `json:"owner"`
				PlacementRule string `json:"placement_rule"`
				NumShards     uint32 `json:"num_shards"`
				CreationTime  string `json:"creation_time"`
				Usage         map[string]struct {
					Size       uint64 `json:"size"`
					SizeActual uint64 `json:"size_actual"`
					NumObjects uint64 `json:"num_objects"`
				} `json:"usage"`
			}
			adminJSON(ctx, &stats, "bucket", "stats", "--bucket", b.EntryPointKey())
			rec, err := store.GetBucket(ctx, b.Tenant(), b.Name)
			Expect(err).NotTo(HaveOccurred(), "GetBucket %s", b.EntryPointKey())
			Expect(rec.Info.Bucket.ID).To(Equal(stats.ID), "%s id", b.Name)
			Expect(rec.Info.Bucket.Marker).To(Equal(stats.Marker), "%s marker", b.Name)
			Expect(rec.Info.Owner.String()).To(Equal(stats.Owner), "%s owner", b.Name)
			Expect(rec.Info.PlacementRule.String()).To(Equal(stats.PlacementRule), "%s placement rule", b.Name)
			Expect(rec.Info.Layout.Current.Layout.Normal.NumShards).To(Equal(stats.NumShards), "%s num_shards", b.Name)
			Expect(rec.Info.CreationTime.UTC().Format("2006-01-02T15:04:05.000000Z")).To(Equal(stats.CreationTime), "%s creation time", b.Name)
			main := stats.Usage["rgw.main"]
			Expect(store.BucketStats(ctx, rec)).To(Equal(op.Stats{Size: main.Size, SizeRounded: main.SizeActual, NumObjects: main.NumObjects}),
				"%s stats", b.Name)
		}
	})

	It("lists plain's objects in index order with their sizes, none pending", func(ctx SpecContext) {
		rec, err := store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		var want []op.ObjectEntry
		for _, o := range manifest.ObjectsIn("plain") {
			want = append(want, op.ObjectEntry{Key: meta.ObjKey{Name: o.Key}, Size: o.Size})
		}
		slices.SortFunc(want, func(a, b op.ObjectEntry) int { return strings.Compare(a.Key.IndexKeyName(), b.Key.IndexKeyName()) })
		Expect(want).To(ContainElement(HaveField("Key.Name", "_underscore.bin")), "the manifest's underscore object")

		res, err := store.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 1000})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Truncated).To(BeFalse())
		got := make([]op.ObjectEntry, len(res.Entries))
		for i, e := range res.Entries {
			got[i] = op.ObjectEntry{Key: e.Key, Size: e.Size}
		}
		Expect(got).To(Equal(want))

		// The bucket is quiescent: no index entry holds a pending
		// operation, so the listing had nothing to reconcile.
		var listed []struct {
			Idx   string `json:"idx"`
			Entry struct {
				PendingMap []any `json:"pending_map"`
			} `json:"entry"`
		}
		adminJSON(ctx, &listed, "bi", "list", "--bucket", "plain")
		Expect(listed).To(HaveLen(len(want)))
		for _, l := range listed {
			Expect(l.Entry.PendingMap).To(BeEmpty(), "pending operations on %s", l.Idx)
		}
	})

	It("sees a quota radosgw-admin sets once its notify arrives, without a restart", func(ctx SpecContext) {
		name := radosgwBucket(ctx, alice(), "quota")
		rec, err := store.GetBucket(ctx, "", name)
		Expect(err).NotTo(HaveOccurred(), "priming the cache")
		Expect(rec.Info.Quota.Enabled).To(BeFalse(), "the bucket starts without a quota")

		// An xattr written to the instance behind every gateway's back sends
		// no notify, so only a read from RADOS sees it: a store whose cache
		// is off would see it at once.
		const probe = "user.rgw.rgwgo-cache-probe"
		wop := radosclient.NewWriteOp()
		wop.SetXattr(probe, []byte("1"))
		_, err = pool(ctx, store.ZoneParams().DomainRoot).Write(ctx, rec.Info.Bucket.InstanceOID(), wop, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		cached, err := store.GetBucket(ctx, "", name)
		Expect(err).NotTo(HaveOccurred())
		Expect(cached.Attrs).NotTo(HaveKey(probe), "the primed entry serves the read: the cache is on")

		admin(ctx, "quota", "set", "--bucket", name, "--quota-scope", "bucket", "--max-objects", "7")
		admin(ctx, "quota", "enable", "--bucket", name, "--quota-scope", "bucket")
		Eventually(func(g Gomega) *op.BucketRecord {
			cur, err := store.GetBucket(ctx, "", name)
			g.Expect(err).NotTo(HaveOccurred())
			return cur
		}).WithContext(ctx).WithTimeout(10*time.Second).WithPolling(100*time.Millisecond).Should(And(
			HaveField("Info.Quota.MaxObjects", BeEquivalentTo(7)), HaveField("Info.Quota.Enabled", BeTrue()),
			HaveField("Attrs", HaveKey(probe))), "the notify invalidated the entry and the next read went to RADOS")
	})

	It("reaches the coexisting radosgw's cache with the notify of its own write", func(ctx SpecContext) {
		s3c := s3Client(alice())
		name := radosgwBucket(ctx, alice(), "tags")
		_, err := s3c.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(name)})
		Expect(err).NotTo(HaveOccurred(), "priming radosgw's cache")
		rec, err := store.GetBucket(ctx, "", name)
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Attrs).NotTo(HaveKey(tags.Attr), "the bucket starts untagged")
		Expect(store.PutBucketAttrs(ctx, rec, map[string][]byte{tags.Attr: encodeAt(tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}})}, nil)).To(Succeed())
		Eventually(func(g Gomega) []string {
			out, err := s3c.GetBucketTagging(ctx, &awss3.GetBucketTaggingInput{Bucket: aws.String(name)})
			g.Expect(err).NotTo(HaveOccurred())
			var kv []string
			for _, t := range out.TagSet {
				kv = append(kv, aws.ToString(t.Key)+"="+aws.ToString(t.Value))
			}
			return kv
		}).WithContext(ctx).WithTimeout(10 * time.Second).WithPolling(200 * time.Millisecond).Should(Equal([]string{"k=v"}))
	})

	It("creates a bucket radosgw and radosgw-admin take for their own, and deletes it", func(ctx SpecContext) {
		u := alice()
		owner := meta.UserOwner(userID(u))
		name := randName("create")
		zg := store.ZoneGroup()
		pol := acl.DefaultPolicy(owner, u.UID)
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{
			Name: name, Owner: owner, Zonegroup: zg.ID, Placement: zg.DefaultPlacement,
			Attrs: map[string][]byte{meta.AttrACL: encodeAt(pol)}, Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		deleted := false
		DeferCleanup(func(ctx context.Context) {
			if !deleted {
				cur, gerr := store.GetBucket(ctx, "", name)
				Expect(gerr).NotTo(HaveOccurred())
				Expect(store.DeleteBucket(ctx, cur)).To(Succeed())
			}
		})

		var stats struct {
			ID            string `json:"id"`
			Marker        string `json:"marker"`
			Owner         string `json:"owner"`
			PlacementRule string `json:"placement_rule"`
			NumShards     uint32 `json:"num_shards"`
		}
		adminJSON(ctx, &stats, "bucket", "stats", "--bucket", name)
		Expect(stats.ID).To(Equal(rec.Info.Bucket.ID), "id")
		Expect(stats.Marker).To(Equal(rec.Info.Bucket.Marker), "marker")
		Expect(stats.Owner).To(Equal(u.ID()), "owner")
		Expect(stats.NumShards).To(BeEquivalentTo(11), "num_shards")
		Expect(stats.PlacementRule).To(Equal(zg.DefaultPlacement.String()), "placement rule")
		var owned []string
		adminJSON(ctx, &owned, slices.Concat([]string{"bucket", "list"}, userArgs(u))...)
		Expect(owned).To(ContainElement(name), "bucket list --uid")

		s3c := s3Client(u)
		lb, err := s3c.ListBuckets(ctx, &awss3.ListBucketsInput{})
		Expect(err).NotTo(HaveOccurred())
		var listed []string
		for _, b := range lb.Buckets {
			listed = append(listed, aws.ToString(b.Name))
		}
		Expect(listed).To(ContainElement(name), "ListBuckets through radosgw")
		_, err = s3c.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(name)})
		Expect(err).NotTo(HaveOccurred(), "HeadBucket through radosgw")
		admin(ctx, slices.Concat([]string{"user", "stats", "--sync-stats"}, userArgs(u))...)

		Expect(store.DeleteBucket(ctx, rec)).To(Succeed())
		deleted = true
		owned = nil
		adminJSON(ctx, &owned, slices.Concat([]string{"bucket", "list"}, userArgs(u))...)
		Expect(owned).NotTo(ContainElement(name), "bucket list --uid after the delete")
		Expect(listOIDs(ctx, meta.Pool{Name: manifest.Pools.Index})).NotTo(ContainElement(HavePrefix(".dir."+rec.Info.Bucket.ID)),
			"index shards after the delete")
	})

	It("enforces a user quota radosgw-admin sets and lifts", func(ctx SpecContext) {
		u := alice()
		owner := meta.UserOwner(userID(u))
		rec, err := store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.CheckQuota(ctx, rec, owner, 1, 1)).To(Succeed(), "before the quota")
		DeferCleanup(func(ctx context.Context) {
			admin(ctx, slices.Concat([]string{"quota", "set", "--quota-scope", "user", "--max-objects=-1"}, userArgs(u))...)
			admin(ctx, slices.Concat([]string{"quota", "disable", "--quota-scope", "user"}, userArgs(u))...)
		})
		admin(ctx, slices.Concat([]string{"quota", "set", "--quota-scope", "user", "--max-objects", "0"}, userArgs(u))...)
		admin(ctx, slices.Concat([]string{"quota", "enable", "--quota-scope", "user"}, userArgs(u))...)
		// The quota is read from the user object, whose cache entry the
		// notify invalidates; the stats stay cached, and do not change.
		Eventually(func() error { return store.CheckQuota(ctx, rec, owner, 1, 1) }).
			WithContext(ctx).WithTimeout(10 * time.Second).WithPolling(100 * time.Millisecond).Should(MatchError(op.ErrQuotaExceeded))
		admin(ctx, slices.Concat([]string{"quota", "disable", "--quota-scope", "user"}, userArgs(u))...)
		Eventually(func() error { return store.CheckQuota(ctx, rec, owner, 1, 1) }).
			WithContext(ctx).WithTimeout(10 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())
	})

	It("writes usage radosgw-admin usage show reads", func(ctx SpecContext) {
		u := alice()
		bucket := randName("usage")
		DeferCleanup(trimUsage, u.ID(), bucket)
		usageStore.Log(ctx, op.UsageEntry{
			Owner: meta.UserOwner(userID(u)), Bucket: bucket, Time: time.Now(), Category: "put_obj",
			BytesReceived: 7777, Ops: 1, SuccessfulOps: 1,
		})
		var shown struct {
			Entries []struct {
				User    string `json:"user"`
				Buckets []struct {
					Bucket     string `json:"bucket"`
					Owner      string `json:"owner"`
					Categories []struct {
						Category      string `json:"category"`
						BytesSent     uint64 `json:"bytes_sent"`
						BytesReceived uint64 `json:"bytes_received"`
						Ops           uint64 `json:"ops"`
						SuccessfulOps uint64 `json:"successful_ops"`
					} `json:"categories"`
				} `json:"buckets"`
			} `json:"entries"`
		}
		adminJSON(ctx, &shown, slices.Concat([]string{"usage", "show", "--show-log-entries=true"}, userArgs(u))...)
		type category struct {
			Category                 string
			BytesReceived, Ops, Succ uint64
		}
		var got []category
		for _, e := range shown.Entries {
			for _, b := range e.Buckets {
				if b.Bucket != bucket {
					continue
				}
				Expect(e.User).To(Equal(u.ID()), "the user the entry is filed under")
				Expect(b.Owner).To(Equal(u.ID()), "the entry's owner")
				for _, c := range b.Categories {
					got = append(got, category{c.Category, c.BytesReceived, c.Ops, c.SuccessfulOps})
				}
			}
		}
		Expect(got).To(Equal([]category{{"put_obj", 7777, 1, 1}}))
	})

	It("returns radosgw's NextMarker, the unescaped name, for a page ending at a name starting with an underscore", func(ctx SpecContext) {
		out, err := s3Client(alice()).ListObjects(ctx, &awss3.ListObjectsInput{Bucket: aws.String("plain"), MaxKeys: aws.Int32(1)})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.Contents).To(HaveLen(1))
		Expect(aws.ToString(out.Contents[0].Key)).To(Equal("_underscore.bin"), "the first key")
		Expect(aws.ToString(out.NextMarker)).To(Equal("_underscore.bin"), "radosgw's NextMarker, not the index key __underscore.bin")
		rec, err := store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		res, err := store.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Truncated).To(BeTrue())
		Expect(res.NextMarker).To(Equal(aws.ToString(out.NextMarker)))
	})

	It("keeps its control watches across a restart of the radosgw", func(ctx SpecContext) {
		oids := listOIDs(ctx, store.ZoneParams().ControlPool)
		for i := range 8 {
			Expect(oids).To(ContainElement("notify."+strconv.Itoa(i)), "the control objects")
		}
		deploy := strings.TrimSpace(string(kubectl(ctx, "-n", "rook-ceph", "get", "deploy", "-l", "app=rook-ceph-rgw", "-o", "name")))
		Expect(deploy).To(HavePrefix("deployment.apps/rook-ceph-rgw-"), "one radosgw deployment")
		Expect(deploy).NotTo(ContainSubstring("\n"), "one radosgw deployment")
		kubectl(ctx, "-n", "rook-ceph", "rollout", "restart", deploy)
		kubectl(ctx, "-n", "rook-ceph", "rollout", "status", deploy, "--timeout=5m")
		// The restarted radosgw serves again before the spec ends, so that
		// the specs after it find it.
		Eventually(func() error {
			_, err := s3Client(alice()).HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String("plain")})
			return err
		}).WithContext(ctx).WithTimeout(3 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
		Consistently(runDone).WithTimeout(5*time.Second).WithPolling(100*time.Millisecond).ShouldNot(Receive(), "Store.Run")
		// The watches still deliver: radosgw-admin's notify reaches them.
		name := radosgwBucket(ctx, alice(), "watch")
		_, err := store.GetBucket(ctx, "", name)
		Expect(err).NotTo(HaveOccurred(), "priming the cache")
		admin(ctx, "quota", "set", "--bucket", name, "--quota-scope", "bucket", "--max-objects", "9")
		Eventually(func(g Gomega) int64 {
			rec, err := store.GetBucket(ctx, "", name)
			g.Expect(err).NotTo(HaveOccurred())
			return rec.Info.Quota.MaxObjects
		}).WithContext(ctx).WithTimeout(10 * time.Second).WithPolling(100 * time.Millisecond).Should(BeEquivalentTo(9))
	})
})
