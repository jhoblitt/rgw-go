//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	goadmin "github.com/ceph/go-ceph/rgw/admin"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// The admin gate's own fixtures. Their names are fixed, so a run that
// stopped partway leaves them for the next run to remove first.
const (
	// gateAdmin holds Rook's admin-ops caps, and info, ratelimit and the
	// write caps the trim, index check and metadata requests need.
	gateAdmin     = "rgwgo-admin"
	gateAdminCaps = "accounts=*;buckets=*;users=*;usage=*;metadata=*;zone=read;info=read;ratelimit=*"

	// gateUser owns the buckets every read is of; gateBucket holds two
	// objects and gateEmptyBucket none.
	gateUser        = "admin-gate-u1"
	gateBucket      = "admin-gate-b1"
	gateEmptyBucket = "admin-gate-b2"
	// gateNoCaps holds no caps, gateReader only user-info-without-keys.
	gateNoCaps  = "admin-gate-u2"
	gateReader  = "admin-gate-reader"
	gateAccount = "admin-gate-acct"

	rookUser    = "admin-gate-rook"
	rookUser2   = "admin-gate-rook2"
	rookBucket  = "admin-gate-rook-bucket"
	rookAccount = "admin-gate-rook-acct"

	usageUser   = "admin-gate-usage"
	usageBucket = "admin-gate-usage-bucket"

	gcUser = "admin-gate-gc"
)

var (
	gateAdminKey = keyPair{access: "GATADMINEXAMPLEKEY01", secret: "gatAdminExampleSecret0000000000000000001"}
	gateUserKey  = keyPair{access: "GATU1EXAMPLEKEY00001", secret: "gatU1ExampleSecret000000000000000000001"}
	gateNoCapKey = keyPair{access: "GATU2EXAMPLEKEY00001", secret: "gatU2ExampleSecret000000000000000000001"}
	gateReadKey  = keyPair{access: "GATREADEREXAMPLEKEY1", secret: "gatReaderExampleSecret00000000000000001"}
	usageKey     = keyPair{access: "GATUSAGEEXAMPLEKEY01", secret: "gatUsageExampleSecret00000000000000001"}
	gcKey        = keyPair{access: "GATGCEXAMPLEKEY00001", secret: "gatGcExampleSecret000000000000000000001"}
)

// adminFixtureUsers are every user the gate creates, the ones each
// gateway creates in each format among them.
func adminFixtureUsers() []string {
	users := []string{gateAdmin, gateUser, gateNoCaps, gateReader, rookUser, rookUser2, usageUser, gcUser}
	for _, tag := range []string{"r", "g"} {
		for _, f := range []adminFormat{formatJSON, formatXML} {
			for _, kind := range []string{"ck", "nk"} {
				users = append(users, "admin-gate-"+tag+"-"+string(f)+"-"+kind)
			}
		}
	}
	return users
}

// adminFixtureAccounts are the names of every account the gate creates.
func adminFixtureAccounts() []string {
	accounts := []string{gateAccount, rookAccount}
	for _, tag := range []string{"r", "g"} {
		for _, f := range []adminFormat{formatJSON, formatXML} {
			accounts = append(accounts, "admin-gate-"+tag+"-"+string(f)+"-acct")
		}
	}
	return accounts
}

// removeAdminFixtures removes every user the gate creates with its buckets
// and objects, every account, their usage records and the garbage their
// removal leaves, so that make gate passes after the gate.
func removeAdminFixtures(ctx context.Context) {
	for _, uid := range adminFixtureUsers() {
		if _, err := radosgwAdmin(ctx, "user", "info", "--uid", uid); err == nil {
			tryAdmin(ctx, "user", "rm", "--uid", uid, "--purge-data")
		}
		tryAdmin(ctx, "usage", "trim", "--uid", uid, "--yes-i-really-mean-it")
	}
	for _, name := range adminFixtureAccounts() {
		if _, err := radosgwAdmin(ctx, "account", "get", "--account-name", name); err == nil {
			tryAdmin(ctx, "account", "rm", "--account-name", name)
		}
	}
	gcProcess(ctx)
}

// radosgwAdmin is admin without failing the spec on an error.
func radosgwAdmin(ctx context.Context, args ...string) ([]byte, error) {
	return cephtest.RadosgwAdmin(ctx, conf, args...)
}

// createUser creates uid through radosgw-admin with k and the extra
// arguments.
func createUser(ctx context.Context, uid string, k keyPair, extra ...string) {
	GinkgoHelper()
	admin(ctx, append([]string{
		"user", "create", "--uid", uid, "--display-name", "admin gate " + uid,
		"--access-key", k.access, "--secret-key", k.secret,
	}, extra...)...)
}

// bucketMarker is the marker radosgw-admin bucket stats reports for bucket.
func bucketMarker(ctx context.Context, bucket string) string {
	GinkgoHelper()
	var st struct {
		Marker string `json:"marker"`
	}
	adminJSON(ctx, &st, "bucket", "stats", "--bucket", bucket)
	Expect(st.Marker).NotTo(BeEmpty(), "%s's marker", bucket)
	return st.Marker
}

