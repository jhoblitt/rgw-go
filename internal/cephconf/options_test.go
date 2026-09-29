package cephconf_test

import (
	"errors"
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// getterFunc adapts a function to cephconf.Getter.
type getterFunc func(name string) (string, error)

func (f getterFunc) ConfigGet(name string) (string, error) { return f(name) }

var _ = Describe("Options", func() {
	var o *cephconf.Options
	BeforeEach(func() {
		o = cephconf.NewOptions(cephconf.MapGetter{
			"rgw_max_chunk_size":                   "4194304",
			"rgw_enable_apis":                      "s3, s3website, swift",
			"rgw_gc_processor_period":              "3600",
			"rgw_cache_enabled":                    "true",
			"rgw_relaxed_s3_bucket_names":          "false",
			"rgw_dns_name":                         "",
			"ms_shutdown_timeout":                  "5000",
			"rgw_bucket_default_quota_max_objects": "-1",
			"objecter_inflight_op_bytes":           "18446744073709551615",
			"rgw_zone":                             "ceph-objectstore",
			"rgw_frontends":                        "four",
		})
	})
	It("returns a string option's text as librados renders it", func() {
		Expect(o.String("rgw_zone")).To(Equal("ceph-objectstore"))
		Expect(o.String("rgw_dns_name")).To(BeEmpty())
	})
	It("parses a size option's bare byte count", func() {
		Expect(o.Size("rgw_max_chunk_size")).To(BeEquivalentTo(4194304))
	})
	It("parses a secs option or an int counting seconds", func() {
		Expect(o.Seconds("rgw_gc_processor_period")).To(Equal(time.Hour))
	})
	It("parses a millisecs option", func() {
		Expect(o.Millis("ms_shutdown_timeout")).To(Equal(5 * time.Second))
	})
	It("parses signed and unsigned counts over their full range", func() {
		Expect(o.Int64("rgw_bucket_default_quota_max_objects")).To(BeEquivalentTo(-1))
		Expect(o.Uint64("objecter_inflight_op_bytes")).To(BeEquivalentTo(uint64(math.MaxUint64)))
	})
	It("parses the true and false librados renders a bool as", func() {
		Expect(o.Bool("rgw_cache_enabled")).To(BeTrue())
		Expect(o.Bool("rgw_relaxed_s3_bucket_names")).To(BeFalse())
	})
	It("splits a list option and drops the empty items", func() {
		Expect(o.List("rgw_enable_apis")).To(Equal([]string{"s3", "s3website", "swift"}))
		Expect(o.List("rgw_dns_name")).To(BeEmpty())
	})
	It("splits on every delimiter ceph::split takes, as radosgw splits rgw_enable_apis", func() {
		l := cephconf.NewOptions(cephconf.MapGetter{"rgw_enable_apis": "s3;admin=sts\tiam\n,,notifications"})
		Expect(l.List("rgw_enable_apis")).To(Equal([]string{"s3", "admin", "sts", "iam", "notifications"}))
	})
	DescribeTable("fails naming the option on a value its type does not render",
		func(parse func(*cephconf.Options) error, name string) {
			Expect(parse(o)).To(MatchError(ContainSubstring("option " + name + ":")))
		},
		Entry("a non-numeric size", func(o *cephconf.Options) error { _, err := o.Size("rgw_frontends"); return err }, "rgw_frontends"),
		Entry("a negative uint", func(o *cephconf.Options) error {
			_, err := o.Uint64("rgw_bucket_default_quota_max_objects")
			return err
		}, "rgw_bucket_default_quota_max_objects"),
		Entry("an int past int64", func(o *cephconf.Options) error { _, err := o.Int64("objecter_inflight_op_bytes"); return err }, "objecter_inflight_op_bytes"),
		Entry("a bool spelled other than librados spells it", func(o *cephconf.Options) error { _, err := o.Bool("rgw_max_chunk_size"); return err }, "rgw_max_chunk_size"),
		Entry("seconds past time.Duration's range", func(o *cephconf.Options) error {
			_, err := cephconf.NewOptions(cephconf.MapGetter{"big": "9223372036854775807"}).Seconds("big")
			return err
		}, "big"),
		Entry("an empty millisecs value", func(o *cephconf.Options) error { _, err := o.Millis("rgw_dns_name"); return err }, "rgw_dns_name"),
	)
	DescribeTable("reports an option librados does not know as ErrUnknownOption",
		func(parse func(*cephconf.Options) error) {
			Expect(parse(o)).To(MatchError(cephconf.ErrUnknownOption))
		},
		Entry("String", func(o *cephconf.Options) error { _, err := o.String("rgw_nope"); return err }),
		Entry("Int64", func(o *cephconf.Options) error { _, err := o.Int64("rgw_nope"); return err }),
		Entry("Uint64", func(o *cephconf.Options) error { _, err := o.Uint64("rgw_nope"); return err }),
		Entry("Bool", func(o *cephconf.Options) error { _, err := o.Bool("rgw_nope"); return err }),
		Entry("Size", func(o *cephconf.Options) error { _, err := o.Size("rgw_nope"); return err }),
		Entry("Seconds", func(o *cephconf.Options) error { _, err := o.Seconds("rgw_nope"); return err }),
		Entry("Millis", func(o *cephconf.Options) error { _, err := o.Millis("rgw_nope"); return err }),
		Entry("List", func(o *cephconf.Options) error { _, err := o.List("rgw_nope"); return err }),
	)
	It("reports librados's ENOENT for an unknown name as ErrUnknownOption, keeping the cause", func() {
		enoent := &radosclient.Error{Errno: -2, Op: "config get rgw_nope"}
		u := cephconf.NewOptions(getterFunc(func(string) (string, error) { return "", enoent }))
		_, err := u.String("rgw_nope")
		Expect(err).To(MatchError(cephconf.ErrUnknownOption))
		Expect(err).To(MatchError(enoent))
	})
	It("passes any other getter failure through", func() {
		closed := errors.New("rados: closed")
		u := cephconf.NewOptions(getterFunc(func(string) (string, error) { return "", closed }))
		_, err := u.Bool("rgw_cache_enabled")
		Expect(err).To(MatchError(closed))
		Expect(err).NotTo(MatchError(cephconf.ErrUnknownOption))
	})
})

var _ = Describe("MapGetter", func() {
	It("returns the mapped value, or ErrUnknownOption naming a missing key", func() {
		m := cephconf.MapGetter{"a": "1"}
		Expect(m.ConfigGet("a")).To(Equal("1"))
		_, err := m.ConfigGet("b")
		Expect(err).To(MatchError(cephconf.ErrUnknownOption))
		Expect(err).To(MatchError(ContainSubstring(": b")))
	})
})
