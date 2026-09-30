package cephconf_test

import (
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
)

var _ = Describe("ParseEarly", func() {
	It("takes ceph's early arguments and leaves the rest in order", func() {
		e, err := cephconf.ParseEarly([]string{
			"--foreground", "--id=rgw.a", "-c", "/etc/ceph/x.conf", "--cluster", "prod",
			"--rgw-frontends=beast port=8080", "--no-config-file", "--setuser=ceph",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Name).To(Equal("client.rgw.a"))
		Expect(e.ConfFile).To(Equal("/etc/ceph/x.conf"))
		Expect(e.Cluster).To(Equal("prod"))
		Expect(e.NoConfigFile).To(BeTrue())
		Expect(e.Rest).To(Equal([]string{"--foreground", "--rgw-frontends=beast port=8080", "--setuser=ceph"}))
	})
	It("takes only --id from Rook's argv", func() {
		// Rook v1.20.7's radosgw arguments (object/spec.go makeDaemonContainer,
		// controller.DaemonFlags, config.DefaultFlags), mon variables expanded.
		before := []string{
			"--fsid=b3f3b4a4-9d6e-4a36-8f30-1f6b0c8e2d11",
			"--keyring=/etc/ceph/keyring-store/keyring",
			"--default-log-to-stderr=true",
			"--default-err-to-stderr=true",
			"--default-mon-cluster-log-to-stderr=true",
			"--default-log-stderr-prefix=debug ",
			"--default-log-to-file=false",
			"--default-mon-cluster-log-to-file=false",
			"--mon-host=[v2:10.96.0.10:3300,v1:10.96.0.10:6789]",
			"--mon-initial-members=a",
		}
		after := []string{
			"--setuser=ceph",
			"--setgroup=ceph",
			"--foreground",
			"--rgw-frontends=beast port=8080",
			"--rgw-mime-types-file=/etc/ceph/rgw/mime.types",
			"--rgw-realm=ceph-objectstore",
			"--rgw-zonegroup=ceph-objectstore",
			"--rgw-zone=ceph-objectstore",
		}
		e, err := cephconf.ParseEarly(slices.Concat(before, []string{"--id=rgw.ceph.objectstore.a"}, after))
		Expect(err).NotTo(HaveOccurred())
		Expect(e).To(Equal(cephconf.EarlyArgs{
			Name: "client.rgw.ceph.objectstore.a",
			Rest: slices.Concat(before, after),
		}))
	})
	It("keeps -c and --no-config-file together for the connect to decide", func() {
		e, err := cephconf.ParseEarly([]string{"--no-config-file", "--conf=/etc/ceph/x.conf"})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.ConfFile).To(Equal("/etc/ceph/x.conf"))
		Expect(e.NoConfigFile).To(BeTrue())
	})
	It("leaves the cluster to the caller and defaults to client.admin", func() {
		e, err := cephconf.ParseEarly(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Cluster).To(BeEmpty(), "radosgw derives the name when --cluster is absent")
		Expect(e.Name).To(Equal("client.admin"))
	})
	DescribeTable("accepts every spelling of the name",
		func(args []string, want string) {
			e, err := cephconf.ParseEarly(args)
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Name).To(Equal(want))
		},
		Entry("--id=", []string{"--id=rgw.a"}, "client.rgw.a"),
		Entry("--id value", []string{"--id", "rgw.a"}, "client.rgw.a"),
		Entry("-i", []string{"-i", "rgw.a"}, "client.rgw.a"),
		Entry("--user", []string{"--user=rgw.a"}, "client.rgw.a"),
		Entry("-n type.id", []string{"-n", "client.rgw.a"}, "client.rgw.a"),
		Entry("--name=", []string{"--name=client.rgw.a"}, "client.rgw.a"),
		Entry("--conf= spelling", []string{"--conf=/x"}, "client.admin"),
		Entry("-n with auth, a type ceph accepts", []string{"-n", "auth.x"}, "auth.x"),
		Entry("--id after --name, which keeps the type", []string{"--name=mgr.a", "--id=b"}, "mgr.b"),
	)
	It("rejects a name without a type", func() {
		_, err := cephconf.ParseEarly([]string{"-n", "rgw.a"})
		Expect(err).To(MatchError(ContainSubstring("TYPE.ID")))
	})
	It("rejects a name without a dot, listing the types by name", func() {
		_, err := cephconf.ParseEarly([]string{"--name=client"})
		Expect(err).To(MatchError(ContainSubstring("TYPE.ID, valid types are: auth, mon, osd, mds, mgr, client")))
	})
	It("stops at -- and keeps it", func() {
		e, err := cephconf.ParseEarly([]string{"--id=a", "--", "--id=b"})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Name).To(Equal("client.a"))
		Expect(e.Rest).To(Equal([]string{"--", "--id=b"}))
	})
	It("reports -v and --version", func() {
		for _, f := range []string{"-v", "--version"} {
			e, err := cephconf.ParseEarly([]string{f})
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Version).To(BeTrue(), f)
		}
	})
	It("stops at -v, where ceph prints the version and exits", func() {
		e, err := cephconf.ParseEarly([]string{"-v", "--name=rgw.a"})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Version).To(BeTrue())
	})
	It("fails on an early option missing its value", func() {
		_, err := cephconf.ParseEarly([]string{"--id=a", "--cluster"})
		Expect(err).To(MatchError(ContainSubstring("--cluster requires an argument")))
	})
	It("matches dashes and underscores alike and drops --show_args", func() {
		e, err := cephconf.ParseEarly([]string{"--no_config_file", "--show-args", "--foreground"})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.NoConfigFile).To(BeTrue())
		Expect(e.Rest).To(Equal([]string{"--foreground"}))
	})
	It("leaves an option that only begins with an early option's name", func() {
		args := []string{"--cluster-network=10.0.0.0/24", "--cluster_addr=10.0.0.1", "--version=1"}
		e, err := cephconf.ParseEarly(args)
		Expect(err).NotTo(HaveOccurred())
		Expect(e).To(Equal(cephconf.EarlyArgs{Name: "client.admin", Rest: args}))
	})
})
