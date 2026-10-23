package clusterconf

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// MFASerial and MFASeed are the TOTP device the s3-tests conf names for its
// main user: its serial, and RFC 6238's test seed in hex, fixed as the
// users' keys are, so that every run writes the same conf.
const (
	MFASerial = "s3tests-totp"
	MFASeed   = "3132333435363738393031323334353637383930"
)

// MFAConf is the device as the conf's [s3 main] names it, a line each.
func MFAConf() string {
	return "mfa_serial = " + MFASerial + "\nmfa_seed = " + MFASeed
}

// mfaDevice is a device as radosgw-admin mfa list prints it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.cc#L30-L50 at v19.2.7 and v20.2.4).
type mfaDevice struct {
	ID       string `json:"id"`
	Seed     string `json:"seed"`
	SeedType string `json:"seed_type"`
}

// mfaList is radosgw-admin mfa list's output, which lists no device for a
// user without an otp object
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L10921-L10937 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/radosgw-admin/radosgw-admin.cc#L11513-L11529 at v20.2.4).
type mfaList struct {
	Entries []mfaDevice `json:"entries"`
}

// device is the entry of l whose id is serial.
func (l mfaList) device(serial string) (mfaDevice, bool) {
	i := slices.IndexFunc(l.Entries, func(d mfaDevice) bool { return d.ID == serial })
	if i < 0 {
		return mfaDevice{}, false
	}
	return l.Entries[i], true
}

// userMFA is what EnsureMFA reads of radosgw-admin user info.
type userMFA struct {
	MFAIDs []string `json:"mfa_ids"`
}

// EnsureMFA gives the user uid the device MFASerial afresh, with MFASeed at
// radosgw-admin's default step and window. A device of that serial the user
// has is removed first: otp_set keeps an instance's checks and last success
// when it replaces its config
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L301-L348 at v19.2.7 and v20.2.4),
// so only a removal keeps an earlier run's replay state, through either
// gateway, from refusing this run's codes. mfa create adds the serial to the
// user's mfa_ids, which verify_mfa checks before the device. EnsureMFA then
// checks that the user's record lists it and that the device holds MFASeed:
// a user created again after a removal starts with no mfa_ids beside the
// devices its predecessor left (docs/ceph-upstream-bugs.md, "radosgw's user
// removal leaves the user's MFA devices and their seeds in the otp pool").
func (c *Configurer) EnsureMFA(ctx context.Context, uid string) error {
	rgw, err := c.radosgw(ctx)
	if err != nil {
		return err
	}
	var before mfaList
	if err := c.adminJSON(ctx, rgw, &before, "mfa", "list", "--uid", uid); err != nil {
		return err
	}
	if _, ok := before.device(MFASerial); ok {
		slog.InfoContext(ctx, "removing the MFA device an earlier run gave the user",
			slog.String("uid", uid), slog.String("serial", MFASerial))
		if err := c.admin(ctx, rgw, "mfa", "remove", "--uid", uid, "--totp-serial", MFASerial); err != nil {
			return err
		}
	}
	if err := c.admin(ctx, rgw, "mfa", "create", "--uid", uid, "--totp-serial", MFASerial,
		"--totp-seed", MFASeed, "--totp-seed-type", "hex"); err != nil {
		return err
	}
	var user userMFA
	if err := c.adminJSON(ctx, rgw, &user, "user", "info", "--uid", uid); err != nil {
		return err
	}
	if !slices.Contains(user.MFAIDs, MFASerial) {
		return fmt.Errorf("the user %s's record lists the MFA devices %v, not %s, after mfa create", uid, user.MFAIDs, MFASerial)
	}
	var after mfaList
	if err := c.adminJSON(ctx, rgw, &after, "mfa", "list", "--uid", uid); err != nil {
		return err
	}
	d, ok := after.device(MFASerial)
	if !ok {
		return fmt.Errorf("the user %s has no MFA device %s after mfa create", uid, MFASerial)
	}
	if d.SeedType != "hex" || !strings.EqualFold(d.Seed, MFASeed) {
		return fmt.Errorf("mfa create stored a %s seed other than the conf's in the user %s's MFA device %s",
			d.SeedType, uid, MFASerial)
	}
	return nil
}

// admin runs radosgw-admin in the toolbox on the radosgw's site.
func (c *Configurer) admin(ctx context.Context, rgw radosgw, args ...string) error {
	if _, err := c.Cluster.Toolbox(ctx, slices.Concat([]string{"radosgw-admin"}, args, rgw.siteArgs())...); err != nil {
		return fmt.Errorf("radosgw-admin %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
