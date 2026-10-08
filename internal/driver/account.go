package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The account store is rgwrados::account (driver/rados/account.cc) and
// rgwrados::users (driver/rados/users.cc). A bare line number is
// account.cc's at v19.2.6, and each fact cited holds at v20.2.4 too.
//
// radosgw finds an account by name or email through a redirect object
// holding the account's id (RGWUID). This store claims a write's name and
// email redirects before it writes the account, so an account never holds
// a name or email without its redirect naming it; radosgw writes them
// after. A redirect is created exclusively under a fresh version, and one
// this store removes is removed under the version it read, and only while
// it names the account removing it. A redirect whose account is gone, or
// no longer holds that name or email, is stale: a write or a removal that
// stopped part way leaves one, a lookup through it finds nothing, and a
// later claim may take it over once a read past the metadata cache shows
// it stale.

// accountNameObj is the name redirect "name.<tenant>$<name>" in the account
// pool (:92-100); the name keeps its case.
func (s *Store) accountNameObj(tenant, name string) sysObj {
	return sysObj{pool: s.zone.Params.AccountPool, oid: "name." + tenant + "$" + name}
}

// accountUsersObj is the account users index "users.<id>" in the account
// pool (:52-58). An account's email redirect is emailIndexObj's: accounts
// and users share users.email (:108-114).
func (s *Store) accountUsersObj(id string) sysObj {
	return sysObj{pool: s.zone.Params.AccountPool, oid: "users." + id}
}

// redirect is RedirectObj (:117-121): a redirect object as read, the id it
// names and the version it was read at; found is false when it is missing.
type redirect struct {
	obj   sysObj
	kind  string
	id    string
	v     objv
	found bool
}

// readRedirect is read_redirect (:123-146): a missing object is no error,
// one that does not decode is radosgw's -EIO.
func (s *Store) readRedirect(ctx context.Context, o sysObj, kind string) (redirect, error) {
	r := redirect{obj: o, kind: kind}
	res, err := s.sysobj.read(ctx, o, readParams{data: true, objv: &r.v})
	switch {
	case errors.Is(err, radosclient.ErrNotFound), errors.Is(err, errNegativeEntry):
		r.v = objv{}
		return r, nil
	case err != nil:
		return r, op.FromRADOS(err, op.ScopeService)
	}
	d := denc.NewDecoder(res.data)
	r.id = string(meta.DecodeUID(d))
	if err := d.Err(); err != nil {
		return r, fmt.Errorf("%w: decoding an account %s redirect: %w", op.ErrInternalError, kind, err)
	}
	r.found = true
	return r, nil
}

// holder is the account a found redirect names when that account still
// holds what the redirect indexes, holds being nameHeld or emailHeld; nil
// for a stale redirect.
func (s *Store) holder(ctx context.Context, r redirect, holds func(meta.AccountInfo) bool) (*op.AccountRecord, error) {
	rec, err := s.GetAccount(ctx, r.id)
	switch {
	case errors.Is(err, op.ErrNoSuchEntity):
		return nil, nil
	case err != nil:
		return nil, err
	case !holds(rec.Info):
		return nil, nil
	}
	return rec, nil
}

func nameHeld(tenant, name string) func(meta.AccountInfo) bool {
	return func(a meta.AccountInfo) bool { return a.Tenant == tenant && a.Name == name }
}

func emailHeld(email string) func(meta.AccountInfo) bool {
	return func(a meta.AccountInfo) bool { return lowerASCII(a.Email) == lowerASCII(email) }
}

