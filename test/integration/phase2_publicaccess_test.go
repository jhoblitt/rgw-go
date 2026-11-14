//go:build integration

package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// blockUser owns the public access block gate's buckets. Its name and keys
// are fixed, so a run that stopped partway leaves it for the next run to
// remove first.
const blockUser = "publicaccess-gate"

var blockKey = keyPair{access: "PABGATEEXAMPLEKEY001", secret: "pabGateExampleSecret" + strings.Repeat("0", 19) + "1"}

// The objects of an enforcement bucket: blockACLObject is public by its
// ACL, public-read, and blockPolicyObject is private by its ACL and public
// by the bucket policy, which grants everyone s3:GetObject of it alone.
const (
	blockACLObject    = "acl-public.txt"
	blockPolicyObject = "policy-public.txt"
)

// blockXMLHeader is the declaration dump_start writes before a document.
const blockXMLHeader = `<?xml version="1.0" encoding="UTF-8"?>`

// blockClient sends each request on a connection of its own and never
// follows a redirect: radosgw sends a block past the Content-Length of
// GetPublicAccessBlock's 404 (docs/differences.md, "A GetPublicAccessBlock
// error is its error document alone"), which a reused connection would read
// as the start of its next response.
var blockClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: &http.Transport{DisableKeepAlives: true, DisableCompression: true},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// blockGateway is one gateway as the public access block gate reaches it:
// its S3 endpoint and an S3 client of blockUser's against it.
type blockGateway struct {
	name, base string
	s3         *awss3.Client
}

// blockSign signs req as blockUser with SigV4 over an unsigned payload.
func blockSign(ctx context.Context, req *http.Request) error {
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	return v4.NewSigner().SignHTTP(ctx, blockKey.creds(), req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now())
}

// blockAnswer is what the gate compares a response by: its status, and the
// error code its body carries when the status is an error's. A write the S3
// client saw succeed is 200, whichever success status it was.
type blockAnswer struct {
	Status int
	Code   string
}

var (
	blockServed = blockAnswer{Status: http.StatusOK}
	blockDenied = blockAnswer{Status: http.StatusForbidden, Code: "AccessDenied"}
)

// blockErrorCode is the Code of an S3 error document, "" for any other body.
func blockErrorCode(body []byte) string {
	var e struct{ Code string }
	if xml.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.Code
}

// blockGet sends a GET of pathQuery to g through blockClient, signed as
// blockUser when signed and anonymous otherwise, and returns its status
// and its body as sent.
func blockGet(ctx context.Context, g blockGateway, pathQuery string, signed bool) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+pathQuery, http.NoBody)
	if err != nil {
		return 0, nil, fmt.Errorf("building GET %s for %s: %w", pathQuery, g.name, err)
	}
	if signed {
		if err = blockSign(ctx, req); err != nil {
			return 0, nil, fmt.Errorf("signing GET %s for %s: %w", pathQuery, g.name, err)
		}
	}
	resp, err := blockClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("GET %s on %s: %w", pathQuery, g.name, err)
	}
	b, err := io.ReadAll(resp.Body)
	if err = errors.Join(err, resp.Body.Close()); err != nil {
		return 0, nil, fmt.Errorf("reading GET %s on %s: %w", pathQuery, g.name, err)
	}
	return resp.StatusCode, b, nil
}

// blockAnswerOf is the blockAnswer of a status and body.
func blockAnswerOf(status int, body []byte) blockAnswer {
	if status < http.StatusBadRequest {
		return blockAnswer{Status: status}
	}
	return blockAnswer{Status: status, Code: blockErrorCode(body)}
}

// blockSDKAnswer is the blockAnswer of an S3 client call's error.
func blockSDKAnswer(err error) blockAnswer {
	if err == nil {
		return blockServed
	}
	a := blockAnswer{Status: statusOf(err)}
	if ae, ok := errors.AsType[smithy.APIError](err); ok {
		a.Code = ae.ErrorCode()
	}
	return a
}

