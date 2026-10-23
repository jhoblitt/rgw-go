package clusterconf

import (
	"encoding/json"
	"time"
)

var (
	Reads     = reads
	StampTime = stampTime
)

// ChangedSince is changedSince over ceph config log -f json's output.
func ChangedSince(log []byte, window int, option, entity string, start time.Time) (bool, error) {
	var sets []changeSet
	if err := json.Unmarshal(log, &sets); err != nil {
		return false, err
	}
	return changedSince(sets, window, option, entity, start)
}
