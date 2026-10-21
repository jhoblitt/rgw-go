package driver_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// rgwWriter is a radosgw-shaped versioned PutObject of one version of k, in
// radosgw's order (_do_write_meta, then set_olh and update_olh,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3352-L3375 at v19.2.7,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8639-L8762 at v19.2.7):
// the prepare, the instance head, the ADD completion, which radosgw sends
// unawaited and which reaches the shard before the link, the OLH head read
// without following, the init, the link, then the replay from marker 0 and
// its trim. The index calls are built from cls_rgw's requests; the head read
// and the init are the driver's, which send radosgw's ops. The replay's head
// op is taken only when the log's last epoch is at or above the stored
// olh.ver, as the OSD's u64-mode GTE takes it, with nothing scheduled
// between its read of olh.ver and its write, and one refused is success, as
// set_olh takes it. The writer never removes its own pending attr. Each step
// is a method, Steps lists them in order, and a spec can run another writer
// between any two.
type rgwWriter struct {
	c   *fakerados.Cluster
	s   *driver.Store
	rec *op.BucketRecord
	rel denc.Release
	key meta.ObjKey
	// epoch is the link's olh_epoch, 0 for the class's next.
	epoch uint64

	tag     string
	headVer uint64
	head    *op.ObjectState
	opTag   string
	log     rgwcls.ReadOLHLogRet
	last    uint64
	applied bool
}

// newRGWWriter is the writer of version instance of k in rec's bucket,
// through s and its cluster c.
func newRGWWriter(c *fakerados.Cluster, s *driver.Store, rec *op.BucketRecord, rel denc.Release, instance string) *rgwWriter {
	return &rgwWriter{c: c, s: s, rec: rec, rel: rel, key: meta.ObjKey{Name: "k", Instance: instance}, tag: "rgw-tag-" + instance}
}

func (w *rgwWriter) shard() string { return indexShardOID(w.rec, w.key.Name) }

func (w *rgwWriter) zonesTrace() []string {
	return []string{w.s.Zone().ID + ":" + w.rec.Info.Bucket.Key()}
}

// shardWrite runs call on the version's index shard behind assert_exists
// and the reshard guard, as radosgw sends every index write.
func (w *rgwWriter) shardWrite(ctx context.Context, call func(op *radosclient.WriteOp)) error {
	GinkgoHelper()
	p, err := w.c.Pool(ctx, rookIndexPool, "")
	Expect(err).NotTo(HaveOccurred())
	op := radosclient.NewWriteOp()
	op.AssertExists()
	rgwcls.GuardBucketResharding(op, w.rel)
	call(op)
	_, err = p.Write(ctx, w.shard(), op, radosclient.OpFlagNone)
	return err
}

// Prepare is UpdateIndex::prepare of the version's ADD, VERSIONED_OP set.
func (w *rgwWriter) Prepare(ctx context.Context) {
	GinkgoHelper()
	p := rgwcls.PrepareOp{Op: rgwcls.OpAdd, Key: rgwcls.ObjKey{Name: w.key.Name, Instance: w.key.Instance}, Tag: w.tag}
	if w.rel < denc.Tentacle {
		p.BILogFlags, p.ZonesTrace = rgwcls.BILogFlagVersionedOp, w.zonesTrace()
	}
	Expect(w.shardWrite(ctx, func(op *radosclient.WriteOp) { rgwcls.BucketPrepareOp(op, p, w.rel) })).To(Succeed())
}

// HeadWrite writes the instance head.
func (w *rgwWriter) HeadWrite(ctx context.Context) {
	GinkgoHelper()
	w.headVer = writeInstanceHead(ctx, w.c, w.key)
}

// Complete is UpdateIndex::complete of the ADD.
func (w *rgwWriter) Complete(ctx context.Context) {
	GinkgoHelper()
	c := rgwcls.CompleteOp{
		Op: rgwcls.OpAdd, Key: rgwcls.ObjKey{Name: w.key.Name, Instance: w.key.Instance},
		Ver: rgwcls.EntryVer{Pool: w.c.PoolID(testDataPool), Epoch: w.headVer}, Meta: versionMeta(w.key),
		Tag: w.tag, BILogFlags: rgwcls.BILogFlagVersionedOp, ZonesTrace: w.zonesTrace(),
	}
	Expect(w.shardWrite(ctx, func(op *radosclient.WriteOp) { rgwcls.BucketCompleteOp(op, c, w.rel) })).To(Succeed())
}

