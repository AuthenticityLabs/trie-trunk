package cache

import (
	"bytes"
	"io"
)

func writeEvent(out io.Writer, stream uint32, raw []byte) error {
	if _, err := out.Write(order.AppendUint32([]byte{3}, stream)); err != nil {
		return err
	}
	_, err := out.Write(raw)
	return err
}

func (l *Layout) sameEvent(a, b []byte) bool {
	return bytes.Equal(a, b)
}