// GetAccount implements op.AccountStore as read (:163-198): the info, the
// rgw attrs, the version and the mtime. A missing account is
// ErrNoSuchEntity; an object that does not decode or names another id is
// radosgw's -EIO.
func (s *Store) GetAccount(ctx context.Context, id string) (*op.AccountRecord, error) {
	v := &objv{}
	res, err := s.sysobj.read(ctx, s.accountObj(id), readParams{data: true, attrs: true, meta: true, objv: v})
	switch {
	case errors.Is(err, radosclient.ErrNotFound), errors.Is(err, errNegativeEntry):
		return nil, fmt.Errorf("account %s: %w", id, op.ErrNoSuchEntity)
	case err != nil:
		return nil, op.FromRADOS(err, op.ScopeService)
	}
	d := denc.NewDecoder(res.data)
	info := meta.DecodeAccountInfo(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("%w: decoding account %s: %w", op.ErrInternalError, id, err)
	}
	if info.ID != id {
		return nil, fmt.Errorf("%w: the object of account %s names %q", op.ErrInternalError, id, info.ID)
	}
	return &op.AccountRecord{Info: info, Attrs: res.attrs, Version: v.read, Mtime: res.mtime}, nil
}

// AccountName implements op.AccountStore.
func (s *Store) AccountName(ctx context.Context, id string) (string, error) {
	rec, err := s.GetAccount(ctx, id)
	if err != nil {
		return "", err
	}
	return rec.Info.Name, nil
}

// GetAccountByName implements op.AccountStore as read_by_name (:200-217).
// A stale redirect is ErrNoSuchEntity, where radosgw returns whichever
// account it names.
func (s *Store) GetAccountByName(ctx context.Context, tenant, name string) (*op.AccountRecord, error) {
	return s.accountByRedirect(ctx, s.accountNameObj(tenant, name), "name", nameHeld(tenant, name))
}

// GetAccountByEmail implements op.AccountStore as read_by_email (:219-239):
// an email a user holds names no account. A stale redirect is
// ErrNoSuchEntity too.
func (s *Store) GetAccountByEmail(ctx context.Context, email string) (*op.AccountRecord, error) {
	return s.accountByRedirect(ctx, s.emailIndexObj(email), "email", emailHeld(email))
}

func (s *Store) accountByRedirect(ctx context.Context, o sysObj, kind string, holds func(meta.AccountInfo) bool) (*op.AccountRecord, error) {
	r, err := s.readRedirect(ctx, o, kind)
	if err != nil {
		return nil, err
	}
	if r.found && meta.ValidAccountID(r.id) {
		rec, err := s.holder(ctx, r, holds)
		if err != nil || rec != nil {
			return rec, err
		}
	}
	return nil, fmt.Errorf("account %s: %w", kind, op.ErrNoSuchEntity)
}

// PutAccount implements op.AccountStore as write (:242-392). A change of id
// is refused first. A create then writes its account object exclusively
// with no name and no email, so that the account exists, with a version,
// before any redirect names it. Then the redirects of the name and email
// rec holds are claimed, changed or not (claimRedirect), so a refusal of
// either comes before the account holds them, and a write retried after
// one that stopped completes them. Then the whole account under the
// version read, or the one the create's first write left; a claim taken
// over meanwhile fenced that version, so the write then fails
// ErrConcurrentModification instead of landing a name it lost. Last, as
// radosgw does after its account write, old's name and email redirects
// when they still name the account, a failure of each logged and ignored
// (:343-389). radosgw claims the new redirects after the account object and
// ignores their failure, so one failure leaves two accounts holding one
// name.
//
// A create whose claim fails removes the name redirect it claimed, then its
// nameless object, each under the version it wrote, keeping the object when
// the redirect's removal fails, so no redirect it claimed is left naming no
// account; one that stops before that leaves a nameless account, which no
// lookup by name or email finds. A zero rec.Version over a stored account,
// which always has a version past it, is written only as a create, which
// the stored account refuses.
func (s *Store) PutAccount(ctx context.Context, rec *op.AccountRecord, old *meta.AccountInfo, opts op.PutAccountOptions) error {
	info := rec.Info
	id := info.ID
	if old != nil && old.ID != id {
		return fmt.Errorf("account %s: changing the id to %s: %w", old.ID, id, op.ErrInvalidArgument)
	}
	sameName := old != nil && old.Tenant == info.Tenant && old.Name == info.Name
	sameEmail := old != nil && lowerASCII(old.Email) == lowerASCII(info.Email)

	var dropName, dropEmail *redirect
	if old != nil && !sameName && old.Name != "" {
		r, err := s.readRedirect(ctx, s.accountNameObj(old.Tenant, old.Name), "name")
		if err != nil {
			return err
		}
		if r.found && r.id == id {
			dropName = &r
		}
	}
	if old != nil && !sameEmail && old.Email != "" {
		r, err := s.readRedirect(ctx, s.emailIndexObj(old.Email), "email")
		if err != nil {
			return err
		}
		if r.found && r.id == id {
			dropEmail = &r
		}
	}

	v := &objv{read: rec.Version}
	create := old == nil || opts.Exclusive || rec.Version.Ver == 0
	if create {
		blank := &op.AccountRecord{Info: info}
		blank.Info.Name, blank.Info.Email = "", ""
		if err := s.writeAccount(ctx, blank, true, &objv{write: newWriteVersion()}); err != nil {
			return err
		}
		v = &objv{read: blank.Version}
	}
	if nameV, err := s.claimRedirects(ctx, info); err != nil {
		if create {
			if s.unclaim(ctx, s.accountNameObj(info.Tenant, info.Name), "name", id, nameV) {
				s.removeBlank(ctx, id, v.read)
			}
		}
		return err
	}
	if err := s.writeAccount(ctx, rec, false, v); err != nil {
		return err
	}

	if dropName != nil {
		s.dropRedirect(ctx, *dropName, id)
	}
	if dropEmail != nil {
		s.dropRedirect(ctx, *dropEmail, id)
	}
	return nil
}

