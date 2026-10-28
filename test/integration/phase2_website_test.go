//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/frontend"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/test/gate"
)

const (
	// websiteUser owns the website gate's buckets and reads their instances
	// through both admin interfaces. Its name and keys are fixed, so a run
	// that stopped partway leaves it for the next run to remove first.
	websiteUser = "website-gate"
	// websiteName is rgw_dns_s3website_name on both gateways: a Host of a
	// bucket under it makes a website request.
	websiteName = "website.rgw-go.test"
)

var websiteKey = keyPair{access: "WEBGATEEXAMPLEKEY001", secret: "webGateExampleSecret" + strings.Repeat("0", 19) + "1"}

// siteObjects are the documents of a site's bucket by key, each written
// public-read, as an anonymous website reader needs.
var siteObjects = map[string]string{
	"index.html":     "<html>the index</html>",
	"page.html":      "<html>a page</html>",
	"sub/index.html": "<html>a directory's index</html>",
	"error.html":     "<html>the error document</html>",
}

// webGateway is one gateway as the website gate reaches it: its S3 endpoint,
// which also serves website requests by their Host, and an S3 client of
// websiteUser's against it.
type webGateway struct {
	name, base string
	s3         *awss3.Client
}

// webCase is a website GET and the answer both gateways must give it.
type webCase struct {
	path   string
	status int
	// location is the Location wanted, "{host}" standing for the request's
	// Host.
	location string
	// doc is the object whose body the answer carries; a redirect's is the
	// error page, which both gateways must send alike.
	doc string
}

// siteCase is a website configuration, the name its captured vectors go
// under, and the GETs that show it served.
type siteCase struct {
	vector string
	conf   *types.WebsiteConfiguration
	gets   []webCase
}

// indexSite is an index document, an error document and two routing rules:
// one that redirects a prefix to the request's host at 302, and one that
// redirects a missing key of another prefix to a fixed host and key.
func indexSite() siteCase {
	return siteCase{
		vector: "index",
		conf: &types.WebsiteConfiguration{
			IndexDocument: &types.IndexDocument{Suffix: aws.String("index.html")},
			ErrorDocument: &types.ErrorDocument{Key: aws.String("error.html")},
			RoutingRules: []types.RoutingRule{
				{
					Condition: &types.Condition{KeyPrefixEquals: aws.String("docs/")},
					Redirect:  &types.Redirect{ReplaceKeyPrefixWith: aws.String("documents/"), HttpRedirectCode: aws.String("302")},
				},
				{
					Condition: &types.Condition{KeyPrefixEquals: aws.String("gone/"), HttpErrorCodeReturnedEquals: aws.String("404")},
					Redirect: &types.Redirect{
						Protocol: types.ProtocolHttps, HostName: aws.String("example.com"), ReplaceKeyWith: aws.String("moved.html"),
					},
				},
			},
		},
		gets: []webCase{
			{path: "/", status: http.StatusOK, doc: "index.html"},
			{path: "/page.html", status: http.StatusOK, doc: "page.html"},
			{path: "/sub/", status: http.StatusOK, doc: "sub/index.html"},
			{path: "/sub", status: http.StatusOK, doc: "sub/index.html"},
			{path: "/missing.html", status: http.StatusNotFound, doc: "error.html"},
			{path: "/docs/a.html", status: http.StatusFound, location: "http://{host}/documents/a.html"},
			{path: "/gone/x.html", status: http.StatusMovedPermanently, location: "https://example.com/moved.html"},
		},
	}
}

// redirectSite redirects every request to another host.
func redirectSite() siteCase {
	return siteCase{
		vector: "redirect",
		conf: &types.WebsiteConfiguration{
			RedirectAllRequestsTo: &types.RedirectAllRequestsTo{HostName: aws.String("www.example.com"), Protocol: types.ProtocolHttps},
		},
		gets: []webCase{
			{path: "/", status: http.StatusMovedPermanently, location: "https://www.example.com/"},
			{path: "/a/b.html", status: http.StatusMovedPermanently, location: "https://www.example.com/a/b.html"},
		},
	}
}

// webAnswer is what two gateways' answers to a website request are
// compared by. Body has its request and host ids masked.
type webAnswer struct {
	Status                            int
	Location, ContentType, ETag, Body string
}

