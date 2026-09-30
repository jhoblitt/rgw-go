// Package fakerados is an in-memory radosclient.Cluster for specs. Its pools
// run the seam's steps against stored objects as an OSD runs them on a
// replicated pool, dispatch class calls to emulators a spec registers, and
// deliver notifies to registered watches. It exists so the driver's specs can
// assert the exact operations rgw-go composes without a cluster; it is not a
// RADOS.
//
// A write op is atomic. It runs on the op's own copy of the object, which is
// stored only when every step succeeds, and existence follows that copy: a
// Remove, then a Create, leaves an empty object. The comparisons and every
// read a class makes see the object as stored before the op instead, because
// the OSD serves CMPXATTR, OMAP_CMP's values, ASSERT_VER and a class's
// cls_cxx_getxattr from the store rather than from the op's earlier steps
// (src/osd/PrimaryLogPG.cc at v19.2.6). A flags step replaces the flags of
// the step before it, as librados's set_last_op_flags does.
//
// Whether an op writes comes from its steps, as the OSD takes it from each
// step's mode and each called method's WR flag, whatever the op's kind
// (OpInfo::set_from_op): a read op keeps what a WR method in it writes,
// without stamping an mtime. An op that does not write fails on a missing
// object with ENOENT before any step runs, and a method that changes the
// object without the WR flag fails with EIO. A successful op that wrote
// returns no step's output unless it carries ReturnVec; with ReturnVec it
// fails with EOVERFLOW, applying nothing, when a step's output passes the
// OSD's osd_max_write_op_reply_len (PrimaryLogPG::execute_ctx). The fake
// sees what a method writes by what it changes in the object.
//
// Locators do not change where the fake keeps an object: each namespace of a
// pool holds its objects by name alone.
package fakerados
