//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

// layoutGateUser signs every request of the ?layout and otp gate. radosgw
// refuses ?layout to a user without the admin=read cap, which no caps
// command can grant (docs/ceph-upstream-bugs.md, "radosgw's ?layout checks a
// cap type no caps command can grant"), and lets an admin user past that
// refusal
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L228-L236 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L228-L239 at v20.2.4).
// The otp section needs metadata=*. The user's name and keys are fixed, so a
// run that stopped partway leaves it for the next run to remove first.
const layoutGateUser = "layout-otp-gate"

var layoutGateKey = keyPair{access: "LAYOUTGATEEXAMPLEKEY", secret: "layoutGateExampleSecret" + strings.Repeat("0", 16) + "1"}

// The tier a cloud-transitioned head's manifest names. Its secret is a
// constant of this file and opens nothing.
const (
	layoutTierKey    = "LAYOUTGATETIERKEY1"
	layoutTierSecret = "layout-gate-tier-secret"
	layoutMask       = "******"
)

// RFC 6238's SHA-1 test key, "12345678901234567890", as the hex and the
// base32 radosgw-admin mfa create takes.
const (
	otpGateHexSeed    = "3132333435363738393031323334353637383930"
	otpGateBase32Seed = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
)

// removeLayoutGateUser removes layoutGateUser with its buckets and objects.
func removeLayoutGateUser(ctx context.Context) {
	if _, err := radosgwAdmin(ctx, "user", "info", "--uid", layoutGateUser); err == nil {
		tryAdmin(ctx, "user", "rm", "--uid", layoutGateUser, "--purge-data")
	}
}

// trimLayoutGateUsage removes layoutGateUser's usage records.
func trimLayoutGateUsage(ctx context.Context) {
	tryAdmin(ctx, "usage", "trim", "--uid", layoutGateUser, "--yes-i-really-mean-it")
}

// adminSend is adminCall for a request with a body, whose SHA-256 the
// signature covers.
func adminSend(ctx context.Context, g adminGateway, k keyPair, method, pathQuery string, body []byte) adminResponse {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, method, g.url+pathQuery, bytes.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", hash)
	Expect(v4.NewSigner().SignHTTP(ctx, k.creds(), req, hash, "s3", "default", time.Now())).To(Succeed())
	resp, err := rawClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "%s %s on %s", method, pathQuery, g.name)
	defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
	b, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred(), "reading %s %s on %s", method, pathQuery, g.name)
	return adminResponse{status: resp.StatusCode, header: resp.Header, body: b}
}

// layoutGet is g's ?layout of bucket's key.
func layoutGet(ctx context.Context, g adminGateway, bucket, key string) adminResponse {
	GinkgoHelper()
	return adminCall(ctx, g, layoutGateKey, http.MethodGet, "/"+bucket+"/"+key+"?layout")
}

// layoutHeaders are the response headers a ?layout answer is compared by:
// the document's type and length, and whether it is offered as ranges.
var layoutHeaders = []string{"Content-Type", "Content-Length", "Accept-Ranges"}

// expectSameLayout requires rgw-go's ?layout answer to equal radosgw's: both
// 200, the same headers, a Content-Length that is the body's, and the same
// body to the byte.
func expectSameLayout(what string, radosgw, rgwgo adminResponse) {
	GinkgoHelper()
	Expect(radosgw.status).To(Equal(http.StatusOK), "%s: radosgw's status; body %s", what, radosgw.body)
	Expect(rgwgo.status).To(Equal(http.StatusOK), "%s: rgw-go's status; body %s", what, rgwgo.body)
	for _, name := range layoutHeaders {
		Expect(rgwgo.header.Values(name)).To(Equal(radosgw.header.Values(name)), "%s: %s", what, name)
	}
	Expect(rgwgo.header.Get("Content-Length")).To(Equal(strconv.Itoa(len(rgwgo.body))), "%s: rgw-go's Content-Length", what)
	Expect(radosgw.header.Get("Content-Length")).To(Equal(strconv.Itoa(len(radosgw.body))), "%s: radosgw's Content-Length", what)
	Expect(string(rgwgo.body)).To(Equal(string(radosgw.body)), "%s: the ?layout body", what)
}

// layoutHead is a ?layout body's head: the RADOS object the object's head
// lives in. A key that starts with "_" has its head stored under an object
// locator, which Loc names and a read of the oid alone does not find.
type layoutHead struct {
	Head struct {
		Pool string `json:"pool"`
		OID  string `json:"oid"`
		Loc  string `json:"loc"`
	} `json:"head"`
}

func layoutHeadOf(body []byte) layoutHead {
	GinkgoHelper()
	var h layoutHead
	Expect(json.Unmarshal(body, &h)).To(Succeed(), "decoding a ?layout body: %s", body)
	Expect(h.Head.OID).NotTo(BeEmpty(), "the head in %s", body)
	return h
}

// layoutHeadXattrs reads h's xattrs as RADOS holds them.
func layoutHeadXattrs(ctx context.Context, h layoutHead) map[string][]byte {
	GinkgoHelper()
	rop := radosclient.NewReadOp()
	x := rop.GetXattrs()
	_, err := pool(ctx, meta.Pool{Name: h.Head.Pool}).WithLocator(h.Head.Loc).Read(ctx, h.Head.OID, rop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "reading %s's xattrs", h.Head.OID)
	Expect(x.Err).NotTo(HaveOccurred())
	return x.Xattrs
}

