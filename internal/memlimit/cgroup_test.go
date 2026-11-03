package memlimit_test

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memlimit"
)

// newCgroup is a Cgroup over an empty proc filesystem and cgroup mount in a
// directory of the spec's own.
func newCgroup() memlimit.Cgroup {
	root := GinkgoT().TempDir()
	return memlimit.Cgroup{Proc: filepath.Join(root, "proc"), Mount: filepath.Join(root, "cgroup")}
}

// writeFile writes content to name, a slash-separated path under dir.
func writeFile(dir, name, content string) {
	GinkgoHelper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	Expect(os.MkdirAll(filepath.Dir(p), 0o750)).To(Succeed())
	Expect(os.WriteFile(p, []byte(content), 0o600)).To(Succeed())
}

// layout puts cg's process in the cgroup self/cgroup names, under a cgroup
// v2 mount with the memory controller, and gives each cgroup in maxes, a
// directory under the mount ("" for its root), its memory.max.
func layout(cg memlimit.Cgroup, self string, maxes map[string]string) {
	GinkgoHelper()
	writeFile(cg.Proc, "self/cgroup", self)
	writeFile(cg.Mount, "cgroup.controllers", "cpuset cpu io memory hugetlb pids\n")
	for dir, limit := range maxes {
		writeFile(cg.Mount, path.Join(dir, "cgroup.controllers"), "memory pids\n")
		writeFile(cg.Mount, path.Join(dir, "memory.max"), limit)
	}
}