// blockSection is dump_xml's PublicAccessBlockConfiguration section for b:
// no namespace, and each setting as dump_bool writes it, lower-case true or
// false
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_public_access.cc#L11-L20 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_public_access.cc#L11-L20 at v20.2.4).
func blockSection(b acl.PublicAccessBlock) string {
	return fmt.Sprintf("<PublicAccessBlockConfiguration><BlockPublicAcls>%t</BlockPublicAcls>"+
		"<IgnorePublicAcls>%t</IgnorePublicAcls><BlockPublicPolicy>%t</BlockPublicPolicy>"+
		"<RestrictPublicBuckets>%t</RestrictPublicBuckets></PublicAccessBlockConfiguration>",
		b.BlockPublicACLs, b.IgnorePublicACLs, b.BlockPublicPolicy, b.RestrictPublicBuckets)
}

// blockDocument is the GetPublicAccessBlock body radosgw sends for a stored
// b: the declaration, then b's section
// (RGWGetBucketPublicAccessBlock_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4613-L4624 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5124-L5135 at v20.2.4).
func blockDocument(b acl.PublicAccessBlock) string { return blockXMLHeader + blockSection(b) }

// blockVector is the user.rgw.public-access attr radosgw writes for b, by
// hand rather than by the encoder under test: ENCODE_START(1, 1)'s version,
// compat and little-endian length, then the four settings in order, a byte
// each (PublicAccessBlockConfiguration::encode,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_public_access.h#L45-L52 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_public_access.h#L45-L52 at v20.2.4).
func blockVector(b acl.PublicAccessBlock) []byte {
	v := []byte{1, 1, 4, 0, 0, 0}
	for _, s := range []bool{b.BlockPublicACLs, b.IgnorePublicACLs, b.BlockPublicPolicy, b.RestrictPublicBuckets} {
		if s {
			v = append(v, 1)
		} else {
			v = append(v, 0)
		}
	}
	return v
}

// blockConfig is b as the S3 client's PutPublicAccessBlock sends it, every
// setting named.
func blockConfig(b acl.PublicAccessBlock) *types.PublicAccessBlockConfiguration {
	return &types.PublicAccessBlockConfiguration{
		BlockPublicAcls: aws.Bool(b.BlockPublicACLs), IgnorePublicAcls: aws.Bool(b.IgnorePublicACLs),
		BlockPublicPolicy: aws.Bool(b.BlockPublicPolicy), RestrictPublicBuckets: aws.Bool(b.RestrictPublicBuckets),
	}
}

// blockPolicyStatusDocument is the GetBucketPolicyStatus body for isPublic:
// PolicyStatus in S3's namespace after the declaration, IsPublic as
// dump_bool writes it
// (RGWGetBucketPolicyStatus_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4584-L4602 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5095-L5113 at v20.2.4).
func blockPolicyStatusDocument(isPublic bool) string {
	return fmt.Sprintf(`%s<PolicyStatus xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsPublic>%t</IsPublic></PolicyStatus>`,
		blockXMLHeader, isPublic)
}

// blockAllowPolicy is a bucket policy of one statement that allows principal,
// a Principal value, s3:GetObject of resource, under condition, a Condition
// value, or none when condition is nil.
func blockAllowPolicy(principal any, resource string, condition any) string {
	GinkgoHelper()
	stmt := map[string]any{"Effect": "Allow", "Principal": principal, "Action": "s3:GetObject", "Resource": resource}
	if condition != nil {
		stmt["Condition"] = condition
	}
	b, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []any{stmt}})
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}

// blockEveryone is the Principal that names every principal.
var blockEveryone = map[string]string{"AWS": "*"}

// blockObjectARN is the ARN of bucket's key.
func blockObjectARN(bucket, key string) string { return "arn:aws:s3:::" + bucket + "/" + key }

// blockBucket creates a private bucket of blockUser's through radosgw.
// blockUser's removal removes it.
func blockBucket(ctx context.Context, radosgw blockGateway) string {
	GinkgoHelper()
	name := randName("pab")
	_, err := radosgw.s3.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(name)})
	Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", name)
	return name
}

