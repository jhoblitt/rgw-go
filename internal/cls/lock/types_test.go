package lock_test

import (
	"encoding/binary"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// encodable is every request and reply type of the package.
type encodable interface {
	Encode(e *denc.Encoder, r denc.Release)
}

func encode(v encodable) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}

// decodeInfo decodes b whole as a cls_lock_get_info_reply.
func decodeInfo(b []byte) (lock.Info, error) {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	info := lock.DecodeInfo(d)
	if d.Err() == nil {
		Expect(d.Remaining()).To(BeZero(), "the reply decoded with bytes left over")
	}
	return info, d.Err()
}

// replyWith encodes a get_info reply holding client.7/"" with addr, the bytes
// written where locker_info_t's entity_addr_t goes.
func replyWith(addr []byte) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	e.U32(1)
	lf := e.BeginStruct(1, 1)
	lock.EntityName{Type: lock.EntityTypeClient, Num: 7}.Encode(e, denc.Squid)
	e.String("")
	e.EndStruct(lf)
	inf := e.BeginStruct(1, 1)
	e.U32(1700000000)
	e.U32(500000000)
	e.Raw(addr)
	e.String("desc")
	e.EndStruct(inf)
	e.U8(1)
	e.String("")
	e.EndStruct(f)
	return e.Bytes()
}

// addr2 is entity_addr_t as encoded with CEPH_FEATURE_MSG_ADDR2 and
// SERVER_NAUTILUS: the marker 1, then ENCODE_START(1, 1) around the type, the
// nonce, the sockaddr length and the sockaddr with a little-endian family.
func addr2(typ, nonce uint32, family uint16, data []byte) []byte {
	e := denc.NewEncoder()
	e.U8(1)
	f := e.BeginStruct(1, 1)
	e.U32(typ)
	e.U32(nonce)
	e.U32(uint32(2 + len(data))) //nolint:gosec // a sockaddr is at most 28 bytes
	e.U16(family)
	e.Raw(data)
	e.EndStruct(f)
	return e.Bytes()
}

// legacyAddr is entity_addr_t as encoded without CEPH_FEATURE_MSG_ADDR2: a
// u32 0, whose first byte is the marker 0, the nonce, then a 128-byte
// sockaddr_storage whose family is big-endian.
func legacyAddr(nonce uint32, family uint16, data []byte) []byte {
	b := make([]byte, 0, 136)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, nonce)
	ss := make([]byte, 128)
	binary.BigEndian.PutUint16(ss, family)
	copy(ss[2:], data)
	return append(b, ss...)
}

