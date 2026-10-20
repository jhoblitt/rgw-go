package otp_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// stable decodes b with dec and, when that succeeds, checks that the value
// re-encodes to bytes that decode without error and re-encode the same, and,
// when exact, that they decode to the same value. A real_time whose
// nanoseconds carry its seconds past 32 bits is truncated on re-encoding as
// Ceph truncates it, so the types holding one compare by bytes only.
func stable[T interface {
	Encode(*denc.Encoder, denc.Release)
}](t *testing.T, name string, b []byte, dec func(*denc.Decoder) T, exact bool,
) {
	t.Helper()
	d := denc.NewDecoder(b)
	v := dec(d)
	if d.Err() != nil {
		return
	}
	e1 := denc.NewEncoder()
	v.Encode(e1, denc.Squid)
	d2 := denc.NewDecoder(e1.Bytes())
	v2 := dec(d2)
	if err := d2.Err(); err != nil {
		t.Fatalf("%s: re-encoding of %x does not decode: %v", name, b, err)
	}
	if d2.Remaining() != 0 {
		t.Fatalf("%s: re-encoding of %x leaves %d bytes unread", name, b, d2.Remaining())
	}
	e2 := denc.NewEncoder()
	v2.Encode(e2, denc.Squid)
	// The values print redacted, so a mismatch is shown by their encodings,
	// which differ in any field the encoder writes, the seed's included.
	if exact && !reflect.DeepEqual(v, v2) {
		t.Fatalf("%s: %x decodes to a value encoding as %x, which decodes to one encoding as %x", name, b, e1.Bytes(), e2.Bytes())
	}
	if !bytes.Equal(e1.Bytes(), e2.Bytes()) {
		t.Fatalf("%s: re-encoding of %x is not stable: %x then %x", name, b, e1.Bytes(), e2.Bytes())
	}
}

// FuzzReplyDecoders runs every decoder of the package over arbitrary bytes,
// the replies an OSD sends and the requests the class reads: none may panic,
// and whatever decodes must re-encode stably. go test runs the seeds; go test
// -fuzz=FuzzReplyDecoders explores from them.
func FuzzReplyDecoders(f *testing.F) {
	for _, s := range []string{
		getReplyHex, currentTimeReplyHex, setDevAHex, getAllHex, removeDev1Hex, getCurrentTimeHex,
		devAHex, devAStoredHex, devBHex, checkReqHex, replyHex("01"),
		"010110000000" + "03000000" + "746f6b" + stampHex + "02",
		"0101ffffffff", "01010400000000ffffff", "",
	} {
		b, err := hex.DecodeString(s)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		stable(t, "cls_otp_get_otp_reply", b, otp.DecodeGetReply, true)
		stable(t, "cls_otp_get_current_time_reply", b, otp.DecodeGetCurrentTimeReply, false)
		stable(t, "otp_info_t", b, otp.DecodeOTPInfo, true)
		stable(t, "otp_check_t", b, otp.DecodeOTPCheck, false)
		stable(t, "cls_otp_set_otp_op", b, otp.DecodeSetOp, true)
		stable(t, "cls_otp_get_otp_op", b, otp.DecodeGetOp, true)
		stable(t, "cls_otp_remove_otp_op", b, otp.DecodeRemoveOp, true)
		stable(t, "cls_otp_get_current_time_op", b, otp.DecodeGetCurrentTimeOp, true)
		stable(t, "cls_otp_check_otp_op", b, otp.DecodeCheckOp, true)
		stable(t, "cls_otp_get_result_reply", b, otp.DecodeGetResultReply, false)
	})
}

// refReply is cls_otp_get_result_reply as refDecode reads it.
type refReply struct {
	token     string
	sec, nsec uint32
	result    byte
}

// refDecode reads cls_otp_get_result_reply byte by byte, as DECODE_START,
// DECODE_FINISH and otp_check_t::decode read it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_ops.h#L68-L83 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.h#L86-L107 at v19.2.7 and v20.2.4),
// without denc, so that FuzzRecordFailClosed holds Record to a reading it
// shares no code with. It reports false wherever the C++ decode throws.
func refDecode(b []byte) (refReply, bool) {
	off := 0
	take := func(n int) ([]byte, bool) {
		if n < 0 || off+n > len(b) {
			return nil, false
		}
		s := b[off : off+n]
		off += n
		return s, true
	}
	// begin reads a struct header and returns where the struct ends.
	begin := func() (int, bool) {
		h, ok := take(6)
		if !ok || h[1] > 1 {
			return 0, false
		}
		n := int(binary.LittleEndian.Uint32(h[2:]))
		if n > len(b)-off {
			return 0, false
		}
		return off + n, true
	}
	var r refReply
	end, ok := begin()
	if !ok {
		return r, false
	}
	checkEnd, ok := begin()
	if !ok {
		return r, false
	}
	l, ok := take(4)
	if !ok {
		return r, false
	}
	tok, ok := take(int(binary.LittleEndian.Uint32(l)))
	if !ok {
		return r, false
	}
	ts, ok := take(8)
	if !ok {
		return r, false
	}
	res, ok := take(1)
	if !ok || off > checkEnd || checkEnd > end {
		return r, false
	}
	r.token = string(tok)
	r.sec = binary.LittleEndian.Uint32(ts[0:4])
	r.nsec = binary.LittleEndian.Uint32(ts[4:8])
	r.result = res[0]
	return r, true
}

