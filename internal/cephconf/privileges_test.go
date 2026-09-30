package cephconf_test

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
)

var _ = Describe("DropPrivileges", func() {
	It("ignores setuser and setgroup when not root", func() {
		if os.Getuid() == 0 {
			Skip("as root this would drop the suite's own privileges")
		}
		uid, gid := os.Getuid(), os.Getgid()
		o := cephconf.NewOptions(cephconf.MapGetter{"setuser": "ceph", "setgroup": "ceph", "setuser_match_path": ""})
		Expect(cephconf.DropPrivileges(o)).To(Succeed())
		Expect(os.Getuid()).To(Equal(uid))
		Expect(os.Getgid()).To(Equal(gid))
	})
	It("does nothing when neither setuser nor setgroup is set", func() {
		uid := os.Getuid()
		Expect(cephconf.DropPrivileges(cephconf.NewOptions(cephconf.MapGetter{"setuser": "", "setgroup": ""}))).To(Succeed())
		Expect(os.Getuid()).To(Equal(uid))
	})
	It("passes a read failure through", func() {
		Expect(cephconf.DropPrivileges(cephconf.NewOptions(cephconf.MapGetter{}))).To(MatchError(cephconf.ErrUnknownOption))
	})
})

// fakeSystem stands in for the process: it reports uid as its own, records
// every id change DropPrivileges makes, and knows a few accounts.
type fakeSystem struct {
	uid                  int
	setgidErr, setuidErr error
	calls                []string
	users                map[string]*user.User
	groups               map[string]*user.Group
}

func installFakeSystem(uid int) *fakeSystem {
	f := &fakeSystem{
		uid: uid,
		users: map[string]*user.User{
			"ceph": {Username: "ceph", Uid: "167", Gid: "167"},
			"root": {Username: "root", Uid: "0", Gid: "0"},
		},
		groups: map[string]*user.Group{
			"ceph": {Name: "ceph", Gid: "167"},
			"disk": {Name: "disk", Gid: "6"},
		},
	}
	DeferCleanup(cephconf.SetSystem(cephconf.System{
		Getuid: func() int { return f.uid },
		Setgid: func(gid int) error {
			f.calls = append(f.calls, "setgid "+strconv.Itoa(gid))
			return f.setgidErr
		},
		Setuid: func(uid int) error {
			f.calls = append(f.calls, "setuid "+strconv.Itoa(uid))
			return f.setuidErr
		},
		LookupUser: func(name string) (*user.User, error) {
			if u, ok := f.users[name]; ok {
				return u, nil
			}
			return nil, user.UnknownUserError(name)
		},
		LookupGroup: func(name string) (*user.Group, error) {
			if g, ok := f.groups[name]; ok {
				return g, nil
			}
			return nil, user.UnknownGroupError(name)
		},
	}))
	return f
}

// dropOptions holds the three options DropPrivileges reads.
func dropOptions(setuser, setgroup, matchPath string) *cephconf.Options {
	return cephconf.NewOptions(cephconf.MapGetter{"setuser": setuser, "setgroup": setgroup, "setuser_match_path": matchPath})
}

