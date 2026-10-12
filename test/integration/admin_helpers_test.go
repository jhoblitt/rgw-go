//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	goadmin "github.com/ceph/go-ceph/rgw/admin"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// adminGateway is one gateway's admin API: the cluster's radosgw, the
// oracle, or the rgw-go make rgw-go-up started beside it.
type adminGateway struct {
	name string
	// tag ends the names of the fixtures a spec creates through this
	// gateway, so that each gateway's create and modify bodies can be
	// compared with the other's once the tag is masked.
	tag string
	url string
}

// keyPair is an S3 access key and its secret.
type keyPair struct{ access, secret string }

func (k keyPair) creds() aws.Credentials {
	return aws.Credentials{AccessKeyID: k.access, SecretAccessKey: k.secret}
}

// adminResponse is a response as the wire carried it: the client asks for
// no compression, so the body and Content-Length are the gateway's own.
type adminResponse struct {
	status int
	header http.Header
	body   []byte
}

// rawClient sends each request once, keeps Content-Length and the body as
// sent, and never follows a redirect.
var rawClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: &http.Transport{DisableCompression: true},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// adminCall sends method on pathQuery, a path under the gateway's root with
// its query, signed with SigV4 for k as go-ceph's admin client signs.
func adminCall(ctx context.Context, g adminGateway, k keyPair, method, pathQuery string) adminResponse {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, method, g.url+pathQuery, http.NoBody)
	Expect(err).NotTo(HaveOccurred())
	Expect(v4.NewSigner().SignHTTP(ctx, k.creds(), req, "UNSIGNED-PAYLOAD", "s3", "default", time.Now())).To(Succeed())
	resp, err := rawClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "%s %s on %s", method, pathQuery, g.name)
	defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred(), "reading %s %s on %s", method, pathQuery, g.name)
	return adminResponse{status: resp.StatusCode, header: resp.Header, body: body}
}

// goCephAdmin is go-ceph's admin client of k's against g.
func goCephAdmin(g adminGateway, k keyPair) *goadmin.API {
	GinkgoHelper()
	api, err := goadmin.New(g.url, k.access, k.secret, &http.Client{Timeout: 30 * time.Second})
	Expect(err).NotTo(HaveOccurred())
	return api
}

