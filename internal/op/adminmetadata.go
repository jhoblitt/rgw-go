package op

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// The metadata API is RGWHandler_Metadata's ops (rgw_rest_metadata.cc and
// rgw_rest_metadata.h, the same at v19.2.6 and v20.2.4) over the metadata
// manager (rgw_metadata.cc:469-552 at v19.2.6, :264-347 at v20.2.4), with
// Env.Metadata serving each section's handler.

// errTopLevel is the top-level handler's -ENOTSUP for a get, put or remove
// that names no section (RGWMetadataTopHandler,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L328-L340 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_metadata.cc#L126-L138 at v20.2.4),
// which set_req_state_err finds no row for and answers UnknownError.
var errTopLevel = &Error{Code: "UnknownError", Status: 500, Errno: int(syscall.ENOTSUP)}

// metadataSections are the sections Env.Metadata serves, in std::set order,
// which the top-level listing lists as RGWMetadataTopHandler lists the
// registered handlers.
var metadataSections = []string{"bucket", "bucket.instance", "user"}

// SyncType is RGWMDLogSyncType as update-type names it
// (rgw_rest_metadata.cc:221-232): APPLY_ALWAYS, APPLY_UPDATES and
// APPLY_NEWER.
type SyncType uint8

// The sync types a put takes.
const (
	SyncAlways SyncType = iota
	SyncByVersion
	SyncByTimestamp
)

// ParseSyncType is string_to_sync_type: the three names compared exactly.
func ParseSyncType(s string) (SyncType, bool) {
	switch s {
	case "update-by-version":
		return SyncByVersion, true
	case "update-by-timestamp":
		return SyncByTimestamp, true
	case "always":
		return SyncAlways, true
	}
	return SyncAlways, false
}

// GetMetadata is RGWOp_Metadata_Get and RGWOp_Metadata_Get_Myself
// (rgw_rest_metadata.cc:54-75): the entry Key names in Section. Cap
// metadata=read (rgw_rest_metadata.h:38-39).
type GetMetadata struct {
	AdminOp
	Section, Key string
	Result       MetadataEntry
}

// Name is get_metadata, which radosgw names Get_Myself too.
func (o *GetMetadata) Name() string { return "get_metadata" }

// VerifyPermission is RGWOp_Metadata_Get::check_caps.
func (o *GetMetadata) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "metadata", meta.CapRead)
}

// Execute reads the entry.
func (o *GetMetadata) Execute(ctx context.Context, r *Request) error {
	if o.Section == "" {
		return errTopLevel
	}
	e, err := r.Env.Metadata.Get(ctx, o.Section, o.Key)
	if err != nil {
		return err
	}
	o.Result = e
	return nil
}

// metadataListMax is the page RGWOp_Metadata_List asks for when the request
// names no max-entries.
const metadataListMax = 1000

// ListMetadata is RGWOp_Metadata_List (rgw_rest_metadata.cc:77-174): the
// keys of Section from Marker. Cap metadata=read (rgw_rest_metadata.h:26-27).
type ListMetadata struct {
	AdminOp
	Section string
	// Marker is the marker argument, base64 as a truncated listing returns
	// it; one rgw::from_base64 cannot decode lists from the beginning.
	Marker string
	// MaxEntries counts only when Extended, the request naming max-entries.
	MaxEntries uint64
	Extended   bool
	Keys       []string
	Truncated  bool
	Count      uint64
	// NextMarker is the lister's marker when Truncated, not yet base64.
	NextMarker string
}

// Name is list_metadata.
func (o *ListMetadata) Name() string { return "list_metadata" }

// VerifyPermission is RGWOp_Metadata_List::check_caps.
func (o *ListMetadata) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "metadata", meta.CapRead)
}

// Execute lists while the lister reports more and the request has room,
// the room measured before each call: max-entries less what is listed, or
// a page of 1000 without it, so a listing without max-entries lists the
// whole section.
func (o *ListMetadata) Execute(ctx context.Context, r *Request) error {
	list := r.Env.Metadata.List
	if o.Section == "" {
		list = listSections
	}
	marker := ""
	if b, ok := fromBase64(o.Marker); ok {
		marker = string(b)
	}
	o.Keys = nil
	for {
		// A listing without max-entries pages through the whole section,
		// which a client that went away should not keep running.
		if err := ctx.Err(); err != nil {
			return err
		}
		left := uint64(metadataListMax)
		if o.Extended {
			left = o.MaxEntries - min(uint64(len(o.Keys)), o.MaxEntries)
		}
		keys, next, more, err := list(ctx, o.Section, marker, int(min(left, uint64(maxListInt)))) //nolint:gosec // bounded above
		if err != nil {
			return err
		}
		o.Keys = append(o.Keys, keys...)
		o.Truncated = more
		marker = next
		if !more || left == 0 {
			break
		}
	}
	o.Count = uint64(len(o.Keys))
	o.NextMarker = ""
	if o.Truncated {
		o.NextMarker = marker
	}
	return nil
}