// claimRedirects claims the redirects of the name and the email info holds.
// It returns the version the name's claim left, zero when it made none, so
// a create whose email claim fails can give the name up.
func (s *Store) claimRedirects(ctx context.Context, info meta.AccountInfo) (meta.ObjVersion, error) {
	var nameV meta.ObjVersion
	if info.Name != "" {
		v, err := s.claimRedirect(ctx, s.accountNameObj(info.Tenant, info.Name), "name", info.ID, nameHeld(info.Tenant, info.Name))
		if err != nil {
			return nameV, err
		}
		nameV = v
	}
	if info.Email != "" {
		_, err := s.claimRedirect(ctx, s.emailIndexObj(info.Email), "email", info.ID, emailHeld(info.Email))
		return nameV, err
	}
	return nameV, nil
}

// unclaim removes a redirect a refused create claimed, under the version
// its claim left; a zero v is no claim. A failure is logged and answers
// false: the create then keeps its nameless object, so the redirect names
// an account that exists and does not hold it, which is stale.
func (s *Store) unclaim(ctx context.Context, o sysObj, kind, id string, v meta.ObjVersion) bool {
	if v.Ver == 0 {
		return true
	}
	if err := s.sysobj.remove(ctx, o, &objv{read: v}); err != nil {
		redirectFailed(ctx, "removing a refused create's", kind, id, err)
		return false
	}
	return true
}

// removeBlank removes a create's nameless account object under the version
// its write left; a failure is logged and leaves the nameless account.
func (s *Store) removeBlank(ctx context.Context, id string, v meta.ObjVersion) {
	if err := s.sysobj.remove(ctx, s.accountObj(id), &objv{read: v}); err != nil {
		redirectFailed(ctx, "removing a refused create's", "account", id, err)
	}
}

// writeAccount writes rec's account object under v, leaving rec's version
// and mtime those written.
func (s *Store) writeAccount(ctx context.Context, rec *op.AccountRecord, exclusive bool, v *objv) error {
	data := encodeAt(rec.Info, s.release)
	mtime, err := s.sysobj.write(ctx, s.accountObj(rec.Info.ID), data, rec.Attrs, exclusive, time.Time{}, v)
	switch {
	case errors.Is(err, radosclient.ErrExists):
		return fmt.Errorf("account %s: %w", rec.Info.ID, op.ErrAccountAlreadyExists)
	case err != nil:
		return op.FromRADOS(err, op.ScopeService)
	}
	rec.Version, rec.Mtime = v.read, mtime
	return nil
}

// claimTries bounds the reads of a redirect that keeps changing under a
// claim.
const claimTries = 3

