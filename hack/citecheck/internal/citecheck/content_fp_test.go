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

// newFPFixture returns a ceph fixture whose v19.2.6 and v20.2.4, the
// releases the [S] and [T] markers name, hold the files the specs of the
// sweep's false positives cite.
func newFPFixture() *cephFixture {
	r := newCephFixture()
	r.write(arnPath, fixtureArn)
	r.write(lcPath, fixtureLC)
	r.commit("squid")
	r.git("tag", "v19.2.6")
	lines := strings.SplitAfter(fixtureLC, "\n")
	r.write(lcPath, lines[0]+fixtureLCHelper+strings.Join(lines[1:], ""))
	r.commit("tentacle")
	r.git("tag", "v20.2.4")
	return r
}

const (
	arnPath = "src/rgw/rgw_fp_arn.h"
	lcPath  = "src/rgw/rgw_fp_lc.cc"
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

	DescribeTable("still fails a wrong link of the same shape", reports,
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