// maxListInt bounds a page so it converts to int on any platform.
const maxListInt = 1<<31 - 1

// listSections is RGWMetadataTopHandler's listing
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L352-L388 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_metadata.cc#L150-L186 at v20.2.4):
// the sections from the first not below marker, and as the next marker the
// first section not returned.
func listSections(_ context.Context, _, marker string, maxEntries int) (keys []string, next string, more bool, err error) {
	i, _ := slices.BinarySearch(metadataSections, marker)
	end := min(i+max(maxEntries, 0), len(metadataSections))
	keys = slices.Clone(metadataSections[i:end])
	if end < len(metadataSections) {
		return keys, metadataSections[end], true, nil
	}
	return keys, "", false, nil
}

// PutMetadata is RGWOp_Metadata_Put::execute past its body read
// (rgw_rest_metadata.cc:234-274), RGWMetadataManager::put
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L501-L552 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_metadata.cc#L296-L347 at v20.2.4)
// and Squid's RGWMetadataHandlerPut_SObj::put_pre
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L255-L278 at v19.2.6)
// with check_versions
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_metadata.h#L196-L218 at v19.2.6).
// The entry written is the one Section and Key name: the body's "key" is
// decoded after the manager has found the handler and entry, so it changes
// neither. Cap metadata=write (rgw_rest_metadata.h:61-62).
//
// Tentacle's handlers take the sync type and ignore it, applying every put
// (docs/ceph-upstream-bugs.md, "Tentacle's metadata put ignores
// update-type"); Sync applies on both releases.
type PutMetadata struct {
	AdminOp
	Section, Key string
	Body         []byte
	Sync         SyncType
	// Applied is false for a put check_versions refused, STATUS_NO_APPLY.
	Applied bool
	// OnDisk is the version put_pre read, what RGWX_UPDATE_VERSION reports:
	// zero until check_versions has passed, as put_pre records it only then.
	OnDisk meta.ObjVersion
}

// Name is set_metadata.
func (o *PutMetadata) Name() string { return "set_metadata" }

// VerifyPermission is RGWOp_Metadata_Put::check_caps.
func (o *PutMetadata) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "metadata", meta.CapWrite)
}

// UpdateStatus is the RGWX_UPDATE_STATUS the put reports: "skipped" for
// STATUS_NO_APPLY, "applied" for STATUS_APPLIED, and "" for an applied
// bucket entry point, whose handler answers 0 rather than STATUS_APPLIED
// at both releases (RGWMetadataHandlerPut_Bucket::put_post,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2289-L2317 at v19.2.6;
// RGWBucketMetadataHandler::put,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L2355-L2401 at v20.2.4),
// so radosgw answers it 200 without the header.
func (o *PutMetadata) UpdateStatus() string {
	switch {
	case !o.Applied:
		return "skipped"
	case o.Section == "bucket":
		return ""
	}
	return "applied"
}

// Execute decodes the body, reads the stored entry, and writes the body's
// document under the version read when check_versions lets it.
func (o *PutMetadata) Execute(ctx context.Context, r *Request) error {
	o.Applied, o.OnDisk = false, meta.ObjVersion{}
	// find_handler runs before the body is parsed
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L511-L514 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_metadata.cc#L306-L309 at v20.2.4):
	// a section without a handler is ENOENT whatever the body holds.
	if o.Section != "" && !slices.Contains(metadataSections, o.Section) {
		return fmt.Errorf("metadata section %q: %w", o.Section, ErrNoSuchKey)
	}
	incoming, mtime, data, err := decodeMetadataPut(o.Body)
	if err != nil {
		return err
	}
	if o.Section == "" {
		return errTopLevel
	}
	cur, err := r.Env.Metadata.Get(ctx, o.Section, o.Key)
	switch {
	case errors.Is(err, ErrNoSuchKey):
		cur = MetadataEntry{}
	case err != nil:
		return err
	}
	if !checkVersions(cur.Version, cur.Mtime, incoming, mtime, o.Sync) {
		return nil
	}
	o.OnDisk = cur.Version
	err = r.Env.Metadata.Put(ctx, o.Section, o.Key,
		MetadataEntry{Key: o.Key, Data: data, Version: incoming, Mtime: mtime},
		PutMetadataOptions{IfVersion: new(cur.Version)})
	if err != nil {
		return err
	}
	o.Applied = true
	return nil
}

// checkVersions is check_versions for the sync types the API names.
func checkVersions(ondisk meta.ObjVersion, ondiskTime time.Time, incoming meta.ObjVersion, incomingTime time.Time, sync SyncType) bool {
	switch sync {
	case SyncByVersion:
		return ondisk.Tag == incoming.Tag && ondisk.Ver < incoming.Ver
	case SyncByTimestamp:
		return realTime(ondiskTime) < realTime(incomingTime)
	}
	return true
}

