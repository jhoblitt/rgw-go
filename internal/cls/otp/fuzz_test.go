package otp_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"unicode/utf8"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
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
		devAHex, devAStoredHex, devBHex,
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
