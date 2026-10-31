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

// newFPFixture returns a ceph fixture whose v19.2.6 and v20.2.4, the
// releases the [S] and [T] markers name, hold the files the specs of the
// sweep's false positives cite.
func newFPFixture() *cephFixture {
	r := newCephFixture()
	r.write(arnPath, fixtureArn)
	r.write(lcPath, fixtureLC)
	r.write(verPath, fixtureVer)
	r.write(asioPath, fixtureAsio)
	r.write(envPath, fixtureEnv)
	r.commit("squid")
	r.git("tag", "v19.2.6")
	lines := strings.SplitAfter(fixtureLC, "\n")
	r.write(lcPath, lines[0]+fixtureLCHelper+strings.Join(lines[1:], ""))
	r.commit("tentacle")
	r.git("tag", "v20.2.4")
	return r
}

const (
	arnPath  = "src/rgw/rgw_fp_arn.h"
	lcPath   = "src/rgw/rgw_fp_lc.cc"
	verPath  = "src/rgw/rgw_fp_ver.cc"
	asioPath = "src/rgw/rgw_fp_asio.cc"
	envPath  = "src/rgw/rgw_fp_env.cc"
)

var (
	// asio27 is ClientIO::init_env's lines, and env912 RGWEnv::set's.
	asio27 = cephURL("v19.2.6", asioPath, "L2-L7")
	env912 = cephURL("v19.2.6", envPath, "L9-L12")
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
	)

	DescribeTable("still fails a wrong link of the same shape", reports,
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
