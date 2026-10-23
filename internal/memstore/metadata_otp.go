package memstore

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// otpEntry is one entry of the otp section: the devices in id order, each
// id once, as the class's header lists them, with the version and mtime the
// entry was written at. It holds seeds, so nothing formats it.
type otpEntry struct {
	devices []otp.OTPInfo
	version meta.ObjVersion
	mtime   time.Time
}

// getOTP is the RADOS driver's otp get.
func (s *Store) getOTP(key string) (op.MetadataEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ent, ok := s.otps[key]
	if !ok {
		return op.MetadataEntry{}, fmt.Errorf("otp %q: %w", key, op.ErrNoSuchKey)
	}
	doc := op.OTPDocument{Devices: slices.Clone(ent.devices)}
	data, err := json.Marshal(doc)
	if err != nil {
		return op.MetadataEntry{}, err
	}
	return op.MetadataEntry{Key: key, Data: data, Doc: doc, Version: ent.version, Mtime: ent.mtime}, nil
}

// putOTP is the RADOS driver's otp put over the entry key names, which it
// replaces whole, reading nothing first: the document op.DecodeOTPDocument
// takes, its devices kept by id with the last of a repeated id winning, as
// otp_set writes them; the version prepare_mfa_write writes; and e's mtime,
// or the stored one when e has none, as the OSD keeps an object's mtime for
// a write of mtime zero. Seeds are taken as written: the class's parse of
// them is the RADOS driver's alone.
func (s *Store) putOTP(key string, e op.MetadataEntry) error {
	doc, err := op.DecodeOTPDocument(e.Data)
	if err != nil {
		return err
	}
	byID := map[string]otp.OTPInfo{}
	for _, d := range doc.Devices {
		byID[d.ID] = d
	}
	devs := slices.SortedFunc(maps.Values(byID), func(a, b otp.OTPInfo) int { return cmp.Compare(a.ID, b.ID) })
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.otps[key]
	ent := &otpEntry{devices: devs, mtime: e.Mtime}
	switch {
	case e.Version.Tag == "":
		ent.version = meta.ObjVersion{Ver: 1, Tag: s.newTag()}
	case e.Version.Ver != 0:
		ent.version = e.Version
	case exists:
		ent.version = meta.ObjVersion{Ver: old.version.Ver + 1, Tag: old.version.Tag}
	default:
		ent.version = meta.ObjVersion{Ver: 2, Tag: s.newTag()}
	}
	if ent.mtime.IsZero() && exists {
		ent.mtime = old.mtime
	}
	s.otps[key] = ent
	return nil
}

// removeOTP is the RADOS driver's otp remove.
func (s *Store) removeOTP(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.otps[key]; !ok {
		return fmt.Errorf("otp %q: %w", key, op.ErrNoSuchKey)
	}
	delete(s.otps, key)
	return nil
}