// blockEnforcementBucket is a blockBucket holding blockACLObject and
// blockPolicyObject under the policy that makes the second public, all
// written through radosgw before either gateway reads the bucket.
func blockEnforcementBucket(ctx context.Context, radosgw blockGateway) string {
	GinkgoHelper()
	name := blockBucket(ctx, radosgw)
	for key, canned := range map[string]types.ObjectCannedACL{
		blockACLObject: types.ObjectCannedACLPublicRead, blockPolicyObject: types.ObjectCannedACLPrivate,
	} {
		_, err := radosgw.s3.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(name), Key: aws.String(key), Body: strings.NewReader("the body of " + key), ACL: canned,
		})
		Expect(err).NotTo(HaveOccurred(), "writing %s/%s through radosgw", name, key)
	}
	_, err := radosgw.s3.PutBucketPolicy(ctx, &awss3.PutBucketPolicyInput{
		Bucket: aws.String(name), Policy: aws.String(blockEnforcementPolicy(name)),
	})
	Expect(err).NotTo(HaveOccurred(), "PutBucketPolicy of %s through radosgw", name)
	return name
}

// blockEnforcementPolicy is the public policy of an enforcement bucket: every
// principal may get blockPolicyObject. Both floors' is_public and Amazon S3
// call it public.
func blockEnforcementPolicy(bucket string) string {
	GinkgoHelper()
	return blockAllowPolicy(blockEveryone, blockObjectARN(bucket, blockPolicyObject), nil)
}

// blockCheck is a request the gate sends to an enforcement bucket under a
// block, through each gateway, and the answers it wants.
type blockCheck struct {
	what string
	send func(ctx context.Context, g blockGateway, bucket string) blockAnswer
	// before is the answer with no block, nil for a write the gate sends
	// only under one.
	before *blockAnswer
	// under is the answer under the block, and squidRadosgw Squid's
	// radosgw's where it differs, nil where it does not.
	under, squidRadosgw *blockAnswer
}

// want is the answer gw gives c under the block on the suite's release.
func (c blockCheck) want(gw string) blockAnswer {
	if gw == "radosgw" && manifest.Release == "squid" && c.squidRadosgw != nil {
		return *c.squidRadosgw
	}
	return *c.under
}

// blockAnonymousGet is a check's request: an anonymous GET of key.
func blockAnonymousGet(key string) func(context.Context, blockGateway, string) blockAnswer {
	return func(ctx context.Context, g blockGateway, bucket string) blockAnswer {
		GinkgoHelper()
		status, body, err := blockGet(ctx, g, "/"+bucket+"/"+key, false)
		Expect(err).NotTo(HaveOccurred())
		return blockAnswerOf(status, body)
	}
}

// blockPutPublicReadACL is a check's request: the owner's PutBucketAcl of the
// canned public-read.
func blockPutPublicReadACL(ctx context.Context, g blockGateway, bucket string) blockAnswer {
	_, err := g.s3.PutBucketAcl(ctx, &awss3.PutBucketAclInput{Bucket: aws.String(bucket), ACL: types.BucketCannedACLPublicRead})
	return blockSDKAnswer(err)
}

// blockPutPublicPolicy is a check's request: the owner's PutBucketPolicy of the
// bucket's own public policy again.
func blockPutPublicPolicy(ctx context.Context, g blockGateway, bucket string) blockAnswer {
	_, err := g.s3.PutBucketPolicy(ctx, &awss3.PutBucketPolicyInput{
		Bucket: aws.String(bucket), Policy: aws.String(blockEnforcementPolicy(bucket)),
	})
	return blockSDKAnswer(err)
}

// blockCase is a block the gate writes and the checks that show it
// enforced.
type blockCase struct {
	block  acl.PublicAccessBlock
	checks []blockCheck
}

// aclBlock blocks and ignores public ACLs.
//   - BlockPublicAcls refuses the owner's PutBucketAcl of an ACL that
//     is_public calls public, on both floors (RGWPutACLs::execute,
//     https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5908-L5913 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6551-L6556 at v20.2.4).
//   - IgnorePublicAcls sets aside the object ACL's grant to AllUsers, so an
//     anonymous GET of blockACLObject is refused
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1572-L1575 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1626-L1629 at v20.2.4),
//     while the bucket policy still grants an anonymous GET of
//     blockPolicyObject.
func aclBlock() blockCase {
	return blockCase{
		block: acl.PublicAccessBlock{BlockPublicACLs: true, IgnorePublicACLs: true},
		checks: []blockCheck{
			{what: "the owner's PutBucketAcl of public-read", send: blockPutPublicReadACL, under: &blockDenied},
			{
				what: "an anonymous GET of the object its ACL makes public", send: blockAnonymousGet(blockACLObject),
				before: &blockServed, under: &blockDenied,
			},
			{
				what: "an anonymous GET of the object the bucket policy grants", send: blockAnonymousGet(blockPolicyObject),
				before: &blockServed, under: &blockServed,
			},
		},
	}
}

