package op

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// The admin bucket ops are driver/rados/rgw_rest_bucket.cc's RGWOp_* bodies
// over driver/rados/rgw_bucket.cc's RGWBucketAdminOp. rgw_rest_bucket.cc's
// line numbers are the same at v19.2.6 and v20.2.4; a bare rgw_bucket.cc
// line is v19.2.6's. radosgw's REST ops pass no error-message sink to
// RGWBucketAdminOp, so its refusals reach the client without the text
// radosgw-admin prints, and these carry none either.

// BucketStatsData is what bucket_stats (rgw_bucket.cc:1351-1436; v20.2.4
// :1514-1602) renders for one bucket.
type BucketStatsData struct {
	Rec *BucketRecord
	// HasIndex is a bucket of the local zonegroup with a Normal index, the
	// only one bucket_stats reads index stats for.
	HasIndex bool
	Index    BucketIndexStats
	// Tags is the bucket's tag set when its tags attr is present and
	// decodes; bucket_stats skips one that does not.
	Tags *tags.Set
}

// bucketStats reads what bucket_stats renders for rec.
func bucketStats(ctx context.Context, env *Env, rec *BucketRecord) (BucketStatsData, error) {
	d := BucketStatsData{Rec: rec}
	d.HasIndex = rec.Info.Zonegroup == env.Zone.ZoneGroup().ID && rec.Info.Layout.Current.Layout.Type == meta.IndexNormal
	if d.HasIndex {
		st, err := env.BucketAdmin.IndexStats(ctx, rec)
		if err != nil {
			return BucketStatsData{}, err
		}
		d.Index = st
	}
	if raw, ok := rec.Attrs[tags.Attr]; ok {
		dec := denc.NewDecoder(raw)
		if set := tags.Decode(dec); dec.Err() == nil {
			d.Tags = &set
		}
	}
	return d, nil
}

// splitBucket is the "tenant/name" split RGWBucket::init and the link's new
// name make: at the first '/', else tenant and the whole name.
func splitBucket(s, tenant string) (t, name string) {
	t, name, ok := strings.Cut(s, "/")
	if !ok {
		return tenant, s
	}
	return t, name
}

// loadAdminBucket is RGWBucket::init (rgw_bucket.cc:169-216; v20.2.4
// :170-217) for a request that names no bucket id: bucket and uid both
// empty is EINVAL; the bucket is the "tenant/name" bucket names, else the
// uid's tenant; then, when the uid names a user, that user. A bucket or user that does not exist is
// radosgw's -ENOENT, NoSuchKey; the callers that answer otherwise map it.
func loadAdminBucket(ctx context.Context, env *Env, uid meta.UserID, bucket string) (*BucketRecord, error) {
	rec, err := loadInitBucket(ctx, env, uid, bucket, "")
	if err != nil {
		return nil, err
	}
	if _, err := loadInitUser(ctx, env, uid); err != nil {
		return nil, err
	}
	return rec, nil
}

// loadInitBucket is RGWBucket::init's bucket load, by its instance when
// bucketID is given, as load_bucket loads an rgw_bucket with an id
// (driver/rados/rgw_sal_rados.cc:616-629; v20.2.4 :634-647).
func loadInitBucket(ctx context.Context, env *Env, uid meta.UserID, bucket, bucketID string) (*BucketRecord, error) {
	if bucket == "" && uid.ID == "" {
		return nil, ErrInvalidArgument
	}
	tenant, name := splitBucket(bucket, uid.Tenant)
	var rec *BucketRecord
	var err error
	if bucketID != "" {
		rec, err = env.Buckets.GetBucketInstance(ctx, meta.BucketID{Tenant: tenant, Name: name, ID: bucketID})
	} else {
		rec, err = env.Buckets.GetBucket(ctx, tenant, name)
	}
	if errors.Is(err, ErrNoSuchBucket) {
		return nil, ErrNoSuchKey
	}
	return rec, err
}

// loadInitUser is RGWBucket::init's user load, nil when the uid names none.
func loadInitUser(ctx context.Context, env *Env, uid meta.UserID) (*UserRecord, error) {
	if uid.ID == "" {
		return nil, nil
	}
	u, err := env.Users.GetUser(ctx, uid)
	switch {
	case errors.Is(err, ErrNoSuchUser):
		return nil, ErrNoSuchKey
	case err != nil:
		return nil, storeErr(err)
	}
	return u, nil
}

