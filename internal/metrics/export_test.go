package metrics

import (
	"io/fs"

	"github.com/prometheus/client_golang/prometheus"
)

// ThreadsCollector returns the rgw_go_process_threads collector reading proc
// in place of /proc.
func ThreadsCollector(proc fs.FS) prometheus.Collector { return threadsCollector{proc: proc} }
