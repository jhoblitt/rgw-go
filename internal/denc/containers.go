package denc

import (
	"cmp"
	"maps"
	"slices"
)

// EncodeSlice writes a std::vector or std::list: a u32 count then each element.
func EncodeSlice[T any](e *Encoder, xs []T, enc func(*Encoder, T)) {
	e.length(len(xs))
	for _, x := range xs {
		enc(e, x)
	}
}

// DecodeSlice reads a u32 count then that many elements. An empty or failed
// decode returns nil.
func DecodeSlice[T any](d *Decoder, dec func(*Decoder) T) []T {
	n := d.count()
	if n == 0 {
		return nil
	}
	xs := make([]T, 0, d.capHint(n))
	for range n {
		x := dec(d)
		if d.err != nil {
			return nil
		}
		xs = append(xs, x)
	}
	return xs
}

// EncodeMap writes a std::map: a u32 count then each key and value, keys in
// sorted order as std::map iterates them.
func EncodeMap[K cmp.Ordered, V any](e *Encoder, m map[K]V, encK func(*Encoder, K), encV func(*Encoder, V)) {
	e.length(len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		encK(e, k)
		encV(e, m[k])
	}
}

// DecodeMap reads a u32 count then that many keys and values. The first value
// for a duplicated key wins, as std::map's emplace keeps it. An empty or
// failed decode returns nil.
func DecodeMap[K comparable, V any](d *Decoder, decK func(*Decoder) K, decV func(*Decoder) V) map[K]V {
	n := d.count()
	if n == 0 {
		return nil
	}
	m := make(map[K]V, d.capHint(n))
	for range n {
		k := decK(d)
		v := decV(d)
		if d.err != nil {
			return nil
		}
		if _, dup := m[k]; !dup {
			m[k] = v
		}
	}
	return m
}

// EncodeStringMap writes the attrs shape, map<string, bufferlist>: sorted
// keys with Bytes32 values.
func EncodeStringMap(e *Encoder, m map[string][]byte) {
	EncodeMap(e, m, (*Encoder).String, (*Encoder).Bytes32)
}

// DecodeStringMap reads the attrs shape, map<string, bufferlist>.
func DecodeStringMap(d *Decoder) map[string][]byte {
	return DecodeMap(d, (*Decoder).String, (*Decoder).Bytes32)
}

// EncodeOptional writes a std::optional: a u8 presence flag then the value
// when v is non-nil.
func EncodeOptional[T any](e *Encoder, v *T, enc func(*Encoder, T)) {
	if v == nil {
		e.Bool(false)
		return
	}
	e.Bool(true)
	enc(e, *v)
}

// DecodeOptional reads a u8 presence flag then the value when present. An
// absent value or a failed decode returns nil.
func DecodeOptional[T any](d *Decoder, dec func(*Decoder) T) *T {
	if !d.Bool() {
		return nil
	}
	v := dec(d)
	if d.err != nil {
		return nil
	}
	return &v
}

// maxPrealloc bounds the elements reserved up front for a decoded count;
// larger containers grow through append as their elements actually decode.
const maxPrealloc = 1024

// capHint sizes a container for n elements without trusting a hostile count:
// each element takes at least one byte, and no more than maxPrealloc elements
// are reserved whatever their size.
func (d *Decoder) capHint(n int) int {
	return min(n, d.Remaining(), maxPrealloc)
}

// count reads a u32 element count, returning 0 after a failure.
func (d *Decoder) count() int {
	n := int(d.U32())
	if d.err != nil {
		return 0
	}
	return n
}