// webClient sends each request on a connection of its own and never
// follows a redirect: radosgw writes a website error page one byte past its
// Content-Length (docs/exclusions.md, "A website error page ends at its
// Content-Length"), which a reused connection would read as the start of
// its next response.
var webClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: &http.Transport{DisableKeepAlives: true, DisableCompression: true},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// websiteHost is the Host of bucket's website.
func websiteHost(bucket string) string { return bucket + "." + websiteName }

// webGet sends an anonymous GET of path to g as a website request for
// bucket.
func webGet(ctx context.Context, g webGateway, bucket, path string) webAnswer {
	GinkgoHelper()
	a, err := webFetch(ctx, g, bucket, path)
	Expect(err).NotTo(HaveOccurred())
	return a
}

// webFetch is webGet returning its error, for a caller that retries.
func webFetch(ctx context.Context, g webGateway, bucket, path string) (webAnswer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+path, http.NoBody)
	if err != nil {
		return webAnswer{}, err
	}
	req.Host = websiteHost(bucket)
	resp, err := webClient.Do(req)
	if err != nil {
		return webAnswer{}, fmt.Errorf("GET %s of %s on %s: %w", path, req.Host, g.name, err)
	}
	body, err := io.ReadAll(resp.Body)
	err = errors.Join(err, resp.Body.Close())
	if err != nil {
		return webAnswer{}, fmt.Errorf("reading GET %s of %s on %s: %w", path, req.Host, g.name, err)
	}
	return webAnswer{
		Status: resp.StatusCode, Location: resp.Header.Get("Location"), ContentType: resp.Header.Get("Content-Type"),
		ETag: resp.Header.Get("ETag"), Body: string(normaliseRead(body)),
	}, nil
}

// expectSameSite sends each GET to both gateways as a website request for
// bucket and requires the same answer from both, and the one the case wants.
func expectSameSite(ctx context.Context, radosgw, rgwgo webGateway, bucket string, gets []webCase) {
	GinkgoHelper()
	for _, c := range gets {
		want := webGet(ctx, radosgw, bucket, c.path)
		Expect(webGet(ctx, rgwgo, bucket, c.path)).To(Equal(want), "GET %s: rgw-go's answer against radosgw's", c.path)
		Expect(want.Status).To(Equal(c.status), "GET %s: status; body %s", c.path, want.Body)
		Expect(want.Location).To(Equal(strings.ReplaceAll(c.location, "{host}", websiteHost(bucket))), "GET %s: Location", c.path)
		if c.doc != "" {
			Expect(want.Body).To(Equal(siteObjects[c.doc]), "GET %s: body", c.path)
		}
	}
}

// siteOf is the website configuration GetBucketWebsite answered with.
func siteOf(out *awss3.GetBucketWebsiteOutput) types.WebsiteConfiguration {
	return types.WebsiteConfiguration{
		ErrorDocument: out.ErrorDocument, IndexDocument: out.IndexDocument,
		RedirectAllRequestsTo: out.RedirectAllRequestsTo, RoutingRules: out.RoutingRules,
	}
}

// expectWebsite waits for g's GetBucketWebsite of bucket to answer want: the
// other gateway's write reaches g's cache through its notify.
func expectWebsite(ctx context.Context, g webGateway, bucket string, want *types.WebsiteConfiguration) {
	GinkgoHelper()
	Eventually(func(g2 Gomega) types.WebsiteConfiguration {
		out, err := g.s3.GetBucketWebsite(ctx, &awss3.GetBucketWebsiteInput{Bucket: aws.String(bucket)})
		g2.Expect(err).NotTo(HaveOccurred())
		return siteOf(out)
	}).WithContext(ctx).WithTimeout(10*time.Second).WithPolling(200*time.Millisecond).Should(Equal(*want),
		"GetBucketWebsite of %s on %s", bucket, g.name)
}

