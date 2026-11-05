## INCIDENT: the mix smoke filled the OSD (10:23-10:31)

- The mix smoke (M, 2000 requests, gcloop beside it) moved 22.2 GB in 45 s (~470 MiB/s, 25 req/s;
  smoke/mix/M-1/mix-timeline.csv) and stalled at 1109 requests at 10:23:49. Its PUTs write ~30% of
  the bytes, ~140 MiB/s, and the first `gc process --include-all` pass never finished. Raw use went
  to 9.5 GiB of 10 GiB, OSD backfillfull, then BlueStore ENOSPC.
- osd.0 aborted at 17:23:55Z in BlueStore::_txc_add_transaction ("unexpected error", ENOSPC on a
  write), then crash-looped on every start since: BlueFS::_flush_range_F "bluefs enospc" during
  RocksDB WAL recovery (WriteLevel0TableForRecovery); "unable to allocate 0x80000 ... free
  0x121000" (1.1 MiB free of 10 GiB). 7 crashes in `ceph crash ls`, pod CrashLoopBackOff.
- Cause, mine: the mix's overwrite garbage rate (~140 MiB/s) is far above what one gc pass deletes,
  and a 10 GiB OSD gives ~45 s of headroom; the full ratios (0.85/0.90/0.95) did not stop writes in
  time because the OSD's usage reports lag a 470 MiB/s load.
- Stopped: the mix (stop file, then TERM), its run.sh, sampler and gcloop. rgw-go M (pid 174130)
  still running at 10:35 to serve the cleanup.

## Recovery of osd.0, in place (10:35-)

Changes made, each to be put back (see the put-back table):
- 10:36 `ceph config set osd.0 bluestore_rocksdb_options_annex avoid_flush_during_recovery=true`;
  10:39 `...=avoid_flush_during_recovery=true,write_buffer_size=1073741824`; 10:40
  `...,allow_fallocate=false`; 10:41 `ceph config set osd.0 bluefs_max_log_runway 131072` and
  `bluefs_min_log_runway 65536`. Each followed by deleting the OSD pod to restart it now.
- Results: with avoid_flush + the 1 GiB memtable, RocksDB recovery finished without flushing, then
  the new MANIFEST's write needed a 4 MiB BlueFS allocation with 1.7 MiB free (0x1b1000) and
  BlueRocksEnv aborted ("unrecognized error code", ENOSPC). The runway settings did not change
  that. In place, BlueFS has no room for the MANIFEST or a new WAL; RocksDB preallocates both
  (manifest_preallocation_size, WAL ~1.1 x write_buffer_size) and BlueRocksEnv aborts on the
  ENOSPC. So space has to come from another device.
- rgw-go M (pid 174130) stopped at 10:38 so that its pending writes would not be resent when the
  OSD comes back. A `radosgw-admin gc process --include-all` from the gcloop is still blocked in
  the toolbox (host pid 174969, uid 2016); it only deletes garbage.
- Next: a rescue pod with the OSD deployment's spec and `sleep infinity`, the deployment and the
  operator scaled to 0 meanwhile; in it, a file-backed DB device (ceph-bluestore-tool
  bluefs-bdev-new-db), ceph-osd run by hand, the smoke's objects removed, then bluefs-bdev-migrate
  back to the main device and the file removed.
- 10:43 rook-ceph-operator scaled 0, rook-ceph-osd-0 scaled 0; pod rw-rescue-osd-0 created from
  the deployment's pod spec (realworld/rescue/pod.json) with the osd container sleeping.
- 10:45 the four osd.0 config overrides removed again (`ceph config rm`); in the rescue pod a 4 GiB
  sparse file /var/lib/rook/rw-rescue-db.img (the node's root fs, 145 GiB free) added as BlueFS DB
  device: `ceph-bluestore-tool --path /var/lib/ceph/osd/ceph-0 --dev-target ... bluefs-bdev-new-db`
  ("DB device added"). ceph-osd started by hand with the deployment's args: mounted, booted,
  217 PGs active+clean, "1 OSD(s) experiencing BlueFS spillover".
- 10:46 realworld/rescue-rm.sh: rw-mix marker c3be24b2-d18a-452a-a6f2-21221cae1df4.130623.1, 1539
  __shadow_ objects of rw-mix removed with `rados --force-full rm` (4032 objects in the data pool
  before). Then `radosgw-admin bucket rm --bucket rw-mix --purge-objects --bypass-gc` (ERROR
  "could not drain handles ... -2", the tails were gone), then again without --bypass-gc: bucket
  gone; `gc process --include-all`: 0 gc entries; `rados ls` of buckets.data: 18 objects, as at the
  start (state-start.txt: 18 objects, 34 MiB).
- 10:47 ceph-osd stopped (TERM, clean); `bluefs-bdev-migrate --devs-source .../block.db --dev-target
  .../block`: "device removed:1"; bluefs-bdev-sizes then "1 : device size 10 GiB : using 211 MiB";
  `fsck`: success; show-label: the main device only. The file removed; rescue pod deleted;
  rook-ceph-osd-0 scaled to 1 (pod ...-2gn95 Running, up in the osdmap after 5 s and still up at
  60 s, 0 restarts); rook-ceph-operator scaled to 1.
- 10:49 `ceph -s`: 1 osd up, 217 PGs active+clean, 221 objects 70 MiB, 159 MiB used of 10 GiB;
  warnings as at the start plus "17 daemons have recently crashed" (all osd.0, my incident). The
  `ceph config dump` is identical to the start state's (rescue/config-start.txt vs config-now.txt).