// RenameIntentAttr is the bucket instance xattr rgw-go writes while a
// bucket link renames the bucket, from the first write of the bucket's
// owner to the last step, on the instance under the old key and the one
// under the new key. radosgw neither writes nor reads it. While it is live,
// rgw-go refuses every link, unlink and chown of the bucket but a retry of
// that rename, which completes it; it is live while the new name has no
// entry point or one naming the bucket's id (docs/exclusions.md, "Bucket
// link and unlink write differently from radosgw").
const RenameIntentAttr = meta.AttrPrefix + "rgw-go-rename"

// RenameIntent is the rename RenameIntentAttr records: the old and new
// keys, tenant and name, and the owner the rename gives the bucket.
type RenameIntent struct {
	SrcTenant string `json:"src_tenant"`
	SrcName   string `json:"src_name"`
	DstTenant string `json:"dst_tenant"`
	DstName   string `json:"dst_name"`
	Owner     string `json:"owner"`
	// From are the owners the rename takes the bucket from, whose list
	// entries a retry still has to remove after the first try gave the
	// instance to Owner.
	From []string `json:"from,omitempty"`
}

// RemovingAttr is the bucket instance xattr rgw-go's RADOS driver writes,
// under the version it read, when it starts removing a bucket, with a
// value new to each removal: a rename that read the instance before fails
// its guarded write of the record, and one that reads it after refuses the
// bucket, so a removal and a rename of one bucket never both change it.
// radosgw neither writes nor reads it.
const RemovingAttr = meta.AttrPrefix + "rgw-go-removing"

// ErrRenamePending is ConcurrentModification for a bucket an unfinished
// rename marks. An S3 DeleteBucket answers it as the 409 it is, where it
// takes a plain ConcurrentModification from the store as a lost race.
var ErrRenamePending = fmt.Errorf("an unfinished bucket rename holds the bucket: %w", ErrConcurrentModification)

// ErrRemovalRaced is ConcurrentModification for a bucket removal that lost
// its entry point to another write after it claimed the bucket: the bucket
// stays, and an S3 DeleteBucket answers it as the 409 it is. radosgw
// answers that race 204 having removed nothing: its entry point removal
// returns ECANCELED before the instance, the index or the owner's list
// entry goes (docs/ceph-upstream-bugs.md, "radosgw's bucket delete answers
// success when its instance removal loses a race").
var ErrRemovalRaced = fmt.Errorf("the bucket changed during its removal: %w", ErrConcurrentModification)

// refuseUnfinishedRename is ErrRenamePending for a bucket a live rename
// record marks, by either of its names: removing it would remove the index
// the rename's other instance shares. A record is live while its new name
// loads nothing or loads the bucket's own id.
func refuseUnfinishedRename(ctx context.Context, env *Env, rec *BucketRecord) error {
	i, ok := DecodeRenameIntent(rec.Attrs)
	if !ok {
		return nil
	}
	dst, err := env.Buckets.GetBucket(ctx, i.DstTenant, i.DstName)
	switch {
	case errors.Is(err, ErrNoSuchBucket):
		return ErrRenamePending
	case err != nil:
		return err
	case dst.Info.Bucket.ID == rec.Info.Bucket.ID:
		return ErrRenamePending
	}
	return nil
}

// Encode is the attr's value.
func (i RenameIntent) Encode() []byte {
	b, err := json.Marshal(i)
	if err != nil {
		panic(err) // a struct of strings always marshals
	}
	return b
}

// DecodeRenameIntent reads the intent attrs hold, false when they hold
// none or one that does not decode.
func DecodeRenameIntent(attrs map[string][]byte) (RenameIntent, bool) {
	raw, ok := attrs[RenameIntentAttr]
	if !ok {
		return RenameIntent{}, false
	}
	var i RenameIntent
	if err := json.Unmarshal(raw, &i); err != nil {
		return RenameIntent{}, false
	}
	return i, true
}

// bucketsCap is the check_caps of every admin bucket op: buckets, read or
// write.
func bucketsCap(r *Request, perm uint32) error { return CheckCaps(r, "buckets", perm) }

