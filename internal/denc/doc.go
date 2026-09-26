// Package denc reads and writes Ceph's wire encoding: little-endian integers,
// length-prefixed strings and bufferlists, containers, times, and the versioned
// struct framing of ENCODE_START/DECODE_START. It covers exactly the subset the
// RGW stored types and object-class requests use; feature-dependent encoding,
// encode_nohead, and DENC framing are out of scope.
package denc
