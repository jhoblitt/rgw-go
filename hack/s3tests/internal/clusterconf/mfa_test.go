package clusterconf_test

import (
	"errors"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/viper"

	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/clusterconf"
)

var _ = Describe("Configurer.EnsureMFA", func() {
	var w *world

	BeforeEach(func() { w = newWorld() })

	// mfaEvents are the radosgw-admin mfa and user commands the world ran.
	mfaEvents := func() []string {
		var out []string
		for _, e := range w.events {
			if strings.HasPrefix(e, "radosgw-admin mfa ") || strings.HasPrefix(e, "radosgw-admin user ") {
				out = append(out, e)
			}
		}
		return out
	}
	const (
		list   = "radosgw-admin mfa list --uid " + mfaUser
		remove = "radosgw-admin mfa remove --uid " + mfaUser + " --totp-serial " + clusterconf.MFASerial
		create = "radosgw-admin mfa create --uid " + mfaUser + " --totp-serial " + clusterconf.MFASerial +
			" --totp-seed " + clusterconf.MFASeed + " --totp-seed-type hex"
		info = "radosgw-admin user info --uid " + mfaUser
	)

	It("creates the device for a user without one, and checks the user's record and the seed", func(ctx SpecContext) {
		Expect(w.configurer().EnsureMFA(ctx, mfaUser)).To(Succeed(), w.trace())
		Expect(mfaEvents()).To(Equal([]string{list, create, info, list}))
		Expect(w.devices[mfaUser]).To(HaveKeyWithValue(clusterconf.MFASerial,
			&otpDevice{seed: clusterconf.MFASeed, seedType: "hex"}))
		Expect(w.mfaIDs[mfaUser]).To(Equal([]string{clusterconf.MFASerial}))
	})

	It("recreates the device on a second provisioning of the same user, so its replay state starts fresh", func(ctx SpecContext) {
		Expect(w.configurer().EnsureMFA(ctx, mfaUser)).To(Succeed(), w.trace())
		w.devices[mfaUser][clusterconf.MFASerial].lastSuccess = 59357048 // a check the test's run made
		w.events = nil
		Expect(w.configurer().EnsureMFA(ctx, mfaUser)).To(Succeed(), w.trace())
		Expect(mfaEvents()).To(Equal([]string{list, remove, create, info, list}))
		Expect(w.devices[mfaUser][clusterconf.MFASerial].lastSuccess).To(BeZero(),
			"otp_set would keep the last success; only the removal clears it")
	})

	It("replaces a device left in the otp object with another seed and missing from the user's record", func(ctx SpecContext) {
		w.devices[mfaUser] = map[string]*otpDevice{clusterconf.MFASerial: {seed: "deadbeef", seedType: "hex", lastSuccess: 7}}
		Expect(w.configurer().EnsureMFA(ctx, mfaUser)).To(Succeed(), w.trace())
		Expect(mfaEvents()).To(Equal([]string{list, remove, create, info, list}))
		Expect(w.devices[mfaUser]).To(HaveKeyWithValue(clusterconf.MFASerial,
			&otpDevice{seed: clusterconf.MFASeed, seedType: "hex"}))
		Expect(w.mfaIDs[mfaUser]).To(Equal([]string{clusterconf.MFASerial}))
	})

	It("fails, creating nothing, when the removal fails", func(ctx SpecContext) {
		w.devices[mfaUser] = map[string]*otpDevice{clusterconf.MFASerial: {seed: clusterconf.MFASeed, seedType: "hex"}}
		w.mfaFails["remove"] = errors.New("exit status 13")
		err := w.configurer().EnsureMFA(ctx, mfaUser)
		Expect(err).To(MatchError(And(ContainSubstring("radosgw-admin mfa remove"), ContainSubstring("exit status 13"))))
		Expect(mfaEvents()).To(Equal([]string{list, remove}))
	})

	It("fails when mfa create leaves the serial out of the user's mfa_ids", func(ctx SpecContext) {
		w.createDropsID = true
		Expect(w.configurer().EnsureMFA(ctx, mfaUser)).To(MatchError(ContainSubstring(
			"the user s3tests-main's record lists the MFA devices [], not s3tests-totp, after mfa create")))
	})

	It("refuses a device whose stored seed is not the conf's, naming mfa create, as a rerun removes and creates it again", func(ctx SpecContext) {
		w.createSeed = "deadbeef"
		err := w.configurer().EnsureMFA(ctx, mfaUser)
		Expect(err).To(MatchError(
			"mfa create stored a hex seed other than the conf's in the user s3tests-main's MFA device s3tests-totp"))
		Expect(err.Error()).NotTo(ContainSubstring("deadbeef"), "the message names no seed")
	})

	It("prints the conf's lines for the device once it has given it", func(ctx SpecContext) {
		v := viper.New()
		cmd := clusterconf.NewMFACmd(v, clusterconf.Deps{Exec: w.exec, Proc: w.proc, Now: func() time.Time { return w.clock }, Sleep: w.sleep})
		Expect(v.BindPFlags(cmd.Flags())).To(Succeed())
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(GinkgoWriter)
		cmd.SetArgs([]string{"--release", "squid", "--uid", mfaUser})
		Expect(cmd.ExecuteContext(ctx)).To(Succeed(), w.trace())
		Expect(out.String()).To(Equal("mfa_serial = s3tests-totp\nmfa_seed = 3132333435363738393031323334353637383930\n"))
		Expect(mfaEvents()).To(Equal([]string{list, create, info, list}))
	})
})
