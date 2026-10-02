package compression

import (
	"fmt"
	"io"
	"sort"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Window is what RGWGetObj_Decompress::fixup_range derives for a decompressed
// range: which blocks to read, the compressed byte range that holds them, and
// how many decoded bytes to skip and deliver.
type Window struct {
	First, Last      int    // block indexes, inclusive
	QOfs, QLen       uint64 // decoded bytes to skip at the start of First; decoded bytes to deliver
	CompOfs, CompEnd uint64 // compressed range to read, inclusive end as the C++ keeps it
}

// Range is fixup_range (src/rgw/rgw_compression.cc): with partial set the
// blocks holding the decoded bytes [ofs, end] are chosen by old_ofs, else
// every block.
func Range(blocks []meta.CompressionBlock, partial bool, ofs, end uint64) (Window, error) {
	if len(blocks) == 0 {
		// rgw_compression_info_from_attr answers EIO for a block list it
		// decoded empty.
		return Window{}, fmt.Errorf("%w: no compression blocks", ErrCorrupt)
	}
	if end < ofs {
		return Window{}, fmt.Errorf("%w: range %d-%d ends before it starts", ErrCorrupt, ofs, end)
	}
	w := Window{First: 0, Last: len(blocks) - 1}
	if partial {
		w.Last = 0
		if len(blocks) > 1 {
			// upper_bound over blocks[1:] for ofs, then one back
			fb := 1 + sort.Search(len(blocks)-1, func(i int) bool { return ofs < blocks[1+i].OldOfs })
			w.First = fb - 1
			// lower_bound from fb with "old_ofs <= end" as the ordering, then one back
			lb := fb + sort.Search(len(blocks)-fb, func(i int) bool { return blocks[fb+i].OldOfs > end })
			w.Last = lb - 1
		}
	}
	if ofs < blocks[w.First].OldOfs {
		return Window{}, fmt.Errorf("%w: block %d starts at %d, after the range's %d", ErrCorrupt, w.First, blocks[w.First].OldOfs, ofs)
	}
	w.QOfs = ofs - blocks[w.First].OldOfs
	w.QLen = end + 1 - ofs
	w.CompOfs = blocks[w.First].NewOfs
	w.CompEnd = blocks[w.Last].NewOfs + blocks[w.Last].Len - 1
	return w, nil
}

// MaxBlockLen bounds a block's decoded and stored lengths, which come from
// the block map and which a Stream allocates and buffers whole, as radosgw
// does without a bound. radosgw makes each block from one read of at most
// rgw_max_chunk_size (RGWPutObj_ObjStore::get_data, rgw_rest.cc:1071-1078 at
// v19.2.6, :1076-1083 at v20.2.4), an option with no minimum or maximum
// (rgw.yaml.in:84-98 at v19.2.6, :84-101 at v20.2.4) and a default of 4 MiB,
// and multisite sync makes blocks of 512 KiB (rgw_rados.cc:3576 at v19.2.6,
// :3744 at v20.2.4). A zone writes its data in chunks of rgw_max_chunk_size,
// which an OSD refuses past osd_max_write_size, 90 MiB by default
// (PrimaryLogPG.cc:2139-2147 at v19.2.6, :2186-2194 at v20.2.4). 1 GiB is 256
// times the default chunk and eleven times that default write limit, and a
// quarter of what a block's 32-bit length headers can claim.
const MaxBlockLen = 1 << 30

// Stream is RGWGetObj_Decompress::handle_data: compressed bytes arrive in
// order through Write, complete blocks are decoded as they become whole, and
// the decoded bytes of the window are written to w. Buffers are reused across
// blocks. Each block must decode to the length the block map gives it: the
// distance to the next block's old_ofs, and for the last block to orig_size,
// as RGWPutObj_Compress records them. radosgw checks neither and hands on
// whatever a block decodes to. Close reports a short stream. After an error
// every call returns it.
type Stream struct {
	dec      Decoder
	blocks   []meta.CompressionBlock
	origSize uint64
	win      Window
	message  *int32
	w        io.Writer
	next     int    // the block being assembled
	cur      uint64 // compressed offset of the next byte Write takes
	pending  []byte // the bytes of blocks[next] so far, when it arrives in pieces
	out      []byte // decode buffer
	skip     uint64 // decoded bytes still to skip before the window
	left     uint64 // decoded bytes of the window still to deliver
	err      error
}

// NewStream returns a Stream that decodes the blocks of win from compressed
// bytes starting at win.CompOfs. origSize is the object's orig_size.
func NewStream(dec Decoder, blocks []meta.CompressionBlock, origSize uint64, win Window, message *int32, w io.Writer) *Stream {
	return &Stream{
		dec: dec, blocks: blocks, origSize: origSize, win: win, message: message, w: w,
		next: win.First, cur: win.CompOfs, skip: win.QOfs, left: win.QLen,
	}
}

// Write takes the next compressed bytes. It fails with ErrCorrupt on a block
// that does not decode, on blocks that overlap, on a block past MaxBlockLen,
// and on bytes past the window's last block.
func (s *Stream) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n := len(p)
	for len(p) > 0 && s.next <= s.win.Last {
		b := s.blocks[s.next]
		if b.Len > MaxBlockLen {
			s.err = fmt.Errorf("%w: block %d of %d stored bytes, more than %d", ErrCorrupt, s.next, b.Len, MaxBlockLen)
			return n - len(p), s.err
		}
		if s.cur < b.NewOfs {
			// handle_data seeks to each block's new_ofs, passing over any
			// bytes between blocks.
			gap := min(uint64(len(p)), b.NewOfs-s.cur)
			p = p[gap:]
			s.cur += gap
			continue
		}
		if len(s.pending) == 0 && s.cur > b.NewOfs {
			s.err = fmt.Errorf("%w: block %d at %d overlaps the block before it, which ends at %d", ErrCorrupt, s.next, b.NewOfs, s.cur)
			return n - len(p), s.err
		}
		end := b.NewOfs + b.Len
		take := min(uint64(len(p)), end-s.cur)
		var err error
		if len(s.pending) == 0 && take == b.Len {
			err = s.emit(p[:take])
		} else {
			s.pending = append(s.pending, p[:take]...)
			if uint64(len(s.pending)) == b.Len {
				err = s.emit(s.pending)
				s.pending = s.pending[:0]
			}
		}
		p = p[take:]
		s.cur += take
		if err != nil {
			s.err = err
			return n - len(p), err
		}
		if s.cur == end {
			s.next++
		}
	}
	if len(p) > 0 {
		s.err = fmt.Errorf("%w: %d bytes past the last block", ErrCorrupt, len(p))
		return n - len(p), s.err
	}
	return n, nil
}

