package main

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("the analysis", func() {
	It("sums smaps Rss by kind of mapping", func() {
		smaps := strings.Join([]string{
			"55d79ad4b000-55d79ad4c000 r-xp 00000000 fd:01 123 /usr/bin/radosgw",
			"Rss:                   4 kB",
			"55d79b000000-55d79c000000 rw-p 00000000 00:00 0 [heap]",
			"Rss:               10240 kB",
			"161c5e400000-161cbb800000 rw-p 00000000 00:00 0",
			"Rss:              204800 kB",
			"7f0000000000-7f0000001000 ---p 00000000 00:00 0",
			"Rss:                   0 kB",
			"7f0000001000-7f0000801000 rw-p 00000000 00:00 0",
			"Rss:                  64 kB",
			"7f1000000000-7f1000400000 rw-p 00000000 00:00 0",
			"Rss:                2048 kB",
			"7ffe2bed5000-7ffe2bef6000 rw-p 00000000 00:00 0 [stack]",
			"Rss:                  32 kB",
			"VmFlags: rd wr mr mw me gd ac",
		}, "\n")
		c, err := smapsClasses(strings.NewReader(smaps))
		Expect(err).NotTo(HaveOccurred())
		Expect(c).To(Equal(map[string]int64{
			classFile: 4, classBrk: 10240, classGoHeap: 204800, classAnon: 2048, classStacks: 64, classProcStk: 32,
		}))
	})

	It("takes a phase's peak to 1 s past its end and its steady state from its second half", func() {
		r := &Run{Marks: map[string][2]float64{"p": {10, 20}}}
		for t := 0.0; t <= 30; t++ {
			v := int64(100)
			switch {
			case t == 21:
				v = 900
			case t == 25:
				v = 5000
			case t >= 15 && t <= 20:
				v = 300
			}
			r.Samples = append(r.Samples, Sample{T: t, Status: map[string]int64{"VmRSS": v * 1024}})
		}
		m := metricByName("VmRSS")
		p, ok := r.Peak("p", m)
		Expect(ok).To(BeTrue())
		Expect(p).To(Equal(900.0))
		s, ok := r.Steady("p", m)
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(300.0))
	})

	It("reads radosgw's PSS from the in-pod loop by time", func() {
		r := &Run{PSS: [][5]float64{{10.2, 0, 2048, 0, 0}, {11.2, 0, 4096, 0, 0}}}
		v, ok := pssAt(r, Sample{T: 11.0})
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(4.0))
		_, ok = pssAt(r, Sample{T: 20})
		Expect(ok).To(BeFalse())
	})

	It("reads gctrace lines, summing their five CPU figures", func() {
		trace := "{\"time\":\"x\"}\n" +
			"gc 17 @30.051s 2%: 0.10+5.0+0.20 ms clock, 3.2+1.5/40/2.5+6.4 ms cpu, 1527->1531->713 MB, 1565 MB goal, 0 MB stacks, 0 MB globals, 32 P\n" +
			"gc 18 @49.621s 2%: 0.1+1+0.1 ms clock, 1+0/2/3+4 ms cpu, 1393->1394->831 MB, 1428 MB goal, 0 MB stacks, 0 MB globals, 32 P (forced)\n"
		ls := parseGCTrace([]byte(trace))
		Expect(ls).To(HaveLen(2))
		Expect(ls[0]).To(Equal(gcLine{N: 17, CPUms: 53.6, Start: 1527, End: 1531, Live: 713, Goal: 1565}))
		Expect(ls[1].N).To(Equal(18))
		Expect(ls[1].CPUms).To(Equal(10.0))
	})

	It("reads marks into windows", func() {
		m := parseMarks([]byte("1.5,put64,start\n7.25,put64,end\n9,idle,start\n"))
		Expect(m).To(Equal(map[string][2]float64{"put64": {1.5, 7.25}, "idle": {9, 0}}))
	})
})