// claimRedirect makes the redirect o name account id before id's object
// holds what o indexes, reading the redirect past the metadata cache: one
// that names id is written again unchanged under the version read, so a
// takeover that judged it stale before then fails its versioned removal;
// a missing one is created exclusively under a fresh version
// (write_redirect, :148-160). One naming a user, whose ids
// the email index shares (:108-114), or another account that holds what o
// indexes, read past the cache under its version, is
// ErrAccountAlreadyExists. One naming an account that is gone or holds
// something else is stale and taken over: that account's object is first
// written again unchanged under the version that showed it stale, so a
// write of it in flight fails rather than land the name it no longer has
// the redirect for, then the redirect is removed under the version it was
// read at and created again. A redirect that changes meanwhile is read
// again, up to claimTries times, then ErrConcurrentModification. It
// returns the version the redirect's write left.
func (s *Store) claimRedirect(ctx context.Context, o sysObj, kind, id string, holds func(meta.AccountInfo) bool) (meta.ObjVersion, error) {
	for range claimTries {
		r, err := s.readRedirectFresh(ctx, o, kind)
		if err != nil {
			return meta.ObjVersion{}, err
		}
		link := encodeAt(meta.UID(id), s.release)
		if r.found && r.id == id {
			v := &objv{read: r.v.read}
			_, err = s.sysobj.write(ctx, o, link, nil, false, time.Time{}, v)
			switch {
			case err == nil:
				return v.read, nil
			case errors.Is(err, radosclient.ErrCanceled), errors.Is(err, radosclient.ErrNotFound):
				continue
			default:
				return meta.ObjVersion{}, op.FromRADOS(err, op.ScopeService)
			}
		}
		if r.found {
			if !meta.ValidAccountID(r.id) {
				return meta.ObjVersion{}, fmt.Errorf("account %s: the %s is a user's: %w", id, kind, op.ErrAccountAlreadyExists)
			}
			stale, ferr := s.fenceStaleHolder(ctx, r.id, holds)
			switch {
			case errors.Is(ferr, radosclient.ErrCanceled):
				continue
			case ferr != nil:
				return meta.ObjVersion{}, ferr
			case !stale:
				return meta.ObjVersion{}, fmt.Errorf("account %s: the %s is taken: %w", id, kind, op.ErrAccountAlreadyExists)
			}
			err = s.sysobj.remove(ctx, o, &objv{read: r.v.read})
			switch {
			case errors.Is(err, radosclient.ErrCanceled), errors.Is(err, radosclient.ErrNotFound):
				continue
			case err != nil:
				return meta.ObjVersion{}, op.FromRADOS(err, op.ScopeService)
			}
		}
		v := &objv{write: newWriteVersion()}
		_, err = s.sysobj.write(ctx, o, link, nil, true, time.Time{}, v)
		switch {
		case err == nil:
			return v.read, nil
		case errors.Is(err, radosclient.ErrExists):
			continue
		default:
			return meta.ObjVersion{}, op.FromRADOS(err, op.ScopeService)
		}
	}
	return meta.ObjVersion{}, fmt.Errorf("account %s: the %s kept changing: %w", id, kind, op.ErrConcurrentModification)
}

// fenceStaleHolder reads account holder past the metadata cache and reports
// whether it is no holder of what a redirect naming it indexes. A holder
// that is gone is; one that exists is written again, its data, rgw attrs
// and mtime unchanged, under the version read, which fails with
// radosclient.ErrCanceled when it changed meanwhile. A holder stored
// without a version cannot be fenced and is taken for a holder.
func (s *Store) fenceStaleHolder(ctx context.Context, holder string, holds func(meta.AccountInfo) bool) (bool, error) {
	o := s.accountObj(holder)
	info, found, err := s.readFresh(ctx, o)
	if err != nil || !found {
		return !found, err
	}
	d := denc.NewDecoder(info.data)
	acct := meta.DecodeAccountInfo(d)
	if derr := d.Err(); derr != nil {
		return false, fmt.Errorf("%w: decoding account %s: %w", op.ErrInternalError, holder, derr)
	}
	if acct.ID != holder || holds(acct) || info.version.Ver == 0 {
		return false, nil
	}
	_, err = s.sysobj.write(ctx, o, info.data, filterRGWAttrs(info.xattrs), false, info.mtime, &objv{read: info.version})
	if err != nil && !errors.Is(err, radosclient.ErrCanceled) {
		return false, op.FromRADOS(err, op.ScopeService)
	}
	return err == nil, err
}

