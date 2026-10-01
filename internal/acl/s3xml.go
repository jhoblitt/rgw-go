package acl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// The S3 ACL forms are rgw_acl_s3.cc's, identical at v19.2.6 and v20.2.4.

// The group URIs of rgw_acl_s3.cc:20-21.
const (
	URIAllUsers           = "http://acs.amazonaws.com/groups/global/AllUsers"
	URIAuthenticatedUsers = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers"
)

// GroupForURI is acl_uri_to_group (rgw_acl_s3.cc:585-593): the group uri
// names exactly, GroupNone for any other.
func GroupForURI(uri string) uint32 {
	switch uri {
	case URIAllUsers:
		return GroupAllUsers
	case URIAuthenticatedUsers:
		return GroupAuthenticatedUsers
	}
	return GroupNone
}

// URIForGroup is acl_group_to_uri (rgw_acl_s3.cc:595-607): group's uri, and
// false for a group without one.
func URIForGroup(group uint32) (string, bool) {
	switch group {
	case GroupAllUsers:
		return URIAllUsers, true
	case GroupAuthenticatedUsers:
		return URIAuthenticatedUsers, true
	}
	return "", false
}

// Resolver looks grantees up; authz.UserResolver implements it over
// op.UserStore. A method reports a miss, radosgw's -ENOENT, with the sentinel
// it names, wrapped or not. Any other error is a failed lookup, which
// FromHeaders returns unchanged and ParseS3XML reports as the 400 it gives a
// miss.
type Resolver interface {
	// OwnerByEmail is read_aclowner_by_email (rgw_acl_s3.cc:325-337),
	// load_owner_by_email and then read_owner_display_name: the owner with
	// the address, and its display name. ErrUnresolvableEmail when no user
	// has the address; ErrNoSuchOwner when the owner the address names
	// cannot be read (:335-336), which is also a miss.
	OwnerByEmail(ctx context.Context, email string) (Owner, error)
	// DisplayName is read_owner_display_name (rgw_acl_s3.cc:300-323): a
	// user's display name or an account's name. ErrNoSuchOwner when it does
	// not exist.
	DisplayName(ctx context.Context, owner meta.Owner) (string, error)
}

var (
	// ErrInvalid is radosgw's -EINVAL.
	ErrInvalid = errors.New("acl: invalid")
	// ErrUnresolvableEmail is radosgw's -ERR_UNRESOLVABLE_EMAIL.
	ErrUnresolvableEmail = errors.New("acl: unresolvable email")
	// ErrNoSuchOwner is a Resolver's miss for an owner, the -ENOENT of
	// radosgw's user and account lookups.
	ErrNoSuchOwner = errors.New("acl: no such owner")
	// ErrGranteeNotFound is the -ENOENT a grant header naming no user or
	// account fails with.
	ErrGranteeNotFound = errors.New("acl: grantee not found")
)

// ParseError is a ParseS3XML failure: Err, ErrInvalid or
// ErrUnresolvableEmail, with the message radosgw sets in s->err.message,
// empty where it sets none.
type ParseError struct {
	Message string
	Err     error
}

// Error returns Err's text, followed by Message when there is one.
func (e *ParseError) Error() string {
	if e.Message == "" {
		return e.Err.Error()
	}
	return e.Err.Error() + ": " + e.Message
}

// Unwrap returns Err.
func (e *ParseError) Unwrap() error { return e.Err }

// The messages parse_policy and resolve_grant set in s->err.message
// (rgw_acl_s3.cc:492-670).
const (
	msgMissingPolicy      = "Missing element AccessControlPolicy"
	msgInvalidOwner       = "Invalid Owner ID"
	msgUnresolvableEmail  = "The e-mail address you provided does not match any account on record."
	msgInvalidCanonicalID = "Invalid CanonicalUser id"
	msgInvalidGroupURI    = "Invalid group uri"
	msgInvalidGranteeType = "Invalid Grantee type"
)

