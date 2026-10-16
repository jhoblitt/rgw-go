package compression

// ZstdEncodersMadeForTest is the number of zstd encoders made so far.
func ZstdEncodersMadeForTest() int64 { return zstdEncodersMade.Load() }
