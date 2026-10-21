package op

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// ChecksumInput is what a write's checksum request is read from: the
// request's headers, or POST object's form fields, through Field; the
// trailers of its aws-chunked payload; and the body they follow.
type ChecksumInput struct {
	// Field returns the value of the field of a lower-case name, and false
	// for one the request does not carry; nil carries none.
	Field func(name string) (string, bool)
	// Trailers are the payload's trailers, AuthResult.Trailers; nil when
	// the payload keeps none.
	Trailers Trailers
	// Body is the body the checksum is computed over.
	Body io.Reader
}

func (in ChecksumInput) field(name string) (string, bool) {
	if in.Field == nil {
		return "", false
	}
	return in.Field(name)
}

// The checksum request fields, by the names radosgw reads them under
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L65-L136 at v20.2.4,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4507-L4521 at v20.2.4).
const (
	checksumAlgorithmField    = "x-amz-checksum-algorithm"
	sdkChecksumAlgorithmField = "x-amz-sdk-checksum-algorithm"
	checksumTypeField         = "x-amz-checksum-type"
	trailerField              = "x-amz-trailer"
	checksumValuePrefix       = "x-amz-checksum-"
)

// checksumValueNames are the algorithm names whose x-amz-checksum-<name>
// fields carry a value: every name cksum.ParseName knows, Tentacle's xxh3
// among them, and the three cksum.ForeignName knows, so that a value of
// any type a write refuses is found and refused.
var checksumValueNames = func() []string {
	var names []string
	for t := cksum.CRC32; t.Info().Name != ""; t++ {
		names = append(names, t.Info().Name)
	}
	return append(names, "xxh3", "md5", "xxhash64", "xxhash128")
}()

// checksumType is the type an algorithm name names for a write, and known
// false for a name that names none. A name of a type a v20.2.4 radosgw
// has no id for, main's MD5, XXHASH64 and XXHASH128, and one of a type
// rgw-go does not compute, XXHASH3 and BLAKE3, are ErrInvalidArgument, the
// EINVAL RGWPutObj_Cksum::Factory answers for a name it does not know
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L40-L73 at v20.2.4,
// caught at
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4601-L4611 at v20.2.4),
// so that no store is asked for a record that gateway cannot read or one
// rgw-go cannot compute.
func checksumType(name string) (t cksum.Type, known bool, err error) {
	if cksum.ForeignName(name) {
		return cksum.None, true, fmt.Errorf("%w: checksum algorithm %q has no Tentacle type", ErrInvalidArgument, name)
	}
	t, known = cksum.ParseName(name)
	if known && !t.Writable() {
		return t, true, fmt.Errorf("%w: rgw-go computes no %s checksum", ErrInvalidArgument, t.Info().Name)
	}
	return t, known, nil
}

// algorithmField is the algorithm field radosgw reads,
// x-amz-checksum-algorithm, else x-amz-sdk-checksum-algorithm, and its
// value
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L65-L82 at v20.2.4).
func algorithmField(in ChecksumInput) (string, bool) {
	for _, name := range [...]string{checksumAlgorithmField, sdkChecksumAlgorithmField} {
		if v, ok := in.field(name); ok {
			return v, true
		}
	}
	return "", false
}

// checksumValue is where a request supplies a checksum's value: a field
// x-amz-checksum-<alg>, whose value it holds, or a trailer x-amz-trailer
// names.
type checksumValue struct {
	name    string
	typ     cksum.Type
	value   string
	trailer bool
}

// valueFields are the x-amz-checksum-<alg> fields in carries.
func valueFields(in ChecksumInput) ([]checksumValue, error) {
	var vs []checksumValue
	for _, alg := range checksumValueNames {
		v, ok := in.field(checksumValuePrefix + alg)
		if !ok {
			continue
		}
		t, _, err := checksumType(alg)
		if err != nil {
			return nil, err
		}
		vs = append(vs, checksumValue{name: checksumValuePrefix + alg, typ: t, value: v})
	}
	return vs, nil
}