// ParseS3XML is rgw::s3::parse_policy (rgw_acl_s3.cc:609-670): it parses doc
// as RGWACLXMLParser_S3 does, resolves the owner, which must exist, and adds
// the grants of the AccessControlList in document order, each resolved
// through r as resolve_grant does (:492-540). The document's owner display
// name replaces the resolved one when it is not empty; a grantee's never
// does. Every failure is a *ParseError, and a failed lookup of any kind is a
// missing grantee, as radosgw reports it.
func ParseS3XML(ctx context.Context, r Resolver, doc []byte) (Policy, error) {
	root, err := xmltext.ParseFunc(doc, xmlEnd)
	if err != nil {
		return Policy{}, &ParseError{Err: ErrInvalid}
	}
	if root.Name != "AccessControlPolicy" {
		return Policy{}, &ParseError{Message: msgMissingPolicy, Err: ErrInvalid}
	}
	// xmlEnd has accepted every element, the root's Owner and
	// AccessControlList included (:463-473), so radosgw never sends
	// parse_policy's "Missing element Owner" or "Missing element
	// AccessControlList" (:630-633, :651-654).
	xmlOwner, _ := ownerOf(root.FindFirst("Owner"))
	id := meta.ParseOwner(xmlOwner.ID)
	name, err := r.DisplayName(ctx, id)
	if err != nil {
		slog.DebugContext(ctx, "acl owner lookup failed", slog.String("owner", id.String()), slog.Any("error", err))
		return Policy{}, &ParseError{Message: msgInvalidOwner, Err: ErrInvalid}
	}
	if xmlOwner.DisplayName != "" {
		name = xmlOwner.DisplayName
	}
	p := Policy{Owner: Owner{ID: id.String(), DisplayName: name}}
	for e := range root.FindFirst("AccessControlList").Find("Grant") {
		x, _ := grantOf(e)
		g, err := resolveGrant(ctx, r, x)
		if err != nil {
			return Policy{}, err
		}
		p.ACL.AddGrant(g)
	}
	return p, nil
}

// resolveGrant is resolve_grant (rgw_acl_s3.cc:492-540).
func resolveGrant(ctx context.Context, r Resolver, x xmlGrant) (Grant, error) {
	switch x.typ {
	case GranteeEmail:
		if x.value == "" {
			return Grant{}, &ParseError{Err: ErrInvalid}
		}
		o, err := r.OwnerByEmail(ctx, x.value)
		if err != nil {
			slog.DebugContext(ctx, "acl grantee email lookup failed", slog.Any("error", err))
			return Grant{}, &ParseError{Message: msgUnresolvableEmail, Err: ErrUnresolvableEmail}
		}
		return canonGrant(o.ID, o.DisplayName, x.perm), nil
	case GranteeCanonUser:
		id := meta.ParseOwner(x.value)
		name, err := r.DisplayName(ctx, id)
		if err != nil {
			slog.DebugContext(ctx, "acl grantee lookup failed", slog.String("grantee", id.String()), slog.Any("error", err))
			return Grant{}, &ParseError{Message: msgInvalidCanonicalID, Err: ErrInvalid}
		}
		return canonGrant(id.String(), name, x.perm), nil
	case GranteeGroup:
		if g := GroupForURI(x.value); g != GroupNone {
			return groupGrant(g, x.perm), nil
		}
		return Grant{}, &ParseError{Message: msgInvalidGroupURI, Err: ErrInvalid}
	}
	return Grant{}, &ParseError{Message: msgInvalidGranteeType, Err: ErrInvalid}
}

// canonGrant is ACLGrant::set_canon.
func canonGrant(id, name string, perm Permission) Grant {
	return Grant{Type: GranteeCanonUser, ID: id, Name: name, Permission: perm}
}

// groupGrant is ACLGrant::set_group.
func groupGrant(group uint32, perm Permission) Grant {
	return Grant{Type: GranteeGroup, Group: group, Permission: perm}
}

// xmlGrant is what ACLGrant_S3::xml_end (rgw_acl_s3.cc:197-244) reads from a
// Grant: the grantee's type, the text of the grantee element that type
// names, and the flags of the grant's first Permission.
type xmlGrant struct {
	typ   GranteeType
	value string
	perm  Permission
}

// errRefused is an xml_end returning false, which fails the parse.
var errRefused = errors.New("acl: element refused")

// xmlEnd is the xml_end of the class RGWACLXMLParser_S3::alloc_obj gives an
// element named e.Name (rgw_acl_s3.cc:555-581), whatever its parent; any
// other element has XMLObj's, which accepts it.
func xmlEnd(e *xmltext.Element) error {
	ok := true
	switch e.Name {
	case "AccessControlPolicy":
		ok = e.FindFirst("AccessControlList") != nil && e.FindFirst("Owner") != nil
	case "Owner":
		_, ok = ownerOf(e)
	case "Grant":
		_, ok = grantOf(e)
	case "Permission":
		_, ok = permissionFlags(e.Text)
	}
	if !ok {
		return errRefused
	}
	return nil
}