// layoutHeadData reads all of h's data.
func layoutHeadData(ctx context.Context, h layoutHead) []byte {
	GinkgoHelper()
	p := pool(ctx, meta.Pool{Name: h.Head.Pool}).WithLocator(h.Head.Loc)
	rop := radosclient.NewReadOp()
	st := rop.Stat()
	_, err := p.Read(ctx, h.Head.OID, rop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "statting %s", h.Head.OID)
	Expect(st.Err).NotTo(HaveOccurred())
	rop = radosclient.NewReadOp()
	r := rop.Read(0, st.Size)
	_, err = p.Read(ctx, h.Head.OID, rop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "reading %s", h.Head.OID)
	Expect(r.Err).NotTo(HaveOccurred())
	return r.Data
}

// layoutRewriteHead replaces h's data and sets xattrs on it. A cleanup calls
// it, so it opens the pool itself and closes it before it returns.
func layoutRewriteHead(ctx context.Context, h layoutHead, data []byte, xattrs map[string][]byte) {
	GinkgoHelper()
	p, err := cluster.Pool(ctx, h.Head.Pool, "")
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(p.Close()).To(Succeed()) }()
	wop := radosclient.NewWriteOp()
	wop.WriteFull(data)
	for name, value := range xattrs {
		wop.SetXattr(name, value)
	}
	_, err = p.WithLocator(h.Head.Loc).Write(ctx, h.Head.OID, wop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "rewriting %s", h.Head.OID)
}

// layoutCapture is one ?layout answer as the gate prints it, one JSON line
// after the marker layoutMarker, so that a run's log holds the bodies and
// the stored manifests that produced them. The bodies are strings, which
// keeps their bytes exact where a JSON number would lose them.
type layoutCapture struct {
	Release string `json:"release"`
	Shape   string `json:"shape"`
	Bucket  string `json:"bucket"`
	Key     string `json:"key"`
	// ManifestHex is the head's user.rgw.manifest as RADOS holds it.
	ManifestHex string `json:"manifest_hex"`
	// Radosgw is radosgw's body.
	Radosgw string `json:"radosgw,omitempty"`
	// RGWGo is rgw-go's body, left out where it equals radosgw's.
	RGWGo string `json:"rgwgo,omitempty"`
	Note  string `json:"note,omitempty"`
}

const layoutMarker = "LAYOUT-GOLDEN-JSON "

// printLayoutCapture prints c through GinkgoWriter.
func printLayoutCapture(c layoutCapture) {
	GinkgoHelper()
	line, err := json.Marshal(c)
	Expect(err).NotTo(HaveOccurred())
	GinkgoWriter.Printf("%s%s\n", layoutMarker, line)
}

// layoutCompare asks both gateways for bucket's key's layout, prints what the
// two answered under shape, and requires them to be the same.
func layoutCompare(ctx context.Context, radosgw, rgwgo adminGateway, shape, bucket, key string) {
	GinkgoHelper()
	want := layoutGet(ctx, radosgw, bucket, key)
	got := layoutGet(ctx, rgwgo, bucket, key)
	if want.status == http.StatusOK {
		stored := layoutHeadXattrs(ctx, layoutHeadOf(want.body))[meta.AttrManifest]
		Expect(stored).NotTo(BeEmpty(), "%s: the manifest stored in the head", shape)
		c := layoutCapture{
			Release: manifest.Release, Shape: shape, Bucket: bucket, Key: key,
			ManifestHex: hex.EncodeToString(stored), Radosgw: string(want.body),
		}
		if !bytes.Equal(want.body, got.body) {
			c.RGWGo = string(got.body)
		}
		printLayoutCapture(c)
	}
	expectSameLayout(shape+" ("+bucket+"/"+key+")", want, got)
}

// layoutWrite is one object rgw-go writes for the gate: its shape, key and
// storage class, and the sizes of the parts of a multipart upload or the one
// size of a PUT.
type layoutWrite struct {
	shape, key, class string
	sizes             []int
	multipart         bool
	compressible      bool
}

// layoutWrites are the shapes of object whose layout differs: one in the
// head, none, one whose key starts with an underscore, one exactly filling
// the head, one a byte over, a striped one, a multipart one, and one for
// each codec the population compresses with.
func layoutWrites() []layoutWrite {
	w := []layoutWrite{
		{shape: "in-head", key: "in-head.bin", sizes: []int{1 << 10}},
		{shape: "empty", key: "empty.bin", sizes: []int{0}},
		{shape: "underscore", key: "_underscore.bin", sizes: []int{16}},
		{shape: "head-full", key: "head-full.bin", sizes: []int{4 << 20}},
		{shape: "one-byte-tail", key: "one-byte-tail.bin", sizes: []int{4<<20 + 1}},
		{shape: "striped", key: "striped.bin", sizes: []int{10 << 20}},
		{shape: "multipart", key: "multipart.bin", sizes: []int{5 << 20, 5 << 20, 1 << 20}, multipart: true},
	}
	for _, class := range slices.Sorted(maps.Keys(manifest.StorageClasses)) {
		codec := manifest.StorageClasses[class]
		w = append(w, layoutWrite{
			shape: "compressed-" + codec, key: "compressed-" + codec + ".bin", class: class,
			sizes: []int{1 << 20}, compressible: true,
		})
	}
	return w
}