// s3At is an S3 client of k's against the gateway at url that sends a
// checksum only where an operation requires one and never retries.
func s3At(url string, k keyPair) *awss3.Client {
	return awss3.New(awss3.Options{
		Region:                     "us-east-1",
		BaseEndpoint:               aws.String(url),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider(k.access, k.secret, ""),
		RetryMaxAttempts:           1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

// rgwgoAdminEndpoint is the URL of the rgw-go make rgw-go-up started
// against the suite's cluster: RGW_GO_ADMIN_ENDPOINT, else the endpoint
// file rgw-go-up writes beside the cluster's ceph.conf.
func rgwgoAdminEndpoint() string {
	GinkgoHelper()
	if u := os.Getenv("RGW_GO_ADMIN_ENDPOINT"); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	path := filepath.Join(filepath.Dir(conf), "rgw-go.endpoint")
	b, err := os.ReadFile(path) //nolint:gosec // the harness's own output file
	Expect(err).NotTo(HaveOccurred(), "no rgw-go: run make rgw-go-up RELEASE=%s, or set RGW_GO_ADMIN_ENDPOINT", manifest.Release)
	return strings.TrimSuffix(strings.TrimSpace(string(b)), "/")
}

// The values the normaliser masks, by the key or element name both
// formats write them under. requestNames differ between any two requests;
// fixtureNames between two fixtures created alike, each gateway's own.
var (
	requestNames = []string{"RequestId", "HostId"}
	fixtureNames = []string{
		"id", "bucket_id", "marker", "ver", "master_ver", "max_marker", "tag",
		"mtime", "creation_time", "create_date", "access_key", "secret_key",
	}
)

// fixtureTag is the tag a fixture's name carries, "admin-gate-r-" or
// "admin-gate-g-".
var fixtureTag = regexp.MustCompile(`admin-gate-[rg]-`)

// masker replaces the value of every key or element names lists with a
// placeholder naming it, in JSON, XML and HTML alike.
type masker struct{ json, xml, html *regexp.Regexp }

func newMasker(names ...string) masker {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	alt := strings.Join(quoted, "|")
	return masker{
		json: regexp.MustCompile(`"(` + alt + `)":("(?:[^"\\]|\\.)*"|-?[0-9][0-9.eE+-]*|true|false|null)`),
		xml:  regexp.MustCompile(`<(` + alt + `)>[^<]*</(?:` + alt + `)>`),
		html: regexp.MustCompile(`<li>(` + alt + `): [^<]*</li>`),
	}
}

func (m masker) mask(b []byte) []byte {
	b = m.json.ReplaceAll(b, []byte(`"$1":"@$1@"`))
	b = m.xml.ReplaceAll(b, []byte(`<$1>@$1@</$1>`))
	return m.html.ReplaceAll(b, []byte(`<li>$1: @$1@</li>`))
}

var (
	requestMasker = newMasker(requestNames...)
	fixtureMasker = newMasker(append(append([]string{}, requestNames...), fixtureNames...)...)
)

// normaliseRead masks what differs between two requests for one fixture.
func normaliseRead(b []byte) []byte { return requestMasker.mask(b) }

// normaliseFixture masks what differs between two fixtures created alike
// through the two gateways, the gateway's tag in their names among it.
func normaliseFixture(b []byte) []byte {
	return fixtureMasker.mask(fixtureTag.ReplaceAll(b, []byte("admin-gate-@-")))
}

// adminFormat is a body format the admin API renders.
type adminFormat string

const (
	formatJSON adminFormat = "json"
	formatXML  adminFormat = "xml"
	formatHTML adminFormat = "html"
)

// mime is the Content-Type rgw-go names for a body in f.
func (f adminFormat) mime() string {
	switch f {
	case formatXML:
		return "application/xml"
	case formatHTML:
		return "text/html"
	default:
		return "application/json"
	}
}

// withFormat adds format=f to pathQuery.
func withFormat(pathQuery string, f adminFormat) string {
	sep := "?"
	if strings.Contains(pathQuery, "?") {
		sep = "&"
	}
	return pathQuery + sep + "format=" + string(f)
}

// headerRules is what a response's headers must carry beyond its body.
type headerRules struct {
	// jsonType is the Content-Type rgw-go names whatever the format: the
	// realm, realm-list and period getters name application/json, and a
	// request no handler takes is answered in JSON.
	jsonType bool
	// getter is one of the three realm getters, which name their length
	// and so carry Accept-Ranges on success.
	getter bool
	// untypedOnRadosgw is a success radosgw may answer with no
	// Content-Type: a JSON body its flusher started, or the zone config
	// (docs/exclusions.md, "Admin responses carry a Content-Type on every
	// body").
	untypedOnRadosgw bool
}

// expectAdminHeaders asserts the headers of rgw-go's response and of
// radosgw's to the same request in f.
func expectAdminHeaders(what string, f adminFormat, rules headerRules, rgw, rgwgo adminResponse) {
	GinkgoHelper()
	for _, side := range []struct {
		name string
		r    adminResponse
	}{{"radosgw", rgw}, {"rgw-go", rgwgo}} {
		Expect(side.r.header.Get("Content-Length")).To(Equal(strconv.Itoa(len(side.r.body))),
			"%s: %s's Content-Length", what, side.name)
		wantRanges := ""
		if side.r.status >= http.StatusMultipleChoices || rules.getter {
			wantRanges = "bytes"
		}
		Expect(side.r.header.Get("Accept-Ranges")).To(Equal(wantRanges), "%s: %s's Accept-Ranges", what, side.name)
	}

	wantType := ""
	if len(rgwgo.body) > 0 {
		wantType = f.mime()
		if rules.jsonType {
			wantType = formatJSON.mime()
		}
	}
	Expect(rgwgo.header.Get("Content-Type")).To(Equal(wantType), "%s: rgw-go's Content-Type", what)
	radosgwType := rgw.header.Get("Content-Type")
	if radosgwType == "" && rgw.status < http.StatusMultipleChoices &&
		(rules.untypedOnRadosgw || (f == formatJSON && !rules.getter)) {
		return
	}
	Expect(radosgwType).To(Equal(wantType), "%s: radosgw's Content-Type", what)
}

// expectSameBody requires rgw-go's status and normalised body to equal
// radosgw's.
func expectSameBody(what string, normalise func([]byte) []byte, rgw, rgwgo adminResponse) {
	GinkgoHelper()
	want, got := normalise(rgw.body), normalise(rgwgo.body)
	Expect(rgwgo.status).To(Equal(rgw.status), "%s: status; radosgw's body %s, rgw-go's %s", what, rgw.body, rgwgo.body)
	Expect(string(got)).To(Equal(string(want)), "%s: normalised body", what)
}

// jsonField is the string at key in a JSON object, or in an XML body the
// element named key, "" when there is none.
func jsonField(body []byte, key string) string {
	var doc map[string]any
	if json.Unmarshal(body, &doc) == nil {
		if s, ok := doc[key].(string); ok {
			return s
		}
		return ""
	}
	m := regexp.MustCompile(`<` + regexp.QuoteMeta(key) + `>([^<]*)</` + regexp.QuoteMeta(key) + `>`).FindSubmatch(body)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// fixtureKey is a key pair unique to its gateway tag, format and kind, 20
// and 40 characters as radosgw generates them.
func fixtureKey(tag string, f adminFormat, kind string) keyPair {
	id := strings.ToUpper("GAT" + tag + string(f[0]) + kind)
	return keyPair{
		access: id + strings.Repeat("0", 20-len(id)),
		secret: "gat" + tag + string(f) + kind + strings.Repeat("s", 40-len("gat"+tag+string(f)+kind)),
	}
}

// fixtureName is a name for what, in format f, created through g.
func fixtureName(g adminGateway, f adminFormat, what string) string {
	return "admin-gate-" + g.tag + "-" + string(f) + "-" + what
}

// cephFSID is the fsid the cluster's monitors report.
func cephFSID(ctx context.Context) string {
	GinkgoHelper()
	return strings.TrimSpace(string(kubectl(ctx, "-n", "rook-ceph", "exec", "deploy/rook-ceph-tools", "--",
		"ceph", "--connect-timeout=20", "fsid")))
}

// tryAdmin runs radosgw-admin for a cleanup, which goes on past a fixture
// that is already gone.
func tryAdmin(ctx context.Context, args ...string) {
	if _, err := cephtest.RadosgwAdmin(ctx, conf, args...); err != nil {
		AddReportEntry("cleanup", fmt.Sprintf("radosgw-admin %s: %v", strings.Join(args, " "), err))
	}
}