// expectNoWebsite waits for g's GetBucketWebsite of bucket to answer
// NoSuchWebsiteConfiguration.
func expectNoWebsite(ctx context.Context, g webGateway, bucket string) {
	GinkgoHelper()
	Eventually(func() string {
		_, err := g.s3.GetBucketWebsite(ctx, &awss3.GetBucketWebsiteInput{Bucket: aws.String(bucket)})
		if err == nil {
			return "no error"
		}
		if ae, ok := errors.AsType[smithy.APIError](err); ok {
			return ae.ErrorCode()
		}
		return err.Error()
	}).WithContext(ctx).WithTimeout(10*time.Second).WithPolling(200*time.Millisecond).Should(Equal("NoSuchWebsiteConfiguration"),
		"GetBucketWebsite of %s on %s", bucket, g.name)
}

// siteBucket creates a public-read bucket of websiteUser's through radosgw
// holding siteObjects. websiteUser's removal removes it.
func siteBucket(ctx context.Context, radosgw webGateway) meta.BucketID {
	GinkgoHelper()
	name := randName("web")
	_, err := radosgw.s3.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(name), ACL: types.BucketCannedACLPublicRead})
	Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", name)
	for key, body := range siteObjects {
		_, err = radosgw.s3.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(name), Key: aws.String(key), Body: strings.NewReader(body),
			ACL: types.ObjectCannedACLPublicRead, ContentType: aws.String("text/html"),
		})
		Expect(err).NotTo(HaveOccurred(), "writing %s/%s through radosgw", name, key)
	}
	rec, err := store.GetBucket(ctx, "", name)
	Expect(err).NotTo(HaveOccurred())
	return rec.Info.Bucket
}

// rawInstance is b's instance as RADOS holds it.
func rawInstance(ctx context.Context, b meta.BucketID) []byte {
	GinkgoHelper()
	rop := radosclient.NewReadOp()
	rd := rop.Read(0, 64*kib)
	_, err := pool(ctx, store.ZoneParams().DomainRoot).Read(ctx, b.InstanceOID(), rop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "reading %s", b.InstanceOID())
	Expect(rd.Err).NotTo(HaveOccurred(), "reading %s", b.InstanceOID())
	return slices.Clone(rd.Data[:rd.N])
}

// writeInstance replaces b's instance with raw behind every gateway's back,
// keeping its attrs and version.
func writeInstance(ctx context.Context, b meta.BucketID, raw []byte) {
	GinkgoHelper()
	wop := radosclient.NewWriteOp()
	wop.WriteFull(raw)
	_, err := pool(ctx, store.ZoneParams().DomainRoot).Write(ctx, b.InstanceOID(), wop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "writing %s", b.InstanceOID())
}

// decodeInstance decodes the bucket instance raw, which it must use whole.
func decodeInstance(raw []byte) meta.BucketInfo {
	GinkgoHelper()
	d := denc.NewDecoder(raw)
	info := meta.DecodeBucketInfo(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	Expect(d.Remaining()).To(BeZero(), "bytes past the instance")
	return info
}

// websiteV1 is raw, a bucket instance radosgw wrote with a website whose
// index document is suffix, with its RGWBucketWebsiteConf written back as
// version 1 wrote it, by hand rather than by the encoder under test: the
// members before subdir_marker, listing_css_doc and listing_enabled, which
// an S3 configuration leaves empty, under a (1, 1) header
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.h#L194-L217 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.h#L194-L217 at v20.2.4).
// The configuration is the struct whose first member is suffix, and it sits
// in RGWBucketInfo's own encoding, under no other length
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2268-L2291 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2331-L2354 at v20.2.4),
// so only that encoding's length changes.
func websiteV1(raw []byte, suffix string) []byte {
	GinkgoHelper()
	const header, v2Members = 6, 4 + 4 + 1
	first := append(binary.LittleEndian.AppendUint32(nil, uint32(len(suffix))), suffix...)
	Expect(bytes.Count(raw, first)).To(Equal(1), "the index document in the instance")
	at := bytes.Index(raw, first) - header
	Expect(at).To(BeNumerically(">=", 0), "the configuration's header")
	Expect(raw[at:at+2]).To(Equal([]byte{2, 1}), "RGWBucketWebsiteConf's version and compat")
	end := at + header + int(binary.LittleEndian.Uint32(raw[at+2:]))
	Expect(raw[end-v2Members:end]).To(Equal(make([]byte, v2Members)), "the version 2 members, empty")
	body := raw[at+header : end-v2Members]
	v1 := binary.LittleEndian.AppendUint32([]byte{1, 1}, uint32(len(body)))
	out := slices.Concat(raw[:at], v1, body, raw[end:])
	binary.LittleEndian.PutUint32(out[2:header], uint32(len(out)-header))
	return out
}

