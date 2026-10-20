package objectlock

// DecodeInt is decodeInt with its throw's text, empty when it reads s, for
// the white space no XML text can hold.
func DecodeInt(s string) (int32, string) {
	v, err := decodeInt(s)
	if err != nil {
		return 0, err.what
	}
	return v, ""
}