// BucketInfo is RGWOp_Bucket_Info and RGWBucketAdminOp::info
// (rgw_rest_bucket.cc:19-62; rgw_bucket.cc:1631-1724, list_owner_bucket_info
// :1546-1629; v20.2.4 :1797-1890 and :1712-1795). Cap buckets=read. Exactly
// one of Single, Entries and Names is set: a named bucket's stats; the
// buckets listed, with stats; or their names.
type BucketInfo struct {
	AdminOp
	// Bucket may be "tenant/name".
	Bucket string
	UID    meta.UserID
	// UIDGiven is the uid naming a user, rgw_user::empty false: the
	// listing is then that user's buckets, or its account's.
	UIDGiven   bool
	Stats      bool
	MaxEntries uint32
	Marker     string

	Single  *BucketStatsData
	Entries []BucketStatsData
	Names   []string
	// Paged is a user's listing with MaxEntries above 0, rendered with
	// Truncated, Count and NextMarker.
	Paged      bool
	Truncated  bool
	Count      uint64
	NextMarker string
}

// NewBucketInfo returns a BucketInfo.
func NewBucketInfo() *BucketInfo { return &BucketInfo{} }

// Name is get_bucket_info.
func (o *BucketInfo) Name() string { return "get_bucket_info" }

// VerifyPermission is RGWOp_Bucket_Info::check_caps.
func (o *BucketInfo) VerifyPermission(_ context.Context, r *Request) error {
	return bucketsCap(r, meta.CapRead)
}

// bucketMetadataPage is the page RGWBucketAdminOp::info lists the bucket
// metadata section in.
const bucketMetadataPage = 1000

// Execute is RGWBucketAdminOp::info. A named bucket missing is
// NoSuchBucket (rgw_bucket.cc:1642-1644). Its stats are read from the
// bucket the name loaded, "tenant/name" split. radosgw's bucket_stats looks
// the unsplit name up again under the uid's tenant, which finds the same
// bucket unless the uid names a tenant, and then answers with nothing
// (docs/exclusions.md, "The admin bucket info reports a bucket named
// tenant/name whatever the uid's tenant"). A listing with stats
// skips a bucket it cannot read, as radosgw ignores bucket_stats' return
// there.
func (o *BucketInfo) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	o.Single, o.Entries, o.Names = nil, nil, nil
	o.Paged, o.Truncated, o.Count, o.NextMarker = false, false, 0, ""
	switch {
	case o.Bucket != "":
		rec, err := loadAdminBucket(ctx, env, o.UID, o.Bucket)
		if errors.Is(err, ErrNoSuchKey) {
			return ErrNoSuchBucket
		}
		if err != nil {
			return err
		}
		d, err := bucketStats(ctx, env, rec)
		if err != nil {
			return err
		}
		o.Single = &d
		return nil
	case o.UIDGiven:
		return o.listOwner(ctx, env)
	}
	return o.listAll(ctx, env)
}

// add records one listed bucket: its stats when Stats, skipping a bucket
// that cannot be read, else its name.
func (o *BucketInfo) add(ctx context.Context, env *Env, b meta.BucketID, key string) {
	if !o.Stats {
		o.Names = append(o.Names, key)
		return
	}
	rec, err := env.Buckets.GetBucket(ctx, b.Tenant, b.Name)
	var d BucketStatsData
	if err == nil {
		d, err = bucketStats(ctx, env, rec)
	}
	if err != nil {
		if !errors.Is(err, ErrNoSuchBucket) {
			slog.WarnContext(ctx, "could not read a listed bucket's stats, leaving it out", slog.String("bucket", key),
				slog.String("code", AsError(err).Code))
		}
		return
	}
	o.Entries = append(o.Entries, d)
}