// policyBlock blocks public policies and restricts public buckets.
//   - BlockPublicPolicy refuses the owner's PutBucketPolicy of a policy
//     is_public calls public, on both floors
//     (RGWPutBucketPolicy::execute,
//     https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8116-L8121 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9029-L9034 at v20.2.4).
//   - RestrictPublicBuckets refuses every request from outside the owner's
//     account to a bucket whose policy is public, before the policy or an ACL
//     can grant it, on v20.2.4
//     (verify_object_permission,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1541-L1547 at v20.2.4),
//     and in rgw-go on both releases. Squid's radosgw never enforces it
//     (docs/differences.md, "Squid clusters get Tentacle's checks outside the
//     policy language"), so it serves both anonymous GETs.
func policyBlock() blockCase {
	return blockCase{
		block: acl.PublicAccessBlock{BlockPublicPolicy: true, RestrictPublicBuckets: true},
		checks: []blockCheck{
			{what: "the owner's PutBucketPolicy of the public policy", send: blockPutPublicPolicy, under: &blockDenied},
			{
				what: "an anonymous GET of the object the bucket policy grants", send: blockAnonymousGet(blockPolicyObject),
				before: &blockServed, under: &blockDenied, squidRadosgw: &blockServed,
			},
			{
				what: "an anonymous GET of the object its ACL makes public", send: blockAnonymousGet(blockACLObject),
				before: &blockServed, under: &blockDenied, squidRadosgw: &blockServed,
			},
		},
	}
}

// expectBlock waits for g's GetPublicAccessBlock of bucket to answer
// blockDocument(b): the other gateway's write reaches g's cache through its
// notify.
func expectBlock(ctx context.Context, g blockGateway, bucket string, b acl.PublicAccessBlock) {
	GinkgoHelper()
	Eventually(func(g2 Gomega) string {
		status, body, err := blockGet(ctx, g, "/"+bucket+"?publicAccessBlock", true)
		g2.Expect(err).NotTo(HaveOccurred())
		g2.Expect(status).To(Equal(http.StatusOK), "body %s", body)
		return string(body)
	}).WithContext(ctx).WithTimeout(10*time.Second).WithPolling(200*time.Millisecond).Should(Equal(blockDocument(b)),
		"GetPublicAccessBlock of %s on %s", bucket, g.name)
}

// expectNoBlock waits for g's GetPublicAccessBlock of bucket to answer 404
// NoSuchPublicAccessBlockConfiguration.
func expectNoBlock(ctx context.Context, g blockGateway, bucket string) {
	GinkgoHelper()
	Eventually(func(g2 Gomega) blockAnswer {
		status, body, err := blockGet(ctx, g, "/"+bucket+"?publicAccessBlock", true)
		g2.Expect(err).NotTo(HaveOccurred())
		return blockAnswerOf(status, body)
	}).WithContext(ctx).WithTimeout(10*time.Second).WithPolling(200*time.Millisecond).
		Should(Equal(blockAnswer{Status: http.StatusNotFound, Code: "NoSuchPublicAccessBlockConfiguration"}),
			"GetPublicAccessBlock of %s on %s", bucket, g.name)
}

// expectBlockBefore waits for each gateway to give each check that has one its
// answer with no block.
func expectBlockBefore(ctx context.Context, gateways []blockGateway, bucket string, checks []blockCheck) {
	GinkgoHelper()
	for _, c := range checks {
		if c.before == nil {
			continue
		}
		for _, g := range gateways {
			Eventually(func() blockAnswer { return c.send(ctx, g, bucket) }).WithContext(ctx).
				WithTimeout(10*time.Second).WithPolling(200*time.Millisecond).Should(Equal(*c.before),
				"%s on %s with no block", c.what, g.name)
		}
	}
}