// layoutBytes is the n bytes of w's data.
func (w layoutWrite) layoutBytes(n int) []byte {
	if !w.compressible {
		return randBytes(n)
	}
	return bytes.Repeat([]byte("rgw-go layout gate "), n/19+1)[:n]
}

// layoutPut writes w to bucket through c.
func layoutPut(ctx context.Context, c *awss3.Client, bucket string, w layoutWrite) {
	GinkgoHelper()
	var class types.StorageClass
	if w.class != "" {
		class = types.StorageClass(w.class)
	}
	if !w.multipart {
		_, err := c.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(w.key), StorageClass: class, Body: bytes.NewReader(w.layoutBytes(w.sizes[0])),
		})
		Expect(err).NotTo(HaveOccurred(), "writing %s through rgw-go", w.key)
		return
	}
	created, err := c.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(w.key), StorageClass: class,
	})
	Expect(err).NotTo(HaveOccurred(), "beginning %s through rgw-go", w.key)
	var parts []types.CompletedPart
	for i, n := range w.sizes {
		number := aws.Int32(int32(i + 1))
		part, uploadErr := c.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket: aws.String(bucket), Key: aws.String(w.key), UploadId: created.UploadId, PartNumber: number,
			Body: bytes.NewReader(w.layoutBytes(n)),
		})
		Expect(uploadErr).NotTo(HaveOccurred(), "writing %s's part %d through rgw-go", w.key, i+1)
		parts = append(parts, types.CompletedPart{ETag: part.ETag, PartNumber: number})
	}
	_, err = c.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(w.key), UploadId: created.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	Expect(err).NotTo(HaveOccurred(), "completing %s through rgw-go", w.key)
}

// layoutTier is the cloud tier a head's manifest names: the tier retains the
// head, which is the only reason a transitioned head exists to be read, and
// carries an S3 key and its secret.
func layoutTier() meta.ObjTier {
	tier := meta.NewZoneGroupPlacementTier()
	tier.TierType, tier.StorageClass, tier.RetainHeadObject = meta.TierTypeCloudS3, "CLOUDTIER", true
	tier.S3.Endpoint, tier.S3.TargetPath = "http://127.0.0.1:1", "rgw-go-cloud"
	tier.S3.Key.ID, tier.S3.Key.Secret = layoutTierKey, layoutTierSecret
	return meta.ObjTier{Name: "CLOUDTIER", Tier: tier}
}

// layoutStoredManifest decodes a head's stored manifest.
func layoutStoredManifest(stored []byte) meta.Manifest {
	GinkgoHelper()
	d := denc.NewDecoder(stored)
	m := meta.DecodeManifest(d)
	Expect(d.Err()).NotTo(HaveOccurred(), "decoding the stored manifest")
	return m
}

func layoutEncode(m meta.Manifest, rel denc.Release) []byte {
	e := denc.NewEncoder()
	m.Encode(e, rel)
	return e.Bytes()
}

// layoutTierManifest is the stored manifest with the tier named in it, its
// stripes and rules kept.
func layoutTierManifest(stored []byte, rel denc.Release) []byte {
	m := layoutStoredManifest(stored)
	m.TierType, m.TierConfig = meta.TierTypeCloudS3, layoutTier()
	return layoutEncode(m, rel)
}

// layoutTransitionManifest is the manifest a cloud transition writes over a
// head it keeps: a new manifest of size 0, with no rules, that names the
// head, places both head and tail in the tier's storage class, and holds the
// tier
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2720-L2734 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3541-L3555 at v20.2.4).
func layoutTransitionManifest(stored []byte, rel denc.Release) []byte {
	was := layoutStoredManifest(stored)
	target := meta.PlacementRule{Name: was.HeadPlacementRule.Name, StorageClass: layoutTier().Name}
	m := meta.NewManifest()
	m.Obj, m.HeadPlacementRule = was.Obj, target
	m.TailPlacement = meta.BucketPlacement{Bucket: was.Obj.Bucket, PlacementRule: target}
	m.TierType, m.TierConfig = meta.TierTypeCloudS3, layoutTier()
	return layoutEncode(m, rel)
}

// otpSeed is dev's seed bytes, which the class parses by the seed type.
func otpSeed(dev otp.OTPInfo) []byte {
	GinkgoHelper()
	var b []byte
	var err error
	if dev.SeedType == otp.SeedBase32 {
		b, err = base32.StdEncoding.DecodeString(dev.Seed)
	} else {
		b, err = hex.DecodeString(dev.Seed)
	}
	Expect(err).NotTo(HaveOccurred(), "the seed of %s", dev.ID)
	return b
}

// otpCode is the six-digit code dev accepts at the time at: the HOTP code of
// the step the class counts, (time - time_ofs) / step_size.
func otpCode(dev otp.OTPInfo, at time.Time) string {
	return otptest.HOTP(otpSeed(dev), uint64((at.Unix()-int64(dev.TimeOfs))/int64(dev.StepSize)), 6)
}

// otpStepEnd is when the step of dev that holds at ends.
func otpStepEnd(dev otp.OTPInfo, at time.Time) time.Time {
	step := int64(dev.StepSize)
	return time.Unix(int64(dev.TimeOfs)+((at.Unix()-int64(dev.TimeOfs))/step+1)*step, 0)
}