// ownerOf is ACLOwner_S3::xml_end (rgw_acl_s3.cc:154-170): the ID is
// required and the DisplayName optional.
func ownerOf(e *xmltext.Element) (Owner, bool) {
	id := e.FindFirst("ID")
	if id == nil {
		return Owner{}, false
	}
	o := Owner{ID: id.Text}
	if name := e.FindFirst("DisplayName"); name != nil {
		o.DisplayName = name.Text
	}
	return o, true
}

// granteeElements names the grantee element ACLGrant_S3::xml_end reads for
// each grantee type it accepts.
var granteeElements = map[GranteeType]string{
	GranteeCanonUser: "ID",
	GranteeGroup:     "URI",
	GranteeEmail:     "EmailAddress",
}

// grantOf is ACLGrant_S3::xml_end (rgw_acl_s3.cc:197-244). The grantee's
// type is the attribute written exactly xsi:type, whatever the prefix is
// bound to, if anything. The first Permission's own xml_end, which ran
// first, has accepted its text.
func grantOf(e *xmltext.Element) (xmlGrant, bool) {
	grantee := e.FindFirst("Grantee")
	if grantee == nil {
		return xmlGrant{}, false
	}
	typ, ok := grantee.Attrs["xsi:type"]
	if !ok {
		return xmlGrant{}, false
	}
	perm := e.FindFirst("Permission")
	if perm == nil {
		return xmlGrant{}, false
	}
	flags, _ := permissionFlags(perm.Text)
	g := xmlGrant{typ: s3GranteeType(typ), perm: flags}
	name, ok := granteeElements[g.typ]
	if !ok {
		return xmlGrant{}, false
	}
	v := grantee.FindFirst(name)
	if v == nil {
		return xmlGrant{}, false
	}
	g.value = v.Text
	return g, true
}

// permissionFullControl is the S3 name of the four flags together.
const permissionFullControl = "FULL_CONTROL"

// s3Permissions are the S3 permission names other than FULL_CONTROL, in the
// order to_xml writes them (rgw_acl_s3.cc:36-51).
var s3Permissions = []struct {
	name  string
	flags Permission
}{
	{"READ", PermRead},
	{"WRITE", PermWrite},
	{"READ_ACP", PermReadACP},
	{"WRITE_ACP", PermWriteACP},
}

// permissionFlags is ACLPermission_S3::xml_end (rgw_acl_s3.cc:53-73): the
// flags a permission name gives, compared with strcasecmp and untrimmed.
func permissionFlags(s string) (Permission, bool) {
	if equalFoldASCII(s, permissionFullControl) {
		return PermFullControl, true
	}
	for _, p := range s3Permissions {
		if equalFoldASCII(s, p.name) {
			return p.flags, true
		}
	}
	return PermNone, false
}

// s3GranteeTypeName is ACLGranteeType_S3::to_string (rgw_acl_s3.cc:78-89).
func s3GranteeTypeName(t GranteeType) string {
	switch t {
	case GranteeCanonUser:
		return "CanonicalUser"
	case GranteeEmail:
		return "AmazonCustomerByEmail"
	case GranteeGroup:
		return "Group"
	}
	return "unknown"
}

// s3GranteeType is ACLGranteeType_S3::set (rgw_acl_s3.cc:91-104): the names
// compare exactly.
func s3GranteeType(name string) GranteeType {
	for _, t := range []GranteeType{GranteeCanonUser, GranteeEmail, GranteeGroup} {
		if s3GranteeTypeName(t) == name {
			return t
		}
	}
	return GranteeUnknown
}

// s3XMLNS is XMLNS_AWS_S3 (driver/rados/rgw_user.h:27 at v19.2.6, :26 at
// v20.2.4).
const s3XMLNS = "http://s3.amazonaws.com/doc/2006-03-01/"