// listOwner is list_owner_bucket_info for the uid's user, or for its
// account when it has one: pages of rgw_list_buckets_max_chunk, none
// larger than what MaxEntries leaves, until a page is the last or
// MaxEntries are listed. A uid naming no user is load_user's -ENOENT,
// NoSuchKey, which radosgw sends as a 200 with no document
// (docs/exclusions.md, "Admin responses carry a Content-Type on every
// body").
func (o *BucketInfo) listOwner(ctx context.Context, env *Env) error {
	u, err := env.Users.GetUser(ctx, o.UID)
	switch {
	case errors.Is(err, ErrNoSuchUser):
		return ErrNoSuchKey
	case err != nil:
		return storeErr(err)
	}
	owner := meta.UserOwner(u.Info.UserID)
	if u.Info.AccountID != "" {
		owner = meta.AccountOwner(u.Info.AccountID)
	}
	o.Paged = o.MaxEntries > 0
	chunk := listChunk(env)
	items := chunk
	if o.Paged && int(o.MaxEntries) < chunk { //nolint:gosec // MaxEntries is a uint32
		items = int(o.MaxEntries)
	}
	if o.Stats {
		o.Entries = []BucketStatsData{}
	} else {
		o.Names = []string{}
	}
	marker := o.Marker
	for {
		if o.Paged && int(uint64(o.MaxEntries)-o.Count) < items { //nolint:gosec // at most MaxEntries
			items = int(uint64(o.MaxEntries) - o.Count) //nolint:gosec // at most MaxEntries
		}
		ents, next, more, err := env.Users.ListUserBuckets(ctx, owner, marker, items)
		if err != nil {
			return err
		}
		done := false
		for i := range ents {
			o.add(ctx, env, meta.BucketID{Tenant: o.UID.Tenant, Name: ents[i].Bucket.Name}, ents[i].Bucket.Name)
			o.Count++
			if o.Paged && o.Count >= uint64(o.MaxEntries) {
				done = true
				break
			}
		}
		o.Truncated, marker = more && next != "", next
		if !o.Truncated || done {
			break
		}
	}
	if o.Truncated {
		o.NextMarker = marker
	}
	return nil
}

// listAll is info's listing of the bucket metadata section from its start,
// a thousand keys a page: every bucket of the zone, a tenanted one keyed
// "tenant/name". With Stats each key is split at its '/', which loads what
// radosgw's lookup of the whole key under the empty tenant loads, as both
// name the entry point "tenant/name" (rgw_bucket::get_key,
// rgw_basic_types.cc:62-79 at v19.2.6 and v20.2.4). radosgw's loop stops at a listing
// failure and answers what it listed with a 200; rgw-go answers the
// failure.
func (o *BucketInfo) listAll(ctx context.Context, env *Env) error {
	if o.Stats {
		o.Entries = []BucketStatsData{}
	} else {
		o.Names = []string{}
	}
	marker := ""
	for {
		keys, next, more, err := env.Metadata.List(ctx, "bucket", marker, bucketMetadataPage)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, k := range keys {
			tenant, name := splitBucket(k, "")
			o.add(ctx, env, meta.BucketID{Tenant: tenant, Name: name}, k)
		}
		if !more || next == marker {
			return nil
		}
		marker = next
	}
}

// bucketOwner is the owner RGWBucketAdminOp::link and ::unlink act for:
// the account when account-id is given, else the uid's user, else EINVAL
// (rgw_bucket.cc:1001-1009 and :1028-1036).
func bucketOwner(uid meta.UserID, accountID string) (meta.Owner, error) {
	switch {
	case accountID != "":
		return meta.AccountOwner(accountID), nil
	case uid.ID != "":
		return meta.UserOwner(uid), nil
	}
	return meta.Owner{}, ErrInvalidArgument
}

// LinkBucket is RGWOp_Bucket_Link and RGWBucketAdminOp::link
// (rgw_rest_bucket.cc:128-170; rgw_bucket.cc:1026-1187, v20.2.4
// :1177-1338). Cap buckets=write.
type LinkBucket struct {
	AdminOp
	UID       meta.UserID
	AccountID string
	// Bucket and NewBucketName may be "tenant/name".
	Bucket        string
	BucketID      string
	NewBucketName string
}

// NewLinkBucket returns a LinkBucket.
func NewLinkBucket() *LinkBucket { return &LinkBucket{} }

// Name is link_bucket.
func (o *LinkBucket) Name() string { return "link_bucket" }

// VerifyPermission is RGWOp_Bucket_Link::check_caps.
func (o *LinkBucket) VerifyPermission(_ context.Context, r *Request) error {
	return bucketsCap(r, meta.CapWrite)
}