// readFresh reads o from RADOS past the metadata cache, with its data, its
// xattrs, its mtime and its version, caching what it read; found is false
// for a missing object.
func (s *Store) readFresh(ctx context.Context, o sysObj) (cacheInfo, bool, error) {
	info, err := s.sysobj.readMiss(ctx, o, normalName(o.pool, o.oid), readParams{data: true, objv: &objv{}})
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return cacheInfo{}, false, nil
	case err != nil:
		return cacheInfo{}, false, op.FromRADOS(err, op.ScopeService)
	}
	return info, true, nil
}

// readRedirectFresh is readRedirect past the metadata cache.
func (s *Store) readRedirectFresh(ctx context.Context, o sysObj, kind string) (redirect, error) {
	r := redirect{obj: o, kind: kind}
	info, found, err := s.readFresh(ctx, o)
	if err != nil || !found {
		return r, err
	}
	d := denc.NewDecoder(info.data)
	r.id = string(meta.DecodeUID(d))
	if err := d.Err(); err != nil {
		return r, fmt.Errorf("%w: decoding an account %s redirect: %w", op.ErrInternalError, kind, err)
	}
	r.v = objv{read: info.version}
	r.found = true
	return r, nil
}

// dropRedirect removes r under the version it was read at, when it named
// account id. A failure is logged.
func (s *Store) dropRedirect(ctx context.Context, r redirect, id string) {
	if !r.found || r.id != id {
		return
	}
	if err := s.sysobj.remove(ctx, r.obj, &objv{read: r.v.read}); err != nil {
		redirectFailed(ctx, "removing", r.kind, id, err)
	}
}

// redirectFailed logs a redirect step radosgw lets fail. It names the
// account and the kind of redirect, not the object: an email redirect is
// named by the email.
func redirectFailed(ctx context.Context, step, kind, id string, err error) {
	slog.WarnContext(ctx, "an account redirect step failed, going on as radosgw does",
		slog.String("step", step), slog.String("redirect", kind), slog.String("account", id), slog.String("code", op.AsError(err).Code))
}

// RemoveAccount implements op.AccountStore as remove (:394-438), first
// refusing an account that still has a role, a group, an OpenID Connect
// provider or a topic, as rgw::account::remove does before it
// (rgw_account.cc:366-456; accountResources). Then the account object
// under rec's version, then its name and email redirects and its users
// index, a failure of each logged and ignored. The redirects are read past
// the metadata cache before the account object is removed, a failed read
// refusing the removal, and each is then removed only when it named the
// account, under the version read then: a create of the same id that
// claims one meanwhile writes it again, so the removal leaves it. radosgw
// removes both unread and unchecked (:409-426), so it can remove a name or
// email another account or a user now holds.
//
// A zero rec.Version, which a stored account always has a version past, is
// ErrConcurrentModification: the removal is never unchecked. The account
// object's version check fails alike for a changed and a missing object,
// so a failed check is told apart by reading it back: ErrNoSuchEntity for a
// missing account, ErrConcurrentModification for a changed one.
func (s *Store) RemoveAccount(ctx context.Context, rec *op.AccountRecord) error {
	info := rec.Info
	if rec.Version.Ver == 0 {
		return fmt.Errorf("removing account %s without its version: %w", info.ID, op.ErrConcurrentModification)
	}
	if err := s.accountResources(ctx, info.ID); err != nil {
		return err
	}
	var name, email redirect
	if info.Name != "" {
		r, err := s.readRedirectFresh(ctx, s.accountNameObj(info.Tenant, info.Name), "name")
		if err != nil {
			return err
		}
		name = r
	}
	if info.Email != "" {
		r, err := s.readRedirectFresh(ctx, s.emailIndexObj(info.Email), "email")
		if err != nil {
			return err
		}
		email = r
	}
	o := s.accountObj(info.ID)
	err := s.sysobj.remove(ctx, o, &objv{read: rec.Version})
	switch {
	case errors.Is(err, radosclient.ErrNotFound),
		errors.Is(err, radosclient.ErrCanceled) && s.missing(ctx, o):
		return fmt.Errorf("removing account %s: %w", info.ID, op.ErrNoSuchEntity)
	case err != nil:
		return op.FromRADOS(err, op.ScopeService)
	}
	s.dropRedirect(ctx, name, info.ID)
	s.dropRedirect(ctx, email, info.ID)
	if err := s.sysobj.remove(ctx, s.accountUsersObj(info.ID), nil); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
		redirectFailed(ctx, "removing", "users index", info.ID, err)
	}
	return nil
}

