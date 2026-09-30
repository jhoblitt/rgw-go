package cephconf

import "os/user"

// DropPrivileges's resolution and match-path check, reached directly.
var (
	ResolveUser     = resolveUser
	ResolveGroup    = resolveGroup
	MatchPathAllows = matchPathAllows
)

// System is what DropPrivileges runs against: the process's uid, the id
// setters and the account lookups.
type System struct {
	Getuid      func() int
	Setgid      func(gid int) error
	Setuid      func(uid int) error
	LookupUser  func(name string) (*user.User, error)
	LookupGroup func(name string) (*user.Group, error)
}

// SetSystem makes DropPrivileges and the resolution run against s until the
// returned restore runs. Every field is replaced, so a field left nil
// panics when called instead of reaching the real process.
func SetSystem(s System) (restore func()) {
	old := System{getuid, setgid, setuid, lookupUser, lookupGroup}
	getuid, setgid, setuid, lookupUser, lookupGroup = s.Getuid, s.Setgid, s.Setuid, s.LookupUser, s.LookupGroup
	return func() {
		getuid, setgid, setuid = old.Getuid, old.Setgid, old.Setuid
		lookupUser, lookupGroup = old.LookupUser, old.LookupGroup
	}
}