// Execute is RGWBucketAdminOp::link: the owner, RGWBucket::init, the
// display name, the account's name or the user's, refusing an account's
// user, then the bucket's new key, renamed when it differs from the
// loaded one, and the driver's change of owner. The key's tenant is the
// uid's, so linking a bucket to a user of another tenant moves it there.
// For an account radosgw takes the empty uid's empty tenant; rgw-go takes
// the account's (docs/ceph-upstream-bugs.md, "radosgw's bucket link to an
// account moves the bucket out of the account's tenant").
func (o *LinkBucket) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	owner, err := bucketOwner(o.UID, o.AccountID)
	if err != nil {
		return err
	}
	rec, missing := loadInitBucket(ctx, env, o.UID, o.Bucket, o.BucketID)
	if missing != nil && (!errors.Is(missing, ErrNoSuchKey) || o.BucketID != "") {
		return missing
	}
	user, err := loadInitUser(ctx, env, o.UID)
	if missing == nil && err != nil {
		return err
	}
	var displayName, tenant string
	switch {
	case o.AccountID != "":
		a, aerr := env.Accounts.GetAccount(ctx, o.AccountID)
		switch {
		case errors.Is(aerr, ErrNoSuchEntity) || errors.Is(aerr, ErrNoSuchKey):
			return ErrNoSuchKey
		case aerr != nil:
			return storeErr(aerr)
		}
		displayName, tenant = a.Info.Name, a.Info.Tenant
	case err != nil:
		return missing
	case user.Info.AccountID != "":
		return ErrInvalidArgument
	default:
		displayName, tenant = user.Info.DisplayName, o.UID.Tenant
	}
	if missing != nil {
		if rec = o.unfinishedRename(ctx, env, tenant); rec == nil {
			return missing
		}
	}
	if o.BucketID != "" && o.BucketID != rec.Info.Bucket.ID {
		return ErrInvalidArgument
	}
	if o.BucketID != "" {
		if err := refuseNamedElsewhere(ctx, env, rec); err != nil {
			return err
		}
	}
	target := meta.BucketID{Tenant: tenant, Name: rec.Info.Bucket.Name}
	if o.NewBucketName != "" {
		target.Tenant, target.Name = splitBucket(o.NewBucketName, tenant)
	}
	var newName *meta.BucketID
	if target.Tenant != rec.Info.Bucket.Tenant || target.Name != rec.Info.Bucket.Name {
		newName = &target
	}
	return env.BucketAdmin.ChangeBucketOwner(ctx, rec, owner, displayName, newName)
}

// refuseNamedElsewhere is the check a link by bucket id makes before it
// gives the instance rec loaded by its id a name: every entry point of the
// bucket metadata section, page by page, is loaded, and one that loads the
// bucket's id under another name is BucketAlreadyExists. radosgw's own
// failed rename leaves an instance carrying the live bucket's id that no
// entry point names, and linking it would give one bucket's data two
// owners (docs/ceph-upstream-bugs.md, "radosgw's bucket link by bucket id
// takes the name from the live bucket"). A listed entry point that loads
// nothing, and every listing or read failure, refuses too: what it names
// cannot be told. A store that cannot list the section, as the RADOS
// driver cannot yet, answers its NotImplemented, so the link is refused
// rather than made blind (docs/exclusions.md, "On the RADOS driver, three
// admin bucket requests answer 501 NotImplemented for now"). Each by-id
// link reads every bucket's entry point and instance.
func refuseNamedElsewhere(ctx context.Context, env *Env, rec *BucketRecord) error {
	own := rec.Info.Bucket
	marker := ""
	for {
		keys, next, more, err := env.Metadata.List(ctx, "bucket", marker, bucketMetadataPage)
		if err != nil {
			return storeErr(err)
		}
		for _, k := range keys {
			tenant, name := splitBucket(k, "")
			if tenant == own.Tenant && name == own.Name {
				continue
			}
			b, err := env.Buckets.GetBucket(ctx, tenant, name)
			switch {
			case errors.Is(err, ErrNoSuchBucket):
				return ErrConcurrentModification
			case err != nil:
				return storeErr(err)
			case b.Info.Bucket.ID == own.ID:
				return ErrBucketAlreadyExists
			}
		}
		if !more {
			return nil
		}
		if next == marker {
			return fmt.Errorf("%w: the bucket metadata listing made no progress past %q", ErrInternalError, marker)
		}
		marker = next
	}
}