var _ = Describe("DropPrivileges as root", func() {
	var sys *fakeSystem
	BeforeEach(func() { sys = installFakeSystem(0) })

	DescribeTable("drops to the resolved ids, setgid before setuid",
		func(setuser, setgroup string, want []string) {
			Expect(cephconf.DropPrivileges(dropOptions(setuser, setgroup, ""))).To(Succeed())
			Expect(sys.calls).To(Equal(want))
		},
		Entry("a user and a group, as Rook passes", "ceph", "ceph", []string{"setgid 167", "setuid 167"}),
		Entry("a user alone, taking its passwd group", "ceph", "", []string{"setgid 167", "setuid 167"}),
		Entry("a group that overrides the user's", "ceph", "disk", []string{"setgid 6", "setuid 167"}),
		Entry("a numeric user, leaving the group", "167", "", []string{"setuid 167"}),
		Entry("a group alone, staying root", "", "ceph", []string{"setgid 167"}),
	)
	It("changes nothing for root's ids, without reading setuser_match_path", func() {
		for _, setuser := range []string{"root", "0"} {
			o := cephconf.NewOptions(cephconf.MapGetter{"setuser": setuser, "setgroup": ""})
			Expect(cephconf.DropPrivileges(o)).To(Succeed(), setuser)
		}
		Expect(sys.calls).To(BeEmpty())
	})
	It("does nothing for a caller that is not root", func() {
		sys.uid = 1000
		Expect(cephconf.DropPrivileges(dropOptions("ceph", "ceph", ""))).To(Succeed())
		Expect(sys.calls).To(BeEmpty())
	})
	It("stops at a failed setgid, before setuid", func() {
		sys.setgidErr = syscall.EPERM
		err := cephconf.DropPrivileges(dropOptions("ceph", "ceph", ""))
		Expect(err).To(MatchError(syscall.EPERM))
		Expect(err).To(MatchError(ContainSubstring("setgid 167")))
		Expect(sys.calls).To(Equal([]string{"setgid 167"}))
	})
	It("returns a failed setuid", func() {
		sys.setuidErr = syscall.EPERM
		err := cephconf.DropPrivileges(dropOptions("ceph", "ceph", ""))
		Expect(err).To(MatchError(syscall.EPERM))
		Expect(err).To(MatchError(ContainSubstring("setuid 167")))
		Expect(sys.calls).To(Equal([]string{"setgid 167", "setuid 167"}))
	})
	It("fails on a user or group it cannot resolve, changing nothing", func() {
		Expect(cephconf.DropPrivileges(dropOptions("nobody-here", "", ""))).
			To(MatchError(user.UnknownUserError("nobody-here")))
		Expect(cephconf.DropPrivileges(dropOptions("ceph", "nogroup-here", ""))).
			To(MatchError(user.UnknownGroupError("nogroup-here")))
		Expect(sys.calls).To(BeEmpty())
	})
	Context("with setuser_match_path", func() {
		var (
			path  string
			owner syscall.Stat_t
		)
		BeforeEach(func() {
			path = filepath.Join(GinkgoT().TempDir(), "data")
			Expect(os.WriteFile(path, nil, 0o600)).To(Succeed())
			Expect(syscall.Stat(path, &owner)).To(Succeed())
			uid, gid := int(owner.Uid), int(owner.Gid)
			sys.users["owner"] = &user.User{Username: "owner", Uid: strconv.Itoa(uid), Gid: strconv.Itoa(gid)}
			sys.users["other"] = &user.User{Username: "other", Uid: strconv.Itoa(uid + 1), Gid: strconv.Itoa(gid + 1)}
		})
		It("drops when the path is owned by the ids it drops to", func() {
			// A zero id asks for no change, so on a runner with gid 0, as
			// OpenShift runs containers, only the uid drops.
			var want []string
			if owner.Gid != 0 {
				want = append(want, "setgid "+strconv.Itoa(int(owner.Gid)))
			}
			if owner.Uid != 0 {
				want = append(want, "setuid "+strconv.Itoa(int(owner.Uid)))
			}
			if want == nil {
				Skip("root owns the path, and its ids ask for no change")
			}
			Expect(cephconf.DropPrivileges(dropOptions("owner", "", path))).To(Succeed())
			Expect(sys.calls).To(Equal(want))
		})
		It("keeps root when another user owns the path", func() {
			Expect(cephconf.DropPrivileges(dropOptions("other", "", path))).To(Succeed())
			Expect(sys.calls).To(BeEmpty())
		})
		It("fails when the path cannot be stat'd, changing nothing", func() {
			missing := filepath.Join(filepath.Dir(path), "missing")
			Expect(cephconf.DropPrivileges(dropOptions("ceph", "ceph", missing))).
				To(MatchError(ContainSubstring("setuser_match_path")))
			Expect(sys.calls).To(BeEmpty())
		})
	})
})