// bucketGone reports that radosgw-admin finds no bucket of that name.
func bucketGone(ctx context.Context, bucket string) bool {
	_, err := radosgwAdmin(ctx, "bucket", "stats", "--bucket", bucket)
	return err != nil && strings.Contains(err.Error(), errNoSuchBucket)
}

// gcMarkerOIDs are the objects the GC queue names whose names carry marker.
func gcMarkerOIDs(ctx context.Context, marker string) []string {
	GinkgoHelper()
	oids := []string{}
	for _, e := range gcList(ctx) {
		for _, o := range e.Objs {
			if strings.HasPrefix(o.OID, marker+"_") {
				oids = append(oids, o.OID)
			}
		}
	}
	slices.Sort(oids)
	return slices.Compact(oids)
}

// removeDataOIDs deletes oids from the data pool, which a leaking removal
// left with neither an index entry nor a GC entry.
func removeDataOIDs(ctx context.Context, oids []string) {
	GinkgoHelper()
	h, err := cluster.Pool(ctx, manifest.Pools.Data, "")
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(h.Close()).To(Succeed()) }()
	for _, oid := range oids {
		wop := radosclient.NewWriteOp()
		wop.Remove()
		_, err := h.Write(ctx, oid, wop, radosclient.OpFlagNone)
		if !isNotFound(err) {
			Expect(err).NotTo(HaveOccurred(), "removing %s", oid)
		}
	}
}

// accountIDPattern is an account id as radosgw generates it.
var accountIDPattern = regexp.MustCompile(`^RGW[0-9]{17}$`)

// readCase is one request the body oracle sends to both gateways.
type readCase struct {
	what   string
	method string
	path   string
	// key signs the request; the gate's admin when nil.
	key   *keyPair
	rules headerRules
	// mask names the values beyond the request's own that differ between
	// the two gateways' answers to it: rgw-go's listing markers are its
	// own (docs/exclusions.md, "Admin listing markers are rgw-go's own").
	mask []string
	// exclusion is the docs/exclusions.md entry that records how rgw-go's
	// answer differs, "" when it must not. Its difference is applied to
	// radosgw's answer, by strip or by oracle, and rgw-go's must then equal
	// the result.
	exclusion string
	// strip removes from radosgw's body only what the exclusion says rgw-go
	// leaves out, or masks only what it says rgw-go masks; it fails when it
	// changes nothing.
	strip func([]byte) []byte
	// oracle is the request whose radosgw answer stands for rgw-go's, when
	// the exclusion makes rgw-go answer as radosgw answers another request.
	oracle *oracleRequest
	// status is radosgw's status, 200 when zero, so that a request both
	// gateways refuse alike cannot pass for one they serve.
	status int
	// emptyOnRadosgw is radosgw answering with no body.
	emptyOnRadosgw bool
}

// oracleRequest is a request sent to radosgw alone.
type oracleRequest struct{ method, path string }

