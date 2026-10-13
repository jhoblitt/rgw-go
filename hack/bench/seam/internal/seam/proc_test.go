package seam_test

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/gomega"
)

// processState is a process's state letter from /proc, or "gone".
func processState(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "gone"
	}
	_, after, ok := strings.Cut(string(data), ") ")
	if !ok || after == "" {
		return "gone"
	}
	return after[:1]
}

// childPid reads the pid a spec's /bin/sh wrote, waiting for it to appear.
func childPid(path string) int {
	var pid int
	Eventually(func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err
	}).WithTimeout(5 * time.Second).WithPolling(20 * time.Millisecond).Should(Succeed())
	return pid
}