// ReadHead is set_olh's get_obj_state of the OLH head, not following it.
func (w *rgwWriter) ReadHead(ctx context.Context) {
	GinkgoHelper()
	var err error
	w.head, err = w.s.ReadOLHHeadForTest(ctx, w.rec, meta.ObjKey{Name: w.key.Name})
	Expect(err).NotTo(HaveOccurred())
}

// Init is olh_init_modification. It reports false for its ECANCELED, after
// which set_olh reads the head again.
func (w *rgwWriter) Init(ctx context.Context) bool {
	GinkgoHelper()
	var err error
	w.opTag, _, err = w.s.OLHInitModificationForTest(ctx, w.head, nil)
	if errors.Is(err, radosclient.ErrCanceled) {
		return false
	}
	Expect(err).NotTo(HaveOccurred())
	return true
}

// Link is bucket_index_link_olh.
func (w *rgwWriter) Link(ctx context.Context) {
	GinkgoHelper()
	l := rgwcls.LinkOLHOp{
		Key: rgwcls.ObjKey{Name: w.key.Name, Instance: w.key.Instance}, OLHTag: string(w.head.Attrs[meta.AttrOLHIDTag]),
		OpTag: w.opTag, OLHEpoch: w.epoch, ZonesTrace: w.zonesTrace(),
	}
	Expect(w.shardWrite(ctx, func(op *radosclient.WriteOp) { rgwcls.LinkOLH(op, l, w.rel) })).To(Succeed())
}

// ReadLog is update_olh's read of the pending log from marker 0, under the
// head's OLH tag as ReadHead and Init left it; the class's ECANCELED, the
// OLH replaced, ends the replay, as set_olh takes it for success.
func (w *rgwWriter) ReadLog(ctx context.Context) {
	GinkgoHelper()
	w.log, w.applied = rgwcls.ReadOLHLogRet{}, false
	p, err := w.c.Pool(ctx, rookIndexPool, "")
	Expect(err).NotTo(HaveOccurred())
	rop := radosclient.NewReadOp()
	q := rgwcls.ReadOLHLogOp{OLH: rgwcls.ObjKey{Name: w.key.Name}, OLHTag: string(w.head.Attrs[meta.AttrOLHIDTag])}
	res := rgwcls.ReadOLHLog(rop, q, w.rel)
	_, err = p.Read(ctx, w.shard(), rop, radosclient.OpFlagNone)
	if errors.Is(err, radosclient.ErrCanceled) {
		return
	}
	Expect(err).NotTo(HaveOccurred())
	w.log, err = res.Result()
	Expect(err).NotTo(HaveOccurred())
	Expect(w.log.IsTruncated).To(BeFalse(), "the writer replays one page")
}

// Apply is apply_olh_log's head op over the log ReadLog read and the head
// as ReadHead and Init left it, taken only when the log's last epoch is at
// or above the stored olh.ver; an empty log writes nothing
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8423-L8425 at v19.2.7).
// It replays links alone, all a log of PutObjects holds.
func (w *rgwWriter) Apply(ctx context.Context) {
	GinkgoHelper()
	if len(w.log.Log) == 0 {
		return
	}
	epochs := slices.Sorted(maps.Keys(w.log.Log))
	w.last = epochs[len(epochs)-1]
	linkEpoch := parseVer(w.head.Attrs[meta.AttrOLHVer])
	var cur rgwcls.ObjKey
	var dm, link bool
	if b, ok := w.head.Attrs[meta.AttrOLHInfo]; ok {
		info := meta.DecodeOLHInfo(denc.NewDecoder(b))
		cur, dm = rgwcls.ObjKey{Name: info.Target.Key.Name, Instance: info.Target.Key.Instance}, info.Removed
	}
	var pending []string
	for _, epoch := range epochs {
		for _, e := range w.log.Log[epoch] {
			Expect(e.Op).To(Equal(rgwcls.OLHOpLinkOLH), "the writer replays links alone: %+v", e)
			if linkEpoch < epoch || cur.Instance == "" || cur.Instance > e.Key.Instance {
				link, cur, dm = true, e.Key, e.DeleteMarker
			}
			pending = append(pending, meta.AttrOLHPendingPrefix+e.OpTag)
		}
	}
	stored := slices.Clone(w.c.Object(testDataPool, "", olhHeadOID(w.key)).Xattrs[meta.AttrOLHVer])
	if w.last < parseVer(stored) {
		return // the OSD's GTE refuses it, and set_olh takes the ECANCELED for success
	}
	op := radosclient.NewWriteOp()
	op.CmpXattr(meta.AttrOLHIDTag, radosclient.CmpEQ, w.head.Attrs[meta.AttrOLHIDTag])
	op.CmpXattr(meta.AttrOLHVer, radosclient.CmpEQ, stored)
	op.SetXattr(meta.AttrOLHVer, []byte(strconv.FormatUint(w.last, 10)))
	op.SetMtime(w.head.Mtime)
	for _, name := range pending {
		op.RmXattr(name)
	}
	if link {
		info := meta.OLHInfo{Target: meta.Obj{Bucket: w.rec.Info.Bucket, Key: meta.ObjKey{Name: cur.Name, Instance: cur.Instance}}, Removed: dm}
		op.SetXattr(meta.AttrOLHInfo, encodeAt(info, w.rel))
	}
	data, err := w.c.Pool(ctx, testDataPool, "")
	Expect(err).NotTo(HaveOccurred())
	_, err = data.Write(ctx, olhHeadOID(w.key), op, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "nothing runs between the GTE's read and its write")
	w.applied = true
}

