package meta

import "crypto/rand"

// ChooseFrom is choose_from
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L33-L42 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L33-L42 at v20.2.4):
// n characters of table, each picked by a random byte.
//
// It draws as radosgw does where char is signed, as on x86-64:
// static_cast<unsigned> sign-extends a byte of 0x80 or more to 32 bits before
// the modulo. A radosgw built where char is unsigned, as on aarch64, ppc64le
// and s390x, reads the byte unsigned. The two reads pick other characters
// only for a table whose length does not divide 2^32 - 256, which among
// radosgw's tables is gen_rand_alphanumeric_plain's 62, and as the draw is
// random no client can tell them apart.
func ChooseFrom(table string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return chooseBytes(table, b)
}

// chooseBytes replaces each byte of b with the character of table it picks,
// read as ChooseFrom's godoc describes.
func chooseBytes(table string, b []byte) string {
	for i, c := range b {
		pos := uint32(int32(int8(c)))        //nolint:gosec // static_cast<unsigned>(char) sign-extends as this does
		b[i] = table[pos%uint32(len(table))] //nolint:gosec // radosgw's tables are at most 64 characters long
	}
	return string(b)
}
