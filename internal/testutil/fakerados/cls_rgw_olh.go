package fakerados

// The methods RGWClass emulates of those that link and unlink a versioned
// object's instances and keep its olh entry and log, bucket_link_olh to
// bucket_clear_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4699-L4703 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5116-L5120 at v20.2.4).
var _ = rgwTable(methodTable{})