// Trim is apply_olh_log's trim of the log to the epoch Apply wrote, after a
// head op that landed; its ECANCELED is success.
func (w *rgwWriter) Trim(ctx context.Context) {
	GinkgoHelper()
	if !w.applied {
		return
	}
	t := rgwcls.TrimOLHLogOp{OLH: rgwcls.ObjKey{Name: w.key.Name}, Ver: w.last, OLHTag: string(w.head.Attrs[meta.AttrOLHIDTag])}
	if err := w.shardWrite(ctx, func(op *radosclient.WriteOp) { rgwcls.TrimOLHLog(op, t, w.rel) }); !errors.Is(err, radosclient.ErrCanceled) {
		Expect(err).NotTo(HaveOccurred())
	}
}

// InitRetrying is set_olh's init, after which an ECANCELED reads the head
// again and inits again.
func (w *rgwWriter) InitRetrying(ctx context.Context) {
	GinkgoHelper()
	for !w.Init(ctx) {
		w.ReadHead(ctx)
	}
}

// Steps are the write's steps in radosgw's order.
func (w *rgwWriter) Steps() []func(context.Context) {
	return []func(context.Context){w.Prepare, w.HeadWrite, w.Complete, w.ReadHead, w.InitRetrying, w.Link, w.ReadLog, w.Apply, w.Trim}
}

// Run is the whole write.
func (w *rgwWriter) Run(ctx context.Context) {
	GinkgoHelper()
	for _, step := range w.Steps() {
		step(ctx)
	}
}

// parseVer is olh.ver as the OSD's u64 compare and apply_olh_log's
// strict_strtoll read it: the number, 0 when absent or not a number.
func parseVer(b []byte) uint64 {
	digits, _, _ := strings.Cut(string(b), "\x00")
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// olhHeadOID is the OLH head object of key's name in the PUT fixture's
// bucket.
func olhHeadOID(key meta.ObjKey) string {
	return putBucketID + "_" + meta.ObjKey{Name: key.Name, NS: key.NS}.OID()
}

// instanceHeadOID is the head object of key's version in the PUT fixture's
// bucket.
func instanceHeadOID(key meta.ObjKey) string { return putBucketID + "_" + key.OID() }

// versionMeta is the index meta every fixture version is completed with.
func versionMeta(key meta.ObjKey) rgwcls.DirEntryMeta {
	size := uint64(len("data-" + key.Instance))
	return rgwcls.DirEntryMeta{
		Category: rgwcls.CategoryMain, Size: size, AccountedSize: size,
		Mtime: time.Date(2026, 10, 15, 11, 0, 0, 0, time.UTC), ETag: "etag-" + key.Instance, Owner: "alice", OwnerDisplayName: "Alice",
		StorageClass: meta.StorageClassStandard,
	}
}

// writeInstanceHead writes key's version head, holding "data-<instance>",
// its write tag and its ETag, and returns the version the write left.
func writeInstanceHead(ctx context.Context, c *fakerados.Cluster, key meta.ObjKey) uint64 {
	GinkgoHelper()
	p, err := c.Pool(ctx, testDataPool, "")
	Expect(err).NotTo(HaveOccurred())
	op := radosclient.NewWriteOp()
	op.WriteFull([]byte("data-" + key.Instance))
	op.SetXattr(meta.AttrIDTag, []byte(versionWriteTag(key)))
	op.SetXattr(meta.AttrETag, []byte("etag-"+key.Instance))
	ver, err := p.Write(ctx, instanceHeadOID(key), op, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred())
	return ver
}

// versionWriteTag is the write tag writeInstanceHead gives key's version.
func versionWriteTag(key meta.ObjKey) string { return "idtag-" + key.Instance }
