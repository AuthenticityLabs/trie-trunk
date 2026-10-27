package cache

// Per-event compression is a negotiated envelope, not a new PIL primitive.
// The reserved NULL event-identity mask cannot be a valid input event or a
// stripped population (whose event identity is ABSENT).
import (
	"bytes"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"io"
	"net/http"
	"strings"
)

const CompressionFeature = pil.CompressionFeature

func hasCompression(r *http.Request) bool {
	return strings.Contains(","+r.Header.Get("PIL-Features")+",", ","+CompressionFeature+",")
}
func writeEvent(out io.Writer, l *Layout, stream uint32, raw []byte, compressed bool) error {
	var err error
	if !compressed {
		raw, err = l.ExpandEvent(raw)
		if err != nil {
			return err
		}
	}
	if _, err = out.Write(order.AppendUint32([]byte{3}, stream)); err != nil {
		return err
	}
	_, err = out.Write(raw)
	return err
}

func (l *Layout) sameEvent(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	if !l.IsCompressed(a) && !l.IsCompressed(b) {
		return false
	}
	var x, y Frame
	if l.Scan(a, &x) != nil || l.Scan(b, &y) != nil {
		return false
	}
	for i, v := range x.Values {
		if !l.equal(i, v, y.Values[i]) {
			return false
		}
	}
	return true
}
