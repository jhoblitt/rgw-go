package meta

import (
	"encoding/json"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// CompressionInfo is RGWCompressionInfo, the user.rgw.compression attr of a
// compressed object: the compressor, the uncompressed size, and where each
// compressed block landed. The zero value lacks the C++ member defaults;
// NewCompressionInfo has them.
type CompressionInfo struct {
	Type     string `json:"compression_type"`
	OrigSize uint64 `json:"orig_size"`
	// CompressorMessage is the compressor's per-object message, when it
	// produced one.
	CompressorMessage *int32             `json:"compressor_message,omitempty"`
	Blocks            []CompressionBlock `json:"blocks"`
}

// NewCompressionInfo returns what RGWCompressionInfo's constructor leaves: the
// type "none".
func NewCompressionInfo() CompressionInfo { return CompressionInfo{Type: "none"} }

// MarshalJSON renders RGWCompressionInfo::dump, which writes an empty block
// list as [].
func (c CompressionInfo) MarshalJSON() ([]byte, error) {
	type plain CompressionInfo
	p := plain(c)
	if p.Blocks == nil {
		p.Blocks = []CompressionBlock{}
	}
	return json.Marshal(p)
}

// Encode mirrors RGWCompressionInfo::encode, ENCODE_START(2, 1).
func (c CompressionInfo) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(c.Type)
	e.U64(c.OrigSize)
	denc.EncodeOptional(e, c.CompressorMessage, (*denc.Encoder).I32)
	denc.EncodeSlice(e, c.Blocks, func(e *denc.Encoder, b CompressionBlock) { b.Encode(e, r) })
	e.EndStruct(f)
}

// DecodeCompressionInfo mirrors RGWCompressionInfo::decode, DECODE_START(2);
// version 1 has no compressor message.
func DecodeCompressionInfo(d *denc.Decoder) CompressionInfo {
	h := d.BeginStruct(2)
	c := NewCompressionInfo()
	c.Type = d.String()
	c.OrigSize = d.U64()
	if h.Version >= 2 {
		c.CompressorMessage = denc.DecodeOptional(d, (*denc.Decoder).I32)
	}
	c.Blocks = denc.DecodeSlice(d, DecodeCompressionBlock)
	d.EndStruct(h)
	return c
}

// CompressionBlock is compression_block: a block's offset in the original
// data, its offset in the stored data, and its stored length.
type CompressionBlock struct {
	OldOfs uint64 `json:"old_ofs"`
	NewOfs uint64 `json:"new_ofs"`
	Len    uint64 `json:"len"`
}

// Encode mirrors compression_block::encode, ENCODE_START(1, 1).
func (b CompressionBlock) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(b.OldOfs)
	e.U64(b.NewOfs)
	e.U64(b.Len)
	e.EndStruct(f)
}

// DecodeCompressionBlock mirrors compression_block::decode, DECODE_START(1).
func DecodeCompressionBlock(d *denc.Decoder) CompressionBlock {
	h := d.BeginStruct(1)
	var b CompressionBlock
	b.OldOfs = d.U64()
	b.NewOfs = d.U64()
	b.Len = d.U64()
	d.EndStruct(h)
	return b
}
