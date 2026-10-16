// Package compression decodes the blocks of an object radosgw compressed, in
// the per-block framing src/compressor gives each of the four codecs it may
// have used: zlib, snappy, zstd and lz4. It also maps a range of the object
// onto those blocks and streams the decoded range, as RGWGetObj_Decompress
// does, and encodes a block in its codec's framing, as each compressor's
// compress does.
package compression