// unfinishedRename is the bucket a rename this request names left under its
// new key after the old name went, nil when there is none: the request's
// old name no longer loads, the new key does, and that bucket records a
// rename from the old name to it. A retry of the rename then completes it.
func (o *LinkBucket) unfinishedRename(ctx context.Context, env *Env, tenant string) *BucketRecord {
	srcTenant, srcName := splitBucket(o.Bucket, o.UID.Tenant)
	dst := meta.BucketID{Tenant: tenant, Name: srcName}
	if o.NewBucketName != "" {
		dst.Tenant, dst.Name = splitBucket(o.NewBucketName, tenant)
	}
	if dst.Tenant == srcTenant && dst.Name == srcName {
		return nil
	}
	rec, err := env.Buckets.GetBucket(ctx, dst.Tenant, dst.Name)
	if err != nil {
		return nil
	}
	i, ok := DecodeRenameIntent(rec.Attrs)
	if !ok || i.SrcTenant != srcTenant || i.SrcName != srcName || i.DstTenant != dst.Tenant || i.DstName != dst.Name {
		return nil
	}
	return rec
}

// UnlinkBucket is RGWOp_Bucket_Unlink and RGWBucketAdminOp::unlink
// (rgw_rest_bucket.cc:172-209; rgw_bucket.cc:999-1024, v20.2.4
// :1150-1175). Cap buckets=write.
type UnlinkBucket struct {
	AdminOp
	UID       meta.UserID
	AccountID string
	Bucket    string
}

// NewUnlinkBucket returns an UnlinkBucket.
func NewUnlinkBucket() *UnlinkBucket { return &UnlinkBucket{} }

// Name is unlink_bucket.
func (o *UnlinkBucket) Name() string { return "unlink_bucket" }

// VerifyPermission is RGWOp_Bucket_Unlink::check_caps.
func (o *UnlinkBucket) VerifyPermission(_ context.Context, r *Request) error {
	return bucketsCap(r, meta.CapWrite)
}

// Execute is RGWBucketAdminOp::unlink: the owner, RGWBucket::init, then the
// driver's unlink with the entry point updated.
func (o *UnlinkBucket) Execute(ctx context.Context, r *Request) error {
	owner, err := bucketOwner(o.UID, o.AccountID)
	if err != nil {
		return err
	}
	rec, err := loadAdminBucket(ctx, r.Env, o.UID, o.Bucket)
	if err != nil {
		return err
	}
	return r.Env.BucketAdmin.UnlinkBucketOwner(ctx, rec, owner)
}

// RemoveBucketAdmin is RGWOp_Bucket_Remove and
// RGWBucketAdminOp::remove_bucket (rgw_rest_bucket.cc:211-248;
// rgw_bucket.cc:1282-1327, v20.2.4 :1444-1490). Cap buckets=write.
type RemoveBucketAdmin struct {
	AdminOp
	Bucket, Tenant string
	PurgeObjects   bool
	// BypassGC takes remove_bypass_gc, which purges whatever PurgeObjects
	// says (rgw_bucket.cc:1303-1306).
	BypassGC bool
}

// NewRemoveBucketAdmin returns a RemoveBucketAdmin.
func NewRemoveBucketAdmin() *RemoveBucketAdmin { return &RemoveBucketAdmin{} }

// Name is remove_bucket.
func (o *RemoveBucketAdmin) Name() string { return "remove_bucket" }

// VerifyPermission is RGWOp_Bucket_Remove::check_caps.
func (o *RemoveBucketAdmin) VerifyPermission(_ context.Context, r *Request) error {
	return bucketsCap(r, meta.CapWrite)
}

// Execute is remove_bucket: the bucket by its tenant argument and its name
// unsplit; another zonegroup's bucket is PermanentRedirect; then the
// bypass-gc removal or the ordinary one, purging when asked. radosgw skips
// the zonegroup check for a forwarded request, which it recognizes by an
// argument its parser never lets it see (docs/ceph-upstream-bugs.md), and
// rgw-go, which forwards nothing, never skips it. The op answers every
// -ENOENT as NoSuchBucket (rgw_rest_bucket.cc:245-247). Each pass deletes
// what is left, so a removal that failed partway completes on retry.
func (o *RemoveBucketAdmin) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	rec, err := env.Buckets.GetBucket(ctx, o.Tenant, o.Bucket)
	if errors.Is(err, ErrNoSuchBucket) {
		return o.freeDanglingName(ctx, env)
	}
	if err == nil && rec.Info.Zonegroup != env.Zone.ZoneGroup().ID {
		return ErrPermanentRedirect
	}
	if err == nil {
		if o.BypassGC {
			err = RemoveBucketBypassGC(ctx, env, rec)
		} else {
			err = DeleteBucketWithChildren(ctx, env, rec, o.PurgeObjects)
		}
	}
	if errors.Is(err, ErrNoSuchKey) {
		return ErrNoSuchBucket
	}
	return err
}