// blockPolicyStatus is g's GetBucketPolicyStatus of bucket: its status and its
// body as sent.
func blockPolicyStatus(ctx context.Context, g blockGateway, bucket string) (int, string) {
	GinkgoHelper()
	status, body, err := blockGet(ctx, g, "/"+bucket+"?policyStatus", true)
	Expect(err).NotTo(HaveOccurred())
	return status, string(body)
}

// blockRecorder keeps every byte it reads from r.
type blockRecorder struct {
	r   io.Reader
	buf bytes.Buffer
}

func (rr *blockRecorder) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	rr.buf.Write(p[:n])
	return n, err
}

// blockExchange is what a gateway sent on one connection for two requests
// of blockUser's: a GetPublicAccessBlock of a bucket without a block, and,
// once that response has ended at its Content-Length, a
// GetBucketPolicyStatus of the same bucket, as a client that reuses its
// connections, a pooling proxy among them, sends its next request.
type blockExchange struct {
	gateway string
	// first is the first response through its Content-Length, and
	// firstBody its body.
	first, firstBody []byte
	firstStatus      int
	// connection is the first response's Connection header.
	connection string
	// surplus is what the gateway sent after the first response's
	// Content-Length and before the second response's status line, or
	// before the connection ended when no second response came.
	surplus []byte
	// second is the second response from its status line, as read, and
	// secondBody its body.
	second, secondBody []byte
	secondStatus       int
	// end is what stopped the read of the second response: nil when it
	// arrived whole, else the write's or read's error, io.EOF for a
	// connection the gateway closed.
	end error
}

func (x blockExchange) String() string {
	return fmt.Sprintf("%s:\n  first response (%d, Connection %q): %q\n  surplus: %q\n  second response (%d): %q\n  end: %v",
		x.gateway, x.firstStatus, x.connection, x.first, x.surplus, x.secondStatus, x.second, x.end)
}

// blockWholeResponse finds the response that starts at tail's first status
// line: its start, status and body, when tail holds all of it.
func blockWholeResponse(tail []byte) (at, status int, body []byte, ok bool) {
	at = bytes.Index(tail, []byte("HTTP/1."))
	if at < 0 {
		return 0, 0, nil, false
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(tail[at:])), nil)
	if err != nil {
		return 0, 0, nil, false
	}
	body, err = io.ReadAll(resp.Body)
	err = errors.Join(err, resp.Body.Close())
	// A body framed by neither a length nor chunks ends only with the
	// connection, which exchangeBlock reads until.
	if err != nil || resp.ContentLength < 0 && len(resp.TransferEncoding) == 0 {
		return 0, 0, nil, false
	}
	return at, resp.StatusCode, body, true
}

// blockConnectionHeader is the Connection header of the response raw starts
// with, as sent: net/http's reader drops a Connection: close for the
// response's Close.
func blockConnectionHeader(raw []byte) string {
	GinkgoHelper()
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw)))
	_, err := tp.ReadLine()
	Expect(err).NotTo(HaveOccurred())
	h, err := tp.ReadMIMEHeader()
	Expect(err).NotTo(HaveOccurred(), "the headers of %q", raw)
	return h.Get("Connection")
}

// exchangeBlock sends g blockExchange's two requests for bucket on one
// connection and keeps every byte g sends back.
func exchangeBlock(ctx context.Context, g blockGateway, bucket string) blockExchange {
	GinkgoHelper()
	u, err := url.Parse(g.base)
	Expect(err).NotTo(HaveOccurred())
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	Expect(err).NotTo(HaveOccurred(), "dialing %s at %s", g.name, u.Host)
	DeferCleanup(conn.Close)
	Expect(conn.SetDeadline(time.Now().Add(30 * time.Second))).To(Succeed())
	rec := &blockRecorder{r: conn}
	br := bufio.NewReader(rec)
	send := func(query string) error {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, g.base+"/"+bucket+"?"+query, http.NoBody)
		Expect(reqErr).NotTo(HaveOccurred())
		Expect(blockSign(ctx, req)).To(Succeed())
		return req.Write(conn)
	}

	x := blockExchange{gateway: g.name}
	Expect(send("publicAccessBlock")).To(Succeed(), "writing GetPublicAccessBlock to %s", g.name)
	resp, err := http.ReadResponse(br, nil)
	Expect(err).NotTo(HaveOccurred(), "the first response from %s: %q", g.name, rec.buf.Bytes())
	x.firstBody, err = io.ReadAll(resp.Body)
	Expect(errors.Join(err, resp.Body.Close())).NotTo(HaveOccurred(), "the first response's body from %s: %q", g.name, rec.buf.Bytes())
	n := rec.buf.Len() - br.Buffered()
	x.first, x.firstStatus = bytes.Clone(rec.buf.Bytes()[:n]), resp.StatusCode
	x.connection = blockConnectionHeader(x.first)

	x.end = send("policyStatus")
	tail := bytes.Clone(rec.buf.Bytes()[n:])
	chunk := make([]byte, 4096)
	for {
		if at, status, body, ok := blockWholeResponse(tail); ok {
			x.surplus, x.second, x.secondStatus, x.secondBody, x.end = tail[:at], tail[at:], status, body, nil
			return x
		}
		if x.end != nil {
			x.surplus = tail
			return x
		}
		var k int
		k, x.end = rec.r.Read(chunk)
		tail = append(tail, chunk[:k]...)
	}
}

