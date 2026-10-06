// Package admin is radosgw's admin REST API, RGWRESTMgr_Admin and the
// managers registered under rgw_admin_entry: it parses a request's resource
// and arguments as the admin handlers do, dispatches it to the op radosgw
// would run, authenticates it, and renders the response and error document
// in JSON, XML or HTML through internal/formatter, as radosgw's formatters
// write them.
package admin
