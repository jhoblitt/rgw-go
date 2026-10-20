package rgw_test

import (
	"errors"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The seams the lifecycle store and worker build on.
var (
	_ func(*radosclient.ReadOp, string) *rgw.LCEntryResult        = rgw.LCGetEntry
	_ func(radosclient.Execer, rgw.LCEntry)                       = rgw.LCSetEntry
	_ func(radosclient.Execer, rgw.LCEntry)                       = rgw.LCRmEntry
	_ func(*radosclient.ReadOp, string) *rgw.LCEntryResult        = rgw.LCGetNextEntry
	_ func(radosclient.Execer, rgw.LCHead)                        = rgw.LCPutHead
	_ func(*radosclient.ReadOp) *rgw.LCHeadResult                 = rgw.LCGetHead
	_ func(*radosclient.ReadOp, string, uint32) *rgw.LCListResult = rgw.LCListEntries
)

// recorder is an Execer that is neither op, recording each exec it is given.
type recorder struct{ steps []radosclient.Step }

func (r *recorder) Exec(class, method string, in []byte) *radosclient.ExecResult {
	res := &radosclient.ExecResult{}
	r.steps = append(r.steps, &radosclient.ExecStep{Class: class, Method: method, In: in, Result: res})
	return res
}

// lcEmptyEntry is the entry lc_get_next_entry replies past a shard's last
// key: cls_rgw_lc_entry v1 with every field zero.
var lcEmptyEntry = []byte{1, 1, 0x10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

var _ = Describe("the lc calls", func() {
	DescribeTable("each write adds one exec carrying its request, to any Execer and to a write op",
		func(method string, call func(radosclient.Execer), want []byte) {
			rec := &recorder{}
			call(rec)
			Expect(soleExec(rec.steps, method).In).To(Equal(want))
			op := radosclient.NewWriteOp()
			call(op)
			Expect(soleExec(op.Steps(), method).In).To(Equal(want))
		},
		Entry("lc_set_entry wraps the entry at version 2", "lc_set_entry",
			func(op radosclient.Execer) { rgw.LCSetEntry(op, lcEntry) }, lcCarrier(2, 2, lcEntryBytes)),
		Entry("lc_rm_entry wraps the entry at version 2", "lc_rm_entry",
			func(op radosclient.Execer) { rgw.LCRmEntry(op, lcEntry) }, lcCarrier(2, 2, lcEntryBytes)),
		Entry("lc_put_head wraps the head at version 1", "lc_put_head",
			func(op radosclient.Execer) { rgw.LCPutHead(op, lcHead) }, lcCarrier(1, 1, lcHeadBytes)),
	)

	It("lc_set_entry sends every cls_rgw_lc_set_entry_op golden's re-encoding", func() {
		for _, c := range goldens("cls_rgw_lc_set_entry_op") {
			rec := &recorder{}
			rgw.LCSetEntry(rec, decodeWhole(c.Bin, rgw.DecodeLCSetEntryOp).Entry)
			Expect(soleExec(rec.steps, "lc_set_entry").In).To(Equal(c.ReEnc), "%s/%s", c.Archive, c.Name)
		}
	})

	It("lc_rm_entry sends every corpus request byte for byte", func() {
		for _, c := range loadRaw("cls_rgw_lc_rm_entry_op") {
			rec := &recorder{}
			rgw.LCRmEntry(rec, decodeWhole(c.bin, rgw.DecodeLCRmEntryOp).Entry)
			Expect(soleExec(rec.steps, "lc_rm_entry").In).To(Equal(c.bin), c.id)
		}
	})

	Describe("lc_get_entry", func() {
		It("sends the marker and decodes every cls_rgw_lc_get_entry_ret golden", func() {
			for _, c := range goldens("cls_rgw_lc_get_entry_ret") {
				op := radosclient.NewReadOp()
				res := rgw.LCGetEntry(op, ":b:m")
				step := soleExec(op.Steps(), "lc_get_entry")
				Expect(step.In).To(Equal(lcMarkerBytes))
				step.Result.Set(c.Bin, 0)
				Expect(res.Result()).To(Equal(decodeWhole(c.Bin, rgw.DecodeLCGetEntryRet).Entry), "%s/%s", c.Archive, c.Name)
			}
		})

		DescribeTable("decodes a version 1 reply either way it was written",
			func(reply []byte, want rgw.LCEntry) {
				op := radosclient.NewReadOp()
				res := rgw.LCGetEntry(op, ":b:m")
				execResult(op).Set(reply, 0)
				Expect(res.Result()).To(Equal(want))
			},
			Entry("holding an entry", lcCarrier(1, 1, lcEntryBytes), lcEntry),
			Entry("holding the legacy pair", lcLegacyPairBytes, lcLegacyPair),
		)

		It("passes the class's ENOENT for a key the shard does not hold through", func() {
			op := radosclient.NewReadOp()
			res := rgw.LCGetEntry(op, ":b:m")
			execResult(op).Set(nil, -2)
			_, err := res.Result()
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})
	})

	Describe("lc_get_next_entry", func() {
		It("sends the marker and decodes the next entry", func() {
			op := radosclient.NewReadOp()
			res := rgw.LCGetNextEntry(op, ":b:m")
			step := soleExec(op.Steps(), "lc_get_next_entry")
			Expect(step.In).To(Equal(lcMarkerBytes))
			step.Result.Set(lcCarrier(2, 2, lcEntryBytes), 0)
			Expect(res.Result()).To(Equal(lcEntry))
		})

		It("decodes the empty entry the class replies past the shard's last key", func() {
			op := radosclient.NewReadOp()
			res := rgw.LCGetNextEntry(op, "")
			Expect(soleExec(op.Steps(), "lc_get_next_entry").In).To(Equal([]byte{1, 1, 4, 0, 0, 0, 0, 0, 0, 0}))
			execResult(op).Set(lcCarrier(2, 2, lcEmptyEntry), 0)
			Expect(res.Result()).To(Equal(rgw.LCEntry{}))
		})

		It("decodes a version 1 reply as the legacy pair", func() {
			op := radosclient.NewReadOp()
			res := rgw.LCGetNextEntry(op, "")
			execResult(op).Set(lcLegacyPairBytes, 0)
			Expect(res.Result()).To(Equal(lcLegacyPair))
		})
	})

	DescribeTable("passes the class's EIO for a stored value that is not an entry through as a *radosclient.Error",
		func(method string, add func(op *radosclient.ReadOp) *rgw.LCEntryResult) {
			op := radosclient.NewReadOp()
			res := add(op)
			soleExec(op.Steps(), method).Result.Set(nil, -int32(syscall.EIO))
			_, err := res.Result()
			Expect(err).To(WithTransform(func(err error) int32 {
				if rerr, ok := errors.AsType[*radosclient.Error](err); ok {
					return rerr.Errno
				}
				return 0
			}, Equal(int32(syscall.EIO))), "%v", err)
			Expect(err).To(MatchError(ContainSubstring("exec rgw." + method)))
		},
		Entry("lc_get_entry", "lc_get_entry",
			func(op *radosclient.ReadOp) *rgw.LCEntryResult { return rgw.LCGetEntry(op, ":b:m") }),
		Entry("lc_get_next_entry", "lc_get_next_entry",
			func(op *radosclient.ReadOp) *rgw.LCEntryResult { return rgw.LCGetNextEntry(op, ":a:1") }),
	)

	Describe("lc_get_head", func() {
		It("sends no input and decodes the head", func() {
			op := radosclient.NewReadOp()
			res := rgw.LCGetHead(op)
			step := soleExec(op.Steps(), "lc_get_head")
			Expect(step.In).To(BeEmpty())
			step.Result.Set(lcCarrier(1, 1, lcHeadBytes), 0)
			Expect(res.Result()).To(Equal(lcHead))
		})

		It("decodes the zero head the class replies for a shard with no header", func() {
			op := radosclient.NewReadOp()
			res := rgw.LCGetHead(op)
			execResult(op).Set(lcCarrier(1, 1, lcCarrier(2, 2, make([]byte, 20))), 0)
			Expect(res.Result()).To(Equal(rgw.LCHead{}))
		})
	})

	Describe("lc_list_entries", func() {
		It("sends the marker and the maximum at version 3 and returns the entries sorted by bucket", func() {
			op := radosclient.NewReadOp()
			res := rgw.LCListEntries(op, ":b:m", 100)
			step := soleExec(op.Steps(), "lc_list_entries")
			Expect(step.In).To(Equal(lcListOpBytes))
			step.Result.Set(lcListV3Bytes(), 0)
			Expect(res.Result()).To(Equal(rgw.LCListEntriesRet{
				Entries: []rgw.LCEntry{lcEntryA, lcComplete}, Truncated: true, Version: new(uint8(3)),
			}))
		})

		It("decodes a legacy version 2 reply, whose map carries no start times", func() {
			op := radosclient.NewReadOp()
			res := rgw.LCListEntries(op, "", 2)
			execResult(op).Set(lcCarrier(2, 1, append(append([]byte{}, lcLegacyMap...), 1)), 0)
			Expect(res.Result()).To(Equal(rgw.LCListEntriesRet{
				Entries:   []rgw.LCEntry{lcEntryA, {Bucket: ":b:m", Status: rgw.LCStatusComplete}},
				Truncated: true, Version: new(uint8(2)),
			}))
		})
	})

	DescribeTable("a read's result names its method in a reply that fails to decode, and needs the op to have run",
		func(method string, add func(op *radosclient.ReadOp) func() error) {
			op := radosclient.NewReadOp()
			result := add(op)
			Expect(result()).To(MatchError(radosclient.ErrIncomplete))
			soleExec(op.Steps(), method).Result.Set([]byte{1, 1, 100, 0, 0, 0}, 0)
			err := result()
			Expect(err).To(MatchError(denc.ErrShortBuffer))
			Expect(err).To(MatchError(ContainSubstring("rgw: decoding " + method + " reply")))
		},
		Entry("lc_get_entry", "lc_get_entry", func(op *radosclient.ReadOp) func() error {
			res := rgw.LCGetEntry(op, "")
			return func() error { _, err := res.Result(); return err }
		}),
		Entry("lc_get_next_entry", "lc_get_next_entry", func(op *radosclient.ReadOp) func() error {
			res := rgw.LCGetNextEntry(op, "")
			return func() error { _, err := res.Result(); return err }
		}),
		Entry("lc_get_head", "lc_get_head", func(op *radosclient.ReadOp) func() error {
			res := rgw.LCGetHead(op)
			return func() error { _, err := res.Result(); return err }
		}),
		Entry("lc_list_entries", "lc_list_entries", func(op *radosclient.ReadOp) func() error {
			res := rgw.LCListEntries(op, "", 1)
			return func() error { _, err := res.Result(); return err }
		}),
	)
})