var _ = Describe("the admin gate", Ordered, ContinueOnFailure, Label("integration", "rgw-go"), func() {
	var (
		radosgw, rgwgo adminGateway
		release        denc.Release
		b1Instance     string
		accountID      string
	)

	BeforeAll(func(ctx SpecContext) {
		radosgw = adminGateway{name: "radosgw", tag: "r", url: endpoint}
		rgwgo = adminGateway{name: "rgw-go", tag: "g", url: rgwgoAdminEndpoint()}
		release = store.Release()

		removeAdminFixtures(ctx)
		DeferCleanup(removeAdminFixtures)

		createUser(ctx, gateAdmin, gateAdminKey, "--caps", gateAdminCaps)
		createUser(ctx, gateUser, gateUserKey, "--email", gateUser+"@example.com", "--max-buckets", "5",
			"--caps", "usage=read")
		createUser(ctx, gateNoCaps, gateNoCapKey)
		createUser(ctx, gateReader, gateReadKey, "--caps", "user-info-without-keys=read")
		admin(ctx, "quota", "set", "--quota-scope=user", "--uid", gateUser, "--max-objects", "100", "--max-size", "1048576")
		admin(ctx, "quota", "enable", "--quota-scope=user", "--uid", gateUser)
		admin(ctx, "ratelimit", "set", "--ratelimit-scope=user", "--uid", gateUser, "--max-read-ops", "10", "--max-write-bytes", "4096")
		admin(ctx, "ratelimit", "enable", "--ratelimit-scope=user", "--uid", gateUser)

		s3c := s3At(endpoint, gateUserKey)
		for _, b := range []string{gateBucket, gateEmptyBucket} {
			_, err := s3c.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(b)})
			Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", b)
		}
		for key, size := range map[string]int{"obj1": 1, "obj2": 64 << 10} {
			_, err := s3c.PutObject(ctx, &awss3.PutObjectInput{
				Bucket: aws.String(gateBucket), Key: aws.String(key), Body: bytes.NewReader(randBytes(size)),
			})
			Expect(err).NotTo(HaveOccurred(), "writing %s/%s through radosgw", gateBucket, key)
		}
		admin(ctx, "ratelimit", "set", "--ratelimit-scope=bucket", "--bucket", gateBucket, "--max-write-ops", "20")
		admin(ctx, "ratelimit", "enable", "--ratelimit-scope=bucket", "--bucket", gateBucket)
		var st struct {
			ID string `json:"id"`
		}
		adminJSON(ctx, &st, "bucket", "stats", "--bucket", gateBucket)
		b1Instance = gateBucket + ":" + st.ID

		var acct struct {
			ID string `json:"id"`
		}
		adminJSON(ctx, &acct, "account", "create", "--account-name", gateAccount)
		accountID = acct.ID
		Expect(accountID).To(MatchRegexp(accountIDPattern.String()))

		// radosgw logs gateUser's writes to its usage log within a tick.
		Eventually(func(g Gomega) {
			var u struct {
				Summary []struct {
					User string `json:"user"`
				} `json:"summary"`
			}
			g.Expect(json.Unmarshal(admin(ctx, "usage", "show", "--uid", gateUser), &u)).To(Succeed())
			g.Expect(u.Summary).NotTo(BeEmpty())
		}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(Succeed())
	})

	It("reports the cluster's fsid on both gateways", func(ctx SpecContext) {
		fsid := cephFSID(ctx)
		Expect(fsid).To(MatchRegexp(`^[0-9a-f-]{36}$`))
		for _, g := range []adminGateway{radosgw, rgwgo} {
			info, err := goCephAdmin(g, gateAdminKey).GetInfo(ctx)
			Expect(err).NotTo(HaveOccurred(), "GetInfo on %s", g.name)
			Expect(info.InfoSpec.StorageBackends).To(ConsistOf(goadmin.StorageBackend{Name: "rados", ClusterID: fsid}),
				"GetInfo on %s", g.name)
		}
	})

	It("serves the admin calls Rook makes, in Rook's order", func(ctx SpecContext) {
		api := goCephAdmin(rgwgo, gateAdminKey)
		maxBuckets := 10
		By("creating and reading the user")
		u, err := api.CreateUser(ctx, goadmin.User{
			ID: rookUser, DisplayName: "rook user", MaxBuckets: &maxBuckets, UserCaps: "users=read;buckets=*",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(u.Keys).To(HaveLen(1))

		u, err = api.GetUser(ctx, goadmin.User{ID: rookUser})
		Expect(err).NotTo(HaveOccurred())
		Expect(u.Caps).To(ConsistOf(goadmin.UserCapSpec{Type: "buckets", Perm: "*"}, goadmin.UserCapSpec{Type: "users", Perm: "read"}))
		Expect(u.Keys).To(ConsistOf(HaveField("User", rookUser)))
		Expect(u.OpMask).To(Equal("read, write, delete"))
		Expect(u.DefaultPlacement).To(BeEmpty())
		Expect(u.MaxBuckets).To(HaveValue(Equal(10)))
		userKey := keyPair{access: u.Keys[0].AccessKey, secret: u.Keys[0].SecretKey}

		By("modifying, capping and keying the user")
		noBuckets := -1
		u, err = api.ModifyUser(ctx, goadmin.User{ID: rookUser, MaxBuckets: &noBuckets})
		Expect(err).NotTo(HaveOccurred())
		Expect(u.MaxBuckets).To(HaveValue(Equal(-1)))

		caps, err := api.RemoveUserCap(ctx, rookUser, "users=read")
		Expect(err).NotTo(HaveOccurred())
		Expect(caps).To(ConsistOf(goadmin.UserCapSpec{Type: "buckets", Perm: "*"}))
		caps, err = api.AddUserCap(ctx, rookUser, "usage=read")
		Expect(err).NotTo(HaveOccurred())
		Expect(caps).To(ConsistOf(goadmin.UserCapSpec{Type: "buckets", Perm: "*"}, goadmin.UserCapSpec{Type: "usage", Perm: "read"}))

		enabled, maxObjects, maxSize := true, int64(50), int64(1<<20)
		Expect(api.SetUserQuota(ctx, goadmin.QuotaSpec{
			UID: rookUser, QuotaType: "user", Enabled: &enabled, MaxObjects: &maxObjects, MaxSize: &maxSize,
		})).To(Succeed())
		q, err := api.GetUserQuota(ctx, goadmin.QuotaSpec{UID: rookUser, QuotaType: "user"})
		Expect(err).NotTo(HaveOccurred())
		Expect(q.Enabled).To(HaveValue(BeTrue()))
		Expect(q.MaxObjects).To(HaveValue(Equal(maxObjects)))
		Expect(q.MaxSize).To(HaveValue(Equal(maxSize)))

		keys, err := api.CreateKey(ctx, goadmin.UserKeySpec{UID: rookUser, KeyType: "s3"})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(HaveValue(HaveLen(2)))
		var added string
		for _, k := range *keys {
			if k.AccessKey != userKey.access {
				added = k.AccessKey
			}
		}
		Expect(api.RemoveKey(ctx, goadmin.UserKeySpec{UID: rookUser, KeyType: "s3", AccessKey: added})).To(Succeed())
		u, err = api.GetUser(ctx, goadmin.User{ID: rookUser})
		Expect(err).NotTo(HaveOccurred())
		Expect(u.Keys).To(ConsistOf(HaveField("AccessKey", userKey.access)))

		By("refusing a bucket to a user whose max_buckets is negative, on both gateways")
		for _, g := range []adminGateway{radosgw, rgwgo} {
			_, err = s3At(g.url, userKey).CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(rookBucket)})
			Expect(err).To(MatchError(ContainSubstring("StatusCode: 403")), "CreateBucket on %s with max_buckets -1", g.name)
		}
		maxBuckets = 5
		u, err = api.ModifyUser(ctx, goadmin.User{ID: rookUser, MaxBuckets: &maxBuckets})
		Expect(err).NotTo(HaveOccurred())
		Expect(u.MaxBuckets).To(HaveValue(Equal(5)))

		By("creating a bucket through S3 with the user's key")
		s3c := s3At(rgwgo.url, userKey)
		_, err = s3c.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(rookBucket)})
		Expect(err).NotTo(HaveOccurred())
		_, err = s3c.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(rookBucket), Key: aws.String("k"), Body: bytes.NewReader([]byte("rook")),
		})
		Expect(err).NotTo(HaveOccurred())

		By("reading, limiting, linking and removing the bucket")
		b, err := api.GetBucketInfo(ctx, goadmin.Bucket{Bucket: rookBucket})
		Expect(err).NotTo(HaveOccurred())
		Expect(b.Owner).To(Equal(rookUser))
		Expect(b.Usage.RgwMain.NumObjects).To(HaveValue(Equal(uint64(1))))
		Expect(b.Usage.RgwMain.Size).To(HaveValue(Equal(uint64(4))))
		Expect(b.BucketQuota.Enabled).To(HaveValue(BeFalse()))

		bucketObjects := int64(3)
		Expect(api.SetIndividualBucketQuota(ctx, goadmin.QuotaSpec{
			UID: rookUser, Bucket: rookBucket, Enabled: &enabled, MaxObjects: &bucketObjects,
		})).To(Succeed())
		b, err = api.GetBucketInfo(ctx, goadmin.Bucket{Bucket: rookBucket})
		Expect(err).NotTo(HaveOccurred())
		Expect(b.BucketQuota.Enabled).To(HaveValue(BeTrue()))
		Expect(b.BucketQuota.MaxObjects).To(HaveValue(Equal(bucketObjects)))

		_, err = api.CreateUser(ctx, goadmin.User{ID: rookUser2, DisplayName: "rook user 2"})
		Expect(err).NotTo(HaveOccurred())
		Expect(api.LinkBucket(ctx, goadmin.BucketLinkInput{Bucket: rookBucket, BucketID: b.ID, UID: rookUser2})).To(Succeed())
		b, err = api.GetBucketInfo(ctx, goadmin.Bucket{Bucket: rookBucket})
		Expect(err).NotTo(HaveOccurred())
		Expect(b.Owner).To(Equal(rookUser2))

		p, err := api.GetBucketPolicy(ctx, goadmin.Bucket{Bucket: rookBucket})
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Owner.ID).To(Equal(rookUser2), "a link gives the bucket the new owner's default ACL")

		buckets, err := api.ListBuckets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(buckets).To(ContainElement(rookBucket))

		purge := true
		Expect(api.RemoveBucket(ctx, goadmin.Bucket{Bucket: rookBucket, PurgeObject: &purge})).To(Succeed())
		Expect(bucketGone(ctx, rookBucket)).To(BeTrue(), "%s after RemoveBucket", rookBucket)

		Expect(api.RemoveUser(ctx, goadmin.User{ID: rookUser2})).To(Succeed())
		Expect(api.RemoveUser(ctx, goadmin.User{ID: rookUser})).To(Succeed())
		_, err = api.GetUser(ctx, goadmin.User{ID: rookUser})
		Expect(err).To(MatchError(goadmin.ErrNoSuchUser))

		By("creating, reading, changing and deleting an account")
		acct, err := api.CreateAccount(ctx, goadmin.Account{Name: rookAccount})
		Expect(err).NotTo(HaveOccurred())
		Expect(acct.ID).To(MatchRegexp(accountIDPattern.String()))
		Expect(acct.Name).To(Equal(rookAccount))
		accountBuckets := int64(7)
		acct, err = api.ModifyAccount(ctx, goadmin.Account{ID: acct.ID, MaxBuckets: &accountBuckets})
		Expect(err).NotTo(HaveOccurred())
		Expect(acct.MaxBuckets).To(HaveValue(Equal(accountBuckets)))
		if release < denc.Tentacle {
			// Squid's radosgw checks a cap type no caps command can grant for
			// both (docs/ceph-upstream-bugs.md, "Squid's account admin API
			// checks a cap type that does not exist for get and delete").
			_, err = api.GetAccount(ctx, acct.ID)
			Expect(err).To(MatchError(goadmin.ErrAccessDenied))
			Expect(api.DeleteAccount(ctx, acct.ID)).To(MatchError(goadmin.ErrAccessDenied))
		} else {
			got, getErr := api.GetAccount(ctx, acct.ID)
			Expect(getErr).NotTo(HaveOccurred())
			Expect(got.Name).To(Equal(rookAccount))
			Expect(got.MaxBuckets).To(HaveValue(Equal(accountBuckets)))
			quotaSize := int64(1 << 20)
			Expect(api.SetAccountQuota(ctx, goadmin.AccountQuotaSpec{
				ID: acct.ID, QuotaType: goadmin.AccountQuotaTypeAccount, Enabled: &enabled, MaxSize: &quotaSize,
			})).To(Succeed())
			got, err = api.GetAccount(ctx, acct.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Quota.Enabled).To(HaveValue(BeTrue()))
			Expect(got.Quota.MaxSize).To(HaveValue(Equal(quotaSize)))
			Expect(api.DeleteAccount(ctx, acct.ID)).To(Succeed())
		}

		users, err := api.GetUsers(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(users).To(HaveValue(ContainElement(gateAdmin)))
	})

	It("answers every request as radosgw does, in JSON, XML and HTML", func(ctx SpecContext) {
		formats := []adminFormat{formatJSON, formatXML}
		// Squid's radosgw checks a cap no caps command grants for an account
		// get (docs/ceph-upstream-bugs.md, "Squid's account admin API checks
		// a cap type that does not exist for get and delete").
		accountGetStatus := http.StatusOK
		if release < denc.Tentacle {
			accountGetStatus = http.StatusForbidden
		}
		// Every difference is collected, so that one run reports them all.
		var failures []string
		check := func(f func()) { failures = append(failures, InterceptGomegaFailures(f)...) }
		// radosgw's account modify answers a name another account holds with
		// the S3 table's 409 BucketAlreadyExists (docs/ceph-upstream-bugs.md,
		// "radosgw's account modify answers BucketAlreadyExists for a taken
		// name or email"), and rgw-go answers it alike.
		const renameToTaken = "account rename to a taken name"

		// Each gateway creates and changes fixtures of its own, alike but for
		// the tag in their names, and the two answers are compared with what
		// differs between two such fixtures masked.
		for _, f := range formats {
			resp := map[string][2]adminResponse{}
			for i, g := range []adminGateway{radosgw, rgwgo} {
				ck, nk := fixtureName(g, f, "ck"), fixtureName(g, f, "nk")
				ckKey, addKey := fixtureKey(g.tag, f, "c"), fixtureKey(g.tag, f, "a")
				send := func(what, method, path string) {
					r := resp[what]
					r[i] = adminCall(ctx, g, gateAdminKey, method, withFormat(path, f))
					resp[what] = r
				}
				send("user create with keys", http.MethodPut, "/admin/user?uid="+ck+"&display-name=Gate+"+ck+
					"&email="+ck+"@example.com&access-key="+ckKey.access+"&secret-key="+ckKey.secret+
					"&max-buckets=5&user-caps=usage=read")
				send("user create without keys", http.MethodPut, "/admin/user?uid="+nk+"&display-name=Gate+"+nk+"&generate-key=false")
				send("user modify", http.MethodPost, "/admin/user?uid="+nk+"&display-name=Changed+"+nk+"&max-buckets=9")
				send("key create", http.MethodPut, "/admin/user?key&uid="+nk+"&access-key="+addKey.access+"&secret-key="+addKey.secret)
				send("caps add", http.MethodPut, "/admin/user?caps&uid="+nk+"&user-caps="+url.QueryEscape("buckets=read;metadata=*"))
				send("user info with stats", http.MethodGet, "/admin/user?uid="+ck+"&stats=true")
				send("account create", http.MethodPost, "/admin/account?name="+fixtureName(g, f, "acct"))
				id := jsonField(resp["account create"][i].body, "id")
				Expect(id).To(MatchRegexp(accountIDPattern.String()), "%s's account create in %s: %s", g.name, f, resp["account create"][i].body)
				send("account modify", http.MethodPut, "/admin/account?id="+id+"&max-buckets=7&email="+fixtureName(g, f, "acct")+"@example.com")
				send(renameToTaken, http.MethodPut, "/admin/account?id="+id+"&name="+gateAccount)
				if release >= denc.Tentacle {
					send("account quota set", http.MethodPut, "/admin/account?quota&id="+id+"&quota-type=account&max-size=1048576&enabled=true")
					send("account get", http.MethodGet, "/admin/account?id="+id)
				}
			}
			for what, r := range resp {
				want, code := http.StatusOK, ""
				if what == renameToTaken {
					want, code = http.StatusConflict, "BucketAlreadyExists"
				}
				what = fmt.Sprintf("%s in %s", what, f)
				check(func() {
					Expect(r[0].status).To(Equal(want), "%s: radosgw's status: %s", what, r[0].body)
					Expect(string(r[0].body)).To(ContainSubstring(code), "%s: radosgw's error code", what)
				})
				check(func() { expectAdminHeaders(what, f, headerRules{}, r[0], r[1]) })
				check(func() { expectSameBody(what, normaliseFixture, r[0], r[1]) })
			}
		}

		reads := []readCase{
			{what: "info", path: "/admin/info"},
			{what: "zone config", path: "/admin/config?type=zone", rules: headerRules{untypedOnRadosgw: true}},
			{what: "user info", path: "/admin/user?uid=" + gateUser},
			{what: "user info with stats", path: "/admin/user?uid=" + gateUser + "&stats=true"},
			{
				what: "user info without keys", path: "/admin/user?uid=" + gateUser, key: &gateReadKey,
				exclusion: "The admin user document withholds the Swift TempURL keys with the other keys",
				strip:     stripTempURLKeys,
			},
			{what: "user info by access key", path: "/admin/user?access-key=" + gateUserKey.access},
			{what: "user quota", path: "/admin/user?quota&uid=" + gateUser + "&quota-type=user"},
			{what: "user bucket quota", path: "/admin/user?quota&uid=" + gateUser + "&quota-type=bucket"},
			{what: "bucket info", path: "/admin/bucket?bucket=" + gateBucket},
			{what: "bucket list", path: "/admin/bucket"},
			{what: "bucket list with stats", path: "/admin/bucket?stats=true"},
			{what: "a uid's buckets", path: "/admin/bucket?uid=" + gateUser},
			{what: "a uid's buckets with stats", path: "/admin/bucket?uid=" + gateUser + "&stats=true"},
			{what: "a uid's buckets paged", path: "/admin/bucket?uid=" + gateUser + "&max-entries=1"},
			{what: "bucket policy", path: "/admin/bucket?policy&bucket=" + gateBucket},
			{what: "object policy", path: "/admin/bucket?policy&bucket=" + gateBucket + "&object=obj1"},
			{what: "bucket index check", path: "/admin/bucket?index&bucket=" + gateBucket + "&check-objects=true&fix=true"},
			{what: "account get", path: "/admin/account?id=" + accountID, status: accountGetStatus},
			{what: "metadata get user", path: "/admin/metadata/user?key=" + gateUser},
			{what: "metadata get bucket", path: "/admin/metadata/bucket?key=" + gateBucket},
			{what: "metadata get bucket instance", path: "/admin/metadata/bucket.instance?key=" + b1Instance},
			{
				what: "metadata sections", path: "/admin/metadata",
				exclusion: "The admin metadata API serves four sections, and differs from radosgw's in five more ways",
				strip:     stripUnservedSections,
			},
			{what: "metadata list users", path: "/admin/metadata/user"},
			{what: "metadata list buckets", path: "/admin/metadata/bucket"},
			{what: "metadata list paged", path: "/admin/metadata/user?max-entries=2", mask: []string{"marker"}},
			{what: "user list", path: "/admin/user?list"},
			{what: "user list paged", path: "/admin/user?list&max-entries=2", mask: []string{"marker"}},
			{what: "usage", path: "/admin/usage?uid=" + gateUser + "&show-summary=false"},
			{what: "usage with summary", path: "/admin/usage?uid=" + gateUser + "&show-summary=true"},
			{what: "realm", path: "/admin/realm", rules: headerRules{jsonType: true, getter: true}},
			{what: "realm list", path: "/admin/realm?list", rules: headerRules{jsonType: true, getter: true}},
			{
				// populate.sh gives the zonegroup a cloud-s3 tier with a secret.
				what: "period", path: "/admin/realm/period", rules: headerRules{jsonType: true, getter: true},
				exclusion: "The period getter masks a cloud tier's S3 secret",
				strip:     maskTierSecrets,
			},
			{what: "bucket ratelimit", path: "/admin/ratelimit?ratelimit-scope=bucket&bucket=" + gateBucket},
			{what: "user ratelimit", path: "/admin/ratelimit?ratelimit-scope=user&uid=" + gateUser},
			{what: "global ratelimit", path: "/admin/ratelimit?global=true"},

			{what: "AccessDenied", path: "/admin/user?uid=" + gateUser, key: &gateNoCapKey, status: http.StatusForbidden},
			{what: "NoSuchKey", path: "/admin/metadata/user?key=admin-gate-missing", status: http.StatusNotFound},
			{what: "NoSuchBucket", path: "/admin/bucket?bucket=admin-gate-missing", status: http.StatusNotFound},
			{what: "405 on info", method: http.MethodPost, path: "/admin/info", status: http.StatusMethodNotAllowed},
			{what: "405 on config", method: http.MethodPost, path: "/admin/config?type=zone", status: http.StatusMethodNotAllowed},
			{what: "405 on metadata", method: http.MethodPost, path: "/admin/metadata/user", status: http.StatusMethodNotAllowed},
			{what: "405 on usage", method: http.MethodPut, path: "/admin/usage", status: http.StatusMethodNotAllowed},
			{what: "405 on ratelimit", method: http.MethodPut, path: "/admin/ratelimit", status: http.StatusMethodNotAllowed},
			{what: "405 on realm", method: http.MethodPut, path: "/admin/realm", status: http.StatusMethodNotAllowed},
			{
				// radosgw's Sync_Bucket answers a single zone 200 with no body,
				// which no strip turns into rgw-go's 405; rgw-go answers as
				// radosgw answers any route it has no op for.
				what: "405 on bucket sync", method: http.MethodPut, path: "/admin/bucket?sync&bucket=" + gateBucket,
				exclusion: "Multisite: the admin Sync_Bucket op", emptyOnRadosgw: true,
				oracle: &oracleRequest{method: http.MethodPost, path: "/admin/info"},
			},
			{
				what: "405 on an unregistered resource", path: "/admin/admin-gate-missing", rules: headerRules{jsonType: true},
				status: http.StatusMethodNotAllowed,
			},
			{what: "405 on the admin root", path: "/admin/", rules: headerRules{jsonType: true}, status: http.StatusMethodNotAllowed},
		}
		for _, f := range formats {
			for _, c := range reads {
				expectSameAnswer(ctx, check, radosgw, rgwgo, c, f)
			}
		}
		for _, c := range []readCase{
			{what: "info", path: "/admin/info"},
			{what: "NoSuchBucket", path: "/admin/bucket?bucket=admin-gate-missing", status: http.StatusNotFound},
		} {
			expectSameAnswer(ctx, check, radosgw, rgwgo, c, formatHTML)
		}
		Expect(failures).To(BeEmpty(), strings.Join(failures, "\n\n"))
	})

	It("pages a metadata listing to the keys radosgw lists whole", func(ctx SpecContext) {
		whole := adminCall(ctx, radosgw, gateAdminKey, http.MethodGet, "/admin/metadata/user")
		Expect(whole.status).To(Equal(http.StatusOK))
		var want []string
		Expect(json.Unmarshal(whole.body, &want)).To(Succeed())

		var got []string
		marker := ""
		for pages := 0; ; pages++ {
			Expect(pages).To(BeNumerically("<", len(want)+2), "rgw-go's listing does not end")
			path := "/admin/metadata/user?max-entries=2"
			if marker != "" {
				path += "&marker=" + url.QueryEscape(marker)
			}
			r := adminCall(ctx, rgwgo, gateAdminKey, http.MethodGet, path)
			Expect(r.status).To(Equal(http.StatusOK), "%s: %s", path, r.body)
			var page struct {
				Keys      []string `json:"keys"`
				Truncated bool     `json:"truncated"`
				Marker    string   `json:"marker"`
			}
			Expect(json.Unmarshal(r.body, &page)).To(Succeed())
			Expect(len(page.Keys)).To(BeNumerically("<=", 2))
			got = append(got, page.Keys...)
			if !page.Truncated {
				break
			}
			marker = page.Marker
		}
		Expect(got).To(ConsistOf(want))
	})

	It("logs a write to the usage log and trims it within the bound", func(ctx SpecContext) {
		createUser(ctx, usageUser, usageKey)
		s3c := s3At(rgwgo.url, usageKey)
		_, err := s3c.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(usageBucket)})
		Expect(err).NotTo(HaveOccurred())
		_, err = s3c.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(usageBucket), Key: aws.String("k"), Body: bytes.NewReader([]byte("usage")),
		})
		Expect(err).NotTo(HaveOccurred())

		api := goCephAdmin(rgwgo, gateAdminKey)
		show := true
		Eventually(func(g Gomega) []string {
			u, getErr := api.GetUsage(ctx, goadmin.Usage{UserID: usageUser, ShowSummary: &show})
			g.Expect(getErr).NotTo(HaveOccurred())
			var categories []string
			for _, s := range u.Summary {
				for _, c := range s.Categories {
					categories = append(categories, c.Category)
				}
			}
			return categories
		}).WithContext(ctx).WithTimeout(15*time.Second).WithPolling(500*time.Millisecond).Should(ContainElement("put_obj"),
			"rgw-go flushes its usage log each rgw_usage_log_tick_interval, 1 s under the parity options")

		removeAll := true
		start := time.Now()
		Expect(api.TrimUsage(ctx, goadmin.Usage{UserID: usageUser, RemoveAll: &removeAll})).To(Succeed())
		Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second))
		u, err := api.GetUsage(ctx, goadmin.Usage{UserID: usageUser, ShowSummary: &show})
		Expect(err).NotTo(HaveOccurred())
		Expect(u.Entries).To(BeEmpty())
	})

	It("removes buckets through the GC with purge-objects and around it with bypass-gc", func(ctx SpecContext) {
		createUser(ctx, gcUser, gcKey)
		tailed := func(g adminGateway, bucket string) (marker string, tails []string) {
			GinkgoHelper()
			s3c := s3At(g.url, gcKey)
			_, err := s3c.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)})
			Expect(err).NotTo(HaveOccurred(), "creating %s through %s", bucket, g.name)
			for _, key := range []string{"a", "b"} {
				// Past rgw_max_chunk_size, 4 MiB, so each object has a tail stripe.
				_, err := s3c.PutObject(ctx, &awss3.PutObjectInput{
					Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(randBytes(5 << 20)),
				})
				Expect(err).NotTo(HaveOccurred(), "writing %s/%s through %s", bucket, key, g.name)
			}
			marker = bucketMarker(ctx, bucket)
			for _, oid := range markerOIDs(ctx, marker) {
				if oid != marker+"_a" && oid != marker+"_b" {
					tails = append(tails, oid)
				}
			}
			Expect(tails).To(HaveLen(2), "%s's tail stripes", bucket)
			return marker, tails
		}

		for _, g := range []adminGateway{rgwgo, radosgw} {
			purged := "admin-gate-purge-" + g.tag + "-" + strings.ToLower(rand.Text()[:6])
			marker, tails := tailed(g, purged)
			r := adminCall(ctx, g, gateAdminKey, http.MethodDelete, "/admin/bucket?bucket="+purged+"&purge-objects=true")
			Expect(r.status).To(Equal(http.StatusOK), "%s's purge: %s", g.name, r.body)
			Expect(bucketGone(ctx, purged)).To(BeTrue(), "%s after %s's purge", purged, g.name)
			Expect(gcMarkerOIDs(ctx, marker)).To(Equal(tails), "%s's purge sends the tails to the GC", g.name)

			bypassed := "admin-gate-bypass-" + g.tag + "-" + strings.ToLower(rand.Text()[:6])
			marker, tails = tailed(g, bypassed)
			// What a removal leaks is in no index and no GC entry, so the
			// user's removal would leave it behind.
			DeferCleanup(func(ctx context.Context) { removeDataOIDs(ctx, markerOIDs(ctx, marker)) })
			r = adminCall(ctx, g, gateAdminKey, http.MethodDelete, "/admin/bucket?bucket="+bypassed+"&bypass-gc=true")
			Expect(r.status).To(Equal(http.StatusOK), "%s's bypass-gc removal: %s", g.name, r.body)
			Expect(bucketGone(ctx, bypassed)).To(BeTrue(), "%s after %s's bypass-gc removal", bypassed, g.name)
			Expect(gcMarkerOIDs(ctx, marker)).To(BeEmpty(), "%s's bypass-gc removal queues nothing", g.name)
			left := markerOIDs(ctx, marker)
			if g == rgwgo {
				Expect(left).To(BeEmpty(), "rgw-go's bypass-gc removal deletes every head and tail")
				continue
			}
			// docs/ceph-upstream-bugs.md, "radosgw's admin API bypass-gc
			// removal leaks a tail and runs unbounded": the first object's
			// tail stripes stay, with no index entry and no GC entry.
			Expect(left).To(HaveLen(1), "radosgw's bypass-gc removal leaks one object's tail")
			Expect(tails).To(ContainElement(left[0]))
		}
	})
})

