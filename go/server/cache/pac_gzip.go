package cache

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sync"
)

// SealedGzip is created only by SealGzip. Its independent DEFLATE stream ends
// at a byte-aligned sync-flush boundary before an empty final block. This is
// ordinary gzip, but the boundary lets us reuse the compressed data in one
// larger gzip stream. Arbitrary third-party .gz files are not spliceable here.
type SealedGzip struct {
	bytes  []byte
	length uint64
	crc    uint32
}

func (s *SealedGzip) Bytes() []byte { return s.bytes }

var gzipHeader = []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 255}
var gzipFinal = []byte{1, 0, 0, 255, 255} // byte-aligned BFINAL=1, empty stored block

func SealGzip(raw []byte) (*SealedGzip, error) {
	return SealGzipLevel(raw, gzip.BestSpeed)
}

// Each pool retains its writer's level across Reset. Allocate writers lazily;
// policies that never use a level incur no compressor allocation for it.
var pacGzipWriters = func() (p [10]*sync.Pool) {
	p[gzip.BestSpeed] = &fastGzipWriters
	for level := 2; level <= gzip.BestCompression; level++ {
		p[level] = &sync.Pool{New: func() any {
			w, _ := gzip.NewWriterLevel(io.Discard, level)
			return w
		}}
	}
	return
}()

// SealGzipLevel changes sealing effort only. All levels produce the same
// spliceable profile and can coexist in one response without recompression.
func SealGzipLevel(raw []byte, level int) (*SealedGzip, error) {
	if level < gzip.BestSpeed || level > gzip.BestCompression {
		return nil, fmt.Errorf("PAC gzip level must be between 1 and 9")
	}
	var out bytes.Buffer
	pool := pacGzipWriters[level]
	w := pool.Get().(*gzip.Writer)
	w.Reset(&out)
	_, err := w.Write(raw)
	if err == nil {
		err = w.Flush()
	} // byte alignment and BFINAL=0
	if closeErr := w.Close(); err == nil {
		err = closeErr
	}
	w.Reset(io.Discard)
	pool.Put(w)
	if err != nil {
		return nil, err
	}
	return RestoreSealedGzip(out.Bytes(), uint64(len(raw)))
}

// RestoreSealedGzip borrows an immutable stored extent produced by SealGzip.
// The enclosing storage binding must identify this producer profile and verify
// its integrity. Terminal checks alone do NOT certify arbitrary gzip input.
// Restore reads only framing/checksum bytes; it never decodes the PAC body.
func RestoreSealedGzip(b []byte, decodedLength uint64) (*SealedGzip, error) {
	if len(b) < 28 || !bytes.Equal(b[:3], gzipHeader[:3]) || b[3] != 0 ||
		!bytes.Equal(b[len(b)-13:len(b)-8], gzipFinal) ||
		!bytes.Equal(b[len(b)-17:len(b)-13], []byte{0, 0, 255, 255}) ||
		order.Uint32(b[len(b)-4:]) != uint32(decodedLength) {
		return nil, fmt.Errorf("gzip extent does not match sealed PAC profile")
	}
	return &SealedGzip{bytes: b, length: decodedLength, crc: order.Uint32(b[len(b)-8:])}, nil
}

// GzipPACWriter produces ONE gzip member from independently sealed PACs and
// response-specific controls. Compressed payload bytes are copied unchanged.
// Only the shared outer header/final block/checksum trailer are constructed.
type GzipPACWriter struct {
	out             io.Writer
	raw             bytes.Buffer
	started, closed bool
	length          uint64
	crc             uint32
	err             error
}

func NewGzipPACWriter(out io.Writer) *GzipPACWriter { return &GzipPACWriter{out: out} }
func (w *GzipPACWriter) write(b []byte) error {
	if w.err != nil {
		return w.err
	}
	n, err := w.out.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	w.err = err
	return err
}
func (w *GzipPACWriter) Write(b []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("gzip PAC response closed")
	}
	if w.err != nil {
		return 0, w.err
	}
	n, _ := w.raw.Write(b)
	if w.raw.Len() >= 128<<10 {
		return n, w.Flush()
	}
	return n, nil
}
func (w *GzipPACWriter) append(s *SealedGzip) error {
	if w.closed {
		return fmt.Errorf("gzip PAC response closed")
	}
	if s == nil {
		return fmt.Errorf("missing sealed PAC")
	}
	if !w.started {
		if err := w.write(gzipHeader); err != nil {
			return err
		}
		w.started = true
	}
	if ^uint64(0)-w.length < s.length {
		return fmt.Errorf("gzip response size overflow")
	}
	// Skip the per-PAC gzip header, empty final block and trailer. Every byte of
	// compressed PAC content and its sync-flush block is retained.
	if err := w.write(s.bytes[10 : len(s.bytes)-13]); err != nil {
		return err
	}
	w.crc = combineCRC32(w.crc, s.crc, s.length)
	w.length += s.length
	return nil
}
func (w *GzipPACWriter) Flush() error {
	if w.err != nil {
		return w.err
	}
	if w.raw.Len() == 0 {
		return nil
	}
	s, err := SealGzip(w.raw.Bytes())
	if err != nil {
		return err
	}
	w.raw.Reset()
	return w.append(s)
}

// StoredPAC sends a previously sealed PAC; it never invokes a decoder or encoder.
func (w *GzipPACWriter) StoredPAC(s *SealedGzip) error {
	if _, err := w.Write([]byte{7}); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return w.append(s)
}

// PAC is used for newly assembled query pages. Sealed storage callers use
// StoredPAC instead, avoiding repeated compression.
func (w *GzipPACWriter) PAC(p *PAC) error {
	s, err := SealGzip(p.Bytes)
	if err != nil {
		return err
	}
	return w.StoredPAC(s)
}
func (w *GzipPACWriter) Close() error {
	if w.closed {
		return w.err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if !w.started {
		if err := w.write(gzipHeader); err != nil {
			return err
		}
	}
	w.closed = true
	if err := w.write(gzipFinal); err != nil {
		return err
	}
	var tail [8]byte
	order.PutUint32(tail[:4], w.crc)
	order.PutUint32(tail[4:], uint32(w.length))
	return w.write(tail[:])
}

// IEEE CRC concatenation applies the zero-byte polynomial operator to the first
// CRC, then XORs the second. The operator's 64 powers cover a U64 byte length.
// No scan of uncompressed data is needed at delivery time.
var crcBytePowers = func() (p [64][32]uint32) {
	var bit [32]uint32
	bit[0] = 0xedb88320
	for i := 1; i < 32; i++ {
		bit[i] = 1 << uint(i-1)
	}
	square := func(m [32]uint32) (r [32]uint32) {
		for i := range r {
			r[i] = crcTimes(m, m[i])
		}
		return
	}
	p[0] = square(square(square(bit)))
	for i := 1; i < len(p); i++ {
		p[i] = square(p[i-1])
	}
	return
}()

func crcTimes(m [32]uint32, v uint32) (r uint32) {
	for i := 0; v != 0; i++ {
		if v&1 != 0 {
			r ^= m[i]
		}
		v >>= 1
	}
	return
}
func combineCRC32(a, b uint32, length uint64) uint32 {
	for i := 0; length != 0; i++ {
		if length&1 != 0 {
			a = crcTimes(crcBytePowers[i], a)
		}
		length >>= 1
	}
	return a ^ b
}