// toolboxIn runs args in the Rook toolbox of the manifest's rooket cluster,
// and only it, with stdin as their input.
func toolboxIn(ctx context.Context, stdin []byte, args ...string) []byte {
	GinkgoHelper()
	return kubectlIn(ctx, stdin, slices.Concat([]string{"-n", "rook-ceph", "exec", "-i", "deploy/rook-ceph-tools", "--"}, args)...)
}

// cephArgs is the kubectl command line of the ceph CLI in the toolbox.
func cephArgs(args ...string) []string {
	return slices.Concat([]string{"-n", "rook-ceph", "exec", "deploy/rook-ceph-tools", "--", "ceph", "--connect-timeout=20"}, args)
}

// cephCmd runs the ceph CLI in the toolbox.
func cephCmd(ctx context.Context, args ...string) string {
	GinkgoHelper()
	return strings.TrimSpace(string(kubectl(ctx, cephArgs(args...)...)))
}

// cephOut is cephCmd returning its error, for a caller that retries.
func cephOut(ctx context.Context, args ...string) (string, error) {
	out, err := runKubectl(ctx, nil, cephArgs(args...)...)
	return strings.TrimSpace(string(out)), err
}

// tryCeph runs the ceph CLI for a cleanup, which records a failure and goes
// on, as tryAdmin does.
func tryCeph(ctx context.Context, args ...string) {
	if _, err := cephOut(ctx, args...); err != nil {
		AddReportEntry("cleanup", fmt.Sprintf("ceph %s: %v", strings.Join(args, " "), err))
	}
}

// dencoderDump is the toolbox's ceph-dencoder dump_json of the
// RGWBucketInfo raw; hack/rooket/up.sh checks that the toolbox runs the
// daemons' image.
func dencoderDump(ctx context.Context, raw []byte) []byte {
	GinkgoHelper()
	return toolboxIn(ctx, raw, "ceph-dencoder", "type", "RGWBucketInfo", "import", "-", "decode", "dump_json")
}

// dencoderReencode is the toolbox's ceph-dencoder's re-encoding of the
// RGWBucketInfo raw.
func dencoderReencode(ctx context.Context, raw []byte) []byte {
	GinkgoHelper()
	script := `out=$(mktemp) && ceph-dencoder type RGWBucketInfo import - decode encode export "$out" && cat "$out"; rc=$?; rm -f "$out"; exit $rc`
	return toolboxIn(ctx, raw, "sh", "-c", script)
}

// captureVector writes raw, ceph-dencoder's dump of it and its re-encoding
// beside the cluster's client config, under website-vectors/ as
// <name>.v<ceph version>.{bin,json,reenc}, and returns the re-encoding.
func captureVector(ctx context.Context, name string, raw []byte) []byte {
	GinkgoHelper()
	dir := filepath.Join(filepath.Dir(conf), "website-vectors")
	Expect(os.MkdirAll(dir, 0o750)).To(Succeed())
	reenc := dencoderReencode(ctx, raw)
	base := filepath.Join(dir, name+".v"+manifest.CephVersion)
	for ext, b := range map[string][]byte{".bin": raw, ".json": dencoderDump(ctx, raw), ".reenc": reenc} {
		Expect(os.WriteFile(base+ext, b, 0o600)).To(Succeed())
	}
	AddReportEntry("website vector", base)
	return reenc
}

// expectSameMetadata requires rgw-go's admin metadata get of b's instance to
// equal radosgw-admin's.
func expectSameMetadata(ctx context.Context, rgwgo webGateway, b meta.BucketID) {
	GinkgoHelper()
	key := b.Key()
	want := canonical(admin(ctx, "metadata", "get", "bucket.instance:"+key))
	resp := adminCall(ctx, adminGateway{name: rgwgo.name, url: rgwgo.base}, websiteKey, http.MethodGet,
		"/admin/metadata/bucket.instance?key="+url.QueryEscape(key))
	Expect(resp.status).To(Equal(http.StatusOK), "rgw-go's metadata get of %s: %s", key, resp.body)
	Expect(gate.JSONDiff("metadata get bucket.instance:"+key, canonical(resp.body), want)).To(BeEmpty())
}

