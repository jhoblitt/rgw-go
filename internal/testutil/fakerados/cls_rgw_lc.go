package fakerados

// The methods RGWClass emulates of those that keep the lifecycle shards'
// bucket entries and head, lc_get_entry to lc_list_entries
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4734-L4740 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5153-L5159 at v20.2.4).
var _ = rgwTable(methodTable{})
