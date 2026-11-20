package cephtest

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // Ginkgo's DSL is meant to be dot-imported
	. "github.com/onsi/gomega"    //nolint:revive // Gomega's DSL is meant to be dot-imported
)

const (
	// ServiceCloseTimeout bounds the wait, from a registered daemon's exit,
	// for its registration to leave the service map. The close librados
	// sends as it shuts down removes the entry from the manager's pending
	// map at once
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/MgrClient.cc#L67-L101 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/MgrClient.cc#L68-L102 at v20.2.4;
	// https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/DaemonServer.cc#L686-L710 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/DaemonServer.cc#L690-L714 at v20.2.4),
	// and the map service dump reads follows on the manager's next report
	// to the mon, each mgr_tick_period, 2 s
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/mgr.yaml.in#L410-L419 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/mgr.yaml.in#L439-L448 at v20.2.4),
	// and the mon's commit. It is under mgr_service_beacon_grace, 60 s
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/mgr.yaml.in#L113-L118 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/mgr.yaml.in#L113-L118 at v20.2.4),
	// after which the manager drops a silent daemon anyway, so only the close
	// removes the registration within it.
	ServiceCloseTimeout = 30 * time.Second
	// ServiceReregisteredTimeout bounds the wait, from a registered daemon's
	// exit, for an entry librados registered again as it shut down to leave
	// the map (docs/ceph-upstream-bugs.md, "librados registers a service
	// daemon again while it shuts down"). No close follows that registration
	// and it sends no beacon, so the manager's first prune after it stamps
	// its beacon and a prune mgr_service_beacon_grace, 60 s, later drops it
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/DaemonServer.cc#L2677-L2711 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/DaemonServer.cc#L3156-L3190 at v20.2.4),
	// both on the manager's 2 s tick, then the mon publishes the map: about
	// 65 s, and 90 s leaves margin.
	ServiceReregisteredTimeout = 90 * time.Second
	// ServiceSettle is how long the map must list nothing under a daemon's
	// key before its entry counts as gone. The close and the registration
	// librados can send after it reach the manager up to 1 s apart, so a map
	// published between them lists neither, and the next, one 2 s tick
	// later, lists the new one; five ticks leave margin.
	ServiceSettle = 10 * time.Second

	// servicePoll is how often the departure wait reads the map.
	servicePoll = time.Second
)

// ServiceRegistration is one registration of a daemon in ceph service dump
// -f json's output, which a spec's reader decodes the entry into: the
// manager stamps an entry anew whenever it adds it.
type ServiceRegistration struct {
	// StartEpoch and StartStamp are the map's epoch and the manager's clock
	// when it last added the entry.
	StartEpoch uint64 `json:"start_epoch"`
	StartStamp string `json:"start_stamp"`
	// Raw is the entry as the map gives it, for messages.
	Raw string `json:"-"`
}

// registration names r, or none.
func registration(r *ServiceRegistration) string {
	if r == nil {
		return "no entry"
	}
	return fmt.Sprintf("start_epoch %d, start_stamp %s", r.StartEpoch, r.StartStamp)
}

// rawOf is r as the map gave it, or none.
func rawOf(r *ServiceRegistration) string {
	if r == nil {
		return "no entry"
	}
	return r.Raw
}

// ServiceDeparture is a daemon that registered in the manager's service map
// and has stopped.
type ServiceDeparture struct {
	// Who is the daemon's possessive in messages and report entries, such as
	// "rgw-go's".
	Who string
	// Key is the daemon's name in the map; Registered is the registration
	// found under it before the daemon stopped.
	Key        string
	Registered ServiceRegistration
	// Exit names the moment the daemon stopped, such as "rgw-go exited", and
	// Exited is that moment: librados has sent its close by then.
	Exit   string
	Exited time.Time
}

// departureBounds are a departure wait's bounds, from the exit, and its
// polling.
type departureBounds struct {
	close, again, settle, poll time.Duration
}

