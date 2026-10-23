package s3_test

import (
	"cmp"
	"context"
	"encoding/xml"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// indexVersion is one version in a versionIndex: instance "" is the null
// version, or, with plain, an object written before the bucket was ever
// versioned.
type indexVersion struct {
	name, instance string
	plain          bool
	deleteMarker   bool
}

// versionIndex is a bucket's versions in index order, each key's newest
// first, listed as op.ListObjectsParams and op.ListObjectsResult document
// the store's versions listing: a Marker with no MarkerInstance starts past
// every version of it, or at its first with RelistMarker; an instance, "null"
// naming the null version, starts
// after that version, or past every version of Marker when it is gone; a
// marker inside a common prefix starts past the prefix. NextMarkerInstance is
// "null" for the null version and "" for an unversioned object or a common
// prefix.
type versionIndex []indexVersion

func (x versionIndex) list(p op.ListObjectsParams) op.ListObjectsResult {
	start := 0
	if p.Marker != "" {
		start = len(x)
		for i, v := range x {
			if v.name > p.Marker || p.RelistMarker && p.MarkerInstance == "" && v.name == p.Marker && !v.plain {
				start = i
				break
			}
		}
		for i, v := range x {
			if p.MarkerInstance != "" && v.name == p.Marker && cmp.Or(v.instance, "null") == p.MarkerInstance {
				start = i + 1
				break
			}
		}
		if cp, ok := commonPrefix(p.Marker, p); ok {
			for start < len(x) && strings.HasPrefix(x[start].name, cp) {
				start++
			}
		}
	}
	var res op.ListObjectsResult
	n := 0
	for i := start; i < len(x); i++ {
		v := x[i]
		if !strings.HasPrefix(v.name, p.Prefix) {
			continue
		}
		cp, isPrefix := commonPrefix(v.name, p)
		if isPrefix && len(res.CommonPrefixes) > 0 && res.CommonPrefixes[len(res.CommonPrefixes)-1] == cp {
			continue
		}
		if n == p.MaxKeys {
			res.Truncated = true
			return res
		}
		n++
		if isPrefix {
			res.CommonPrefixes = append(res.CommonPrefixes, cp)
			res.NextMarker, res.NextMarkerInstance = cp, ""
			continue
		}
		res.Entries = append(res.Entries, op.ObjectEntry{
			Key: meta.ObjKey{Name: v.name, Instance: v.instance}, ETag: "e", Size: 1,
			IsLatest: i == 0 || x[i-1].name != v.name, DeleteMarker: v.deleteMarker, Exists: true,
		})
		res.NextMarker, res.NextMarkerInstance = v.name, v.instance
		if v.instance == "" && !v.plain {
			res.NextMarkerInstance = "null"
		}
	}
	res.NextMarker, res.NextMarkerInstance = "", ""
	return res
}

// commonPrefix is the common prefix p's delimiter rolls name into.
func commonPrefix(name string, p op.ListObjectsParams) (string, bool) {
	if p.Delimiter == "" || !strings.HasPrefix(name, p.Prefix) {
		return "", false
	}
	i := strings.Index(name[len(p.Prefix):], p.Delimiter)
	if i < 0 {
		return "", false
	}
	return name[:len(p.Prefix)+i+len(p.Delimiter)], true
}

// versionsDoc is the ListVersionsResult a client reads.
type versionsDoc struct {
	XMLName             xml.Name `xml:"ListVersionsResult"`
	EncodingType        string   `xml:"EncodingType"`
	Name                string   `xml:"Name"`
	Prefix              string   `xml:"Prefix"`
	MaxKeys             int      `xml:"MaxKeys"`
	Delimiter           string   `xml:"Delimiter"`
	IsTruncated         bool     `xml:"IsTruncated"`
	CommonPrefixes      []string `xml:"CommonPrefixes>Prefix"`
	KeyMarker           string   `xml:"KeyMarker"`
	VersionIDMarker     string   `xml:"VersionIdMarker"`
	NextKeyMarker       *string  `xml:"NextKeyMarker"`
	NextVersionIDMarker *string  `xml:"NextVersionIdMarker"`
	Items               []struct {
		XMLName   xml.Name
		Key       string `xml:"Key"`
		VersionID string `xml:"VersionId"`
		IsLatest  bool   `xml:"IsLatest"`
	} `xml:",any"`
}

var _ = Describe("list_bucket with versions on a versioned bucket", func() {
	var (
		h       *s3.Handler
		buckets *opfakes.FakeBucketStore
		index   versionIndex
	)
	BeforeEach(func(ctx SpecContext) {
		store := memstore.New(memstore.Config{Release: denc.Squid})
		alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(alice.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
		rec.Info.Flags |= meta.BucketVersioned
		index = nil
		buckets = &opfakes.FakeBucketStore{}
		buckets.GetBucketReturns(rec, nil)
		buckets.ListObjectsStub = func(_ context.Context, _ *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
			return index.list(p), nil
		}
		env := testEnv(store)
		env.Buckets = buckets
		h = s3.NewHandler(env, authAs(alice), testConfig(s3.Config{}))
	})
	get := func(target string) versionsDoc {
		GinkgoHelper()
		rec := serveReq(h, http.MethodGet, target, nil)
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		var doc versionsDoc
		Expect(xml.Unmarshal(rec.Body.Bytes(), &doc)).To(Succeed(), rec.Body.String())
		return doc
	}
	// page lists the bucket's versions from query on, each next page from
	// the document's NextKeyMarker, URL-decoded when encoding-type=url
	// encoded it, and NextVersionIdMarker. It returns each page's common
	// prefixes and versions as key@version-id, in document order.
	page := func(query string) [][]string {
		GinkgoHelper()
		var pages [][]string
		target := "/plain?versions&" + query
		for {
			Expect(len(pages)).To(BeNumerically("<", 20), "the listing ends")
			doc := get(target)
			ids := slices.Clone(doc.CommonPrefixes)
			for _, it := range doc.Items {
				ids = append(ids, it.Key+"@"+it.VersionID)
			}
			pages = append(pages, ids)
			if !doc.IsTruncated {
				Expect(doc.NextKeyMarker).To(BeNil(), "no next marker on the last page")
				return pages
			}
			Expect(doc.NextKeyMarker).NotTo(BeNil(), "a truncated page names its next key")
			Expect(doc.NextVersionIDMarker).NotTo(BeNil(), "a truncated page names its next version")
			next := *doc.NextKeyMarker
			if doc.EncodingType == "url" {
				var err error
				next, err = url.PathUnescape(next)
				Expect(err).NotTo(HaveOccurred())
			}
			target = "/plain?versions&" + query + "&key-marker=" + url.QueryEscape(next) +
				"&version-id-marker=" + url.QueryEscape(*doc.NextVersionIDMarker)
		}
	}
	// markers is each listing's marker and instance as the store got them,
	// " relisting" after one that starts at its marker's first version.
	markers := func() []string {
		GinkgoHelper()
		out := make([]string, buckets.ListObjectsCallCount())
		for i := range out {
			_, _, p := buckets.ListObjectsArgsForCall(i)
			Expect(p.ListVersions).To(BeTrue())
			out[i] = p.Marker + "@" + p.MarkerInstance
			if p.RelistMarker {
				out[i] += " relisting"
			}
		}
		return out
	}

	DescribeTable("pages a key's three versions by the document's next markers",
		func(maxKeys int, wantPages [][]string, wantMarkers []string) {
			index = versionIndex{{name: "k", instance: "v3"}, {name: "k", instance: "v2"}, {name: "k", instance: "v1"}, {name: "z", instance: "z1"}}
			Expect(page("max-keys=" + strconv.Itoa(maxKeys))).To(Equal(wantPages))
			Expect(markers()).To(Equal(wantMarkers), "key-marker and version-id-marker are the listing's marker and instance, rgw_rest_s3.cc:1758-1759 at v19.2.7")
		},
		Entry("at max-keys 1, one version a page", 1,
			[][]string{{"k@v3"}, {"k@v2"}, {"k@v1"}, {"z@z1"}},
			[]string{"@", "k@v3", "k@v2", "k@v1"}),
		Entry("at max-keys 2, a page ending inside the key", 2,
			[][]string{{"k@v3", "k@v2"}, {"k@v1", "z@z1"}},
			[]string{"@", "k@v2"}),
	)
	It("names the next version null for the null version and an unversioned object, and passes null back as sent", func() {
		index = versionIndex{
			{name: "k", instance: "v3"},
			{name: "k"},
			{name: "k", instance: "v1"},
			{name: "p", plain: true},
			{name: "q", instance: "q1"},
		}
		Expect(page("max-keys=1")).To(Equal([][]string{{"k@v3"}, {"k@null"}, {"k@v1"}, {"p@null"}, {"q@q1"}}))
		Expect(markers()).To(Equal([]string{"@", "k@v3", "k@null", "k@v1", "p@null"}),
			"NextVersionIdMarker is null for a next marker without an instance, rgw_rest_s3.cc:1817-1825 at v19.2.7, and version-id-marker reaches the listing as sent")
	})
	It("passes a stale version-id-marker to the listing and echoes both markers as sent", func() {
		index = versionIndex{{name: "k", instance: "v3"}, {name: "k", instance: "v2"}, {name: "z", instance: "z1"}}
		doc := get("/plain?versions&key-marker=k&version-id-marker=gone")
		Expect(doc.KeyMarker).To(Equal("k"))
		Expect(doc.VersionIDMarker).To(Equal("gone"))
		Expect(doc.Items).To(HaveLen(1))
		Expect(doc.Items[0].Key+"@"+doc.Items[0].VersionID).To(Equal("z@z1"), "a version that is gone starts past the rest of its key")
		Expect(markers()).To(Equal([]string{"k@gone"}))
	})
	It("passes a key-marker given alone to the listing with no instance", func() {
		index = versionIndex{{name: "k", instance: "v3"}, {name: "k", instance: "v2"}, {name: "z", instance: "z1"}}
		doc := get("/plain?versions&key-marker=k")
		Expect(doc.VersionIDMarker).To(BeEmpty())
		Expect(doc.Items).To(HaveLen(1))
		Expect(doc.Items[0].Key).To(Equal("z"), "after every version of the key-marker")
		Expect(markers()).To(Equal([]string{"k@"}))
	})
	It("passes a version-id-marker given alone to the listing, which starts at the bucket's first key", func() {
		index = versionIndex{{name: "k", instance: "v3"}, {name: "k", instance: "v2"}, {name: "z", instance: "z1"}}
		doc := get("/plain?versions&version-id-marker=v3")
		Expect(doc.KeyMarker).To(BeEmpty())
		Expect(doc.VersionIDMarker).To(Equal("v3"))
		Expect(doc.Items).To(HaveLen(3), "radosgw sends the class the start key {\"\", v3}, before every key")
		Expect(markers()).To(Equal([]string{"@v3"}))
	})
	It("pages versions under a delimiter, a common prefix's next version named null", func() {
		index = versionIndex{
			{name: "a/1", instance: "x2"},
			{name: "a/1", instance: "x1"},
			{name: "a/2", instance: "y1"},
			{name: "b", instance: "b2"},
			{name: "b", instance: "b1", deleteMarker: true},
		}
		Expect(page("max-keys=1&delimiter=/")).To(Equal([][]string{{"a/"}, {"b@b2"}, {"b@b1"}}))
		Expect(markers()).To(Equal([]string{"@", "a/@null", "b@b2"}))
		doc := get("/plain?versions&delimiter=/&key-marker=b&version-id-marker=b2")
		Expect(doc.Delimiter).To(Equal("/"))
		Expect(doc.Items).To(HaveLen(1))
		Expect(doc.Items[0].XMLName.Local).To(Equal("DeleteMarker"))
		Expect(doc.Items[0].IsLatest).To(BeFalse())
	})
	It("URL-encodes KeyMarker, NextKeyMarker, Prefix and Delimiter under encoding-type=url, as S3 does, and no version marker", func() {
		index = versionIndex{{name: "d/a b+", instance: "v2"}, {name: "d/a b+", instance: "v1"}, {name: "e", instance: "e1"}}
		Expect(page("max-keys=1&encoding-type=url")).To(Equal([][]string{{"d%2Fa%20b%2B@v2"}, {"d%2Fa%20b%2B@v1"}, {"e@e1"}}))
		Expect(markers()).To(Equal([]string{"@", "d/a b+@v2", "d/a b+@v1"}))

		rec := serveReq(h, http.MethodGet, "/plain?versions&max-keys=1&encoding-type=url&key-marker="+url.QueryEscape("d/a b+")+"&version-id-marker=v2", nil)
		Expect(rec.Body.String()).To(ContainSubstring(
			`<KeyMarker>d%2Fa%20b%2B</KeyMarker><VersionIdMarker>v2</VersionIdMarker>`+
				`<NextKeyMarker>d%2Fa%20b%2B</NextKeyMarker><NextVersionIdMarker>v1</NextVersionIdMarker>`),
			"KeyMarker encoded with its slash, as NextKeyMarker is, rgw_rest_s3.cc:1818 at v19.2.7; the version markers as sent")
		rec = serveReq(h, http.MethodGet, "/plain?versions&encoding-type=url&prefix="+url.QueryEscape("d/ a")+"&delimiter="+url.QueryEscape("/+"), nil)
		Expect(rec.Body.String()).To(ContainSubstring(`<Prefix>d/%20a</Prefix><MaxKeys>1000</MaxKeys><Delimiter>/%2B</Delimiter>`),
			"Prefix and Delimiter encoded, each keeping its slash as the common prefixes do, rgw_rest_s3.cc:1800 at v19.2.7")
		rec = serveReq(h, http.MethodGet, "/plain?versions&prefix="+url.QueryEscape("d/ a")+"&delimiter="+url.QueryEscape("/+")+"&key-marker="+url.QueryEscape("d/a b+"), nil)
		Expect(rec.Body.String()).To(ContainSubstring(`<Prefix>d/ a</Prefix><MaxKeys>1000</MaxKeys><Delimiter>/+</Delimiter>`), "as sent without encoding-type")
		Expect(rec.Body.String()).To(ContainSubstring(`<KeyMarker>d/a b+</KeyMarker>`))
	})
	It("renders the list-type=2 form as radosgw does, its continuation-token starting the key again and no version-id-marker read", func() {
		index = versionIndex{{name: "k", instance: "v3"}, {name: "k"}, {name: "z", instance: "z1"}}
		rec := serveReq(h, http.MethodGet, "/plain?list-type=2&versions&max-keys=1&continuation-token=k&version-id-marker=v3", nil)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring(`<KeyContinuationToken>k</KeyContinuationToken><VersionIdContinuationToken></VersionIdContinuationToken>`),
			"KeyContinuationToken and VersionIdContinuationToken are the marker's name and instance, rgw_rest_s3.cc:1982-1983 at v19.2.7")
		Expect(rec.Body.String()).To(ContainSubstring(`<Version><Key>k</Key><VersionId>v3</VersionId>`),
			"the class starts after the token's plain key, at its first version, cls_rgw.cc:358-363 at v19.2.7")
		Expect(markers()).To(Equal([]string{"k@ relisting"}), "the v2 form's marker is the token, with no instance")

		rec = serveReq(h, http.MethodGet, "/plain?list-type=2&versions&max-keys=1&start-after=k", nil)
		Expect(rec.Body.String()).To(ContainSubstring(`<VersionIdContinuationToken></VersionIdContinuationToken><Version><Key>z</Key>`),
			"start-after is a key given alone, after every version of it, as S3 reads a key-marker")
		Expect(markers()[1]).To(Equal("k@"))

		rec = serveReq(h, http.MethodGet, "/plain?list-type=2&versions&max-keys=1", nil)
		Expect(rec.Body.String()).To(ContainSubstring(
			`<NextKeyContinuationToken>k</NextKeyContinuationToken><NextVersionIdContinuationToken>v3</NextVersionIdContinuationToken>`),
			"the next marker's instance as stored, rgw_rest_s3.cc:1984-1987 at v19.2.7")
		rec = serveReq(h, http.MethodGet, "/plain?list-type=2&versions&max-keys=2", nil)
		Expect(rec.Body.String()).To(ContainSubstring(
			`<NextKeyContinuationToken>k</NextKeyContinuationToken><NextVersionIdContinuationToken></NextVersionIdContinuationToken>`),
			"the null version's instance is empty, never null")
	})
	It("pages the list-type=2 form by its tokens as radosgw does, each page after one ending inside a key starting that key again", func() {
		index = versionIndex{{name: "k", instance: "v3"}, {name: "k", instance: "v2"}, {name: "k", instance: "v1"}, {name: "z", instance: "z1"}}
		first := serveReq(h, http.MethodGet, "/plain?list-type=2&versions&max-keys=2", nil).Body.String()
		Expect(first).To(ContainSubstring(`<NextKeyContinuationToken>k</NextKeyContinuationToken><NextVersionIdContinuationToken>v2</NextVersionIdContinuationToken>`))
		second := serveReq(h, http.MethodGet, "/plain?list-type=2&versions&max-keys=2&continuation-token=k", nil).Body.String()
		versionsOf := func(body string) []string {
			return regexp.MustCompile(`<VersionId>([^<]*)</VersionId>`).FindAllString(body, -1)
		}
		Expect(versionsOf(first)).To(Equal([]string{"<VersionId>v3</VersionId>", "<VersionId>v2</VersionId>"}))
		Expect(versionsOf(second)).To(Equal(versionsOf(first)),
			"no version of k is skipped; a key with max-keys versions or more never pages past, as on radosgw")
		Expect(markers()).To(Equal([]string{"@", "k@ relisting"}))
	})
})