// expectBlockExchanges requires each gateway's blockExchange to carry the
// 404 NoSuchPublicAccessBlockConfiguration and then, on the same connection,
// the second response whole; rgw-go's 404 error document to be radosgw's
// but for its ids, and nothing after it; and radosgw to send the default
// block after its 404 on a connection it keeps.
func expectBlockExchanges(radosgw, rgwgo blockExchange) {
	GinkgoHelper()
	for _, x := range []blockExchange{radosgw, rgwgo} {
		Expect(blockAnswerOf(x.firstStatus, x.firstBody)).To(
			Equal(blockAnswer{Status: http.StatusNotFound, Code: "NoSuchPublicAccessBlockConfiguration"}), "%s", x)
		Expect(x.end).NotTo(HaveOccurred(), "%s", x)
		Expect(x.secondStatus).To(Equal(http.StatusOK), "%s", x)
		Expect(string(x.secondBody)).To(Equal(blockPolicyStatusDocument(false)), "%s", x)
	}
	Expect(string(normaliseRead(rgwgo.firstBody))).To(Equal(string(normaliseRead(radosgw.firstBody))),
		"rgw-go's 404 error document against radosgw's, request and host ids masked")
	Expect(rgwgo.surplus).To(BeEmpty(), "%s", rgwgo)
	Expect(radosgw.connection).To(Equal("Keep-Alive"), "%s", radosgw)
	Expect(string(radosgw.surplus)).To(Equal(blockSection(acl.PublicAccessBlock{})), "%s", radosgw)
}

// removeBlockFixtures removes blockUser with its buckets and objects, its
// usage records and the garbage their removal leaves.
func removeBlockFixtures(ctx context.Context) {
	if _, err := radosgwAdmin(ctx, "user", "info", "--uid", blockUser); err == nil {
		tryAdmin(ctx, "user", "rm", "--uid", blockUser, "--purge-data")
	}
	tryAdmin(ctx, "usage", "trim", "--uid", blockUser, "--yes-i-really-mean-it")
	gcProcess(ctx)
}