// freeDanglingName answers a name that loads no bucket: one whose entry
// point names an instance that does not exist, and that no live rename
// claims, loses that entry point, and nothing else
// (BucketAdminStore.RemoveDanglingEntryPoint); one without an entry point
// is NoSuchBucket, as radosgw answers it. radosgw answers both NoSuchBucket
// and frees neither, so a name an unfinished rename reserved would stay
// taken (docs/exclusions.md, "Bucket link and unlink write differently
// from radosgw").
func (o *RemoveBucketAdmin) freeDanglingName(ctx context.Context, env *Env) error {
	err := env.BucketAdmin.RemoveDanglingEntryPoint(ctx, o.Tenant, o.Bucket)
	if errors.Is(err, ErrNoSuchBucket) || errors.Is(err, ErrNoSuchKey) {
		return ErrNoSuchBucket
	}
	return err
}

// SetBucketQuota is RGWOp_Set_Bucket_Quota and RGWBucket::set_quota
// (rgw_rest_bucket.cc:250-328; rgw_bucket.cc:261-272 and :1726-1736).
// Cap buckets=write.
type SetBucketQuota struct {
	AdminOp
	UID meta.UserID
	// UIDGiven and BucketGiven are that the request names the uid and the
	// bucket argument, empty or not.
	UIDGiven    bool
	Bucket      string
	BucketGiven bool
	// Params are used when the request carries no body, or an empty
	// chunked one.
	Params QuotaParams
	// Quota is the quota written.
	Quota meta.Quota
}

// NewSetBucketQuota returns a SetBucketQuota.
func NewSetBucketQuota() *SetBucketQuota { return &SetBucketQuota{} }

// Name is set_bucket_quota.
func (o *SetBucketQuota) Name() string { return "set_bucket_quota" }

// VerifyPermission is RGWOp_Set_Bucket_Quota::check_caps.
func (o *SetBucketQuota) VerifyPermission(_ context.Context, r *Request) error {
	return bucketsCap(r, meta.CapWrite)
}

// Execute is RGWOp_Set_Bucket_Quota::execute: the uid and bucket arguments
// must exist; a request with a body, a Content-Length above 0 or chunked,
// gives one RGWQuotaInfo of at most 1024 bytes, and one without, or with an
// empty chunked body, the arguments over the bucket's current quota, the
// bucket read under the uid's tenant by its unsplit name. A size that would
// lift the limit is refused as the user quota set refuses it
// (QuotaParams.apply, validSizeLimit). Then
// RGWBucketAdminOp::set_quota: RGWBucket::init, and the instance written
// with the quota.
func (o *SetBucketQuota) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	if !o.UIDGiven || !o.BucketGiven {
		return ErrInvalidArgument
	}
	useParams := r.ContentLength == 0
	if !useParams {
		body, err := ReadParamBody(r, quotaInputMaxLen)
		if err != nil {
			return err
		}
		if len(body) > 0 {
			if err := decodeQuotaBody(body, &o.Quota); err != nil {
				return err
			}
		}
		useParams = len(body) == 0
	}
	if useParams {
		cur, err := env.Buckets.GetBucket(ctx, o.UID.Tenant, o.Bucket)
		if errors.Is(err, ErrNoSuchBucket) {
			return ErrNoSuchKey
		}
		if err != nil {
			return err
		}
		if o.Quota, err = o.Params.apply(cur.Info.Quota); err != nil {
			return err
		}
	}
	if !validSizeLimit(o.Quota.MaxSize) {
		return ErrInvalidArgument
	}
	rec, err := loadAdminBucket(ctx, env, o.UID, o.Bucket)
	if err != nil {
		return err
	}
	rec.Info.Quota = o.Quota
	return env.Buckets.PutBucketInfo(ctx, rec)
}

