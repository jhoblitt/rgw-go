package op

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// The read sizes of read_all_chunked_input (rgw_rest.cc:1501-1535 at
// v19.2.6, :1506-1540 at v20.2.4).
const (
	readChunk    = 4096
	maxReadChunk = 128 << 10
)

// ReadParamBody is rgw_rest_read_all_input (rgw_rest.cc:1537-1578 at
// v19.2.6, :1542-1583 at v20.2.4) as RGWOp::read_all_input calls it, which
// allows a chunked body whatever its caller asks (rgw_op.h:215-227 at
// v19.2.6, :229-241 at v20.2.4; docs/ceph-upstream-bugs.md,
// "RGWOp::read_all_input ignores its allow_chunked argument"). A request
// without a Content-Length that is not chunked is MissingContentLength. A
// Content-Length over maxLen is InvalidRange unread; otherwise that many
// bytes are read. A chunked body is read as read_all_chunked_input reads
// it, in reads of 4 KiB doubling to 128 KiB, and is InvalidRange once a
// read fills while the bytes asked for so far exceed maxLen. Either way the body is read through what
// authentication left in r.Body to its final Read, whose error is returned,
// the payload's verdict among them: radosgw drops that verdict
// (docs/exclusions.md, "A body read whole is checked against its signed
// hash"). A body that ends short of its length is RequestTimeout.
func ReadParamBody(r *Request, maxLen uint64) ([]byte, error) {
	src := r.Body
	if src == nil {
		src = http.NoBody
	}
	switch {
	case r.ContentLength < 0:
		return readChunkedParam(src, maxLen)
	case r.ContentLength == 0 && r.Header.Get("Content-Length") == "":
		return nil, ErrMissingContentLength
	case uint64(r.ContentLength) > maxLen:
		return nil, fmt.Errorf("%w: a %d-byte body over rgw_max_put_param_size %d", ErrInvalidRange, r.ContentLength, maxLen)
	}
	body, err := io.ReadAll(io.LimitReader(src, r.ContentLength+1))
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF):
		return nil, fmt.Errorf("%w: the body ended short of its length", ErrRequestTimeout)
	case err != nil:
		return nil, err
	}
	// recv_body reads the length and no more.
	return body[:min(int64(len(body)), r.ContentLength)], nil
}

// readChunkedParam is read_all_chunked_input. Its running total is kept in
// 64 bits where radosgw keeps an int, which differs only for a maxLen of
// 2 GiB or more, and bounds the read by maxLen and one read for any maxLen.
func readChunkedParam(src io.Reader, maxLen uint64) ([]byte, error) {
	need := readChunk
	total := uint64(need)
	var body []byte
	for {
		buf := make([]byte, need)
		n, err := fill(src, buf)
		body = append(body, buf[:n]...)
		switch {
		case errors.Is(err, io.ErrUnexpectedEOF):
			return nil, fmt.Errorf("%w: the body ended short of its framing", ErrRequestTimeout)
		case err != nil && !errors.Is(err, io.EOF):
			return nil, err
		case n < need:
			return body, nil
		}
		// A read that fills its buffer is checked and followed by another,
		// as radosgw does whether or not the body ended with it.
		if need < maxReadChunk {
			need *= 2
		}
		if total > maxLen {
			return nil, fmt.Errorf("%w: a chunked body over rgw_max_put_param_size %d", ErrInvalidRange, maxLen)
		}
		total += uint64(need)
	}
}

// fill reads into buf until it is full, as recv_body fills its buffer, or
// until src ends, which is io.EOF however many bytes were read; any other
// error src returns comes back as it is.
func fill(src io.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		k, err := src.Read(buf[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