// trailerValues are the checksum trailers x-amz-trailer names: each name,
// split on "," as the aws-chunked reader splits it, that is
// x-amz-checksum- in any case followed by an algorithm's name, once
// however often it is listed. Any other name is no checksum's.
func trailerValues(in ChecksumInput) ([]checksumValue, error) {
	list, ok := in.field(trailerField)
	if !ok {
		return nil, nil
	}
	var vs []checksumValue
	for name := range strings.SplitSeq(list, ",") {
		if len(name) < len(checksumValuePrefix) || !foldEqual(name[:len(checksumValuePrefix)], checksumValuePrefix) {
			continue
		}
		t, known, err := checksumType(name[len(checksumValuePrefix):])
		if err != nil {
			return nil, err
		}
		if known && !listedTrailer(vs, name) {
			vs = append(vs, checksumValue{name: name, typ: t, trailer: true})
		}
	}
	return vs, nil
}

func listedTrailer(vs []checksumValue, name string) bool {
	for _, v := range vs {
		if foldEqual(v.name, name) {
			return true
		}
	}
	return false
}

// expected is v's ChecksumRequest.Expected, and the body to hand the store
// with it. A field's value is there from the start. A trailer's is there
// only once the body has returned io.EOF, so the body is wrapped to record
// that Read. Whenever the declared trailer has no value, asked for before
// that Read, never sent, or of a payload that keeps no trailers, Expected
// answers "", which no armored checksum equals, so the store refuses the
// write with BadDigest rather than store the checksum unverified.
func (v checksumValue) expected(in ChecksumInput) (func() (string, bool), io.Reader) {
	if !v.trailer {
		value := v.value
		return func() (string, bool) { return value, true }, in.Body
	}
	body := &endedBody{r: in.Body}
	trailers, name := in.Trailers, v.name
	return func() (string, bool) {
		if body.ended.Load() && trailers != nil {
			if value, ok := trailers.Trailer(name); ok {
				return value, true
			}
		}
		return "", true
	}, body
}

// endedBody records whether its body has returned io.EOF.
type endedBody struct {
	r     io.Reader
	ended atomic.Bool
}

func (e *endedBody) Read(p []byte) (int, error) {
	if e.r == nil {
		e.ended.Store(true)
		return 0, io.EOF
	}
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		e.ended.Store(true)
	}
	return n, err
}

// ChecksumForPut is the checksum request of a PutObject, or of POST
// object's form, and the body to hand the store with it: nil and in.Body
// when it names no algorithm. Its type is the first of:
//   - the algorithm field radosgw reads (algorithmField), which must name
//     a type rgw-go computes (checksumType);
//   - the type of the one x-amz-checksum-<alg> field, or of the one checksum
//     trailer x-amz-trailer names, where radosgw computes nothing
//     (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L40-L73 at v20.2.4)
//     and Amazon S3 verifies it.
//
// A value field or a trailer of a type rgw-go does not compute is
// ErrInvalidArgument, and two values, or a value of another type than the
// algorithm field's, ErrInvalidRequest, so that no value goes unverified.
// The value is the field's, or the trailer's once the body has ended, and a
// declared trailer that brings none is refused (checksumValue.expected). A
// request that names an algorithm and supplies no value has the checksum
// stored unverified, as radosgw stores it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4757-L4790 at v20.2.4).
// The release plays no part: a single object's checksum is an attr of its
// own, which a Squid cluster keeps.
func ChecksumForPut(in ChecksumInput, _ denc.Release) (*ChecksumRequest, io.Reader, error) {
	return checksumRequest(in, UploadChecksum{})
}

// ChecksumForPart is ChecksumForPut for an UploadPart of an upload of
// checksum type and flags up. On Tentacle a part naming no algorithm takes
// up's type, as radosgw's Factory takes the upload's
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4471-L4472 at v20.2.4),
// and every part takes up's flags. A part naming another type than up's is
// ErrInvalidRequest, which radosgw answers only at completion
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7121-L7131 at v20.2.4),
// and a part of an upload whose type rgw-go does not compute
// ErrNotImplemented, whatever the part names, since no completion of it
// would succeed. A Squid upload has neither type nor flags
// (UploadChecksum), so on Squid up plays no part.
func ChecksumForPart(in ChecksumInput, up UploadChecksum, rel denc.Release) (*ChecksumRequest, io.Reader, error) {
	if rel < denc.Tentacle {
		up = UploadChecksum{}
	}
	if err := computableUpload(up); err != nil {
		return nil, nil, err
	}
	return checksumRequest(in, up)
}