var _ = Describe("cls_lock wire types", func() {
	It("encodes cls_lock_unlock_op as ENCODE_START(1, 1) with two strings", func() {
		Expect(encode(lock.UnlockOp{Name: "n"})).To(Equal([]byte{
			1, 1, 9, 0, 0, 0, // version 1, compat 1, body length 9
			1, 0, 0, 0, 'n', // name
			0, 0, 0, 0, // cookie ""
		}))
	})

	It("encodes cls_lock_lock_op in cls_lock_ops.h's field order with a utime_t duration", func() {
		b := encode(lock.LockOp{Name: "RGWCompleteMultipart", Type: lock.TypeExclusive, Duration: 600 * time.Second})
		Expect(b[:6]).To(Equal([]byte{1, 1, 46, 0, 0, 0}), "4+20 name, 1 type, 4+4+4 empty strings, 8 utime, 1 flags")
		Expect(b[6:30]).To(Equal(append([]byte{20, 0, 0, 0}, []byte("RGWCompleteMultipart")...)))
		Expect(b[30]).To(Equal(byte(1)), "EXCLUSIVE")
		Expect(b[31:43]).To(Equal(make([]byte, 12)), "cookie, tag, description empty")
		Expect(b[43:51]).To(Equal([]byte{0x58, 0x02, 0, 0, 0, 0, 0, 0}), "600 s, 0 ns")
		Expect(b[51]).To(Equal(byte(0)), "flags")
		Expect(b).To(HaveLen(52))
	})

	It("encodes a sub-second duration as utime_t's seconds and nanoseconds", func() {
		b := encode(lock.LockOp{Name: "n", Type: lock.TypeShared, Duration: 1500 * time.Millisecond, Flags: lock.FlagMustRenew})
		Expect(b[len(b)-9:len(b)-1]).To(Equal([]byte{1, 0, 0, 0, 0x00, 0x65, 0xcd, 0x1d}), "1 s, 500000000 ns")
		Expect(b[len(b)-1]).To(Equal(byte(2)), "LOCK_FLAG_MUST_RENEW")
	})

	It("round-trips every op through its decoder", func() {
		ops := []struct {
			enc []byte
			dec func(*denc.Decoder) encodable
		}{
			{encode(lock.LockOp{Name: "a", Type: lock.TypeShared, Cookie: "c", Tag: "t", Description: "d", Duration: 90 * time.Second, Flags: lock.FlagMayRenew}), func(d *denc.Decoder) encodable { return lock.DecodeLockOp(d) }},
			{encode(lock.UnlockOp{Name: "a", Cookie: "c"}), func(d *denc.Decoder) encodable { return lock.DecodeUnlockOp(d) }},
			{encode(lock.BreakOp{Name: "a", Locker: lock.EntityName{Type: lock.EntityTypeClient, Num: 4123}, Cookie: "c"}), func(d *denc.Decoder) encodable { return lock.DecodeBreakOp(d) }},
			{encode(lock.AssertOp{Name: "a", Type: lock.TypeExclusive, Cookie: "c", Tag: "t"}), func(d *denc.Decoder) encodable { return lock.DecodeAssertOp(d) }},
			{encode(lock.GetInfoOp{Name: "a"}), func(d *denc.Decoder) encodable { return lock.DecodeGetInfoOp(d) }},
		}
		for i, o := range ops {
			d := denc.NewDecoder(o.enc)
			v := o.dec(d)
			Expect(d.Err()).NotTo(HaveOccurred(), "op %d", i)
			Expect(d.Remaining()).To(BeZero(), "op %d", i)
			Expect(encode(v)).To(Equal(o.enc), "op %d", i)
		}
	})

	It("encodes cls_lock_break_op as name, locker, cookie", func() {
		b := encode(lock.BreakOp{Name: "n", Locker: lock.EntityName{Type: lock.EntityTypeClient, Num: 1}, Cookie: "c"})
		Expect(b[6:]).To(Equal([]byte{
			1, 0, 0, 0, 'n',
			8, 1, 0, 0, 0, 0, 0, 0, 0,
			1, 0, 0, 0, 'c',
		}))
	})

	It("encodes entity_name_t as u8 type then i64 num and prints it as client.<num>", func() {
		n := lock.EntityName{Type: lock.EntityTypeClient, Num: 4123}
		Expect(encode(n)).To(Equal([]byte{8, 0x1b, 0x10, 0, 0, 0, 0, 0, 0}))
		Expect(n.String()).To(Equal("client.4123"))
		p, ok := lock.ParseEntityName(n.String())
		Expect([]any{p, ok}).To(Equal([]any{n, true}))
	})

	DescribeTable("prints entity_name_t as operator<< does",
		func(n lock.EntityName, want string) { Expect(n.String()).To(Equal(want)) },
		Entry("an osd", lock.EntityName{Type: 0x04, Num: 3}, "osd.3"),
		Entry("auth, which ceph_entity_type_name names", lock.EntityName{Type: 0x20, Num: 1}, "auth.1"),
		Entry("a type ceph_entity_type_name does not name", lock.EntityName{Type: 0x40, Num: 1}, "unknown.1"),
		Entry("a negative num, which prints as new", lock.EntityName{Type: lock.EntityTypeClient, Num: -1}, "client.?"),
	)

	DescribeTable("parses entity names as entity_name_t::parse does",
		func(s string, want lock.EntityName, ok bool) {
			got, gotOK := lock.ParseEntityName(s)
			Expect([]any{got, gotOK}).To(Equal([]any{want, ok}), "%q", s)
		},
		Entry("a mon", "mon.2", lock.EntityName{Type: 0x01, Num: 2}, true),
		Entry("an mds", "mds.0", lock.EntityName{Type: 0x02, Num: 0}, true),
		Entry("a mgr", "mgr.9", lock.EntityName{Type: 0x10, Num: 9}, true),
		Entry("a signed num, which strtoll takes", "client.-5", lock.EntityName{Type: lock.EntityTypeClient, Num: -5}, true),
		Entry("auth, which parse does not know", "auth.1", lock.EntityName{}, false),
		Entry("no type", "4123", lock.EntityName{}, false),
		Entry("a new name", "client.?", lock.EntityName{}, false),
		Entry("trailing text", "client.12x", lock.EntityName{}, false),
		Entry("a space before the num", "client. 12", lock.EntityName{}, false),
		Entry("no num", "client.", lock.EntityName{}, false),
	)

	It("decodes a get_info reply, whose holder address follows its marker byte, and re-encodes it untouched", func() {
		addr := []byte{1, 1, 1, 4, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef} // marker 1, then a framed struct: version 1, compat 1, len 4
		reply := replyWith(addr)
		info, err := decodeInfo(reply)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Type).To(Equal(lock.TypeExclusive))
		Expect(info.Lockers).To(HaveLen(1))
		li := info.Lockers[lock.LockerID{Locker: lock.EntityName{Type: lock.EntityTypeClient, Num: 7}}]
		Expect(li.Expiration).To(Equal(time.Unix(1700000000, 500000000).UTC()))
		Expect(li.Addr).To(Equal(addr), "the marker is part of the address")
		Expect(li.Description).To(Equal("desc"))
		Expect(encode(info)).To(Equal(reply))
	})

	It("re-encodes a legacy holder address as the class re-encodes it for a current client", func() {
		in4 := []byte{0x1a, 0x85, 192, 168, 1, 2}
		info, err := decodeInfo(replyWith(legacyAddr(0x0f7222, 2, in4)))
		Expect(err).NotTo(HaveOccurred())
		li := info.Lockers[lock.LockerID{Locker: lock.EntityName{Type: lock.EntityTypeClient, Num: 7}}]
		Expect(li.Addr).To(Equal(addr2(1, 0x0f7222, 2, append(in4, make([]byte, 8)...))),
			"AF_INET: TYPE_LEGACY and sockaddr_in's 16 bytes")
		Expect(li.Description).To(Equal("desc"))

		in6 := make([]byte, 26)
		in6[0], in6[25] = 0x1a, 0xff
		info, err = decodeInfo(replyWith(legacyAddr(9, 10, in6)))
		Expect(err).NotTo(HaveOccurred())
		li = info.Lockers[lock.LockerID{Locker: lock.EntityName{Type: lock.EntityTypeClient, Num: 7}}]
		Expect(li.Addr).To(Equal(addr2(1, 9, 10, in6)), "AF_INET6: sockaddr_in6's 28 bytes")

		info, err = decodeInfo(replyWith(legacyAddr(9, 1, []byte{'/', 't', 'm', 'p'})))
		Expect(err).NotTo(HaveOccurred())
		li = info.Lockers[lock.LockerID{Locker: lock.EntityName{Type: lock.EntityTypeClient, Num: 7}}]
		Expect(li.Addr).To(Equal(addr2(0, 9, 0, make([]byte, 26))),
			"a family set_sockaddr refuses leaves an empty TYPE_NONE address of the union's 28 bytes")
	})

	It("fails an address whose marker is neither 0 nor 1, as entity_addr_t::decode does", func() {
		_, err := decodeInfo(replyWith([]byte{2, 1, 1, 0, 0, 0, 0}))
		Expect(err).To(MatchError(denc.ErrMalformed))
	})

	It("fails an address whose compat is past entity_addr_t's DECODE_START(1)", func() {
		_, err := decodeInfo(replyWith([]byte{1, 2, 2, 0, 0, 0, 0}))
		Expect(err).To(MatchError(denc.ErrIncompatible))
	})

	It("encodes the holders in locker_id_t order: entity type, then num, then cookie", func() {
		li := lock.LockerInfo{Addr: addr2(1, 0, 2, make([]byte, 14))}
		id := func(typ uint8, num int64, cookie string) lock.LockerID {
			return lock.LockerID{Locker: lock.EntityName{Type: typ, Num: num}, Cookie: cookie}
		}
		info := lock.Info{Type: lock.TypeShared, Tag: "t", Lockers: map[lock.LockerID]lock.LockerInfo{
			id(8, 10, "b"): li, id(8, 10, "a"): li, id(8, -1, ""): li, id(8, 9, "z"): li, id(4, 100, ""): li,
		}}
		b := encode(info)
		d := denc.NewDecoder(b)
		h := d.BeginStruct(1)
		var order []lock.LockerID
		for range d.U32() {
			order = append(order, lock.DecodeLockerID(d))
			lock.DecodeLockerInfo(d)
		}
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(order).To(Equal([]lock.LockerID{id(4, 100, ""), id(8, -1, ""), id(8, 9, "z"), id(8, 10, "a"), id(8, 10, "b")}))
		Expect(d.U8()).To(Equal(uint8(lock.TypeShared)))
		Expect(d.String()).To(Equal("t"))
		d.EndStruct(h)
		Expect(d.Err()).NotTo(HaveOccurred())
	})

	It("keeps the last of a repeated holder, as the legacy std::map decode assigns through operator[]", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.U32(2)
		for _, desc := range []string{"first", "second"} {
			lock.LockerID{Locker: lock.EntityName{Type: lock.EntityTypeClient, Num: 1}}.Encode(e, denc.Squid)
			lock.LockerInfo{Addr: addr2(1, 0, 2, make([]byte, 14)), Description: desc}.Encode(e, denc.Squid)
		}
		e.U8(1)
		e.String("")
		e.EndStruct(f)
		info, err := decodeInfo(e.Bytes())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Lockers).To(HaveLen(1))
		Expect(info.Lockers[lock.LockerID{Locker: lock.EntityName{Type: lock.EntityTypeClient, Num: 1}}].Description).To(Equal("second"))
	})

	It("decodes a reply without holders to an empty, writable map", func() {
		info, err := decodeInfo([]byte{1, 1, 9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
		Expect(err).NotTo(HaveOccurred())
		Expect(info).To(Equal(lock.Info{Lockers: map[lock.LockerID]lock.LockerInfo{}, Type: lock.TypeNone}))
	})

	It("keeps a lock type the class does not define, as the C++ cast to ClsLockType keeps it", func() {
		b := encode(lock.AssertOp{Name: "n", Type: 9})
		Expect(lock.DecodeAssertOp(denc.NewDecoder(b)).Type).To(Equal(lock.Type(9)))
	})
})
