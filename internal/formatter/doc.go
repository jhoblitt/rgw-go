// Package formatter transcribes ceph's JSONFormatter, XMLFormatter and
// HTMLFormatter (src/common/Formatter.cc and HTMLFormatter.cc, the same at
// v19.2.6 and v20.2.4), the writers behind radosgw's admin responses, so a
// dump written once renders every format byte for byte as radosgw's does.
package formatter