// MarshalS3XML is rgw::s3::write_policy_xml (rgw_acl_s3.cc:672-676) over the
// to_xml functions (:36-51, :172-181, :246-293, :475-481): the
// AccessControlPolicy document with no XML declaration and no whitespace. The
// Owner is written only for a non-empty id, and a grant only when it holds an
// S3 flag. radosgw writes ids, display names and email addresses raw
// (:177-179, :260-265); they are escaped here as its XML formatter escapes the
// text of its other documents.
func (p Policy) MarshalS3XML() []byte {
	var b strings.Builder
	b.WriteString(`<AccessControlPolicy xmlns="` + s3XMLNS + `">`)
	if id := meta.ParseOwner(p.Owner.ID).String(); id != "" {
		b.WriteString("<Owner>")
		writeText(&b, "ID", id)
		if p.Owner.DisplayName != "" {
			writeText(&b, "DisplayName", p.Owner.DisplayName)
		}
		b.WriteString("</Owner>")
	}
	b.WriteString("<AccessControlList>")
	for _, e := range p.ACL.sortedGrants() {
		writeGrant(&b, e.Grant)
	}
	b.WriteString("</AccessControlList></AccessControlPolicy>")
	return []byte(b.String())
}

// writeGrant is to_xml(const ACLGrant&) (rgw_acl_s3.cc:246-274) with
// to_xml(ACLPermission) (:36-51). A referer, and any type S3 has no name
// for, is an unknown grantee with no element.
func writeGrant(b *strings.Builder, g Grant) {
	perm := g.Permission
	if perm&PermFullControl == 0 {
		return
	}
	k := g.kind()
	b.WriteString(`<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="` +
		s3GranteeTypeName(k) + `">`)
	switch k {
	case GranteeCanonUser:
		writeText(b, "ID", meta.ParseOwner(g.ID).String())
		if g.Name != "" {
			writeText(b, "DisplayName", g.Name)
		}
	case GranteeEmail:
		writeText(b, "EmailAddress", g.Email)
	case GranteeGroup:
		uri, _ := URIForGroup(g.Group)
		writeText(b, "URI", uri)
	}
	b.WriteString("</Grantee>")
	if perm&PermFullControl == PermFullControl {
		writeText(b, "Permission", permissionFullControl)
	} else {
		for _, sp := range s3Permissions {
			if perm&sp.flags != 0 {
				writeText(b, "Permission", sp.name)
			}
		}
	}
	b.WriteString("</Grant>")
}

// writeText writes the element name holding text, escaped.
func writeText(b *strings.Builder, name, text string) {
	b.WriteString("<" + name + ">" + xmltext.Escape(text) + "</" + name + ">")
}

// anonymousUser is RGW_USER_ANON_ID, the anonymous user's id.
const anonymousUser = "anonymous"

// Canned is rgw::s3::create_canned_acl (rgw_acl_s3.cc:678-689) over
// create_canned (:407-461): FULL_CONTROL to owner, then the grants the canned
// ACL names. The policy belongs to owner, or to bucketOwner when owner is the
// anonymous user, who still gets the FULL_CONTROL grant. The names compare
// exactly; ErrInvalid for one radosgw does not know.
func Canned(owner, bucketOwner Owner, canned string) (Policy, error) {
	p := Policy{Owner: owner}
	if sameOwner(owner.ID, anonymousUser) {
		p.Owner = bucketOwner
	}
	p.ACL.AddGrant(canonGrant(owner.ID, owner.DisplayName, PermFullControl))
	switch canned {
	case "", "private":
	case "public-read":
		p.ACL.AddGrant(groupGrant(GroupAllUsers, PermRead))
	case "public-read-write":
		p.ACL.AddGrant(groupGrant(GroupAllUsers, PermRead))
		p.ACL.AddGrant(groupGrant(GroupAllUsers, PermWrite))
	case "authenticated-read":
		p.ACL.AddGrant(groupGrant(GroupAuthenticatedUsers, PermRead))
	case "bucket-owner-read":
		if !sameOwner(bucketOwner.ID, owner.ID) {
			p.ACL.AddGrant(canonGrant(bucketOwner.ID, bucketOwner.DisplayName, PermRead))
		}
	case "bucket-owner-full-control":
		if !sameOwner(bucketOwner.ID, owner.ID) {
			p.ACL.AddGrant(canonGrant(bucketOwner.ID, bucketOwner.DisplayName, PermFullControl))
		}
	default:
		return Policy{}, fmt.Errorf("canned acl %q: %w", canned, ErrInvalid)
	}
	return p, nil
}

