package codec

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// UUIDv7 is an RFC 9562 identifier: 48 Unix-millisecond timestamp bits and
// 74 random bits, plus version and variant. No global counter/lock is needed.
// Equal-millisecond identifiers are not guaranteed to sort by creation order.
type UUIDv7 [16]byte

func NewUUIDv7(at time.Time) (UUIDv7, error) {
	ms := at.UnixMilli()
	if ms < 0 || ms > (1<<48)-1 {
		return UUIDv7{}, fmt.Errorf("timestamp outside UUIDv7 range")
	}
	var id UUIDv7
	if _, err := rand.Read(id[6:]); err != nil {
		return UUIDv7{}, err
	}
	id[0], id[1], id[2], id[3], id[4], id[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	id[6] = id[6]&0x0f | 0x70
	id[8] = id[8]&0x3f | 0x80
	return id, nil
}

func (id UUIDv7) Valid() bool { return id[6]>>4 == 7 && id[8]>>6 == 2 }
func (id UUIDv7) String() string {
	var text [36]byte
	id.format(text[:])
	return string(text[:])
}
func (id UUIDv7) format(text []byte) {
	hex.Encode(text[0:8], id[0:4])
	text[8] = '-'
	hex.Encode(text[9:13], id[4:6])
	text[13] = '-'
	hex.Encode(text[14:18], id[6:8])
	text[18] = '-'
	hex.Encode(text[19:23], id[8:10])
	text[23] = '-'
	hex.Encode(text[24:36], id[10:16])
}
func (id UUIDv7) MarshalJSON() ([]byte, error) {
	data := make([]byte, 38)
	data[0], data[37] = '"', '"'
	id.format(data[1:37])
	return data, nil
}

// CBOR's fixed byte-array encoding emits a 16-byte byte string directly; no
// MarshalCBOR method or intermediate string is needed on the producer path.