// checksumRequest is ChecksumForPart's request on Tentacle, and
// ChecksumForPut's for the zero up.
func checksumRequest(in ChecksumInput, up UploadChecksum) (*ChecksumRequest, io.Reader, error) {
	t := cksum.None
	if v, ok := algorithmField(in); ok {
		at, known, err := checksumType(v)
		if err != nil {
			return nil, nil, err
		}
		if !known {
			return nil, nil, fmt.Errorf("%w: checksum algorithm %q names no algorithm", ErrInvalidArgument, v)
		}
		t = at
	}
	values, err := valueFields(in)
	if err != nil {
		return nil, nil, err
	}
	trailers, err := trailerValues(in)
	if err != nil {
		return nil, nil, err
	}
	values = append(values, trailers...)
	var src *checksumValue
	switch len(values) {
	case 0:
	case 1:
		src = &values[0]
		if t == cksum.None {
			t = src.typ
		} else if src.typ != t {
			return nil, nil, fmt.Errorf("%w: %s names %s where the algorithm is %s", ErrInvalidRequest, src.name, src.typ.Info().Name, t.Info().Name)
		}
	default:
		return nil, nil, fmt.Errorf("%w: %d checksum values, %s and %s", ErrInvalidRequest, len(values), values[0].name, values[1].name)
	}
	switch {
	case up.Type == cksum.None:
	case t == cksum.None:
		t = up.Type
	case t != up.Type:
		return nil, nil, fmt.Errorf("%w: a %s part of a %s upload", ErrInvalidRequest, t.Info().Name, up.Type.Info().Name)
	}
	if t == cksum.None {
		return nil, in.Body, nil
	}
	req := &ChecksumRequest{Type: t, Flags: up.Flags}
	body := in.Body
	if src != nil {
		req.Expected, body = src.expected(in)
	}
	return req, body, nil
}

// computableUpload refuses with ErrNotImplemented an upload whose stored
// checksum type rgw-go does not compute: xxh3 or blake3, which a Tentacle
// radosgw may have stored, or a type a newer radosgw added.
func computableUpload(uc UploadChecksum) error {
	if uc.Type != cksum.None && !uc.Type.Writable() {
		return fmt.Errorf("%w: the upload's checksum type %d is not one rgw-go computes", ErrNotImplemented, uc.Type)
	}
	return nil
}

// ChecksumForCreate is the checksum type and flags a CreateMultipartUpload
// gives its upload, as RGWInitMultipart_ObjStore_S3::get_params reads them
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4507-L4521 at v20.2.4):
//   - the type is the algorithm field's (algorithmField), none for a name
//     that names no algorithm;
//   - x-amz-checksum-type full_object gives FULL_OBJECT and composite
//     COMPOSITE, in any case, and any other value no flag; without it the
//     flags are cksum.FlagsOf the type
//     (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L138-L154 at v20.2.4);
//   - a pair cksum.Permitted refuses is ErrInvalidRequest.
//
// An algorithm field, value field or checksum trailer of a type rgw-go does
// not compute is ErrInvalidArgument (checksumType), as on PutObject, where
// radosgw keeps a name it does not know as none, or a type it computes,
// and would answer a part of the upload or its completion with its own
// result. A Squid upload keeps neither type nor flags, and Squid's radosgw
// reads neither field, so on Squid nothing is read.
func ChecksumForCreate(in ChecksumInput, rel denc.Release) (UploadChecksum, error) {
	if rel < denc.Tentacle {
		return UploadChecksum{}, nil
	}
	t := cksum.None
	if v, ok := algorithmField(in); ok {
		at, known, err := checksumType(v)
		if err != nil {
			return UploadChecksum{}, err
		}
		if known {
			t = at
		}
	}
	if _, err := valueFields(in); err != nil {
		return UploadChecksum{}, err
	}
	if _, err := trailerValues(in); err != nil {
		return UploadChecksum{}, err
	}
	flags := cksum.FlagsOf(t)
	if v, ok := in.field(checksumTypeField); ok {
		switch {
		case foldEqual(v, "full_object"):
			flags = cksum.FlagFullObject
		case foldEqual(v, "composite"):
			flags = cksum.FlagComposite
		default:
			flags = 0
		}
	}
	if !cksum.Permitted(t, flags) {
		return UploadChecksum{}, fmt.Errorf("%w: checksum type %#x does not suit checksum algorithm %s", ErrInvalidRequest, flags, t.Info().Name)
	}
	return UploadChecksum{Type: t, Flags: flags}, nil
}