var _ = Describe("Cgroup.Read", func() {
	var cg memlimit.Cgroup

	BeforeEach(func() {
		cg = newCgroup()
	})

	// file is the memory.max of the cgroup at dir under the mount.
	file := func(dir string) string {
		return filepath.Join(cg.Mount, filepath.FromSlash(dir), "memory.max")
	}

	It("reads a finite memory.max in the process's own cgroup", func() {
		layout(cg, "0::/kubepods/pod1/ctr\n", map[string]string{"kubepods/pod1/ctr": "2147483648\n"})
		Expect(cg.Read()).To(Equal(memlimit.Limit{Bytes: 2147483648, File: file("kubepods/pod1/ctr")}))
	})

	It("takes a parent's finite limit where the process's own cgroup has max", func() {
		layout(cg, "0::/a/b\n", map[string]string{"a/b": "max\n", "a": "1073741824\n"})
		Expect(cg.Read()).To(Equal(memlimit.Limit{Bytes: 1073741824, File: file("a")}))
	})

	DescribeTable("takes the smallest finite limit from the process's cgroup up to the mount root",
		func(leaf, parent, root string, want memlimit.Limit) {
			layout(cg, "0::/a/b\n", map[string]string{"a/b": leaf, "a": parent, "": root})
			want.File = filepath.Join(cg.Mount, want.File)
			Expect(cg.Read()).To(Equal(want))
		},
		Entry("the process's own cgroup's, under a larger parent's", "4096\n", "8192\n", "max\n",
			memlimit.Limit{Bytes: 4096, File: "a/b/memory.max"}),
		Entry("a parent's, over a larger one in the process's own cgroup", "8192\n", "4096\n", "max\n",
			memlimit.Limit{Bytes: 4096, File: "a/memory.max"}),
		Entry("the mount root's, the smallest of three", "8192\n", "6144\n", "4096\n",
			memlimit.Limit{Bytes: 4096, File: "memory.max"}),
	)

	It("passes over a cgroup with no memory.max, as one whose parent does not enable the memory controller has", func() {
		layout(cg, "0::/a/b\n", map[string]string{"a": "4096\n"})
		writeFile(cg.Mount, "a/b/cgroup.controllers", "pids\n")
		Expect(cg.Read()).To(Equal(memlimit.Limit{Bytes: 4096, File: file("a")}))
	})

	It("fails with ErrNoLimit when every memory.max is max", func() {
		layout(cg, "0::/a/b\n", map[string]string{"a/b": "max\n", "a": "max\n"})
		_, err := cg.Read()
		Expect(err).To(MatchError(memlimit.ErrNoLimit))
	})

	It("reads the mount root as the process's cgroup when its own cgroup namespace puts it at 0::/", func() {
		layout(cg, "0::/\n", map[string]string{"": "536870912\n"})
		Expect(cg.Read()).To(Equal(memlimit.Limit{Bytes: 536870912, File: filepath.Join(cg.Mount, "memory.max")}))
	})

	DescribeTable("names a malformed memory.max and what is wrong with it",
		func(content, quoted string) {
			layout(cg, "0::/a\n", map[string]string{"a": content})
			_, err := cg.Read()
			Expect(err).To(MatchError(ContainSubstring(file("a"))))
			Expect(err).To(MatchError(ContainSubstring(quoted)))
			Expect(err).NotTo(MatchError(memlimit.ErrNoLimit))
		},
		Entry("a size with a unit", "2G\n", `"2G"`),
		Entry("a negative count, which no byte count is", "-1\n", `"-1"`),
	)

	It("names a memory.max it cannot read", func() {
		if os.Geteuid() == 0 {
			Skip("root reads a file without read permission")
		}
		layout(cg, "0::/a\n", map[string]string{"a": "4096\n"})
		Expect(os.Chmod(file("a"), 0)).To(Succeed())
		_, err := cg.Read()
		Expect(err).To(MatchError(fs.ErrPermission))
		Expect(err).To(MatchError(ContainSubstring(file("a"))))
	})

	It("names a cgroup.controllers it cannot read", func() {
		if os.Geteuid() == 0 {
			Skip("root reads a file without read permission")
		}
		layout(cg, "0::/a\n", map[string]string{"a": "4096\n"})
		controllers := filepath.Join(cg.Mount, "cgroup.controllers")
		Expect(os.Chmod(controllers, 0)).To(Succeed())
		_, err := cg.Read()
		Expect(err).To(MatchError(fs.ErrPermission))
		Expect(err).To(MatchError(ContainSubstring(controllers)))
	})

	It("names where it looked for the cgroup v2 mount when no cgroup v2 hierarchy is mounted there", func() {
		writeFile(cg.Proc, "self/cgroup", "0::/a\n")
		writeFile(cg.Mount, "a/memory.max", "4096\n")
		_, err := cg.Read()
		Expect(err).To(MatchError(memlimit.ErrNoCgroupV2))
		Expect(err).To(MatchError(ContainSubstring("no cgroup v2 mount at " + cg.Mount + ",")))
	})

	DescribeTable("fails with ErrNoCgroupV2 where no cgroup v2 memory controller covers the process",
		func(self, controllers, reason string) {
			writeFile(cg.Proc, "self/cgroup", self)
			writeFile(cg.Mount, "cgroup.controllers", controllers)
			writeFile(cg.Mount, "a/memory.max", "4096\n")
			_, err := cg.Read()
			Expect(err).To(MatchError(memlimit.ErrNoCgroupV2))
			Expect(err).To(MatchError(ContainSubstring(reason)))
		},
		Entry("cgroup v1, with no 0:: line", "12:memory:/a\n11:pids:/a\n", "memory pids\n", "has no 0:: line"),
		Entry("a hybrid hierarchy whose memory controller is v1's", "4:memory:/a\n0::/a\n", "memory pids\n", "hierarchy 4"),
		Entry("a cgroup v2 mount without the memory controller", "0::/a\n", "cpu pids\n", "cgroup.controllers"),
	)

	It("names /proc/self/cgroup when it cannot read it", func() {
		_, err := cg.Read()
		Expect(err).To(MatchError(fs.ErrNotExist))
		Expect(err).To(MatchError(ContainSubstring(filepath.Join(cg.Proc, "self/cgroup"))))
	})

	It("names the process's cgroup when the mount does not hold it", func() {
		layout(cg, "0::/a/b\n", map[string]string{"a": "4096\n"})
		_, err := cg.Read()
		Expect(err).To(MatchError(fs.ErrNotExist))
		Expect(err).To(MatchError(ContainSubstring(filepath.Join(cg.Mount, "a/b"))))
	})

	DescribeTable("names /proc/self/cgroup when it cannot parse it",
		func(self, reason string) {
			layout(cg, self, map[string]string{"a": "4096\n"})
			_, err := cg.Read()
			Expect(err).To(MatchError(ContainSubstring(filepath.Join(cg.Proc, "self/cgroup"))))
			Expect(err).To(MatchError(ContainSubstring(reason)))
			Expect(err).NotTo(MatchError(memlimit.ErrNoCgroupV2))
		},
		Entry("a line that is not hierarchy-ID:controllers:path", "0:/a\n", `"0:/a"`),
		Entry("a cgroup outside the hierarchy's root", "0::/../a\n", `"/../a"`),
		Entry("a relative cgroup path", "0::a\n", `"a"`),
	)
})