var _ = Describe("resolving setuser", func() {
	It("takes 0 as root's uid", func() {
		uid, gid, err := cephconf.ResolveUser("0")
		Expect(err).NotTo(HaveOccurred())
		Expect(uid).To(Equal(0))
		Expect(gid).To(Equal(0))
	})
	It("takes a number as the uid and leaves the group unchanged", func() {
		uid, gid, err := cephconf.ResolveUser(strconv.Itoa(os.Getuid()))
		Expect(err).NotTo(HaveOccurred())
		Expect(uid).To(Equal(os.Getuid()))
		Expect(gid).To(BeZero(), "a numeric setuser does not take the passwd entry's group")
	})
	It("looks a name up, taking its uid and primary group", func() {
		u, err := user.Current()
		if err != nil {
			Skip("the current uid has no passwd entry: " + err.Error())
		}
		uid, gid, err := cephconf.ResolveUser(u.Username)
		Expect(err).NotTo(HaveOccurred())
		Expect(strconv.Itoa(uid)).To(Equal(u.Uid))
		Expect(strconv.Itoa(gid)).To(Equal(u.Gid))
	})
	It("fails on a name with no passwd entry", func() {
		_, _, err := cephconf.ResolveUser("no-such-user-xyz")
		Expect(err).To(MatchError(ContainSubstring("no-such-user-xyz")))
	})
	It("takes numbers up to 2147483647 as uids and looks larger ones up as names", func() {
		uid, _, err := cephconf.ResolveUser("2147483647")
		Expect(err).NotTo(HaveOccurred())
		Expect(uid).To(Equal(2147483647))
		_, _, err = cephconf.ResolveUser("2147483648")
		Expect(err).To(MatchError(user.UnknownUserError("2147483648")))
	})
})

var _ = Describe("resolving setgroup", func() {
	It("takes a number as the gid", func() {
		Expect(cephconf.ResolveGroup(strconv.Itoa(os.Getgid()))).To(Equal(os.Getgid()))
	})
	It("looks a name up", func() {
		g, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
		if err != nil {
			Skip("the current gid has no group entry: " + err.Error())
		}
		Expect(cephconf.ResolveGroup(g.Name)).To(Equal(os.Getgid()))
	})
	It("fails on a name with no group entry", func() {
		_, err := cephconf.ResolveGroup("no-such-group-xyz")
		Expect(err).To(MatchError(ContainSubstring("no-such-group-xyz")))
	})
	It("takes numbers up to 2147483647 as gids and looks larger ones up as names", func() {
		Expect(cephconf.ResolveGroup("2147483647")).To(Equal(2147483647))
		_, err := cephconf.ResolveGroup("2147483648")
		Expect(err).To(MatchError(user.UnknownGroupError("2147483648")))
	})
})

var _ = Describe("setuser_match_path", func() {
	var path string
	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), "data")
		Expect(os.WriteFile(path, nil, 0o600)).To(Succeed())
	})
	It("allows the drop when the path is owned by the requested ids", func() {
		Expect(cephconf.MatchPathAllows(path, os.Getuid(), os.Getgid())).To(BeTrue())
	})
	It("refuses it when another user owns the path", func() {
		Expect(cephconf.MatchPathAllows(path, os.Getuid()+1, os.Getgid())).To(BeFalse())
	})
	It("refuses it when another group owns the path", func() {
		Expect(cephconf.MatchPathAllows(path, os.Getuid(), os.Getgid()+1)).To(BeFalse())
	})
	It("compares only the ids a drop changes, zero meaning no change", func() {
		Expect(cephconf.MatchPathAllows(path, 0, os.Getgid())).To(BeTrue())
		Expect(cephconf.MatchPathAllows(path, os.Getuid(), 0)).To(BeTrue())
	})
	It("fails when the path cannot be stat'd", func() {
		_, err := cephconf.MatchPathAllows(filepath.Join(filepath.Dir(path), "missing"), 1, 1)
		Expect(err).To(MatchError(ContainSubstring("setuser_match_path")))
	})
})
