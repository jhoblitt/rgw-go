package s3

import "github.com/jhoblitt/rgw-go/internal/op"

// objectHandlers is the object-scope routes over env's options; each entry
// binds to object scope. multi_object_delete, a POST to the bucket, is
// bucketHandlers'.
func objectHandlers(env *op.Env) map[string]HandlerFunc {
	reads := newObjectReads(env)
	writes := newObjectWrites(env)
	return map[string]HandlerFunc{
		"get_obj":         reads.getObject,
		"get_obj_tags":    getObjectTags,
		"get_obj_attrs":   getObjectAttrs,
		"get_acls":        getObjectACL,
		"put_obj":         writes.putObject,
		"copy_obj":        writes.copyObject,
		"delete_obj":      deleteObject,
		"put_acls":        putObjectACL,
		"put_obj_tags":    putObjectTags,
		"delete_obj_tags": deleteObjectTags,
	}
}

// objectWrites serves the object write routes with what radosgw reads once
// at startup: generic_attrs_map, which rgw_rest_init extends with
// rgw_extended_http_attrs (rgw_rest.cc:185-209 at v19.2.6 and v20.2.4).
type objectWrites struct {
	generic []genericAttr
}

func newObjectWrites(env *op.Env) *objectWrites {
	return &objectWrites{generic: genericAttrsFor(env.Conf)}
}
