package citecheck_test

import (
	"bytes"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

// fixtureArn is src/rgw/rgw_fp_arn.h at both releases: enum struct Service
// declared inside namespace rgw at lines 6-8, another Service inside
// namespace other at lines 14-16, rgw::Service written at line 20, and
// other::count at lines 22-26, whose body names Service bare.
const fixtureArn = `// A fixture for the content specs' qualified names.
#pragma once

namespace rgw {

enum struct Service {
  iam, s3, sts
};

} // namespace rgw

namespace other {

enum struct Service {
  ec2, sqs
};

} // namespace other

static_assert(sizeof(rgw::Service) == sizeof(int));

int other::count()
{
  Service s = Service::ec2;
  return static_cast<int>(s);
}
`

// fixtureLC is src/rgw/rgw_fp_lc.cc at v19.2.6: RGWFp::lc_process at lines
// 2-5, and RGWFp::lc_remove, which calls it, at 7-10.
const fixtureLC = `// A fixture for the content specs' release ties.
int RGWFp::lc_process(int shard)
{
  return remove_entry(shard);
}

int RGWFp::lc_remove(int shard)
{
  return lc_process(shard);
}
`

// fixtureLCHelper is what v20.2.4 inserts at line 2 of rgw_fp_lc.cc, which
// moves lc_process to 4-7 and lc_remove to 9-12.
const fixtureLCHelper = `static int shards = 32;

`

// fixtureVer is src/rgw/rgw_fp_ver.cc at both releases: RGWFp::send_response
// at lines 2-7, which dumps MfaDelete at line 5; RGWFp::set_olh at 9-14,
// whose call at lines 11-12 passes unmod_since on its second line;
// RGWFp::send_status at 16-19, which dumps nothing; RGWFp::get_params at
// 21-29, which calls read_status and sets status_changed; and
// RGWFp::read_status at 31-34.
const fixtureVer = `// A fixture for the content specs' one-line links.
void RGWFp::send_response()
{
  dump_start(s);
  s->formatter->dump_string("MfaDelete", mfa_status);
  rgw_flush_formatter_and_reset(s, s->formatter);
}

int RGWFp::set_olh(optional_yield y)
{
  int r = olh_init(dpp, bucket_info,
                   params.unmod_since, y);
  return r;
}

void RGWFp::send_status()
{
  dump_start(s);
}

int RGWFp::get_params(optional_yield y)
{
  int r = read_status(y);
  if (r < 0) {
    return r;
  }
  status_changed = true;
  return 0;
}

int RGWFp::read_status(optional_yield y)
{
  return decode_status(y, &status_changed);
}
`

// fixtureAsio is src/rgw/rgw_fp_asio.cc at both releases: ClientIO::init_env
// at lines 2-7 and ClientIO::flush at 9-13, neither naming RGWEnv.
const fixtureAsio = `// A fixture for the content specs' runs of two files.
void ClientIO::init_env(CephContext* cct)
{
  for (auto& field : request) {
    env.set(field.name, field.value);
  }
}

void ClientIO::flush()
{
  stream.flush();
  sent = true;
}
`

// fixtureEnv is src/rgw/rgw_fp_env.cc at both releases: RGWEnv's
// constructor and init at lines 3-7, and RGWEnv::set at 9-12.
const fixtureEnv = `// A fixture for the content specs' runs of two files.
#include "rgw_fp_env.h"
RGWEnv::RGWEnv() = default;
void RGWEnv::init(CephContext* cct)
{
  conf.init(cct);
}

void RGWEnv::set(std::string name, std::string val)
{
  env_map[std::move(name)] = std::move(val);
}
`

// fixtureACL is src/rgw/rgw_fp_acl.cc at both releases: ACLs written only in
// the comment at line 4, InvalidArgument only in the error table's literal
// at line 6, RGWFp::verify_acl at lines 9-15, whose refusal is 11-13,
// RGWFp::check_range at 17-23, whose refusal is 19-21, and get_obj_state,
// another file's function, named only in the comment at line 25.
const fixtureACL = `// A fixture for the content specs' names that name no code.
#include "rgw_fp.h"

// Canned ACLs are refused under a public access block.
static const rgw_http_errors errs({
  { EINVAL, { 400, "InvalidArgument" }},
});

int RGWFp::verify_acl(req_state* s)
{
  if (s->block_public && s->canned_acl == "public-read") {
    return -EACCES;
  }
  return 0;
}

int RGWFp::check_range(int ofs)
{
  if (ofs < 0) {
    return -EINVAL;
  }
  return 0;
}

// get_obj_state, in rgw_fp_rados.cc, reads the head first.
`

// fixtureNeg is src/rgw/rgw_fp_neg.cc at both releases: RGWFp::register_admin
// at lines 4-9, which registers the admin managers at 6-7 and never calls
// set_logging; RGWFp::register_s3 at 11-17, which calls it at 14;
// RGWFp::verify_sha at 19-25, which refuses a request with no
// x-amz-content-sha256 at 21-23; and RGWFp::check_etag at 27-34, which
// compares If-Match at 29-32.
const fixtureNeg = `// A fixture for the content specs' negative claims.
#include "rgw_fp.h"

int RGWFp::register_admin(RGWREST* rest)
{
  rest->register_resource("usage", new RGWRESTMgr_Usage);
  rest->register_resource("user", new RGWRESTMgr_User);
  return 0;
}

int RGWFp::register_s3(RGWREST* rest)
{
  auto* mgr = new RGWRESTMgr_S3;
  mgr->set_logging(true);
  rest->register_default_mgr(mgr);
  return 0;
}

int RGWFp::verify_sha(req_state* s)
{
  if (!s->info.env->exists("HTTP_X_AMZ_CONTENT_SHA256")) {
    return -EINVAL;
  }
  return 0;
}

int RGWFp::check_etag(req_state* s, const std::string& etag)
{
  const char* if_match = s->info.env->get("HTTP_IF_MATCH");
  if (if_match && etag != if_match) {
    return -ERR_PRECONDITION_FAILED;
  }
  return 0;
}
`

// fixtureRun is src/rgw/rgw_fp_run.cc at v19.2.6: RGWFp::copy at lines 4-15,
// which calls range_to_ofs at line 6 and runs its copy loop at 10-13.
const fixtureRun = `// A fixture for the content specs' runs of citations.
#include "rgw_fp.h"

int RGWFp::copy(optional_yield y)
{
  int r = range_to_ofs(total, ofs, end);
  if (r < 0) {
    return r;
  }
  while (ofs <= end) {
    r = copy_chunk(ofs);
    ofs += chunk;
  }
  return r;
}
`

// fixtureRunHelpers is what v20.2.4 inserts at line 4 of rgw_fp_run.cc,
// which moves copy to 7-18, its range_to_ofs call to 9 and its loop to 13-16.
const fixtureRunHelpers = `static int chunk = 4096;
static int total = 0;

`

// runCopy returns markdown that cites range_to_ofs's call and the copy loop
// at each release, the loop at v20.2.4 linked at lines loopT.
func runCopy(loopT string) string {
	return "A range past the end fails `range_to_ofs` once the copy loop reaches it ([rgw_fp_run.cc:6](" +
		cephURL("v19.2.6", runPath, "L6") + ") and [:10-13](" + cephURL("v19.2.6", runPath, "L10-L13") +
		") at v19.2.6, [:9](" + cephURL("v20.2.4", runPath, "L9") + ") and [:" + strings.ReplaceAll(loopT, "L", "") +
		"](" + cephURL("v20.2.4", runPath, loopT) + ") at v20.2.4).\n"
}

// fixtureRead is src/rgw/rgw_fp_read.cc at both releases: index_of at lines
// 2-10; read_with_boundary at 12-15, which calls it; RGWFp::read_line at
// 17-21, which calls read_with_boundary; RGWFp::get_data at 23-26, which
// calls read_with_boundary too; RGWFp::send at 28-31, which neither
// read_line reaches nor reaches read_line; clamp_window at 33-36; and
// RGWFp::read_all at 38-42, which calls clamp_window and read_line, three
// calls from index_of.
const fixtureRead = `// A fixture for the content specs' subjects reached through calls.
static int index_of(const char* buf, int len)
{
  for (int i = 0; i < len; i++) {
    if (buf[i] == '\n') {
      return i;
    }
  }
  return -1;
}

static int read_with_boundary(const char* buf, int len)
{
  return index_of(buf, len);
}

int RGWFp::read_line(const char* buf, int len)
{
  int max = std::min(len, max_chunk);
  return read_with_boundary(buf, max);
}

int RGWFp::get_data(const char* buf, int len)
{
  return read_with_boundary(buf, len);
}

int RGWFp::send(const char* buf, int len)
{
  return write(buf, len);
}

static int clamp_window(int len)
{
  return len > 64 ? 64 : len;
}

int RGWFp::read_all(const char* buf, int len)
{
  int w = clamp_window(len);
  return read_line(buf, w);
}
`

// fixtureLambda is src/rgw/rgw_fp_lambda.cc at both releases: helper_x at
// lines 2-5; RGWFp::walk at 7-13, which passes std::for_each a lambda that
// calls helper_x; and RGWFp::scan at 15-19, which calls std::for_each with
// no lambda.
const fixtureLambda = `// A fixture for the content specs' lambdas passed to calls.
static int helper_x(int v)
{
  return v * 2;
}

int RGWFp::walk(std::vector<int>& v)
{
  std::for_each(v.begin(), v.end(), [&](int& x) {
    x = helper_x(x);
  });
  return 0;
}

int RGWFp::scan(std::vector<int>& w)
{
  std::for_each(w.begin(), w.end(), print_one);
  return 0;
}
`

// newFPFixture returns a ceph fixture whose v19.2.6 and v20.2.4, the
// releases the [S] and [T] markers name, hold the files the specs of the
// sweep's false positives cite, and whose v19.2.7, a point release between
// them, changes none of those files.
func newFPFixture() *cephFixture {
	r := newCephFixture()
	r.write(readPath, fixtureRead)
	r.write(lambdaPath, fixtureLambda)
	r.write(runPath, fixtureRun)
	r.write(aclPath, fixtureACL)
	r.write(negPath, fixtureNeg)
	r.write(arnPath, fixtureArn)
	r.write(lcPath, fixtureLC)
	r.write(verPath, fixtureVer)
	r.write(asioPath, fixtureAsio)
	r.write(envPath, fixtureEnv)
	r.commit("squid")
	r.git("tag", "v19.2.6")
	r.write("src/rgw/rgw_fp_point.cc", "// a point release touches another file\n")
	r.commit("squid point release")
	r.git("tag", "v19.2.7")
	lines := strings.SplitAfter(fixtureLC, "\n")
	r.write(lcPath, lines[0]+fixtureLCHelper+strings.Join(lines[1:], ""))
	lines = strings.SplitAfter(fixtureRun, "\n")
	r.write(runPath, strings.Join(lines[:3], "")+fixtureRunHelpers+strings.Join(lines[3:], ""))
	r.commit("tentacle")
	r.git("tag", "v20.2.4")
	return r
}

const (
	arnPath    = "src/rgw/rgw_fp_arn.h"
	lcPath     = "src/rgw/rgw_fp_lc.cc"
	verPath    = "src/rgw/rgw_fp_ver.cc"
	asioPath   = "src/rgw/rgw_fp_asio.cc"
	envPath    = "src/rgw/rgw_fp_env.cc"
	aclPath    = "src/rgw/rgw_fp_acl.cc"
	negPath    = "src/rgw/rgw_fp_neg.cc"
	runPath    = "src/rgw/rgw_fp_run.cc"
	readPath   = "src/rgw/rgw_fp_read.cc"
	lambdaPath = "src/rgw/rgw_fp_lambda.cc"
)

var (
	// asio27 is ClientIO::init_env's lines, and env912 RGWEnv::set's.
	asio27 = cephURL("v19.2.6", asioPath, "L2-L7")
	env912 = cephURL("v19.2.6", envPath, "L9-L12")
	// acl1113 is verify_acl's refusal, and acl1921 check_range's.
	acl1113 = cephURL("v19.2.6", aclPath, "L11-L13")
	acl1921 = cephURL("v19.2.6", aclPath, "L19-L21")
	// neg67 is register_admin's registrations.
	neg67 = cephURL("v19.2.6", negPath, "L6-L7")
)

// lcRuns returns a Go comment that cites lc_process and lc_remove at each
// release in the sweep's D7 form, "A at v19.2.6 and B, C at v20.2.4 and D",
// with B linked at bRef and C at cRef.
func lcRuns(bRef, cRef string) string {
	bLines := map[string]string{"v19.2.6": "L7-L10", "v20.2.4": "L9-L12"}[bRef]
	cLines := map[string]string{"v19.2.6": "L2-L5", "v20.2.4": "L4-L7"}[cRef]
	return "package fix\n\n// lcProcess drops the entry, as RGWFp::lc_process does\n// (" +
		cephURL("v19.2.6", lcPath, "L2-L5") + " at v19.2.6\n// and " + cephURL(bRef, lcPath, bLines) + ",\n// " +
		cephURL(cRef, lcPath, cLines) + " at v20.2.4\n// and " + cephURL("v20.2.4", lcPath, "L9-L12") + ").\n" +
		"func lcProcess() {}\n"
}

// lcPairs returns markdown that cites lc_process and lc_remove at each
// release as "A and B at v19.2.6, C and D at v20.2.4", with C linked at
// cRef.
func lcPairs(cRef string) string {
	cLines := map[string]string{"v19.2.6": "L2-L5", "v20.2.4": "L4-L7"}[cRef]
	return "`RGWFp::lc_process` drops the entry ([rgw_fp_lc.cc:2-5](" + cephURL("v19.2.6", lcPath, "L2-L5") +
		") and [:7-10](" + cephURL("v19.2.6", lcPath, "L7-L10") + ") at v19.2.6, [:4-7](" + cephURL(cRef, lcPath, cLines) +
		") and [:9-12](" + cephURL("v20.2.4", lcPath, "L9-L12") + ") at v20.2.4).\n"
}

// lcJoined returns markdown that cites lc_process and lc_remove as "A at
// v19.2.6 and B and C at v20.2.4", with B, lc_process, linked at bRef.
func lcJoined(bRef string) string {
	bLines := map[string]string{"v19.2.6": "L2-L5", "v20.2.4": "L4-L7"}[bRef]
	return "`RGWFp::lc_process` drops the entry ([rgw_fp_lc.cc:2-5](" + cephURL("v19.2.6", lcPath, "L2-L5") +
		") at v19.2.6 and [rgw_fp_lc.cc:4-7](" + cephURL(bRef, lcPath, bLines) + ") and [rgw_fp_lc.cc:9-12](" +
		cephURL("v20.2.4", lcPath, "L9-L12") + ") at v20.2.4).\n"
}

var _ = Describe("The content of links the sweep's false positives cite", func() {
	var (
		ceph           *cephFixture
		stdout, stderr *bytes.Buffer
	)

	BeforeEach(func() {
		ceph = newFPFixture()
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	})

	// content runs --content on a file name holding src and returns what it
	// printed for each link, its file's name dropped, its summary line and
	// its exit status.
	content := func(ctx SpecContext, name, src string) (lines []string, summary string, status int) {
		f := markdown(name, src)
		status = citecheck.ExitStatus(citecheck.Run(ctx, []string{"--content", "--ceph", ceph.dir, f}, nil, stdout, stderr))
		out := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
		for _, l := range out[:len(out)-1] {
			lines = append(lines, strings.TrimPrefix(l, f+":"))
		}
		return lines, out[len(out)-1], status
	}

	// reports checks that --content printed want and summary for a file name
	// holding src, and exited with status.
	reports := func(ctx SpecContext, name, src string, want []string, summary string, status int) {
		lines, gotSummary, gotStatus := content(ctx, name, src)
		Expect(lines).To(Equal(want))
		Expect(gotSummary).To(Equal(summary))
		Expect(gotStatus).To(Equal(status))
	}

	DescribeTable("holds a link the sweep found failed though it was right",
		func(ctx SpecContext, name, src string, links int) {
			lines, summary, status := content(ctx, name, src)
			Expect(lines).To(BeEmpty())
			Expect(summary).To(Equal(fmt.Sprintf("links checked: %d, held: %d, failed: 0, listed: 0", links, links)))
			Expect(status).To(Equal(0))
		},
		Entry("D8: a qualified name its lines declare inside its namespace", "doc.md",
			"Service is `rgw::Service` ([rgw_fp_arn.h:6-8]("+cephURL("v19.2.6", arnPath, "L6-L8")+")).\n", 1),
		Entry("D8: a qualified name declared by the header of a block that encloses its lines", "doc.md",
			"The services are `rgw::Service`'s ([rgw_fp_arn.h:7]("+cephURL("v19.2.6", arnPath, "L7")+")).\n", 1),
		Entry(`D7: "A at v19.2.6 and B, C at v20.2.4 and D", B tied to A's "at v19.2.6", not C's "at v20.2.4"`, "fix.go",
			lcRuns("v19.2.6", "v20.2.4"), 4),
		Entry(`D7 (review D7-2): "A at v19.2.6 and B and C at v20.2.4", B joined by "and" to C, so at v20.2.4`, "doc.md",
			lcJoined("v20.2.4"), 3),
		Entry(`D4 (review D4-1): a name after "no" as a condition the lines test`, "doc.md",
			"A request with no `x-amz-content-sha256` is refused ([rgw_fp_neg.cc:21-23]("+
				cephURL("v19.2.6", negPath, "L21-L23")+")).\n", 1),
		Entry(`D4 (review D4-4): a name after "does not match" the lines compare`, "doc.md",
			"An ETag that does not match `If-Match` fails with 412 ([rgw_fp_neg.cc:29-32]("+
				cephURL("v19.2.6", negPath, "L29-L32")+")).\n", 1),
		Entry("D3 (re-review D3-5): a run whose loop links hold the name beside them no other link holds", "doc.md",
			"A range past the end fails `range_to_ofs` once the loop's `copy_chunk` reaches it ([rgw_fp_run.cc:6]("+
				cephURL("v19.2.6", runPath, "L6")+") and [:10-13]("+cephURL("v19.2.6", runPath, "L10-L13")+") at v19.2.6, "+
				"[:9]("+cephURL("v20.2.4", runPath, "L9")+") and [:13-16]("+cephURL("v20.2.4", runPath, "L13-L16")+
				") at v20.2.4).\n", 4),
	)

	DescribeTable("lists, rather than fails, a link the sweep found failed though nothing beside it judges its lines",
		reports,
		Entry("D6: a one-line link to a function's first line, by a name in its body", "doc.md",
			"`MfaDelete` must be Enabled or Disabled ([rgw_fp_ver.cc:2]("+cephURL("v19.2.6", verPath, "L2")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", verPath, "L2") + ": listed: line 2 holds none of MfaDelete, but the " +
				"definition that starts on it, through line 7, holds one"},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("D6: a one-line link to a call's first line, by a name on the call's next line", "doc.md",
			"`unmod_since` is passed through ([rgw_fp_ver.cc:11]("+cephURL("v19.2.6", verPath, "L11")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", verPath, "L11") + ": listed: line 11 holds none of unmod_since, but the " +
				"statement that starts on it, through line 12, holds one"},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("D6 (review D6-1), documented: a one-line link drifted onto a caller's first line, its body naming them",
			"doc.md", "`status_changed` is set by `read_status` ([rgw_fp_ver.cc:21]("+cephURL("v19.2.6", verPath, "L21")+
				")).\n",
			[]string{"1: " + cephURL("v19.2.6", verPath, "L21") + ": listed: line 21 holds none of status_changed, " +
				"read_status, but the definition that starts on it, through line 29, holds one"},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("D6 (review D6-2), documented: a one-line link drifted onto a caller's first line, its body calling it",
			"doc.md", "The status is decoded by `read_status` ([rgw_fp_ver.cc:21]("+cephURL("v19.2.6", verPath, "L21")+
				")).\n",
			[]string{"1: " + cephURL("v19.2.6", verPath, "L21") + ": listed: line 21 holds none of read_status, but the " +
				"definition that starts on it, through line 29, holds one"},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("D5: a C++ keyword its file lacks, which the same lines of a file its paragraph names hold", "doc.md",
			"The `namespace` field is new (rgw_fp_arn.h differs; [rgw_fp_asio.cc:2-7]("+asio27+")).\n",
			[]string{"1: " + asio27 + ": listed: none of the identifiers named beside it, namespace, stands in " +
				"src/rgw/rgw_fp_asio.cc at v19.2.6"},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("D3: a run's names another link of its release holds, the same code at the other release holding none",
			"doc.md", runCopy("L13-L16"),
			[]string{
				"1: " + cephURL("v19.2.6", runPath, "L10-L13") + ": listed: the names beside it, range_to_ofs, are " +
					"other links': each stands in the lines of a link of its paragraph at v19.2.6, " +
					cephURL("v19.2.6", runPath, "L6") + " first; " +
					cephURL("v20.2.4", runPath, "L13-L16") + " cites the same code at v20.2.4 and holds none either",
				"1: " + cephURL("v20.2.4", runPath, "L13-L16") + ": listed: the names beside it, range_to_ofs, are " +
					"other links': each stands in the lines of a link of its paragraph at v20.2.4, " +
					cephURL("v20.2.4", runPath, "L9") + " first; " +
					cephURL("v19.2.6", runPath, "L10-L13") + " cites the same code at v19.2.6 and holds none either",
			},
			"links checked: 4, held: 2, failed: 0, listed: 2", 0),
		Entry("D3: a run's names a link of another run of its paragraph holds, the releases agreeing", "doc.md",
			"The copy runs past `range_to_ofs`'s check ([rgw_fp_run.cc:10-13]("+cephURL("v19.2.6", runPath, "L10-L13")+
				") and [:13-16]("+cephURL("v20.2.4", runPath, "L13-L16")+"); `RGWFp::copy`, [:6]("+
				cephURL("v19.2.6", runPath, "L6")+") and [:9]("+cephURL("v20.2.4", runPath, "L9")+")).\n",
			[]string{
				"1: " + cephURL("v19.2.6", runPath, "L10-L13") + ": listed: the names beside it, range_to_ofs, are " +
					"other links': each stands in the lines of a link of its paragraph at v19.2.6, " +
					cephURL("v19.2.6", runPath, "L6") + " first; " +
					cephURL("v20.2.4", runPath, "L13-L16") + " cites the same code at v20.2.4 and holds none either",
				"1: " + cephURL("v20.2.4", runPath, "L13-L16") + ": listed: the names beside it, range_to_ofs, are " +
					"other links': each stands in the lines of a link of its paragraph at v20.2.4, " +
					cephURL("v20.2.4", runPath, "L9") + " first; " +
					cephURL("v19.2.6", runPath, "L10-L13") + " cites the same code at v19.2.6 and holds none either",
			},
			"links checked: 4, held: 2, failed: 0, listed: 2", 0),
		Entry("D2: a function its text names that reaches its lines' function through two calls", "doc.md",
			"read_line finds a line end within its window ([rgw_fp_read.cc:4-8]("+cephURL("v19.2.6", readPath, "L4-L8")+
				")).\n",
			[]string{"1: " + cephURL("v19.2.6", readPath, "L4-L8") + ": listed: its lines hold none of read_line, but " +
				"they lie in index_of, which read_line reaches through read_with_boundary"},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("D2: a function its text names that its lines reach through two calls", "doc.md",
			"A line end is found by `index_of` ([rgw_fp_read.cc:25]("+cephURL("v19.2.6", readPath, "L25")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", readPath, "L25") + ": listed: its lines hold none of index_of, but they " +
				"reach index_of through read_with_boundary"},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("D2 (review D2-2), documented: a link drifted from the function its text names into a helper it calls",
			"doc.md", "`read_line` caps the window at the chunk size ([rgw_fp_read.cc:4-8]("+
				cephURL("v19.2.6", readPath, "L4-L8")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", readPath, "L4-L8") + ": listed: its lines hold none of read_line, but " +
				"they lie in index_of, which read_line reaches through read_with_boundary"},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("D3 (review D3-1), documented: a run's links drifted identically at both releases, onto other code",
			"doc.md",
			"A range past the end fails `range_to_ofs` once the copy loop reaches it ([rgw_fp_run.cc:6]("+
				cephURL("v19.2.6", runPath, "L6")+") and [:7-9]("+cephURL("v19.2.6", runPath, "L7-L9")+") at v19.2.6, [:9]("+
				cephURL("v20.2.4", runPath, "L9")+") and [:10-12]("+cephURL("v20.2.4", runPath, "L10-L12")+") at v20.2.4).\n",
			[]string{
				"1: " + cephURL("v19.2.6", runPath, "L7-L9") + ": listed: the names beside it, range_to_ofs, are " +
					"other links': each stands in the lines of a link of its paragraph at v19.2.6, " +
					cephURL("v19.2.6", runPath, "L6") + " first; " +
					cephURL("v20.2.4", runPath, "L10-L12") + " cites the same code at v20.2.4 and holds none either",
				"1: " + cephURL("v20.2.4", runPath, "L10-L12") + ": listed: the names beside it, range_to_ofs, are " +
					"other links': each stands in the lines of a link of its paragraph at v20.2.4, " +
					cephURL("v20.2.4", runPath, "L9") + " first; " +
					cephURL("v19.2.6", runPath, "L7-L9") + " cites the same code at v19.2.6 and holds none either",
			},
			"links checked: 4, held: 2, failed: 0, listed: 2", 0),
	)

	DescribeTable("still fails a wrong link of the same shape", reports,
		Entry("D1, documented: an error code in prose its file writes only in a literal, the right lines lacking it",
			"doc.md", "A negative offset is InvalidArgument ([rgw_fp_acl.cc:19-21]("+acl1921+")).\n",
			[]string{"1: " + acl1921 + ": lines 19-21 of src/rgw/rgw_fp_acl.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, InvalidArgument, which stand elsewhere in it; the nearest line that holds one is 6"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D1 (fix round 3): an error code in prose its file writes only in a literal, linking the wrong lines",
			"doc.md", "A negative offset is InvalidArgument ([rgw_fp_acl.cc:11-13]("+acl1113+")).\n",
			[]string{"1: " + acl1113 + ": lines 11-13 of src/rgw/rgw_fp_acl.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, InvalidArgument, which stand elsewhere in it; the nearest line that holds one is 6"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D1, documented: a plural in prose its file writes only in a comment, the right lines lacking it",
			"doc.md",
			"A public canned ACL under a block of public ACLs is refused ([rgw_fp_acl.cc:11-13]("+acl1113+")).\n",
			[]string{"1: " + acl1113 + ": lines 11-13 of src/rgw/rgw_fp_acl.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, ACLs, which stand elsewhere in it; the nearest line that holds one is 4"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D1 (re-review 4): a plural in prose its file writes only in a comment, linking the wrong lines", "doc.md",
			"A public canned ACL under a block of public ACLs is refused ([rgw_fp_acl.cc:19-21]("+acl1921+")).\n",
			[]string{"1: " + acl1921 + ": lines 19-21 of src/rgw/rgw_fp_acl.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, ACLs, which stand elsewhere in it; the nearest line that holds one is 4"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D2: a function its text names that neither reaches its lines' function nor is reached", "doc.md",
			"read_line finds a line end within its window ([rgw_fp_read.cc:30]("+cephURL("v19.2.6", readPath, "L30")+
				")).\n",
			[]string{"1: " + cephURL("v19.2.6", readPath, "L30") + ": line 30 of src/rgw/rgw_fp_read.cc at v19.2.6 holds " +
				"none of the identifiers named beside it, read_line, which stand elsewhere in it; the nearest line that " +
				"holds one is 41"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D2: a function its text names that its lines reach through no call", "doc.md",
			"A line end is found by `index_of` ([rgw_fp_read.cc:30]("+cephURL("v19.2.6", readPath, "L30")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", readPath, "L30") + ": line 30 of src/rgw/rgw_fp_read.cc at v19.2.6 holds " +
				"none of the identifiers named beside it, index_of, which stand elsewhere in it; the nearest line that " +
				"holds one is 14"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D2 (review D2-1): its own text names a function that reaches its lines' function", "doc.md",
			"The window is capped first ([`RGWFp::read_line`]("+cephURL("v19.2.6", readPath, "L4-L8")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", readPath, "L4-L8") + ": lines 4-8 of src/rgw/rgw_fp_read.cc at v19.2.6 " +
				"hold none of the identifiers its text names, RGWFp::read_line, RGWFp, read_line; the nearest line that " +
				"holds one is 17"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D2 (review D2-3): a function its text names that its lines reach only through three calls", "doc.md",
			"`index_of` scans for the newline ([rgw_fp_read.cc:40-41]("+cephURL("v19.2.6", readPath, "L40-L41")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", readPath, "L40-L41") + ": lines 40-41 of src/rgw/rgw_fp_read.cc at " +
				"v19.2.6 hold none of the identifiers named beside it, index_of, which stand elsewhere in it; the " +
				"nearest line that holds one is 14"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D2 (review D2-4): a function that reaches the lines only through another function's lambda", "doc.md",
			"`RGWFp::scan` prints each element ([rgw_fp_lambda.cc:3-4]("+cephURL("v19.2.6", lambdaPath, "L3-L4")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", lambdaPath, "L3-L4") + ": lines 3-4 of src/rgw/rgw_fp_lambda.cc at " +
				"v19.2.6 hold none of the identifiers named beside it, RGWFp::scan, RGWFp, which stand elsewhere in it; " +
				"the nearest line that holds one is 7"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D2, documented: a variable its text names that its lines' function's caller reads", "doc.md",
			"`max_chunk` bounds the window ([rgw_fp_read.cc:14]("+cephURL("v19.2.6", readPath, "L14")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", readPath, "L14") + ": line 14 of src/rgw/rgw_fp_read.cc at v19.2.6 holds " +
				"none of the identifiers named beside it, max_chunk, which stand elsewhere in it; the nearest line that " +
				"holds one is 19"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D1 (review D1-1): a code-span name its file writes only in a comment, the link's file the wrong one",
			"doc.md", "`get_obj_state` reads the head first ([rgw_fp_acl.cc:19-21]("+acl1921+")).\n",
			[]string{"1: " + acl1921 + ": lines 19-21 of src/rgw/rgw_fp_acl.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, get_obj_state, which stand elsewhere in it; the nearest line that holds one is 25"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D1 (review D1-2): its own text names a name its file writes only in a comment", "doc.md",
			"The head is read first ([`get_obj_state`]("+acl1921+")).\n",
			[]string{"1: " + acl1921 + ": lines 19-21 of src/rgw/rgw_fp_acl.cc at v19.2.6 hold none of the identifiers " +
				"its text names, get_obj_state; the nearest line that holds one is 25"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D1: its own text names an error code in prose its file writes only in a literal", "doc.md",
			"A negative offset is refused ([InvalidArgument]("+acl1113+")).\n",
			[]string{"1: " + acl1113 + ": lines 11-13 of src/rgw/rgw_fp_acl.cc at v19.2.6 hold none of the identifiers " +
				"its text names, InvalidArgument; the nearest line that holds one is 6"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D4 (review D4-2): a name after \"no\" as a condition, the link drifted to other lines", "doc.md",
			"A request with no `x-amz-content-sha256` is refused ([rgw_fp_neg.cc:6-7]("+neg67+")).\n",
			[]string{"1: " + neg67 + ": lines 6-7 of src/rgw/rgw_fp_neg.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, x-amz-content-sha256, which stand elsewhere in it; the nearest line that holds one is 21"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D4 (review D4-3): a name after \"does not match\", the link drifted to other lines", "doc.md",
			"An ETag that does not match `If-Match` fails with 412 ([rgw_fp_neg.cc:6-7]("+neg67+")).\n",
			[]string{"1: " + neg67 + ": lines 6-7 of src/rgw/rgw_fp_neg.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, If-Match, which stand elsewhere in it; the nearest line that holds one is 29"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry(`D4, documented: a name after "without", which the right lines lack`, "doc.md",
			"radosgw registers the admin managers without `set_logging` ([rgw_fp_neg.cc:6-7]("+neg67+")).\n",
			[]string{"1: " + neg67 + ": lines 6-7 of src/rgw/rgw_fp_neg.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, set_logging, which stand elsewhere in it; the nearest line that holds one is 14"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry(`D4, documented: a name after "do not" and a verb in a Go comment, which the right lines lack`, "fix.go",
			"package fix\n\n// The admin API's managers do not call set_logging\n// ("+neg67+").\nfunc admin() {}\n",
			[]string{"4: " + neg67 + ": lines 6-7 of src/rgw/rgw_fp_neg.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, set_logging, which stand elsewhere in it; the nearest line that holds one is 14"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D5, documented: a name its file lacks, which the same lines of a file another link of its run cites hold",
			"doc.md",
			"beast sets one `RGWEnv` variable per header field ([rgw_fp_asio.cc:2-7]("+asio27+"), [rgw_fp_env.cc:9-12]("+
				env912+")).\n",
			[]string{"1: " + asio27 + ": none of the identifiers named beside it, RGWEnv, stands in src/rgw/rgw_fp_asio.cc " +
				"at v19.2.6, but lines 2-7 of src/rgw/rgw_fp_env.cc, which its paragraph names, hold RGWEnv"},
			"links checked: 2, held: 1, failed: 1, listed: 0", 1),
		Entry("D5 (review D5-1): a run's second link to the wrong file, its sibling citing the right one", "doc.md",
			"Each header field lands in `RGWEnv` ([rgw_fp_env.cc:3-7]("+cephURL("v19.2.6", envPath, "L3-L7")+
				"), [rgw_fp_asio.cc:9-12]("+cephURL("v19.2.6", asioPath, "L9-L12")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", asioPath, "L9-L12") + ": none of the identifiers named beside it, RGWEnv, " +
				"stands in src/rgw/rgw_fp_asio.cc at v19.2.6, but lines 9-12 of src/rgw/rgw_fp_env.cc, which its " +
				"paragraph names, hold RGWEnv"},
			"links checked: 2, held: 1, failed: 1, listed: 0", 1),
		Entry("D5 (review D5-2): the same wrong link, the right file cited in another run of the paragraph", "doc.md",
			"`RGWEnv` is built in [rgw_fp_env.cc:3-7]("+cephURL("v19.2.6", envPath, "L3-L7")+"). Each header field "+
				"lands in `RGWEnv` ([rgw_fp_asio.cc:9-12]("+cephURL("v19.2.6", asioPath, "L9-L12")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", asioPath, "L9-L12") + ": none of the identifiers named beside it, RGWEnv, " +
				"stands in src/rgw/rgw_fp_asio.cc at v19.2.6, but lines 9-12 of src/rgw/rgw_fp_env.cc, which its " +
				"paragraph names, hold RGWEnv"},
			"links checked: 2, held: 1, failed: 1, listed: 0", 1),
		Entry("D3: a run's names another link of its release holds, the other release's lines another code", "doc.md",
			runCopy("L14-L17"),
			[]string{
				"1: " + cephURL("v19.2.6", runPath, "L10-L13") + ": lines 10-13 of src/rgw/rgw_fp_run.cc at v19.2.6 " +
					"hold none of the identifiers named beside it, range_to_ofs, which stand elsewhere in it; the nearest " +
					"line that holds one is 6",
				"1: " + cephURL("v20.2.4", runPath, "L14-L17") + ": lines 14-17 of src/rgw/rgw_fp_run.cc at v20.2.4 " +
					"hold none of the identifiers named beside it, range_to_ofs, which stand elsewhere in it; the nearest " +
					"line that holds one is 9",
			},
			"links checked: 4, held: 2, failed: 2, listed: 0", 1),
		Entry("D3, documented: a run at one release whose names another link of it holds", "doc.md",
			"A range past the end fails `range_to_ofs` once the copy loop reaches it ([rgw_fp_run.cc:6]("+
				cephURL("v19.2.6", runPath, "L6")+"), [:10-13]("+cephURL("v19.2.6", runPath, "L10-L13")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", runPath, "L10-L13") + ": lines 10-13 of src/rgw/rgw_fp_run.cc at " +
				"v19.2.6 hold none of the identifiers named beside it, range_to_ofs, which stand elsewhere in it; the " +
				"nearest line that holds one is 6"},
			"links checked: 2, held: 1, failed: 1, listed: 0", 1),
		Entry("D3 (review D3-3): a run at two point releases of one major version, both drifted the same way", "doc.md",
			"A range past the end fails `range_to_ofs` once the copy loop reaches it ([rgw_fp_run.cc:6]("+
				cephURL("v19.2.6", runPath, "L6")+") and [:7-9]("+cephURL("v19.2.6", runPath, "L7-L9")+") at v19.2.6, [:6]("+
				cephURL("v19.2.7", runPath, "L6")+") and [:7-9]("+cephURL("v19.2.7", runPath, "L7-L9")+") at v19.2.7).\n",
			[]string{
				"1: " + cephURL("v19.2.6", runPath, "L7-L9") + ": lines 7-9 of src/rgw/rgw_fp_run.cc at v19.2.6 hold none " +
					"of the identifiers named beside it, range_to_ofs, which stand elsewhere in it; the nearest line " +
					"that holds one is 6; at v20.2.4 they hold one",
				"1: " + cephURL("v19.2.7", runPath, "L7-L9") + ": lines 7-9 of src/rgw/rgw_fp_run.cc at v19.2.7 hold none " +
					"of the identifiers named beside it, range_to_ofs, which stand elsewhere in it; the nearest line " +
					"that holds one is 6; at v20.2.4 they hold one",
			},
			"links checked: 4, held: 2, failed: 2, listed: 0", 1),
		Entry("D3 (re-review D3-4): a run drifted identically at both majors, beside a name no other link holds",
			"doc.md",
			"A range past the end fails `range_to_ofs` once the loop's `copy_chunk` reaches it ([rgw_fp_run.cc:6]("+
				cephURL("v19.2.6", runPath, "L6")+") and [:7-9]("+cephURL("v19.2.6", runPath, "L7-L9")+") at v19.2.6, [:9]("+
				cephURL("v20.2.4", runPath, "L9")+") and [:10-12]("+cephURL("v20.2.4", runPath, "L10-L12")+") at v20.2.4).\n",
			[]string{
				"1: " + cephURL("v19.2.6", runPath, "L7-L9") + ": lines 7-9 of src/rgw/rgw_fp_run.cc at v19.2.6 hold none " +
					"of the identifiers named beside it, range_to_ofs, copy_chunk, which stand elsewhere in it; the " +
					"nearest line that holds one is 6; at v20.2.4 they hold one",
				"1: " + cephURL("v20.2.4", runPath, "L10-L12") + ": lines 10-12 of src/rgw/rgw_fp_run.cc at v20.2.4 hold " +
					"none of the identifiers named beside it, range_to_ofs, copy_chunk, which stand elsewhere in it; the " +
					"nearest line that holds one is 9; at v19.2.6 they hold one",
			},
			"links checked: 4, held: 2, failed: 2, listed: 0", 1),
		Entry("D1: a code name beside a plural its file writes only in a comment", "doc.md",
			"`check_range` refuses a public canned ACL under a block of public ACLs ([rgw_fp_acl.cc:11-13]("+acl1113+
				")).\n",
			[]string{"1: " + acl1113 + ": lines 11-13 of src/rgw/rgw_fp_acl.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, check_range, ACLs, which stand elsewhere in it; the nearest line that holds one is 17"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D1: a name in a code span its file writes only in a literal", "doc.md",
			"A negative offset is `InvalidArgument` ([rgw_fp_acl.cc:11-13]("+acl1113+")).\n",
			[]string{"1: " + acl1113 + ": lines 11-13 of src/rgw/rgw_fp_acl.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, InvalidArgument, which stand elsewhere in it; the nearest line that holds one is 6"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D5: a name its file lacks, which the same lines of a file another run of its paragraph cites hold",
			"doc.md",
			"`RGWEnv` is set in [rgw_fp_env.cc:9-12]("+env912+"); `RGWEnv` starts in [rgw_fp_asio.cc:2-7]("+asio27+").\n",
			[]string{"1: " + asio27 + ": none of the identifiers named beside it, RGWEnv, stands in src/rgw/rgw_fp_asio.cc " +
				"at v19.2.6, but lines 2-7 of src/rgw/rgw_fp_env.cc, which its paragraph names, hold RGWEnv"},
			"links checked: 2, held: 1, failed: 1, listed: 0", 1),
		Entry("D6: a one-line link to the first line of a function whose body lacks the name", "doc.md",
			"`MfaDelete` must be Enabled or Disabled ([rgw_fp_ver.cc:16]("+cephURL("v19.2.6", verPath, "L16")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", verPath, "L16") + ": line 16 of src/rgw/rgw_fp_ver.cc at v19.2.6 holds " +
				"none of the identifiers named beside it, MfaDelete, which stand elsewhere in it; the nearest line that " +
				"holds one is 5"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D6: a one-line link to a statement that ends on its line", "doc.md",
			"`MfaDelete` must be Enabled or Disabled ([rgw_fp_ver.cc:4]("+cephURL("v19.2.6", verPath, "L4")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", verPath, "L4") + ": line 4 of src/rgw/rgw_fp_ver.cc at v19.2.6 holds " +
				"none of the identifiers named beside it, MfaDelete, which stand elsewhere in it; the nearest line that " +
				"holds one is 5"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D6 (review D6-3): a one-line link whose own text names what the definition its line starts calls",
			"doc.md", "The status is decoded first ([`read_status`]("+cephURL("v19.2.6", verPath, "L21")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", verPath, "L21") + ": line 21 of src/rgw/rgw_fp_ver.cc at v19.2.6 holds " +
				"none of the identifiers its text names, read_status; the nearest line that holds one is 23"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry(`D7: "A at v19.2.6 and B, C at v20.2.4 and D", C linked at v19.2.6`, "fix.go",
			lcRuns("v19.2.6", "v19.2.6"),
			[]string{"6: " + cephURL("v19.2.6", lcPath, "L2-L5") + `: its text names v20.2.4, by the "at v20.2.4" after ` +
				"it, but it links lines at v19.2.6"},
			"links checked: 4, held: 3, failed: 1, listed: 0", 1),
		Entry(`D7 (review D7-1): "A at v19.2.6 and B, C at v20.2.4 and D", B linked at v20.2.4`, "fix.go",
			lcRuns("v20.2.4", "v20.2.4"),
			[]string{"5: " + cephURL("v20.2.4", lcPath, "L9-L12") + `: its text names v19.2.6, by the "at v19.2.6" it ` +
				`is joined to by "and" or "or", but it links lines at v20.2.4`},
			"links checked: 4, held: 3, failed: 1, listed: 0", 1),
		Entry(`D7: "A at v19.2.6 and B and C at v20.2.4", B linked at v19.2.6`, "doc.md",
			lcJoined("v19.2.6"),
			[]string{"1: " + cephURL("v19.2.6", lcPath, "L2-L5") + `: its text names v20.2.4, by the "at v20.2.4" after ` +
				"it, but it links lines at v19.2.6"},
			"links checked: 3, held: 2, failed: 1, listed: 0", 1),
		Entry(`D7: "A and B at v19.2.6, C and D at v20.2.4", C linked at v19.2.6, a comma starting its group`, "doc.md",
			lcPairs("v19.2.6"),
			[]string{"1: " + cephURL("v19.2.6", lcPath, "L2-L5") + `: its text names v20.2.4, by the "at v20.2.4" after ` +
				"it, but it links lines at v19.2.6"},
			"links checked: 4, held: 3, failed: 1, listed: 0", 1),
		Entry("D8: a qualified name whose last part its lines declare inside another namespace", "doc.md",
			"Service is `rgw::Service` ([rgw_fp_arn.h:14-16]("+cephURL("v19.2.6", arnPath, "L14-L16")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", arnPath, "L14-L16") + ": lines 14-16 of src/rgw/rgw_fp_arn.h at " +
				"v19.2.6 hold none of the identifiers named beside it, rgw::Service, which stand elsewhere in it; the " +
				"nearest line that holds one is 20"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("D8: a qualified name whose last part its lines use bare, in a function's body", "doc.md",
			"Service is `rgw::Service` ([rgw_fp_arn.h:24-25]("+cephURL("v19.2.6", arnPath, "L24-L25")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", arnPath, "L24-L25") + ": lines 24-25 of src/rgw/rgw_fp_arn.h at " +
				"v19.2.6 hold none of the identifiers named beside it, rgw::Service, which stand elsewhere in it; the " +
				"nearest line that holds one is 20"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
	)
})