// waitStepRoom waits for a step of dev with at least room left, and returns
// when that step ends. The class treats a code it accepted as unused again
// once the step turns (docs/ceph-upstream-bugs.md, "cls_otp computes the
// replay index from an unsigned window distance"), so a spec that needs a
// refusal runs inside one step.
func waitStepRoom(ctx context.Context, dev otp.OTPInfo, room time.Duration) time.Time {
	GinkgoHelper()
	step := time.Duration(dev.StepSize) * time.Second
	Eventually(func() time.Duration { return time.Until(otpStepEnd(dev, time.Now())) }).
		WithContext(ctx).WithTimeout(step+5*time.Second).WithPolling(250*time.Millisecond).
		Should(BeNumerically(">=", room), "a step of %s with %s left", dev.ID, room)
	return otpStepEnd(dev, time.Now())
}

func otpGateDevice(id string, seedType otp.SeedType, seed string, step, window uint32) otp.OTPInfo {
	return otp.OTPInfo{Type: otp.TypeTOTP, ID: id, Seed: seed, SeedType: seedType, StepSize: step, Window: window}
}

// otpCreate is radosgw-admin mfa create of dev for uid.
func otpCreate(ctx context.Context, uid string, dev otp.OTPInfo) {
	GinkgoHelper()
	seedType := "hex"
	if dev.SeedType == otp.SeedBase32 {
		seedType = "base32"
	}
	admin(ctx, "mfa", "create", "--uid", uid, "--totp-serial", dev.ID, "--totp-seed", dev.Seed, "--totp-seed-type", seedType,
		"--totp-seconds", strconv.FormatUint(uint64(dev.StepSize), 10), "--totp-window", strconv.FormatUint(uint64(dev.Window), 10))
}

func otpListed(ctx context.Context, uid string) []otp.OTPInfo {
	GinkgoHelper()
	var out struct {
		Entries []otp.OTPInfo `json:"entries"`
	}
	adminJSON(ctx, &out, "mfa", "list", "--uid", uid)
	return out.Entries
}

// otpCheck is radosgw-admin mfa check of pin: nil for an accepted pin, else
// radosgw-admin's error.
func otpCheck(ctx context.Context, uid, serial, pin string) error {
	_, err := radosgwAdmin(ctx, "mfa", "check", "--uid", uid, "--totp-serial", serial, "--totp-pin", pin)
	return err
}

// otpDevices are the devices in an otp metadata get's body.
func otpDevices(body []byte) []otp.OTPInfo {
	GinkgoHelper()
	var doc struct {
		Data struct {
			Devices []otp.OTPInfo `json:"devices"`
		} `json:"data"`
	}
	Expect(json.Unmarshal(body, &doc)).To(Succeed(), "decoding an otp metadata document")
	return doc.Data.Devices
}

// withData is the metadata document got, a get's body, with its data
// replaced by data. A put keeps the document's version and mtime.
func withData(got []byte, data any) []byte {
	GinkgoHelper()
	doc := map[string]json.RawMessage{}
	if got != nil {
		Expect(json.Unmarshal(got, &doc)).To(Succeed(), "decoding a metadata document")
	}
	b, err := json.Marshal(data)
	Expect(err).NotTo(HaveOccurred())
	doc["data"] = b
	out, err := json.Marshal(doc)
	Expect(err).NotTo(HaveOccurred())
	return out
}

// otpDocument is the data of an otp document holding devs.
func otpDocument(devs ...otp.OTPInfo) map[string][]otp.OTPInfo {
	return map[string][]otp.OTPInfo{"devices": append([]otp.OTPInfo{}, devs...)}
}

// withMFAIDs is the user metadata document got with its mfa_ids set to ids.
func withMFAIDs(got []byte, ids ...string) []byte {
	GinkgoHelper()
	var doc struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	Expect(json.Unmarshal(got, &doc)).To(Succeed(), "decoding a user metadata document")
	Expect(doc.Data).NotTo(BeEmpty(), "the user metadata document's data")
	b, err := json.Marshal(ids)
	Expect(err).NotTo(HaveOccurred())
	doc.Data["mfa_ids"] = b
	return withData(got, doc.Data)
}

// expectSamePut requires rgw-go's answer to a metadata put or remove to
// equal radosgw's: the status, the two headers radosgw reports the put with,
// and the body.
func expectSamePut(what string, radosgw, rgwgo adminResponse) {
	GinkgoHelper()
	Expect(rgwgo.status).To(Equal(radosgw.status), "%s: status; radosgw's body %s, rgw-go's %s", what, radosgw.body, rgwgo.body)
	for _, name := range []string{"RGWX_UPDATE_STATUS", "RGWX_UPDATE_VERSION"} {
		Expect(rgwgo.header.Values(name)).To(Equal(radosgw.header.Values(name)), "%s: %s", what, name)
	}
	Expect(string(rgwgo.body)).To(Equal(string(radosgw.body)), "%s: body", what)
}

func otpMetadataKeys(body []byte) []string {
	GinkgoHelper()
	var keys []string
	Expect(json.Unmarshal(body, &keys)).To(Succeed(), "decoding an otp metadata list: %s", body)
	return keys
}