// sameOwner is rgw_owner's equality for two ids in their to_string form: the
// same alternative holding the same user or account.
func sameOwner(a, b string) bool {
	oa, ob := meta.ParseOwner(a), meta.ParseOwner(b)
	if oa.User == nil || ob.User == nil {
		return oa.User == nil && ob.User == nil && oa.Account == ob.Account
	}
	return *oa.User == *ob.User
}

// GrantHeaders are the x-amz-grant-* header values as
// create_policy_from_headers reads them (rgw_acl_s3.cc:483-490, :691-709):
// each is a comma-separated list of id="...", emailAddress="..." or
// uri="..." items (:339-405). An empty value stands for an absent header.
type GrantHeaders struct {
	Read, Write, ReadACP, WriteACP, FullControl string
}

// FromHeaders is rgw::s3::create_policy_from_headers (rgw_acl_s3.cc:691-709):
// owner's policy with the grants of the headers in the order READ, WRITE,
// READ_ACP, WRITE_ACP, FULL_CONTROL, each header's in its order. A lookup
// miss is ErrGranteeNotFound, the -ENOENT radosgw's lookups return, and any
// other lookup error is returned unchanged, as parse_grantee_str returns it
// (:356-371). An empty item is skipped, as ceph::split skips it, so an empty
// header gives no grants.
func FromHeaders(ctx context.Context, r Resolver, owner Owner, h GrantHeaders) (Policy, error) {
	p := Policy{Owner: owner}
	for _, hdr := range []struct {
		value string
		perm  Permission
	}{
		{h.Read, PermRead},
		{h.Write, PermWrite},
		{h.ReadACP, PermReadACP},
		{h.WriteACP, PermWriteACP},
		{h.FullControl, PermFullControl},
	} {
		for item := range strings.SplitSeq(hdr.value, ",") {
			if item == "" {
				continue
			}
			g, err := parseGrantee(ctx, r, item, hdr.perm)
			if err != nil {
				return Policy{}, err
			}
			p.ACL.AddGrant(g)
		}
	}
	return p, nil
}

// parseGrantee is parse_grantee_str (rgw_acl_s3.cc:339-383). The item splits
// at its first "=" with both sides trimmed, as parse_key_value does
// (rgw_common.cc:661-679 at v19.2.6, :674-692 at v20.2.4), the value then
// losing a quote at each end only when it has both (rgw_trim_quotes,
// :1854-1876, :1917-1939); keys compare case-insensitively.
func parseGrantee(ctx context.Context, r Resolver, item string, perm Permission) (Grant, error) {
	key, value, ok := strings.Cut(item, "=")
	if !ok {
		return Grant{}, fmt.Errorf("grant %q: %w", item, ErrInvalid)
	}
	key, value = trimSpace(key), trimQuotes(value)
	switch {
	case equalFoldASCII(key, "emailAddress"):
		o, err := r.OwnerByEmail(ctx, value)
		if err != nil {
			return Grant{}, lookupError(item, err)
		}
		return canonGrant(o.ID, o.DisplayName, perm), nil
	case equalFoldASCII(key, "id"):
		id := meta.ParseOwner(value)
		name, err := r.DisplayName(ctx, id)
		if err != nil {
			return Grant{}, lookupError(item, err)
		}
		return canonGrant(id.String(), name, perm), nil
	case equalFoldASCII(key, "uri"):
		if g := GroupForURI(value); g != GroupNone {
			return groupGrant(g, perm), nil
		}
	}
	return Grant{}, fmt.Errorf("grant %q: %w", item, ErrInvalid)
}

// lookupError is ErrGranteeNotFound for a Resolver's miss, and err itself
// for any other failure.
func lookupError(item string, err error) error {
	if errors.Is(err, ErrUnresolvableEmail) || errors.Is(err, ErrNoSuchOwner) {
		return fmt.Errorf("grant %q: %w", item, ErrGranteeNotFound)
	}
	return err
}

// cSpace is the set C's isspace accepts in the C locale.
const cSpace = " \t\n\v\f\r"

// trimSpace is rgw_trim_whitespace (rgw_common.cc:1816-1839 at v19.2.6,
// :1879-1902 at v20.2.4).
func trimSpace(s string) string { return strings.Trim(s, cSpace) }

// trimQuotes is rgw_trim_quotes: s trimmed, then without its enclosing
// quotes when it has one at each end.
func trimQuotes(s string) string {
	s = trimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// equalFoldASCII is strcasecmp(a, b) == 0 in the C locale, where only ASCII
// letters fold.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
