package pil

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/store"
)

const (
	Protocol      = "trie-trunk.v1"
	VersionHeader = "Trunk-Version"
	DigestHeader  = "Trunk-Digest"
)

type control struct {
	Op       string         `json:"op"`
	Revision codec.Revision `json:"revision,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func ValidateRequest(r *http.Request) error {
	_, err := requestRevision(r)
	return err
}

func requestRevision(r *http.Request) (codec.Revision, error) {
	var rev codec.Revision
	version, err := codec.ParseVersion(r.Header.Get(VersionHeader))
	if err != nil {
		return rev, err
	}
	digest, err := hex.DecodeString(r.Header.Get(DigestHeader))
	if err != nil || len(digest) != len(rev.Digest) {
		return rev, fmt.Errorf("invalid digest")
	}
	rev.Version = version
	copy(rev.Digest[:], digest)
	return rev, nil
}

func revisionHeaders(h http.Header, r codec.Revision) {
	h.Set(VersionHeader, codec.VersionString(r.Version))
	h.Set(DigestHeader, hex.EncodeToString(r.Digest[:]))
}

func singleFrame(data []byte) ([]byte, error) {
	if len(data) < codec.FrameHeaderSize {
		return nil, io.ErrUnexpectedEOF
	}
	length := int(binary.BigEndian.Uint16(data[:codec.FrameHeaderSize]))
	if length == 0 {
		return nil, codec.ErrFrameSize
	}
	if len(data) < codec.FrameHeaderSize+length {
		return nil, io.ErrUnexpectedEOF
	}
	if len(data) != codec.FrameHeaderSize+length {
		return nil, fmt.Errorf("expected one frame")
	}
	return data[codec.FrameHeaderSize:], nil
}

func cleanError(err error) string {
	switch {
	case errors.Is(err, store.ErrConflict):
		return "schema conflict"
	case errors.Is(err, store.ErrLimit):
		return "capacity limit"
	case errors.Is(err, store.ErrInvalid):
		return "invalid record"
	default:
		return "ingestion failure"
	}
}
