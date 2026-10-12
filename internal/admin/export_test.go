package admin

// Unregistered is the handler NewHandler gives a route no handler is
// registered for, which a spec registers to exercise that route once every
// route radosgw has is served.
func Unregistered(name string) HandlerFunc { return unregistered(name) }