// emit decodes one whole block and writes the part of it inside the window.
func (s *Stream) emit(block []byte) error {
	want, err := s.decodedLen(s.next)
	if err != nil {
		return err
	}
	out, err := s.dec.Decode(s.out, block, want, s.message)
	if err != nil {
		return fmt.Errorf("block %d: %w", s.next, err)
	}
	if len(out) != want {
		return fmt.Errorf("%w: block %d decoded %d bytes, the block map gives it %d", ErrCorrupt, s.next, len(out), want)
	}
	s.out = out
	skip := min(s.skip, uint64(len(out)))
	s.skip -= skip
	out = out[skip:]
	out = out[:min(uint64(len(out)), s.left)]
	if len(out) == 0 {
		return nil
	}
	if _, err := s.w.Write(out); err != nil {
		return err
	}
	s.left -= uint64(len(out))
	return nil
}

// decodedLen is the length block i decodes to by the block map.
func (s *Stream) decodedLen(i int) (int, error) {
	start, end := s.blocks[i].OldOfs, s.origSize
	if i+1 < len(s.blocks) {
		end = s.blocks[i+1].OldOfs
	}
	if end < start {
		return 0, fmt.Errorf("%w: block %d starts at %d, after the end of its data at %d", ErrCorrupt, i, start, end)
	}
	if end-start > MaxBlockLen {
		return 0, fmt.Errorf("%w: block %d of %d decoded bytes, more than %d", ErrCorrupt, i, end-start, MaxBlockLen)
	}
	return int(end - start), nil //nolint:gosec // MaxBlockLen bounds it above
}

// Close reports io.ErrUnexpectedEOF when the compressed bytes ended before the
// window's last block, and ErrCorrupt when the blocks decoded to fewer bytes
// than the window holds.
func (s *Stream) Close() error {
	if s.err != nil {
		return s.err
	}
	if s.next <= s.win.Last {
		return io.ErrUnexpectedEOF
	}
	if s.left > 0 {
		return fmt.Errorf("%w: blocks decoded %d bytes short of the range", ErrCorrupt, s.left)
	}
	return nil
}