// expectSameAnswer sends c to both gateways in f and compares the answers:
// the status, the normalised bodies and the headers. For a case an
// exclusion covers, rgw-go's answer is compared with radosgw's once the
// recorded difference is applied to it.
func expectSameAnswer(ctx context.Context, check func(func()), radosgw, rgwgo adminGateway, c readCase, f adminFormat) {
	GinkgoHelper()
	method := c.method
	if method == "" {
		method = http.MethodGet
	}
	k := gateAdminKey
	if c.key != nil {
		k = *c.key
	}
	path := withFormat(c.path, f)
	rgw := adminCall(ctx, radosgw, k, method, path)
	rgwgoResp := adminCall(ctx, rgwgo, k, method, path)
	what := fmt.Sprintf("%s (%s %s)", c.what, method, path)
	normalise := normaliseRead
	if len(c.mask) > 0 {
		normalise = newMasker(append(append([]string{}, requestNames...), c.mask...)...).mask
	}
	want := c.status
	if want == 0 {
		want = http.StatusOK
	}
	check(func() {
		Expect(rgw.status).To(Equal(want), "%s: radosgw's status: %s", what, rgw.body)
		if c.emptyOnRadosgw {
			Expect(rgw.body).To(BeEmpty(), "%s: radosgw's body", what)
		} else {
			Expect(rgw.body).NotTo(BeEmpty(), "%s: radosgw's body", what)
		}
	})

	oracle := rgw
	switch {
	case c.oracle != nil:
		oracle = adminCall(ctx, radosgw, k, c.oracle.method, withFormat(c.oracle.path, f))
	case c.strip != nil:
		check(func() {
			stripped := c.strip(rgw.body)
			Expect(stripped).NotTo(Equal(rgw.body), "%s: radosgw's body no longer holds what docs/exclusions.md %q says rgw-go leaves out", what, c.exclusion)
			oracle = adminResponse{status: rgw.status, header: rgw.header, body: stripped}
		})
	}
	if c.exclusion != "" {
		AddReportEntry(what, fmt.Sprintf("compared with radosgw's answer as docs/exclusions.md %q records the difference", c.exclusion))
	}
	headers := rgw
	if c.oracle != nil {
		headers = oracle
	}
	check(func() { expectAdminHeaders(what, f, c.rules, headers, rgwgoResp) })
	check(func() { expectSameBody(what, normalise, oracle, rgwgoResp) })
}

var (
	tempURLKeys = regexp.MustCompile(`"temp_url_keys":\[[^\]]*\],|(?s:<temp_url_keys>.*?</temp_url_keys>)`)
	// radosgw lists the sections in name order, user last, so each one
	// rgw-go does not serve is followed by a comma in JSON.
	unservedSections = regexp.MustCompile(`"(?:account|group|roles|topic)",|<key>(?:account|group|roles|topic)</key>`)
)

// stripTempURLKeys removes the temp_url_keys member or element, which
// radosgw writes for a caller it withholds the other keys from.
func stripTempURLKeys(b []byte) []byte { return tempURLKeys.ReplaceAll(b, nil) }

// stripUnservedSections removes the metadata sections radosgw lists and
// rgw-go does not serve.
func stripUnservedSections(b []byte) []byte { return unservedSections.ReplaceAll(b, nil) }
