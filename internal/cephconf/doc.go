// Package cephconf reads Ceph's configuration the way radosgw sees it: every
// option radosgw configures is read back through librados after connect, so
// the command line, ceph.conf and the mon config store apply in librados's
// order, and is typed from the text librados renders it as.
package cephconf
