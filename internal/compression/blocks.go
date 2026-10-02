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

// Stream is RGWGetObj_Decompress::handle_data: compressed bytes arrive in
// order through Write, complete blocks are decoded as they become whole, and
// the decoded bytes of the window are written to w. Buffers are reused across
// blocks. Close reports a short stream. After an error every call returns it.
type Stream struct {
	dec     Decoder
	blocks  []meta.CompressionBlock
	win     Window
	message *int32
	w       io.Writer
	next    int    // the block being assembled
	cur     uint64 // compressed offset of the next byte Write takes
	pending []byte // the bytes of blocks[next] so far, when it arrives in pieces
	out     []byte // decode buffer
	skip    uint64 // decoded bytes still to skip before the window
	left    uint64 // decoded bytes of the window still to deliver
	err     error
}

// NewStream returns a Stream that decodes the blocks of win from compressed
// bytes starting at win.CompOfs.
func NewStream(dec Decoder, blocks []meta.CompressionBlock, win Window, message *int32, w io.Writer) *Stream {
	return &Stream{
		dec: dec, blocks: blocks, win: win, message: message, w: w,
		next: win.First, cur: win.CompOfs, skip: win.QOfs, left: win.QLen,
	}
}

// Write takes the next compressed bytes. It fails with ErrCorrupt on a block
// that does not decode, on blocks that overlap, and on bytes past the window's
// last block.
func (s *Stream) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n := len(p)
	for len(p) > 0 && s.next <= s.win.Last {
		b := s.blocks[s.next]
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
	out, err := s.dec.Decode(s.out, block, s.message)
	if err != nil {
		return fmt.Errorf("block %d: %w", s.next, err)
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
