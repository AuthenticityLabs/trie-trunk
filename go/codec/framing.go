package codec

import (
	"encoding/binary"
	"errors"
	"io"
	"slices"
)

const FrameHeaderSize = 2
const MaxFramePayload = 1<<16 - 1

var ErrFrameSize = errors.New("invalid or oversized CBOR frame length")

// AppendFrame appends [uint16 big-endian payload length][CBOR payload]. The
// length excludes the header. Reusing dst avoids an allocation when capacity
// suffices. CBOR validity is the encoder/decoder's responsibility.
func AppendFrame(dst, payload []byte) ([]byte, error) {
	n := len(payload)
	if n == 0 || n > MaxFramePayload || uint64(len(dst))+FrameHeaderSize+uint64(n) > uint64(^uint(0)>>1) {
		return dst, ErrFrameSize
	}
	start := len(dst)
	dst = slices.Grow(dst, FrameHeaderSize+n)
	dst = dst[:start+FrameHeaderSize+n]
	copy(dst[start+FrameHeaderSize:], payload)
	binary.BigEndian.PutUint16(dst[start:start+FrameHeaderSize], uint16(n))
	return dst, nil
}

// ReadFrame reads exactly one payload, tolerating split headers and bodies.
// maxPayload must be nonzero; the declared length is checked before allocation.
// EOF means a clean frame boundary; truncated headers/bodies return UnexpectedEOF.
// After an invalid length, discard the stream rather than attempting to resync.
func ReadFrame(reader io.Reader, maxPayload uint16) ([]byte, error) {
	if maxPayload == 0 {
		return nil, ErrFrameSize
	}
	var header [FrameHeaderSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(header[:])
	if n == 0 || n > maxPayload || uint64(n) > uint64(^uint(0)>>1) {
		return nil, ErrFrameSize
	}
	payload := make([]byte, int(n))
	if _, err := io.ReadFull(reader, payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return payload, nil
}