// Await waits for d's entry to leave the map and returns the registration
// librados added again under d.Key as the daemon shut down, or nil when the
// entry left by its close. read is the spec's reader of the map: the entry
// under d.Key, or nil when the map lists none. A read that fails must fail
// g, never return nil: only a read that succeeds can find the key empty, and
// a failed read restarts the ServiceSettle an absence must hold for.
//
// librados's close removes d.Registered within ServiceCloseTimeout of the
// exit. While it shuts down librados can also register the daemon again
// under the same key, with a later start_stamp, and that entry leaves only
// within ServiceReregisteredTimeout of the exit. Await takes either, fails
// the spec at once when an entry outlasts its path's bound, and adds a
// report entry naming the path that held and when the entry left. A
// registration other than d.Registered, seen under the key at any poll, is
// the second path. A re-registered entry stays listed for about a minute, so
// the 1 s polls cannot miss it; only one the map first listed more than
// ServiceSettle after it stopped listing the key would go unrecorded, and
// Await would pass on the close.
func (d ServiceDeparture) Await(ctx context.Context, read func(Gomega) *ServiceRegistration) *ServiceRegistration {
	GinkgoHelper()
	return d.await(ctx, read, departureBounds{
		close: ServiceCloseTimeout, again: ServiceReregisteredTimeout, settle: ServiceSettle, poll: servicePoll,
	})
}

func (d ServiceDeparture) await(ctx context.Context, read func(Gomega) *ServiceRegistration, b departureBounds) *ServiceRegistration {
	GinkgoHelper()
	var (
		// last is the entry under the key the last successful read found,
		// and lastRead when that read was.
		last     *ServiceRegistration
		lastRead time.Time
		// reads and failed count the reads and those that failed.
		reads, failed int
		// again is the first entry under the key with another registration
		// than d.Registered.
		again *ServiceRegistration
		// absent is when the settle began: the first successful read to find
		// no entry under the key since a read failed or found one.
		absent time.Time
		// firstAbsent is when the entry left: the first successful read to
		// find no entry since one found it. A failed read does not move it.
		firstAbsent time.Time
	)
	sinceExit := func(t time.Time) time.Duration { return t.Sub(d.Exited).Round(100 * time.Millisecond) }
	since := func() time.Duration { return sinceExit(time.Now()) }
	left := Eventually(func(g Gomega) bool {
		reads++
		succeeded := false
		// A failed read unwinds through here, out of the polled function.
		defer func() {
			if !succeeded {
				failed++
				absent = time.Time{}
			}
		}()
		cur := read(g)
		succeeded = true
		last, lastRead = cur, time.Now()
		if last == nil {
			if absent.IsZero() {
				absent = lastRead
			}
			if firstAbsent.IsZero() {
				firstAbsent = lastRead
			}
			return time.Since(absent) >= b.settle
		}
		absent, firstAbsent = time.Time{}, time.Time{}
		switch {
		case last.StartEpoch == d.Registered.StartEpoch && last.StartStamp == d.Registered.StartStamp:
			if time.Since(d.Exited) > b.close {
				StopTrying(fmt.Sprintf("the close did not remove %s registration under %s (%s): still listed %s after %s, past %s: %s",
					d.Who, d.Key, registration(last), since(), d.Exit, b.close, last.Raw)).Now()
			}
		case time.Since(d.Exited) > b.again:
			StopTrying(fmt.Sprintf("%s entry under %s was registered again (%s, where it was %s): still listed %s after %s, past %s: %s",
				d.Who, d.Key, registration(last), registration(&d.Registered), since(), d.Exit, b.again, last.Raw)).Now()
		case again == nil:
			e := *last
			again = &e
		}
		return false
	}).WithContext(ctx).WithTimeout(b.again+b.settle+5*b.poll).WithPolling(b.poll).
		Should(BeTrue(), func() string {
			path := "no other registration seen"
			if again != nil {
				path = "registered again (" + registration(again) + ")"
			}
			found := "no read succeeded"
			if !lastRead.IsZero() {
				found = fmt.Sprintf("the last successful read, %s after %s, finding %s: %s",
					sinceExit(lastRead), d.Exit, registration(last), rawOf(last))
			}
			return fmt.Sprintf("%s entry under %s %s after %s, %s; %s; %d of %d reads failed",
				d.Who, d.Key, since(), d.Exit, path, found, failed, reads)
		})
	// A fail handler that does not abort the spec, as under
	// InterceptGomegaFailures, returns here: the failure names the path.
	if !left {
		return again
	}
	gone := firstAbsent.Sub(d.Exited).Round(100 * time.Millisecond)
	if again != nil {
		AddReportEntry(d.Who+" entry was registered again as it stopped", fmt.Sprintf(
			"%s, where it was %s; the entry left the map %s after %s: %s",
			registration(again), registration(&d.Registered), gone, d.Exit, again.Raw))
	} else {
		AddReportEntry(d.Who+" registration left the map by its close", fmt.Sprintf(
			"%s after %s, and no other registration under %s in the %s after", gone, d.Exit, d.Key, b.settle))
	}
	return again
}