// The public access block gate writes each block through one gateway and
// requires the other to read it back and enforce it, and the attr each
// writes to be radosgw's byte for byte; requires both to judge the same
// buckets public in GetBucketPolicyStatus but where docs/differences.md
// records that they differ; and records, from the bytes on the wire, what
// each sends after GetPublicAccessBlock's 404 on a connection it keeps.
var _ = Describe("the public access block gate", Ordered, ContinueOnFailure,
	Label("integration", "rgw-go", "phase2", "publicaccess"), func() {
		var gateways map[string]blockGateway

		BeforeAll(func(ctx SpecContext) {
			removeBlockFixtures(ctx)
			DeferCleanup(removeBlockFixtures)
			createUser(ctx, blockUser, blockKey)
			base := rgwgoAdminEndpoint()
			gateways = map[string]blockGateway{
				"radosgw": {name: "radosgw", base: endpoint, s3: s3At(endpoint, blockKey)},
				"rgw-go":  {name: "rgw-go", base: base, s3: s3At(base, blockKey)},
			}
		})

		DescribeTable("enforces one gateway's public access block in the other, from one attr",
			func(ctx SpecContext, writer, reader string, c blockCase) {
				w, rd := gateways[writer], gateways[reader]
				both := []blockGateway{rd, w}
				bucket := blockEnforcementBucket(ctx, gateways["radosgw"])
				// Each gateway caches the bucket here, so the other's write reaches
				// it only through that gateway's notify.
				expectNoBlock(ctx, rd, bucket)
				expectNoBlock(ctx, w, bucket)
				expectBlockBefore(ctx, both, bucket, c.checks)

				By("writing the block through " + writer)
				_, err := w.s3.PutPublicAccessBlock(ctx, &awss3.PutPublicAccessBlockInput{
					Bucket: aws.String(bucket), PublicAccessBlockConfiguration: blockConfig(c.block),
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(bucketAttrs(ctx, bucket)).To(HaveKeyWithValue(op.AttrPublicAccess, blockVector(c.block)),
					"the %s %s wrote, against the stored form radosgw writes", op.AttrPublicAccess, writer)
				expectBlock(ctx, rd, bucket, c.block)
				expectBlock(ctx, w, bucket, c.block)
				for _, chk := range c.checks {
					for _, g := range both {
						got := chk.send(ctx, g, bucket)
						GinkgoWriter.Printf("%s on %s under %s's block: %+v\n", chk.what, g.name, writer, got)
						Expect(got).To(Equal(chk.want(g.name)), "%s on %s under the block %s wrote, on %s",
							chk.what, g.name, writer, manifest.Release)
					}
				}

				By("writing it again through " + reader)
				_, err = rd.s3.PutPublicAccessBlock(ctx, &awss3.PutPublicAccessBlockInput{
					Bucket: aws.String(bucket), PublicAccessBlockConfiguration: blockConfig(c.block),
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(bucketAttrs(ctx, bucket)).To(HaveKeyWithValue(op.AttrPublicAccess, blockVector(c.block)),
					"the %s %s wrote over %s's", op.AttrPublicAccess, reader, writer)

				By("removing it through " + reader)
				_, err = rd.s3.DeletePublicAccessBlock(ctx, &awss3.DeletePublicAccessBlockInput{Bucket: aws.String(bucket)})
				Expect(err).NotTo(HaveOccurred())
				Expect(bucketAttrs(ctx, bucket)).NotTo(HaveKey(op.AttrPublicAccess), "after %s's DeletePublicAccessBlock", reader)
				expectNoBlock(ctx, w, bucket)
				expectNoBlock(ctx, rd, bucket)
				expectBlockBefore(ctx, both, bucket, c.checks)
			},
			Entry("BlockPublicAcls and IgnorePublicAcls written through radosgw", "radosgw", "rgw-go", aclBlock()),
			Entry("BlockPublicAcls and IgnorePublicAcls written through rgw-go", "rgw-go", "radosgw", aclBlock()),
			Entry("BlockPublicPolicy and RestrictPublicBuckets written through radosgw", "radosgw", "rgw-go", policyBlock()),
			Entry("BlockPublicPolicy and RestrictPublicBuckets written through rgw-go", "rgw-go", "radosgw", policyBlock()),
		)

		// Each bucket is set up through radosgw before rgw-go first reads it.
		// IsPublic joins the policy's judgment and the ACL's
		// (RGWGetBucketPolicyStatus::execute,
		// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8608-L8611 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9576-L9579 at v20.2.4);
		// where a floor's is_public departs from Amazon S3's judgment, which
		// rgw-go applies, the entry names that floor's answer and the
		// difference. Amazon S3's answers for a bucket with no policy and for
		// a public ACL were not captured, so those entries pin radosgw's.
		DescribeTable("reports the IsPublic each gateway's judgment gives",
			func(ctx SpecContext, canned types.BucketCannedACL, policy func(bucket string) string, rgwgo, squid, tentacle bool) {
				radosgw := gateways["radosgw"]
				bucket := blockBucket(ctx, radosgw)
				if canned != "" {
					_, err := radosgw.s3.PutBucketAcl(ctx, &awss3.PutBucketAclInput{Bucket: aws.String(bucket), ACL: canned})
					Expect(err).NotTo(HaveOccurred(), "PutBucketAcl of %s through radosgw", bucket)
				}
				if policy != nil {
					_, err := radosgw.s3.PutBucketPolicy(ctx, &awss3.PutBucketPolicyInput{
						Bucket: aws.String(bucket), Policy: aws.String(policy(bucket)),
					})
					Expect(err).NotTo(HaveOccurred(), "PutBucketPolicy of %s through radosgw", bucket)
				}
				want := map[string]bool{"radosgw": map[string]bool{"squid": squid, "tentacle": tentacle}[manifest.Release], "rgw-go": rgwgo}
				for _, gw := range []string{"radosgw", "rgw-go"} {
					status, body := blockPolicyStatus(ctx, gateways[gw], bucket)
					GinkgoWriter.Printf("GetBucketPolicyStatus on %s on %s: %d %s\n", gw, manifest.Release, status, body)
					Expect(status).To(Equal(http.StatusOK), "GetBucketPolicyStatus on %s: %s", gw, body)
					Expect(body).To(Equal(blockPolicyStatusDocument(want[gw])), "GetBucketPolicyStatus on %s on %s", gw, manifest.Release)
				}
			},
			Entry("a private bucket", types.BucketCannedACL(""), nil, false, false, false),
			Entry("a bucket whose ACL is public-read", types.BucketCannedACLPublicRead, nil, true, true, true),
			Entry("a bucket whose ACL is authenticated-read", types.BucketCannedACLAuthenticatedRead, nil, true, true, true),
			Entry("a bucket whose policy grants everyone", types.BucketCannedACL(""),
				func(bucket string) string { return blockAllowPolicy(blockEveryone, blockObjectARN(bucket, "*"), nil) }, true, true, true),
			Entry(`a grant to everyone under Bool aws:SecureTransport true, which radosgw calls not public `+
				`(docs/differences.md, "A bucket policy is public as Amazon S3 judges it")`, types.BucketCannedACL(""),
				func(bucket string) string {
					return blockAllowPolicy(blockEveryone, blockObjectARN(bucket, "*"), map[string]any{"Bool": map[string]string{"aws:SecureTransport": "true"}})
				}, true, false, false),
			Entry(`a grant to everyone from 1.1.1.1/32 alone, which radosgw calls public `+
				`(docs/differences.md, "A bucket policy is public as Amazon S3 judges it")`, types.BucketCannedACL(""),
				func(bucket string) string {
					return blockAllowPolicy(blockEveryone, blockObjectARN(bucket, "*"), map[string]any{"IpAddress": map[string]string{"aws:SourceIp": "1.1.1.1/32"}})
				}, false, true, true),
			Entry(`a grant to one fixed account, which Squid's radosgw calls public `+
				`(docs/differences.md, "A bucket policy is public as Amazon S3 judges it")`, types.BucketCannedACL(""),
				func(bucket string) string {
					return blockAllowPolicy(map[string]string{"AWS": "arn:aws:iam::s3tenant1:root"}, blockObjectARN(bucket, "*"), nil)
				}, false, true, false),
		)

		// radosgw answers an HTTP/1.1 request that does not ask to close with
		// Connection: Keep-Alive, and keeps the connection once the response
		// is written, whatever it wrote
		// (ClientIO::complete_header,
		// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_client.cc#L152-L158 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_client.cc#L152-L158 at v20.2.4;
		// handle_connection,
		// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L361-L363 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L372-L374 at v20.2.4).
		// So the block it writes after the 404's error document, every setting
		// false, waits on the connection ahead of the next response
		// (docs/ceph-upstream-bugs.md, "radosgw sends a public access block
		// after GetPublicAccessBlock's error document, past its
		// Content-Length").
		It("ends GetPublicAccessBlock's 404 at its Content-Length, where radosgw sends a block after it on a connection it keeps",
			func(ctx SpecContext) {
				bucket := blockBucket(ctx, gateways["radosgw"])
				x := map[string]blockExchange{}
				for _, gw := range []string{"radosgw", "rgw-go"} {
					x[gw] = exchangeBlock(ctx, gateways[gw], bucket)
					GinkgoWriter.Println(x[gw].String())
					AddReportEntry("GetPublicAccessBlock's 404 on one connection", x[gw].String())
				}
				expectBlockExchanges(x["radosgw"], x["rgw-go"])
			})
	})
