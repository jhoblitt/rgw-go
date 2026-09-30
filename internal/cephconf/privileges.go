package cephconf

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// The process's ids and account lookups, which specs replace to take the
// root path without being root.
var (
	getuid      = os.Getuid
	setgid      = syscall.Setgid
	setuid      = syscall.Setuid
	lookupUser  = user.Lookup
	lookupGroup = user.LookupGroup
)

// DropPrivileges is global_init's setuser/setgroup handling followed by
// AsioFrontend's drop_privileges: as root, resolve setuser and setgroup (a
// number or a name), honor setuser_match_path, then setgid and setuid for
// every thread, librados's included. As a non-root user it logs and does
// nothing. It runs after the RADOS connect and after every listener is
// bound, so privileged ports bind as the launching user, and before serving;
// serve enforces that order.
//
// o must hold setuser, setgroup and setuser_match_path as read before
// librados fetches the mon config. global_init fixes the ids before it
// fetches that config (global_init.cc at v19.2.6 and v20.2.4), so a value
// in the mon config database never reaches radosgw's drop, while Options
// over a connected cluster would return it.
//
// A uid or gid of zero means no change, as it does in radosgw, so setuser
// root keeps the gateway root. rgw-go also takes setuser 0 as root, where
// radosgw looks "0" up as a user name and fails to start without one.
func DropPrivileges(o *Options) error {
	setuser, err := o.String("setuser")
	if err != nil {
		return err
	}
	setgroup, err := o.String("setgroup")
	if err != nil {
		return err
	}
	if setuser == "" && setgroup == "" {
		return nil
	}
	if getuid() != 0 {
		slog.Info("ignoring setuser and setgroup, not running as root",
			slog.String("setuser", setuser), slog.String("setgroup", setgroup))
		return nil
	}
	var uid, gid int
	if setuser != "" {
		if uid, gid, err = resolveUser(setuser); err != nil {
			return err
		}
	}
	if setgroup != "" {
		if gid, err = resolveGroup(setgroup); err != nil {
			return err
		}
	}
	if uid == 0 && gid == 0 {
		return nil
	}
	// librados expands the path's metavariables as it returns the value, as
	// global_init's early_expand_meta does.
	path, err := o.String("setuser_match_path")
	if err != nil {
		return err
	}
	if path != "" {
		ok, err := matchPathAllows(path, uid, gid)
		if err != nil {
			return err
		}
		if !ok {
			slog.Warn("not dropping privileges, setuser_match_path has another owner",
				slog.String("path", path), slog.Int("uid", uid), slog.Int("gid", gid))
			return nil
		}
	}
	return setIDs(uid, gid)
}

// resolveUser resolves setuser as global_init does (global_init.cc at
// v19.2.6 and v20.2.4): a number is the uid and leaves the group unchanged;
// a name takes its passwd entry's uid and primary group. global_init reads
// the number with atoi and looks a zero up as a name, so radosgw fails on
// "0" where rgw-go takes root's uid.
func resolveUser(s string) (uid, gid int, err error) {
	if n, ok := number(s); ok {
		return n, 0, nil
	}
	u, err := lookupUser(s)
	if err != nil {
		return 0, 0, fmt.Errorf("resolving setuser: %w", err)
	}
	if uid, err = strconv.Atoi(u.Uid); err != nil {
		return 0, 0, fmt.Errorf("resolving setuser %s: %w", s, err)
	}
	if gid, err = strconv.Atoi(u.Gid); err != nil {
		return 0, 0, fmt.Errorf("resolving setuser %s: %w", s, err)
	}
	return uid, gid, nil
}

// resolveGroup resolves setgroup as global_init does: a number is the gid,
// a name takes its group entry's gid.
func resolveGroup(s string) (int, error) {
	if n, ok := number(s); ok {
		return n, nil
	}
	g, err := lookupGroup(s)
	if err != nil {
		return 0, fmt.Errorf("resolving setgroup: %w", err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("resolving setgroup %s: %w", s, err)
	}
	return gid, nil
}

// number reads s as a uid or gid: a whole decimal string from 0 to
// 2147483647, the most an int holds on every platform. global_init's atoi
// takes any leading number instead, modulo 2^32 on glibc x86_64.
func number(s string) (int, bool) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n > math.MaxInt32 {
		return 0, false
	}
	return int(n), true
}

// matchPathAllows is global_init's setuser_match_path check: the drop goes
// ahead only when path is owned by each id it changes. A path that cannot be
// stat'd is an error, as it stops radosgw at v19.2.6 and v20.2.4.
func matchPathAllows(path string, uid, gid int) (bool, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false, fmt.Errorf("stat setuser_match_path %s: %w", path, err)
	}
	return (uid == 0 || uid == int(st.Uid)) && (gid == 0 || gid == int(st.Gid)), nil
}

// setIDs is drop_privileges (rgw_asio_frontend.cc at v19.2.6 and v20.2.4):
// setgid, then setuid, each only for a non-zero id. Supplementary groups are
// kept, as radosgw keeps them: under Kubernetes they carry the pod's
// fsGroup and supplementalGroups, and in a user namespace that denies
// setgroups, clearing them would fail where radosgw starts. With cgo linked,
// as it is wherever librados is, the runtime makes both calls through libc,
// which applies them to every thread in the process.
func setIDs(uid, gid int) error {
	if gid != 0 {
		if err := setgid(gid); err != nil {
			return fmt.Errorf("setgid %d: %w", gid, err)
		}
	}
	if uid != 0 {
		if err := setuid(uid); err != nil {
			return fmt.Errorf("setuid %d: %w", uid, err)
		}
	}
	slog.Info("set uid and gid", slog.Int("uid", uid), slog.Int("gid", gid))
	return nil
}
