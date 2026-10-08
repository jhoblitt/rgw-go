package user_test

import (
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The corpus holds no account type of the user class, so each fixture is
// built field by field from its ENCODE_START(1, 1) framing
// (cls_user_types.h:219-264 and cls_user_ops.h:267-397 at v19.2.6 and
// v20.2.4).

// resourceBytes is cls_user_account_resource as the class stores it.
func resourceBytes(name, path string, metadata []byte) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	e.String(name)
	e.String(path)
	e.Bytes32(metadata)
	e.EndStruct(f)
	return e.Bytes()
}

var _ = Describe("account types", func() {
	It("AccountResource is the name, the path and the metadata bufferlist", func() {
		r := user.AccountResource{Name: "Alice", Path: "/", Metadata: []byte{1, 2}}
		want := resourceBytes("Alice", "/", []byte{1, 2})
		Expect(encodeSquid(r.Encode)).To(Equal(want))
		Expect(decodeWhole(want, user.DecodeAccountResource)).To(Equal(r))
	})
	It("AccountHeader is the count", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.U32(7)
		e.EndStruct(f)
		Expect(encodeSquid(user.AccountHeader{Count: 7}.Encode)).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), user.DecodeAccountHeader)).To(Equal(user.AccountHeader{Count: 7}))
	})
	It("ResourceMetadata is rgwrados::users::resource_metadata, the user id", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.String("u1")
		e.EndStruct(f)
		Expect(encodeSquid(user.ResourceMetadata{UserID: "u1"}.Encode)).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), user.DecodeResourceMetadata)).To(Equal(user.ResourceMetadata{UserID: "u1"}))
	})
	It("round-trips the list reply with its entries, truncation and marker", func() {
		ret := user.AccountResourceListRet{
			Entries:   []user.AccountResource{{Name: "a", Path: "/", Metadata: nil}, {Name: "b", Path: "/x/", Metadata: []byte{9}}},
			Truncated: true, Marker: "b",
		}
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.U32(2)
		e.Raw(resourceBytes("a", "/", nil))
		e.Raw(resourceBytes("b", "/x/", []byte{9}))
		e.Bool(true)
		e.String("b")
		e.EndStruct(f)
		Expect(encodeSquid(ret.Encode)).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), user.DecodeAccountResourceListRet)).To(Equal(ret))
	})
})

var _ = Describe("account requests", func() {
	It("account_resource_add sends the entry, exclusive and the limit", func() {
		w := radosclient.NewWriteOp()
		user.AccountResourceAdd(w, user.AccountResource{Name: "Alice", Path: "/", Metadata: []byte{3}}, true, 1000, denc.Squid)
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.Raw(resourceBytes("Alice", "/", []byte{3}))
		e.Bool(true)
		e.U32(1000)
		e.EndStruct(f)
		Expect(execStep(w.Steps(), "account_resource_add").In).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), user.DecodeAccountResourceAddOp)).To(Equal(user.AccountResourceAddOp{
			Entry: user.AccountResource{Name: "Alice", Path: "/", Metadata: []byte{3}}, Exclusive: true, Limit: 1000,
		}))
	})
	It("account_resource_rm and account_resource_get send the name", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.String("Alice")
		e.EndStruct(f)
		w := radosclient.NewWriteOp()
		user.AccountResourceRm(w, "Alice", denc.Squid)
		Expect(execStep(w.Steps(), "account_resource_rm").In).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), user.DecodeAccountResourceRmOp)).To(Equal(user.AccountResourceRmOp{Name: "Alice"}))
		r := radosclient.NewReadOp()
		user.AccountResourceGet(r, "Alice", denc.Squid)
		Expect(execStep(r.Steps(), "account_resource_get").In).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), user.DecodeAccountResourceGetOp)).To(Equal(user.AccountResourceGetOp{Name: "Alice"}))
	})
	It("account_resource_list sends the marker, the path prefix and the maximum", func() {
		r := radosclient.NewReadOp()
		user.AccountResourceList(r, "m", "/p/", 100, denc.Squid)
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.String("m")
		e.String("/p/")
		e.U32(100)
		e.EndStruct(f)
		Expect(execStep(r.Steps(), "account_resource_list").In).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), user.DecodeAccountResourceListOp)).To(Equal(user.AccountResourceListOp{
			Marker: "m", PathPrefix: "/p/", MaxEntries: 100,
		}))
	})
})

var _ = Describe("account replies", func() {
	It("account_resource_get decodes the entry", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.Raw(resourceBytes("Alice", "/", []byte{4}))
		e.EndStruct(f)
		r := radosclient.NewReadOp()
		res := user.AccountResourceGet(r, "alice", denc.Squid)
		execStep(r.Steps(), "account_resource_get").Result.Set(e.Bytes(), 0)
		got, err := res.Entry()
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(user.AccountResource{Name: "Alice", Path: "/", Metadata: []byte{4}}))
	})
	It("account_resource_list decodes the entries, truncation and marker", func() {
		ret := user.AccountResourceListRet{Entries: []user.AccountResource{{Name: "a", Path: "/", Metadata: nil}}, Truncated: true, Marker: "a"}
		r := radosclient.NewReadOp()
		res := user.AccountResourceList(r, "", "", 1, denc.Squid)
		execStep(r.Steps(), "account_resource_list").Result.Set(encodeSquid(ret.Encode), 0)
		entries, truncated, marker, err := res.Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(Equal(ret.Entries))
		Expect(truncated).To(BeTrue())
		Expect(marker).To(Equal("a"))
	})
	It("reports the method's error and a read before the op ran", func() {
		r := radosclient.NewReadOp()
		get := user.AccountResourceGet(r, "a", denc.Squid)
		list := user.AccountResourceList(r, "", "", 1, denc.Squid)
		_, err := get.Entry()
		Expect(err).To(MatchError(radosclient.ErrIncomplete))
		_, _, _, err = list.Result()
		Expect(err).To(MatchError(radosclient.ErrIncomplete))
		for _, s := range r.Steps() {
			exec, ok := s.(*radosclient.ExecStep)
			Expect(ok).To(BeTrue())
			exec.Result.Set(nil, -int32(syscall.ENOENT))
		}
		_, err = get.Entry()
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		_, _, _, err = list.Result()
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})
})