// recordOf runs Record over a lookup whose method answered b and rval.
func recordOf(t *testing.T, b []byte, rval int32) (otp.OTPCheck, error) {
	t.Helper()
	rd := radosclient.NewReadOp()
	l := otp.LookupCheck(rd, checkReq(), denc.Squid)
	s, ok := rd.Steps()[0].(*radosclient.ExecStep)
	if !ok {
		t.Fatalf("lookup step is %T, not an exec", rd.Steps()[0])
	}
	s.Result.Set(b, rval)
	return l.Record()
}

// FuzzRecordFailClosed runs Record over arbitrary replies and return values
// against refDecode. Record must answer nil only where refDecode finds result
// byte 1, ErrCheckRefused with refDecode's check for any other result, and,
// with the zero check, an error that is neither for a reply refDecode
// refuses and for a negative return value, which keeps its errno. A positive
// return value is a success, as IoCtx::exec's r >= 0 is.
func FuzzRecordFailClosed(f *testing.F) {
	for _, s := range []string{replyHex("01"), replyHex("02"), replyHex("00"), replyHex("ff"), replyHex("01") + "aa", ""} {
		b, err := hex.DecodeString(s)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b, int32(0))
	}
	b, err := hex.DecodeString(replyHex("01"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b, int32(-2))
	f.Add(b, int32(5))
	f.Fuzz(func(t *testing.T, b []byte, rval int32) {
		got, err := recordOf(t, b, rval)
		failed := err != nil && !errors.Is(err, otp.ErrCheckRefused)
		if rval < 0 {
			var re *radosclient.Error
			if !failed || !reflect.DeepEqual(got, otp.OTPCheck{}) || !errors.As(err, &re) || re.Errno != -rval {
				t.Fatalf("rval %d: Record gave %v, %v, not the method's errno", rval, got, err)
			}
			return
		}
		ref, ok := refDecode(b)
		if !ok {
			if !failed || !reflect.DeepEqual(got, otp.OTPCheck{}) {
				t.Fatalf("%x: refDecode refuses it, Record gave %v, %v", b, got, err)
			}
			return
		}
		if failed {
			t.Fatalf("%x: refDecode reads it, Record failed: %v", b, err)
		}
		if got.Token != ref.token || byte(got.Result) != ref.result {
			t.Fatalf("%x: Record read token %q result %d, refDecode %q %d", b, got.Token, got.Result, ref.token, ref.result)
		}
		want := time.Time{}
		if ref.sec != 0 || ref.nsec != 0 {
			want = time.Unix(int64(ref.sec), int64(ref.nsec)).UTC()
		}
		if !got.Timestamp.Equal(want) {
			t.Fatalf("%x: Record read %v, refDecode %v", b, got.Timestamp, want)
		}
		if (ref.result == byte(otp.CheckSuccess)) != (err == nil) {
			t.Fatalf("%x: result %d answered %v", b, ref.result, err)
		}
	})
}

// FuzzUnmarshalJSON decodes arbitrary documents as a device. It must not
// panic, must fail only with meta.ErrBadJSONValue, and a device it accepts
// whose strings are UTF-8, which JSON alone carries unchanged, must survive
// a dump and a decode.
func FuzzUnmarshalJSON(f *testing.F) {
	for _, s := range []string{
		`{"type":2,"id":"dev2","seed":"GEZDGNBV","seed_type":"base32","time_ofs":0,"step_size":30,"window":2}`,
		`{"type":"2","time_ofs":" -5","step_size":"30 ","window":"\t2\n"}`,
		`{"id":12,"seed":true,"step_size":"30\u0000x"}`,
		`{"type":2147483648}`, `{"seed":{"k":"v"}}`, `[1,2]`, `{"seed":"\q"}`, ``,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var d otp.OTPInfo
		if err := d.UnmarshalJSON(b); err != nil {
			if !errors.Is(err, meta.ErrBadJSONValue) {
				t.Fatalf("%q: %v is not meta.ErrBadJSONValue", b, err)
			}
			return
		}
		if !utf8.ValidString(d.ID) || !utf8.ValidString(d.Seed) {
			return
		}
		out, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("%q: dumping %#v: %v", b, d, err)
		}
		var back otp.OTPInfo
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatalf("%q: decoding its dump %s: %v", b, out, err)
		}
		if !reflect.DeepEqual(d, back) {
			again, err := json.Marshal(back)
			t.Fatalf("%q: decodes to a device dumping as %s, which decodes to one dumping as %s (%v)", b, out, again, err)
		}
	})
}