// otpUser creates a user for one spec's devices. When the spec ends it
// removes the user and its otp object, which a user's removal leaves.
func otpUser(ctx context.Context, what string) string {
	GinkgoHelper()
	uid := randName("otp-" + what)
	admin(ctx, "user", "create", "--uid", uid, "--display-name", "layout gate "+what)
	DeferCleanup(removeOTPUser, uid)
	return uid
}

// removeOTPUser removes uid and its otp object, whatever state a spec left it
// in.
func removeOTPUser(ctx context.Context, uid string) {
	tryAdmin(ctx, "user", "rm", "--uid", uid, "--purge-data")
	p := store.ZoneParams().OTPPool
	h, err := cluster.Pool(ctx, p.Name, p.NS)
	Expect(err).NotTo(HaveOccurred(), "opening %s", p)
	defer func() { Expect(h.Close()).To(Succeed()) }()
	wop := radosclient.NewWriteOp()
	wop.Remove()
	_, err = h.Write(ctx, "user:"+uid, wop, radosclient.OpFlagNone)
	if !isNotFound(err) {
		Expect(err).NotTo(HaveOccurred(), "removing %s's otp object", uid)
	}
}

// The ?layout gate sends every populated object's ?layout, and the layout of
// objects rgw-go writes, to radosgw and to rgw-go, and requires the same
// bytes from both. The otp gate makes a device through each gateway's side
// and requires the other to read, list, check, keep and remove it as it was
// made.
var _ = Describe("the ?layout and otp gate", Ordered, ContinueOnFailure, Label("integration", "rgw-go", "phase2", "layout", "otp"), func() {
	var (
		radosgw, rgwgo adminGateway
		// bucket is layoutGateUser's, made through radosgw, and holds what
		// rgw-go writes.
		bucket string
	)

	BeforeAll(func(ctx SpecContext) {
		removeLayoutGateUser(ctx)
		trimLayoutGateUsage(ctx)
		gcProcess(ctx)
		// Cleanups run last registered first, so the garbage is collected
		// once the user and its usage are gone.
		DeferCleanup(gcProcess)
		DeferCleanup(trimLayoutGateUsage)
		DeferCleanup(removeLayoutGateUser)
		createUser(ctx, layoutGateUser, layoutGateKey, "--admin=true", "--caps", "metadata=*")
		radosgw = adminGateway{name: "radosgw", tag: "r", url: endpoint}
		rgwgo = adminGateway{name: "rgw-go", tag: "g", url: rgwgoAdminEndpoint()}
		bucket = randName("layout")
		_, err := s3At(radosgw.url, layoutGateKey).CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)})
		Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", bucket)
	})

	gateway := func(name string) adminGateway {
		GinkgoHelper()
		g := map[string]adminGateway{"radosgw": radosgw, "rgw-go": rgwgo}[name]
		Expect(g.url).NotTo(BeEmpty(), "no gateway %q", name)
		return g
	}

	Describe("?layout", func() {
		It("answers every populated object's layout as radosgw does, byte for byte", func(ctx SpecContext) {
			Expect(manifest.Objects).NotTo(BeEmpty())
			// Every difference is collected, so that one run reports them all.
			var failures []string
			for _, o := range manifest.Objects {
				failures = append(failures, InterceptGomegaFailures(func() {
					layoutCompare(ctx, radosgw, rgwgo, "populated-"+strings.TrimSuffix(o.Key, ".bin"), o.Bucket, o.Key)
				})...)
			}
			Expect(failures).To(BeEmpty(), strings.Join(failures, "\n\n"))
		})

		It("answers the layout of every shape of object rgw-go writes as radosgw does, byte for byte", func(ctx SpecContext) {
			s3c := s3At(rgwgo.url, layoutGateKey)
			writes := layoutWrites()
			for _, w := range writes {
				layoutPut(ctx, s3c, bucket, w)
			}
			var failures []string
			for _, w := range writes {
				failures = append(failures, InterceptGomegaFailures(func() {
					layoutCompare(ctx, radosgw, rgwgo, "rgwgo-"+w.shape, bucket, w.key)
				})...)
			}
			Expect(failures).To(BeEmpty(), strings.Join(failures, "\n\n"))
		})

		// radosgw writes a cloud tier's S3 key and secret in the clear into
		// the ?layout of a head a cloud transition kept (docs/ceph-upstream-bugs.md,
		// "radosgw's ?layout hands a cloud tier's S3 secret to any holder of
		// admin=read"); rgw-go masks the secret (docs/differences.md,
		// "?layout masks a cloud tier's S3 secret"). The head is a
		// radosgw-written object whose manifest is rewritten to name the
		// tier. A transition also writes the retained head empty, so that
		// entry empties it; the spec puts back the data and xattrs it read
		// before, when it ends.
		DescribeTable("masks the secret in the layout of a head that names a cloud tier, where radosgw writes it in the clear",
			func(ctx SpecContext, shape string, rewrite func([]byte, denc.Release) []byte, emptyHead bool) {
				key := shape + ".bin"
				_, err := s3At(radosgw.url, layoutGateKey).PutObject(ctx, &awss3.PutObjectInput{
					Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(randBytes(1 << 10)),
				})
				Expect(err).NotTo(HaveOccurred(), "writing %s through radosgw", key)
				head := layoutHeadOf(layoutGet(ctx, radosgw, bucket, key).body)
				xattrs, data := layoutHeadXattrs(ctx, head), layoutHeadData(ctx, head)
				stored := xattrs[meta.AttrManifest]
				Expect(stored).NotTo(BeEmpty(), "%s's stored manifest", head.Head.OID)
				tiered := rewrite(stored, store.Release())
				DeferCleanup(func(ctx context.Context) { layoutRewriteHead(ctx, head, data, xattrs) })
				if emptyHead {
					layoutRewriteHead(ctx, head, []byte{}, map[string][]byte{meta.AttrManifest: tiered})
					Expect(layoutHeadData(ctx, head)).To(BeEmpty(), "%s after the rewrite", head.Head.OID)
				} else {
					layoutRewriteHead(ctx, head, data, map[string][]byte{meta.AttrManifest: tiered})
				}

				want := layoutGet(ctx, radosgw, bucket, key)
				got := layoutGet(ctx, rgwgo, bucket, key)
				Expect(want.status).To(Equal(http.StatusOK), "radosgw's status; body %s", want.body)
				Expect(got.status).To(Equal(http.StatusOK), "rgw-go's status; body %s", got.body)
				secret, masked := `"secret":"`+layoutTierSecret+`"`, `"secret":"`+layoutMask+`"`
				Expect(string(want.body)).To(ContainSubstring(secret), "radosgw's body carries the tier's secret")
				Expect(string(got.body)).NotTo(ContainSubstring(layoutTierSecret), "rgw-go's body carries no secret")
				Expect(string(got.body)).To(ContainSubstring(masked))
				Expect(string(got.body)).To(ContainSubstring(`"access_key":"`+layoutTierKey+`"`), "rgw-go keeps the access key")
				Expect(string(got.body)).To(Equal(strings.Replace(string(want.body), secret, masked, 1)),
					"rgw-go's body is radosgw's with the secret masked")
				printLayoutCapture(layoutCapture{
					Release: manifest.Release, Shape: shape, Bucket: bucket, Key: key,
					ManifestHex: hex.EncodeToString(tiered), RGWGo: string(got.body),
					Note: "radosgw's body carries the tier's secret; rgwgo is the golden",
				})
			},
			Entry("one that keeps the object's own stripes and rules", "rgwgo-cloud-tier", layoutTierManifest, false),
			Entry("one a cloud transition wrote, empty, of size 0 with no rules", "rgwgo-cloud-transitioned", layoutTransitionManifest, true),
		)

		It("masks the secret in the layout of a head a real cloud transition kept", func() {
			Skip("a transition needs a tier with retain_head_object set and an endpoint that answers, and the population's " +
				"CLOUDTIER has neither: changing it is a zonegroup change committed to a new period, which radosgw reloads on " +
				"(hack/rooket/populate.sh's wait_reload), under the other specs of this suite that read that zonegroup and period")
		})

		It("answers a layout that fails with its error, where radosgw answers 200 and an empty document", func() {
			Skip("no input fails dump_obj_layout's Read::prepare or get_manifest once read_permissions has read the same object " +
				"state: a missing head, or one whose manifest does not decode, fails that read first, and what is left, a versioned " +
				"key whose OLH has pending entries or a head pool that vanishes between the two reads, needs a versioned write " +
				"racing the request or a pool taken from the shared zone (docs/ceph-upstream-bugs.md, \"radosgw's ?layout answers " +
				"200 with an empty document when its layout fails\", Trigger)")
		})
	})

	Describe("the otp section", func() {
		BeforeEach(otptest.RedactDevices)

		It("shows the devices radosgw-admin created as radosgw does, and lists their user", func(ctx SpecContext) {
			uid := otpUser(ctx, "create")
			hexDev := otpGateDevice("gate-hex", otp.SeedHex, otpGateHexSeed, 30, 2)
			b32Dev := otpGateDevice("gate-b32", otp.SeedBase32, otpGateBase32Seed, 60, 3)
			otpCreate(ctx, uid, hexDev)
			otpCreate(ctx, uid, b32Dev)

			path := "/admin/metadata/otp?key=user:" + uid
			want := adminCall(ctx, radosgw, layoutGateKey, http.MethodGet, path)
			got := adminCall(ctx, rgwgo, layoutGateKey, http.MethodGet, path)
			Expect(want.status).To(Equal(http.StatusOK), "radosgw's get")
			expectSameBody("the otp get", normaliseRead, want, got)
			Expect(otpDevices(got.body)).To(ConsistOf(hexDev, b32Dev), "the devices rgw-go shows")
			Expect(otpListed(ctx, uid)).To(ConsistOf(hexDev, b32Dev), "the devices radosgw-admin mfa list prints")

			wantList := adminCall(ctx, radosgw, layoutGateKey, http.MethodGet, "/admin/metadata/otp")
			gotList := adminCall(ctx, rgwgo, layoutGateKey, http.MethodGet, "/admin/metadata/otp")
			Expect(wantList.status).To(Equal(http.StatusOK), "radosgw's list")
			expectSameBody("the otp list", normaliseRead, wantList, gotList)
			Expect(otpMetadataKeys(gotList.body)).To(ContainElement("user:"+uid), "the keys rgw-go lists")
		})

		It("lists, checks and shows a device that rgw-go's otp and user sections wrote, as radosgw wrote one", func(ctx SpecContext) {
			uidR, uidG := otpUser(ctx, "put-r"), otpUser(ctx, "put-g")
			// mfa create has no flag for time_ofs, so only a metadata put can
			// make it non-zero. Ten steps of it put the device's codes outside
			// the window of a check that ignored it.
			dev := otpGateDevice("gate-put", otp.SeedHex, otpGateHexSeed, 60, 3)
			dev.TimeOfs = 600
			body := withData(nil, otpDocument(dev))

			putR := adminSend(ctx, radosgw, layoutGateKey, http.MethodPut, "/admin/metadata/otp?key=user:"+uidR, body)
			putG := adminSend(ctx, rgwgo, layoutGateKey, http.MethodPut, "/admin/metadata/otp?key=user:"+uidG, body)
			Expect(putR.status).To(BeElementOf(http.StatusOK, http.StatusNoContent), "radosgw's put: %s", putR.body)
			expectSamePut("the otp put", putR, putG)

			// radosgw-admin mfa create writes the user's mfa_ids beside the
			// device, so the device needs the user section's put too.
			userPath := "/admin/metadata/user?key=" + uidG
			user := adminCall(ctx, rgwgo, layoutGateKey, http.MethodGet, userPath)
			Expect(user.status).To(Equal(http.StatusOK), "rgw-go's user get: %s", user.body)
			userPut := adminSend(ctx, rgwgo, layoutGateKey, http.MethodPut, userPath, withMFAIDs(user.body, dev.ID))
			Expect(userPut.status).To(BeElementOf(http.StatusOK, http.StatusNoContent), "rgw-go's user put: %s", userPut.body)

			Expect(otpListed(ctx, uidG)).To(ConsistOf(dev), "mfa list after rgw-go's put: the step, window and time_ofs survive it")
			Expect(otpListed(ctx, uidR)).To(ConsistOf(dev), "mfa list after radosgw's put")
			var info struct {
				MFAIDs []string `json:"mfa_ids"`
			}
			adminJSON(ctx, &info, "user", "info", "--uid", uidG)
			Expect(info.MFAIDs).To(ConsistOf(dev.ID), "the user's mfa_ids after rgw-go's user put")
			Expect(string(admin(ctx, "mfa", "check", "--uid", uidG, "--totp-serial", dev.ID, "--totp-pin", otpCode(dev, time.Now())))).
				To(ContainSubstring("ok"), "mfa check of a code computed from the seed, the device's step and its time_ofs")

			got := adminCall(ctx, rgwgo, layoutGateKey, http.MethodGet, "/admin/metadata/otp?key=user:"+uidG)
			want := adminCall(ctx, radosgw, layoutGateKey, http.MethodGet, "/admin/metadata/otp?key=user:"+uidG)
			expectSameBody("the otp get after rgw-go's put", normaliseRead, want, got)
		})

		It("removes a user's devices through either gateway for both, answering as radosgw does", func(ctx SpecContext) {
			// Each gateway removes one user's devices, and the other gateway and
			// radosgw-admin find them gone.
			removers := []struct {
				g   adminGateway
				uid string
			}{{radosgw, otpUser(ctx, "remove-r")}, {rgwgo, otpUser(ctx, "remove-g")}}
			removed := make([]adminResponse, len(removers))
			for i, r := range removers {
				otpCreate(ctx, r.uid, otpGateDevice("gate-remove", otp.SeedHex, otpGateHexSeed, 30, 2))
				removed[i] = adminCall(ctx, r.g, layoutGateKey, http.MethodDelete, "/admin/metadata/otp?key=user:"+r.uid)
				Expect(removed[i].status).To(BeElementOf(http.StatusOK, http.StatusNoContent), "%s's remove: %s", r.g.name, removed[i].body)
			}
			expectSamePut("the otp remove", removed[0], removed[1])
			for _, r := range removers {
				path := "/admin/metadata/otp?key=user:" + r.uid
				want := adminCall(ctx, radosgw, layoutGateKey, http.MethodGet, path)
				got := adminCall(ctx, rgwgo, layoutGateKey, http.MethodGet, path)
				Expect(want.status).To(Equal(http.StatusNotFound), "radosgw's get after %s's remove: %s", r.g.name, want.body)
				expectSameBody("the otp get after "+r.g.name+"'s remove", normaliseRead, want, got)
				Expect(otpListed(ctx, r.uid)).To(BeEmpty(), "mfa list after %s's remove", r.g.name)
				for _, g := range []adminGateway{radosgw, rgwgo} {
					list := adminCall(ctx, g, layoutGateKey, http.MethodGet, "/admin/metadata/otp")
					Expect(list.status).To(Equal(http.StatusOK), "%s's list: %s", g.name, list.body)
					Expect(otpMetadataKeys(list.body)).NotTo(ContainElement("user:"+r.uid), "%s's list after %s's remove", g.name, r.g.name)
				}
			}
		})

		// An otp put reads each kept device's stored record across its remove,
		// so the replay state survives it. Inside a step only: a step on, the
		// class takes an accepted code for unused (docs/ceph-upstream-bugs.md,
		// "cls_otp computes the replay index from an unsigned window
		// distance"), so a refusal needs the put and the second check to
		// finish before the step turns, and a spec that finds the code
		// accepted again after the step turned tries a fresh device.
		DescribeTable("refuses an accepted code again after a put that keeps the device",
			func(ctx SpecContext, putter string) {
				g := gateway(putter)
				uid := otpUser(ctx, "replay")
				path := "/admin/metadata/otp?key=user:" + uid
				for try := 1; try <= 3; try++ {
					dev := otpGateDevice("gate-replay-"+strconv.Itoa(try), otp.SeedHex, otpGateHexSeed, 30, 2)
					otpCreate(ctx, uid, dev)
					stepEnd := waitStepRoom(ctx, dev, 22*time.Second)
					code := otpCode(dev, time.Now())
					Expect(otpCheck(ctx, uid, dev.ID, code)).To(Succeed(), "the first check of a fresh code")

					got := adminCall(ctx, g, layoutGateKey, http.MethodGet, path)
					Expect(got.status).To(Equal(http.StatusOK), "%s's get: %s", g.name, got.body)
					put := adminSend(ctx, g, layoutGateKey, http.MethodPut, path, got.body)
					Expect(put.status).To(BeElementOf(http.StatusOK, http.StatusNoContent), "%s's put: %s", g.name, put.body)

					err := otpCheck(ctx, uid, dev.ID, code)
					if err != nil {
						// A used code is refused with EACCES. A put that lost the
						// device fails the check with ENOENT under the same
						// "MFA check failed" prefix, so the errno is matched, and
						// the device is required to still be listed.
						Expect(err).To(MatchError(ContainSubstring("Permission denied")), "the second check of the same code")
						Expect(otpListed(ctx, uid)).To(ContainElement(dev), "the devices after %s's put", g.name)
						return
					}
					Expect(time.Now()).To(BeTemporally(">", stepEnd),
						"the code was accepted again inside its step, so %s's put dropped the device's replay state", g.name)
				}
				Fail("the step turned inside each of three tries")
			},
			Entry("put through radosgw", "radosgw"),
			Entry("put through rgw-go", "rgw-go"),
		)

		// radosgw's put removes the object and calls otp_set in one op, and
		// otp_set lists the ids stored before the put beside the document's, so
		// a dropped device's id stays in the header and every later get_all
		// fails (docs/ceph-upstream-bugs.md, "radosgw's otp metadata put keeps
		// a dropped device's id in the otp header"). rgw-go writes the header
		// itself, from the document's devices alone.
		DescribeTable("lists the remaining device after a put that drops one",
			func(ctx SpecContext, putter string) {
				g := gateway(putter)
				uid := otpUser(ctx, "drop")
				kept := otpGateDevice("gate-kept", otp.SeedHex, otpGateHexSeed, 30, 2)
				dropped := otpGateDevice("gate-dropped", otp.SeedHex, otpGateHexSeed, 30, 2)
				otpCreate(ctx, uid, kept)
				otpCreate(ctx, uid, dropped)
				path := "/admin/metadata/otp?key=user:" + uid

				before := adminCall(ctx, g, layoutGateKey, http.MethodGet, path)
				Expect(before.status).To(Equal(http.StatusOK), "%s's get before the put: %s", g.name, before.body)
				Expect(otpDevices(before.body)).To(ConsistOf(kept, dropped))
				put := adminSend(ctx, g, layoutGateKey, http.MethodPut, path, withData(before.body, otpDocument(kept)))
				Expect(put.status).To(BeElementOf(http.StatusOK, http.StatusNoContent), "%s's put: %s", g.name, put.body)

				if putter == "radosgw" {
					after := adminCall(ctx, radosgw, layoutGateKey, http.MethodGet, path)
					Expect(after.status).To(Equal(http.StatusNotFound), "radosgw's get after its own dropping put: %s", after.body)
					Expect(otpListed(ctx, uid)).To(BeEmpty(), "radosgw-admin mfa list after radosgw's dropping put")
					GinkgoWriter.Printf("rgw-go's get after radosgw's dropping put: %d\n", adminCall(ctx, rgwgo, layoutGateKey, http.MethodGet, path).status)

					admin(ctx, "mfa", "remove", "--uid", uid, "--totp-serial", dropped.ID)
					restored := adminCall(ctx, radosgw, layoutGateKey, http.MethodGet, path)
					Expect(restored.status).To(Equal(http.StatusOK), "radosgw's get after mfa remove of the dropped id: %s", restored.body)
					Expect(otpDevices(restored.body)).To(ConsistOf(kept))
					Expect(otpListed(ctx, uid)).To(ConsistOf(kept), "mfa list after mfa remove of the dropped id")
					return
				}
				for _, reader := range []adminGateway{radosgw, rgwgo} {
					after := adminCall(ctx, reader, layoutGateKey, http.MethodGet, path)
					Expect(after.status).To(Equal(http.StatusOK), "%s's get after rgw-go's dropping put: %s", reader.name, after.body)
					Expect(otpDevices(after.body)).To(ConsistOf(kept), "%s's devices", reader.name)
				}
				Expect(otpListed(ctx, uid)).To(ConsistOf(kept), "mfa list after rgw-go's dropping put")
			},
			Entry("put through radosgw, which keeps the dropped id", "radosgw"),
			Entry("put through rgw-go, which writes the document's ids alone", "rgw-go"),
		)

		It("accepts a code a second zone has accepted, where one zone refuses it", func() {
			Skip("the replay state lives in each zone's own otp object and no check writes a metadata log entry, so the " +
				"defect shows only between two zones, and the cluster runs one (docs/ceph-upstream-bugs.md, \"radosgw's otp " +
				"replay state stays in the zone that recorded it\")")
		})
	})
})
