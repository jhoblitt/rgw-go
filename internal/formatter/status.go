package formatter

// statusNames is radosgw's http_codes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L44-L88 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L44-L88 at v20.2.4).
var statusNames = map[int]string{
	100: "Continue", 200: "OK", 201: "Created", 202: "Accepted", 204: "No Content",
	205: "Reset Content", 206: "Partial Content", 207: "Multi Status", 208: "Already Reported",
	300: "Multiple Choices", 301: "Moved Permanently", 302: "Found", 303: "See Other",
	304: "Not Modified", 305: "User Proxy", 306: "Switch Proxy", 307: "Temporary Redirect",
	308: "Permanent Redirect", 400: "Bad Request", 401: "Unauthorized", 402: "Payment Required",
	403: "Forbidden", 404: "Not Found", 405: "Method Not Allowed", 406: "Not Acceptable",
	407: "Proxy Authentication Required", 408: "Request Timeout", 409: "Conflict", 410: "Gone",
	411: "Length Required", 412: "Precondition Failed", 413: "Request Entity Too Large",
	414: "Request-URI Too Long", 415: "Unsupported Media Type",
	416: "Requested Range Not Satisfiable", 417: "Expectation Failed",
	422: "Unprocessable Entity", 498: "Rate Limited", 500: "Internal Server Error",
	501: "Not Implemented", 503: "Slow Down", 507: "Insufficient Storage",
}

// StatusName is the name radosgw's http_codes gives status, which dump_errno
// hands the formatter with the status and HTMLFormatter prints in its status
// page; "" for a status the table lacks, which SetStatus takes for no name.
func StatusName(status int) string { return statusNames[status] }