// SuppliedForComplete is the combined checksum a CompleteMultipartUpload
// supplies, Upload.SuppliedChecksum: the value of its one
// x-amz-checksum-<alg> field, of every type, where radosgw's scan stops
// short of crc64nvme
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L120-L136 at v20.2.4);
// nil for none. Its Type is the field's, and None for a name with no
// Tentacle type, which no combination's equals, so the completion's
// comparison refuses a value of any other type than the upload's with
// BadDigest. Two fields are ErrInvalidRequest, where radosgw compares the
// first it finds.
func SuppliedForComplete(in ChecksumInput) (*ChecksumRequest, error) {
	var found []checksumValue
	for _, alg := range checksumValueNames {
		if v, ok := in.field(checksumValuePrefix + alg); ok {
			t, _ := cksum.ParseName(alg)
			found = append(found, checksumValue{name: checksumValuePrefix + alg, typ: t, value: v})
		}
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		req := &ChecksumRequest{Type: found[0].typ}
		req.Expected, _ = found[0].expected(in)
		return req, nil
	}
	return nil, fmt.Errorf("%w: %d checksum values, %s and %s", ErrInvalidRequest, len(found), found[0].name, found[1].name)
}

// partChecksum is the checksum request of an UploadPart of up, read once
// the upload's info is (readUpload), as RGWPutObj::execute reads get_info's
// multipart_cksum_type before its Factory runs
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4453-L4472 at v20.2.4):
// an unknown upload is ErrNoSuchUpload before any checksum field is read.
func partChecksum(ctx context.Context, r *Request, up *Upload, in ChecksumInput) (*ChecksumRequest, io.Reader, error) {
	if err := readUpload(ctx, r, up); err != nil {
		return nil, nil, err
	}
	return ChecksumForPart(in, up.Checksum, r.Env.Zone.Release())
}

// readUpload is get_info's read of up's upload for a part, which gives up
// the upload's destination placement, which MultipartStore.PutPart writes
// the part to, and its checksum type and flags, for the part's checksum
// request or for MultipartStore.CopyPart to compute.
func readUpload(ctx context.Context, r *Request, up *Upload) error {
	got, err := r.Env.Multipart.GetUpload(ctx, r.BucketRec, r.Object, up.ID)
	if err != nil {
		return err
	}
	if got != nil {
		up.Placement, up.Checksum = got.Placement, got.Checksum
	}
	return nil
}

// completeChecksum is the supplied checksum of a CompleteMultipartUpload of
// upload id on Tentacle, once an upload whose type rgw-go does not compute
// is refused with ErrNotImplemented (computableUpload). It is nil, with no
// field read, for an upload without a checksum type, every Squid upload's
// among them, which combines nothing and so compares nothing, as radosgw
// compares a supplied value only with a combination
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7295-L7341 at v20.2.4).
// An upload GetUpload does not find is left to MultipartStore.Complete,
// which answers a completion retried after it succeeded.
func completeChecksum(ctx context.Context, r *Request, id string, in ChecksumInput) (*ChecksumRequest, error) {
	if r.Env.Zone.Release() < denc.Tentacle {
		return nil, nil
	}
	up, err := r.Env.Multipart.GetUpload(ctx, r.BucketRec, r.Object, id)
	switch {
	case errors.Is(err, ErrNoSuchUpload):
		return nil, nil
	case err != nil:
		return nil, err
	case up == nil || up.Checksum.Type == cksum.None:
		return nil, nil
	}
	if err := computableUpload(up.Checksum); err != nil {
		return nil, err
	}
	return SuppliedForComplete(in)
}