// oidcProviderPrefix is oidc_url_oid_prefix (driver/rados/rgw_sal_rados.cc:2136
// at v19.2.6, :2622 at v20.2.4): an account's providers are the objects of
// the oidc pool named "<account id>oidc_url.<url>" (oidc_provider_oid,
// :2138-2143 at v19.2.6).
const oidcProviderPrefix = "oidc_url."

// errResourceFound ends a listing at the first resource it finds.
var errResourceFound = errors.New("driver: an account resource remains")

// accountResources is rgw::account::remove's checks past its users and
// buckets (rgw_account.cc:366-456): one entry of the account's roles,
// groups and topics indexes, "roles.<id>", "groups.<id>" and "topics.<id>"
// in the account pool (account.cc:60-81), each a cls_user account resource
// index, and the oidc pool's objects for an OpenID Connect provider of the
// account. Any one refuses the removal with radosgw's -ENOTEMPTY, the
// BucketNotEmpty row, and its message. A missing index or pool holds none;
// any other failure to read one refuses the removal. radosgw lists the
// topics of the account's tenant instead (docs/ceph-upstream-bugs.md,
// "radosgw's account removal never sees the account's topics"), so it
// removes an account with topics that rgw-go refuses.
func (s *Store) accountResources(ctx context.Context, id string) error {
	for _, ix := range []struct{ prefix, what string }{
		{"roles.", "roles"}, {"groups.", "groups"},
	} {
		if err := s.accountResourceIndex(ctx, ix.prefix+id, ix.what); err != nil {
			return err
		}
	}
	if err := s.accountOIDCProviders(ctx, id); err != nil {
		return err
	}
	return s.accountResourceIndex(ctx, "topics."+id, "topics")
}

// accountResourceIndex refuses the removal when the index oid lists an
// entry.
func (s *Store) accountResourceIndex(ctx context.Context, oid, what string) error {
	pool, err := s.pools.get(ctx, s.zone.Params.AccountPool)
	if err != nil {
		return op.FromRADOS(err, op.ScopeService)
	}
	rop := radosclient.NewReadOp()
	res := user.AccountResourceList(rop, "", "", 1, s.release)
	if _, rerr := pool.Read(ctx, oid, rop, radosclient.OpFlagNone); rerr != nil {
		if errors.Is(rerr, radosclient.ErrNotFound) {
			return nil
		}
		return op.FromRADOS(rerr, op.ScopeService)
	}
	entries, _, _, err := res.Result()
	if err != nil {
		return op.FromRADOS(err, op.ScopeService)
	}
	if len(entries) > 0 {
		return op.ErrBucketNotEmpty.WithMessage("The account cannot be deleted until all " + what + " are removed.")
	}
	return nil
}

