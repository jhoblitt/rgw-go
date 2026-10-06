package s3

import "github.com/jhoblitt/rgw-go/internal/op"

// objectHandlers is the object-scope routes over env's options; each entry
// binds to object scope.
func objectHandlers(env *op.Env) map[string]HandlerFunc {
	reads := newObjectReads(env)
	return map[string]HandlerFunc{
		"get_obj":       reads.getObject,
		"get_obj_tags":  getObjectTags,
		"get_obj_attrs": getObjectAttrs,
		"get_acls":      getObjectACL,
	}
}