// validAdminObjectName is the name check the S3 handler makes,
// validate_object_name (rgw_rest.cc:1846-1859 at v19.2.6, :1850-1863 at
// v20.2.4), with the empty name, which names the bucket there: at most 1024
// bytes of UTF-8, and not empty. No object can carry another name.
func validAdminObjectName(name string) bool {
	return name != "" && len(name) <= 1024 && utf8.ValidString(name)
}

// GetBucketPolicyAdmin is RGWOp_Get_Policy and RGWBucket::get_policy
// (rgw_rest_bucket.cc:64-92; rgw_bucket.cc:907-983, v20.2.4 :1058-1134).
// Cap buckets=read.
type GetBucketPolicyAdmin struct {
	AdminOp
	Bucket, Object string
	Result         acl.Policy
}

// NewGetBucketPolicyAdmin returns a GetBucketPolicyAdmin.
func NewGetBucketPolicyAdmin() *GetBucketPolicyAdmin { return &GetBucketPolicyAdmin{} }

// Name is get_policy.
func (o *GetBucketPolicyAdmin) Name() string { return "get_policy" }

// VerifyPermission is RGWOp_Get_Policy::check_caps.
func (o *GetBucketPolicyAdmin) VerifyPermission(_ context.Context, r *Request) error {
	return bucketsCap(r, meta.CapRead)
}

// Execute is get_policy: RGWBucket::init, then the object's ACL attr when
// an object is named, else the bucket's. A missing object, an object name
// no object can carry, and a bucket without an ACL are -ENOENT, NoSuchKey;
// an object without one is get_attr's -ENODATA, and an ACL that does not
// decode decode_bl's -EIO, both UnknownError, as no S3 error names them.
func (o *GetBucketPolicyAdmin) Execute(ctx context.Context, r *Request) error {
	rec, err := loadAdminBucket(ctx, r.Env, meta.UserID{}, o.Bucket)
	if err != nil {
		return err
	}
	var raw []byte
	if o.Object != "" {
		if !validAdminObjectName(o.Object) {
			return ErrNoSuchKey
		}
		st, err := r.Env.Objects.StatObject(ctx, rec, meta.ObjKey{Name: o.Object})
		if err != nil {
			return err
		}
		if !st.Exists {
			return ErrNoSuchKey
		}
		var ok bool
		if raw, ok = st.Attrs[meta.AttrACL]; !ok {
			return ErrUnknown
		}
	} else {
		var ok bool
		if raw, ok = rec.Attrs[meta.AttrACL]; !ok {
			return ErrNoSuchKey
		}
	}
	d := denc.NewDecoder(raw)
	p := acl.DecodePolicy(d)
	if d.Err() != nil {
		return ErrUnknown
	}
	o.Result = p
	return nil
}

// RemoveObjectAdmin is RGWOp_Object_Remove and RGWBucket::remove_object
// (rgw_rest_bucket.cc:362-390; rgw_bucket.cc:148-161 and :274-289).
// Cap buckets=write.
type RemoveObjectAdmin struct {
	AdminOp
	Bucket, Object string
}

// NewRemoveObjectAdmin returns a RemoveObjectAdmin.
func NewRemoveObjectAdmin() *RemoveObjectAdmin { return &RemoveObjectAdmin{} }

// Name is remove_object.
func (o *RemoveObjectAdmin) Name() string { return "remove_object" }

// VerifyPermission is RGWOp_Object_Remove::check_caps.
func (o *RemoveObjectAdmin) VerifyPermission(_ context.Context, r *Request) error {
	return bucketsCap(r, meta.CapWrite)
}

// Execute is RGWBucket::init, then the object deleted as rgw_remove_object
// deletes it, a missing object being -ENOENT, NoSuchKey. A name no object
// can carry, empty, longer than 1024 bytes or not UTF-8, is answered
// NoSuchKey without reaching the store, the answer radosgw's head read
// gives it, where radosgw would hand a name of over about 2000 bytes to
// RADOS, which refuses it as too long (docs/exclusions.md).
func (o *RemoveObjectAdmin) Execute(ctx context.Context, r *Request) error {
	rec, err := loadAdminBucket(ctx, r.Env, meta.UserID{}, o.Bucket)
	if err != nil {
		return err
	}
	if !validAdminObjectName(o.Object) {
		return ErrNoSuchKey
	}
	return r.Env.Objects.DeleteObject(ctx, rec, meta.ObjKey{Name: o.Object}, DeleteParams{})
}
