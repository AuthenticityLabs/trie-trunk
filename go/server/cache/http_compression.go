package cache

import (
	"fmt"
	pilcodec "github.com/AuthenticityLabs/pill-packer/go/codec"
	"io"
	"net/http"
	"strings"
)

// HTTPCompressionFeature describes request-body compression, independently of
// the PIL layout and the older per-event compression binding.
const HTTPCompressionFeature = "http-gzip-ingress"

func ingressReader(r *http.Request) (io.Reader, func(), error) {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
		return r.Body, func() {}, nil
	case "gzip":
		z, err := pilcodec.GzipReader(r.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid gzip request: %w", err)
		}
		// RFC 1952 permits consecutive members. Read through EOF so a damaged
		// trailer or an incomplete final member cannot receive a success ack.
		return z, func() { _ = z.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("unsupported Content-Encoding")
	}
}