// accountOIDCProviders refuses the removal when the oidc pool holds a
// provider of the account, listing the pool as get_oidc_providers does
// (rgw_sal_rados.cc:2206-2252 at v19.2.6). A zone without an oidc pool, or
// whose pool does not exist, holds none.
func (s *Store) accountOIDCProviders(ctx context.Context, id string) error {
	if s.zone.Params.OIDCPool.Name == "" {
		return nil
	}
	pool, err := s.pools.get(ctx, s.zone.Params.OIDCPool)
	if errors.Is(err, radosclient.ErrNotFound) {
		return nil
	}
	if err != nil {
		return op.FromRADOS(err, op.ScopeService)
	}
	prefix := id + oidcProviderPrefix
	err = pool.ListObjects(ctx, func(oid, _ string) error {
		if strings.HasPrefix(oid, prefix) {
			return errResourceFound
		}
		return nil
	})
	switch {
	case errors.Is(err, errResourceFound):
		return op.ErrBucketNotEmpty.WithMessage("The account cannot be deleted until all OpenIDConnectProviders are removed.")
	case errors.Is(err, radosclient.ErrNotFound):
		return nil
	case err != nil:
		return op.FromRADOS(err, op.ScopeService)
	}
	return nil
}

// accountUsersPool opens the pool of an account's users index and names it.
// rgwrados::users reads and writes the index straight through RADOS, past
// the metadata cache.
func (s *Store) accountUsersPool(ctx context.Context, accountID string) (radosclient.Pool, string, error) {
	o := s.accountUsersObj(accountID)
	p, err := s.pools.get(ctx, o.pool)
	return p, o.oid, err
}

// AddAccountUser implements op.AccountStore as users::add (users.cc:27-51)
// the way PutOperation calls it, not exclusive and with no limit
// (svc_user_rados.cc:365-366): the entry under the display name holds the
// path and the user's id.
func (s *Store) AddAccountUser(ctx context.Context, accountID string, info meta.UserInfo) error {
	pool, oid, err := s.accountUsersPool(ctx, accountID)
	if err != nil {
		return err
	}
	entry := user.AccountResource{
		Name: info.DisplayName, Path: info.Path,
		Metadata: encodeAt(user.ResourceMetadata{UserID: info.UserID.ID}, s.release),
	}
	wop := radosclient.NewWriteOp()
	user.AccountResourceAdd(wop, entry, false, math.MaxUint32, s.release)
	if _, err := pool.Write(ctx, oid, wop, radosclient.OpFlagNone); err != nil {
		return op.FromRADOS(err, op.ScopeService)
	}
	return nil
}

// RemoveAccountUser implements op.AccountStore as users::remove
// (users.cc:91-106): a display name the index lacks is the class's ENOENT,
// ErrNoSuchKey.
func (s *Store) RemoveAccountUser(ctx context.Context, accountID, displayName string) error {
	pool, oid, err := s.accountUsersPool(ctx, accountID)
	if err != nil {
		return err
	}
	wop := radosclient.NewWriteOp()
	user.AccountResourceRm(wop, displayName, s.release)
	if _, err := pool.Write(ctx, oid, wop, radosclient.OpFlagNone); err != nil {
		return op.FromRADOS(err, op.ScopeService)
	}
	return nil
}

// ListAccountUsers implements op.AccountStore as users::list
// (users.cc:108-158) with no path prefix: a missing index lists nothing,
// and next is the class's marker only when it truncated the page.
func (s *Store) ListAccountUsers(ctx context.Context, accountID, marker string, maxIDs uint32) (ids []string, next string, err error) {
	pool, oid, err := s.accountUsersPool(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	rop := radosclient.NewReadOp()
	res := user.AccountResourceList(rop, marker, "", maxIDs, s.release)
	if _, rerr := pool.Read(ctx, oid, rop, radosclient.OpFlagNone); rerr != nil {
		if errors.Is(rerr, radosclient.ErrNotFound) {
			return nil, "", nil
		}
		return nil, "", op.FromRADOS(rerr, op.ScopeService)
	}
	entries, truncated, page, err := res.Result()
	if err != nil {
		return nil, "", op.FromRADOS(err, op.ScopeService)
	}
	for _, e := range entries {
		d := denc.NewDecoder(e.Metadata)
		m := user.DecodeResourceMetadata(d)
		if err := d.Err(); err != nil {
			return nil, "", fmt.Errorf("%w: decoding an entry of %s: %w", op.ErrInternalError, oid, err)
		}
		ids = append(ids, m.UserID)
	}
	if !truncated {
		page = ""
	}
	return ids, page, nil
}