// radosgwDeployment is the cluster's one radosgw deployment and the config
// section its radosgw reads: Rook runs it as client.rgw and its
// deployment's name past rook-ceph-rgw, dashes mapped to dots
// (generateCephXUser, rook pkg/operator/ceph/object/config.go at v1.20.7),
// and config show answers only for a section a running daemon reads.
func radosgwDeployment(ctx context.Context) (deploy, entity string) {
	GinkgoHelper()
	deploy = strings.TrimSpace(string(kubectl(ctx, "-n", "rook-ceph", "get", "deploy", "-l", "app=rook-ceph-rgw", "-o", "name")))
	Expect(deploy).To(HavePrefix("deployment.apps/rook-ceph-rgw-"), "one radosgw deployment")
	Expect(deploy).NotTo(ContainSubstring("\n"), "one radosgw deployment")
	entity = "client.rgw" + strings.ReplaceAll(strings.TrimPrefix(deploy, "deployment.apps/rook-ceph-rgw"), "-", ".")
	Expect(cephCmd(ctx, "config", "show", entity, "rgw_frontends")).NotTo(BeEmpty(), "the radosgw's frontends under %s", entity)
	return deploy, entity
}

// restartRadosgw restarts the radosgw deploy runs and waits for it to serve
// S3 again.
func restartRadosgw(ctx context.Context, deploy string) {
	GinkgoHelper()
	kubectl(ctx, "-n", "rook-ceph", "rollout", "restart", deploy)
	kubectl(ctx, "-n", "rook-ceph", "rollout", "status", deploy, "--timeout=5m")
	Eventually(func() error {
		_, err := s3Client(alice()).HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String("plain")})
		return err
	}).WithContext(ctx).WithTimeout(3 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
}

// removeWebsiteFixtures removes websiteUser with its buckets and objects,
// its usage records and the garbage their removal leaves.
func removeWebsiteFixtures(ctx context.Context) {
	if _, err := radosgwAdmin(ctx, "user", "info", "--uid", websiteUser); err == nil {
		tryAdmin(ctx, "user", "rm", "--uid", websiteUser, "--purge-data")
	}
	tryAdmin(ctx, "usage", "trim", "--uid", websiteUser, "--yes-i-really-mean-it")
	gcProcess(ctx)
}

// loopbackAddr is a loopback address whose port was free a moment ago.
func loopbackAddr(ctx context.Context) string {
	GinkgoHelper()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	addr := ln.Addr().String()
	Expect(ln.Close()).To(Succeed())
	return addr
}

// startWebsiteRGWGo builds rgw-go and runs it on the populated site beside
// the radosgw, with websiteName and the website configuration routes on,
// until the container ends, and returns its endpoint. It runs as
// client.admin, whose config section the harness leaves alone, with the
// background workers off on its command line, as the parity options hold
// them for rgw-go-up's.
func startWebsiteRGWGo(ctx context.Context) string {
	GinkgoHelper()
	built, err := gexec.Build("github.com/jhoblitt/rgw-go/cmd/rgw-go", "-tags=ceph_preview")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(gexec.CleanupBuildArtifacts)
	addr := loopbackAddr(ctx)
	// Not exec.CommandContext: a process a BeforeAll starts outlives that
	// node's context, and DeferCleanup ends it.
	cmd := exec.Command(built, slices.Concat([]string{"serve", "--", "--name=client.admin", "-c", conf}, //nolint:noctx // see above
		manifest.AdminFlags(), []string{
			"--rgw-frontends=beast endpoint=" + addr,
			"--rgw-dns-s3website-name=" + websiteName, "--rgw-enable-static-website=true",
			"--rgw-enable-gc-threads=false", "--rgw-enable-lc-threads=false",
			"--rgw-dynamic-resharding=false", "--rgw-run-sync-thread=false",
		})...)
	s, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		defer s.Kill()
		s.Terminate()
		Eventually(s).WithTimeout(frontend.DrainTimeout+time.Minute).WithPolling(100*time.Millisecond).
			Should(gexec.Exit(), "rgw-go outlived SIGTERM and is killed")
	})
	base := "http://" + addr
	Eventually(func(g Gomega) int {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", http.NoBody)
		g.Expect(err).NotTo(HaveOccurred())
		resp, err := webClient.Do(req)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(resp.Body.Close()).To(Succeed())
		return resp.StatusCode
	}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(250*time.Millisecond).Should(Equal(http.StatusOK), "rgw-go at %s", base)
	return base
}

