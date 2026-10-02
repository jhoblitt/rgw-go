// Package tags is radosgw's RGWObjTags: the tag set of an object or a bucket,
// its stored form under the user.rgw.x-amz-tagging attr, the Tagging XML
// document the tagging subresources read and write, and the x-amz-tagging
// header. The rules are radosgw's at v19.2.6 and v20.2.4, which agree on all
// of them: src/rgw/rgw_tag.{h,cc}, src/rgw/rgw_tag_s3.cc and the tagging ops
// of src/rgw/rgw_rest_s3.cc.
package tags