// realTime is t as ceph::real_time counts it, the zero time being the epoch.
func realTime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// decodeMetadataPut reads a put's body as RGWMetadataManager::put does: a
// JSON document whose "ver" is the version to write, "mtime" the mtime, and
// "data" the section's document, which it must hold. Each refusal is
// ErrInvalidArgument, the manager's -EINVAL. Absent members are zero, as
// JSONDecoder::decode_json resets them. The members are read as the
// section decoders read theirs ("The metadata sections write only what
// keeps a user or bucket whole", docs/exclusions.md): what decode_json
// takes but cannot mean is refused, a "ver" that is no object, a tag that
// is null, an object or an array, and an mtime in neither form a get
// writes.
func decodeMetadataPut(body []byte) (meta.ObjVersion, time.Time, json.RawMessage, error) {
	m, err := meta.JSONMembers(body)
	if err != nil {
		return meta.ObjVersion{}, time.Time{}, nil, fmt.Errorf("%w: the metadata document is not JSON", ErrInvalidArgument)
	}
	var ver meta.ObjVersion
	if v, ok := m["ver"]; ok {
		if ver, err = decodeObjVersion(v.Raw); err != nil {
			return meta.ObjVersion{}, time.Time{}, nil, err
		}
	}
	var mtime time.Time
	if v, ok := m["mtime"]; ok {
		if mtime, err = decodeDumpTime(v.Text); err != nil {
			return meta.ObjVersion{}, time.Time{}, nil, err
		}
	}
	data, ok := m["data"]
	if !ok {
		return meta.ObjVersion{}, time.Time{}, nil, fmt.Errorf("%w: the metadata document has no data", ErrInvalidArgument)
	}
	return ver, mtime, data.Raw, nil
}

// decodeObjVersion is decode_json_obj(obj_version&)
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L49-L53 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_metadata.cc#L45-L49 at v20.2.4):
// "tag" a string, "ver" an unsigned 64-bit number read as strtoull reads
// it.
func decodeObjVersion(raw json.RawMessage) (meta.ObjVersion, error) {
	bad := fmt.Errorf("%w: the metadata document's ver", ErrInvalidArgument)
	if jsonKind(raw) != '{' {
		return meta.ObjVersion{}, bad
	}
	m, err := meta.JSONMembers(raw)
	if err != nil {
		return meta.ObjVersion{}, bad
	}
	var v meta.ObjVersion
	if t, ok := m["tag"]; ok {
		switch jsonKind(t.Raw) {
		case 'n', '{', '[':
			return meta.ObjVersion{}, bad
		}
		v.Tag = t.Text
	}
	if n, ok := m["ver"]; ok {
		s := rgwtext.CString(n.Text)
		u, end, erange := rgwtext.Strtoull(s)
		if end == 0 || erange || strings.TrimLeft(s[end:], cSpaces) != "" {
			return meta.ObjVersion{}, bad
		}
		v.Ver = u
	}
	return v, nil
}

// decodeDumpTime is decode_json_obj(utime_t&) over a member's text, here
// only in the two forms utime_t::gmtime writes, "<sec>.<usec>" and the ISO
// date, as the section decoders read a time.
func decodeDumpTime(text string) (time.Time, error) {
	bad := fmt.Errorf("%w: the metadata document's mtime", ErrInvalidArgument)
	if !strings.Contains(text, "-") && !strings.Contains(text, ".") {
		return time.Time{}, bad
	}
	q, err := json.Marshal(text)
	if err != nil {
		return time.Time{}, bad
	}
	var t meta.Time
	if err := t.UnmarshalJSON(q); err != nil {
		return time.Time{}, bad
	}
	return t.Time, nil
}

// jsonKind is the first byte of a JSON value past white space.
func jsonKind(raw json.RawMessage) byte {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return c
	}
	return 0
}

// RemoveMetadata is RGWOp_Metadata_Delete (rgw_rest_metadata.cc:290-300)
// and RGWMetadataManager::remove
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L554-L574 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_metadata.cc#L349-L369 at v20.2.4).
// Cap metadata=write (rgw_rest_metadata.h:75-76).
type RemoveMetadata struct {
	AdminOp
	Section, Key string
}

// Name is remove_metadata.
func (o *RemoveMetadata) Name() string { return "remove_metadata" }

// VerifyPermission is RGWOp_Metadata_Delete::check_caps.
func (o *RemoveMetadata) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "metadata", meta.CapWrite)
}

// Execute removes the entry.
func (o *RemoveMetadata) Execute(ctx context.Context, r *Request) error {
	if o.Section == "" {
		return errTopLevel
	}
	return r.Env.Metadata.Remove(ctx, o.Section, o.Key)
}