// v1Site is a bucket whose instance holds indexSite's configuration written
// by hand at version 1, and the instance radosgw wrote with it at version 2.
type v1Site struct {
	bucket meta.BucketID
	v1, v2 []byte
}

// The website gate starts an rgw-go of its own, since rgw-go-up's runs
// without a website name, and gives the cluster's radosgw one for the
// container's length: radosgw reads rgw_dns_s3website_name only in
// rgw_rest_init
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L235-L238 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L235-L238 at v20.2.4),
// which it calls as it starts or reloads its realm (rgw_rest_init,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L277 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L286 at v20.2.4),
// so the radosgw restarts under it and again once it is removed. A website
// name turns on radosgw's Host-as-bucket fallback, which leaves a Host that
// looks like an IP address alone (looks_like_ip_address,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2141-L2148 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2153-L2160 at v20.2.4),
// as every S3 client of the suite's is.
var _ = Describe("the website gate", Ordered, ContinueOnFailure, Label("integration", "rgw-go", "phase2", "website"), func() {
	var (
		gateways map[string]webGateway
		// v1Sites are the buckets each gateway rewrites, by its name.
		v1Sites map[string]v1Site
	)

	BeforeAll(func(ctx SpecContext) {
		// Cleanups run last registered first, each whatever the others did,
		// so the radosgw restarts last, once both options are gone.
		deploy, entity := radosgwDeployment(ctx)
		DeferCleanup(restartRadosgw, deploy)
		DeferCleanup(func(ctx context.Context) { tryCeph(ctx, "config", "rm", entity, "rgw_enable_static_website") })
		DeferCleanup(func(ctx context.Context) { tryCeph(ctx, "config", "rm", entity, "rgw_dns_s3website_name") })
		removeWebsiteFixtures(ctx)
		DeferCleanup(removeWebsiteFixtures)
		createUser(ctx, websiteUser, websiteKey, "--caps", "metadata=read")
		radosgw := webGateway{name: "radosgw", base: endpoint, s3: s3At(endpoint, websiteKey)}

		cephCmd(ctx, "config", "set", entity, "rgw_dns_s3website_name", websiteName)
		// radosgw reads rgw_enable_static_website on each request
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4732-L4737 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5248-L5253 at v20.2.4),
		// so the version 1 instances below are written before the restart.
		cephCmd(ctx, "config", "set", entity, "rgw_enable_static_website", "true")
		Eventually(func(g Gomega) string {
			out, err := cephOut(ctx, "config", "show", entity, "rgw_enable_static_website")
			g.Expect(err).NotTo(HaveOccurred())
			return out
		}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(Equal("true"))

		v1Sites = map[string]v1Site{}
		for _, gw := range []string{"radosgw", "rgw-go"} {
			b := siteBucket(ctx, radosgw)
			_, err := radosgw.s3.PutBucketWebsite(ctx, &awss3.PutBucketWebsiteInput{Bucket: aws.String(b.Name), WebsiteConfiguration: indexSite().conf})
			Expect(err).NotTo(HaveOccurred(), "PutBucketWebsite of %s through radosgw", b.Name)
			v2 := rawInstance(ctx, b)
			v1 := websiteV1(v2, aws.ToString(indexSite().conf.IndexDocument.Suffix))
			writeInstance(ctx, b, v1)
			v1Sites[gw] = v1Site{bucket: b, v1: v1, v2: v2}
		}
		// radosgw's own decoder reads the hand-written instance as the one it
		// wrote.
		Expect(captureVector(ctx, "v1", v1Sites["radosgw"].v1)).To(Equal(v1Sites["radosgw"].v2),
			"ceph-dencoder's re-encoding of the version 1 instance")

		// Both gateways start with no cache, so each first reads the version 1
		// instances from RADOS.
		restartRadosgw(ctx, deploy)
		// Until radosgw runs under the website name it serves this Host's GET
		// as the anonymous ListBuckets, 200.
		Eventually(func(g Gomega) webAnswer {
			a, err := webFetch(ctx, radosgw, randName("none"), "/")
			g.Expect(err).NotTo(HaveOccurred())
			return a
		}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(time.Second).
			Should(And(HaveField("Status", http.StatusNotFound), HaveField("Body", ContainSubstring("NoSuchBucket"))),
				"radosgw's answer to a website request for a bucket that does not exist")
		base := startWebsiteRGWGo(ctx)
		gateways = map[string]webGateway{
			"radosgw": radosgw,
			"rgw-go":  {name: "rgw-go", base: base, s3: s3At(base, websiteKey)},
		}
	})

	DescribeTable("serves each gateway's website configuration from the other's endpoint, from one bucket instance",
		func(ctx SpecContext, c siteCase) {
			radosgw, rgwgo := gateways["radosgw"], gateways["rgw-go"]
			b := siteBucket(ctx, radosgw)
			// rgw-go caches the bucket here, so radosgw's write reaches it
			// only through radosgw's notify.
			expectNoWebsite(ctx, rgwgo, b.Name)

			By("writing the configuration through radosgw")
			_, err := radosgw.s3.PutBucketWebsite(ctx, &awss3.PutBucketWebsiteInput{Bucket: aws.String(b.Name), WebsiteConfiguration: c.conf})
			Expect(err).NotTo(HaveOccurred())
			byRadosgw := rawInstance(ctx, b)
			Expect(encodeAt(decodeInstance(byRadosgw))).To(Equal(byRadosgw), "rgw-go's re-encoding of radosgw's instance")
			Expect(captureVector(ctx, c.vector, byRadosgw)).To(Equal(byRadosgw), "ceph-dencoder's re-encoding of radosgw's instance")
			expectWebsite(ctx, rgwgo, b.Name, c.conf)
			expectSameSite(ctx, radosgw, rgwgo, b.Name, c.gets)
			expectSameMetadata(ctx, rgwgo, b)

			By("removing it through radosgw")
			_, err = radosgw.s3.DeleteBucketWebsite(ctx, &awss3.DeleteBucketWebsiteInput{Bucket: aws.String(b.Name)})
			Expect(err).NotTo(HaveOccurred())
			expectNoWebsite(ctx, rgwgo, b.Name)

			By("writing it through rgw-go")
			_, err = rgwgo.s3.PutBucketWebsite(ctx, &awss3.PutBucketWebsiteInput{Bucket: aws.String(b.Name), WebsiteConfiguration: c.conf})
			Expect(err).NotTo(HaveOccurred())
			byRGWGo := rawInstance(ctx, b)
			Expect(string(dencoderDump(ctx, byRGWGo))).To(Equal(string(dencoderDump(ctx, byRadosgw))), "ceph-dencoder's dump of the instance each wrote")
			Expect(byRGWGo).To(Equal(byRadosgw), "the instance each wrote")
			expectWebsite(ctx, radosgw, b.Name, c.conf)
			expectSameSite(ctx, radosgw, rgwgo, b.Name, c.gets)
			expectSameMetadata(ctx, rgwgo, b)
		},
		Entry("an index document, an error document and routing rules", indexSite()),
		Entry("a redirect of every request", redirectSite()),
	)

	DescribeTable("rewrites a hand-written version 1 configuration as version 2 with the instance",
		func(ctx SpecContext, gw string) {
			g, site := gateways[gw], v1Sites[gw]
			Expect(rawInstance(ctx, site.bucket)).To(Equal(site.v1), "the instance before %s rewrites it", gw)
			expectWebsite(ctx, g, site.bucket.Name, indexSite().conf)
			_, err := g.s3.PutBucketTagging(ctx, &awss3.PutBucketTaggingInput{
				Bucket:  aws.String(site.bucket.Name),
				Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("rewritten-by"), Value: aws.String(gw)}}},
			})
			Expect(err).NotTo(HaveOccurred(), "PutBucketTagging through %s", gw)
			Expect(rawInstance(ctx, site.bucket)).To(Equal(site.v2), "the instance %s rewrote", gw)
		},
		Entry("by radosgw", "radosgw"),
		Entry("by rgw-go", "rgw-go"),
	)
})
