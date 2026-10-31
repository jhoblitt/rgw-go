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

// newFPFixture returns a ceph fixture whose v19.2.6 and v20.2.4, the
// releases the [S] and [T] markers name, hold the files the specs of the
// sweep's false positives cite, and v20.2.4 rgw_fp_new.cc besides.
func newFPFixture() *cephFixture {
	r := newCephFixture()
	r.write("src/rgw/rgw_fp_arn.h", fixtureArn)
	r.commit("squid")
	r.git("tag", "v19.2.6")
	r.write("src/rgw/rgw_fp_new.cc", "// new\nint fresh() { return 0; }\n")
	r.commit("tentacle")
	r.git("tag", "v20.2.4")
	return r
}

const arnPath = "src/rgw/rgw_fp_arn.h"

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
	)

	DescribeTable("still fails a wrong link of the same shape", reports,
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
