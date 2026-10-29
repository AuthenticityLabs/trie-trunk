package codec

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"testing"
	"time"
)

func TestUUIDv7RFCVector(t *testing.T) {
	// RFC 9562 Appendix A.6.
	raw, err := hex.DecodeString("017f22e279b07cc398c4dc0c0c07398f")
	if err != nil {
		t.Fatal(err)
	}
	id := UUIDv7(raw)
	if !id.Valid() || uuidTimestamp(id) != 1645557742000 || id.String() != "017f22e2-79b0-7cc3-98c4-dc0c0c07398f" {
		t.Fatalf("RFC vector failed: %s %v", id, uuidTimestamp(id))
	}
	data, err := MarshalCompact(id, PIL)
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err = pil.UnmarshalObject(data, &got); err != nil || !bytes.Equal(got, raw) {
		t.Fatal("PIL UUID bytes", err)
	}
	data, err = json.Marshal(id)
	if err != nil || string(data) != `"017f22e2-79b0-7cc3-98c4-dc0c0c07398f"` {
		t.Fatalf("unexpected JSON: %s %v", data, err)
	}
}

func TestUUIDv7Generation(t *testing.T) {
	at := time.UnixMilli(1645557742000).Add(987 * time.Microsecond)
	seen := make(map[UUIDv7]bool)
	for i := 0; i < 1000; i++ {
		id, err := NewUUIDv7(at)
		if err != nil {
			t.Fatal(err)
		}
		if !id.Valid() || uuidTimestamp(id) != at.UnixMilli() {
			t.Fatalf("invalid generated UUID: %s", id)
		}
		if seen[id] {
			t.Fatal("duplicate UUID at same timestamp")
		}
		seen[id] = true
	}
	if _, err := NewUUIDv7(time.UnixMilli(-1)); err == nil {
		t.Fatal("accepted negative timestamp")
	}
}

// Only tests interpret timestamps; the producer does not model derived times.
func uuidTimestamp(id UUIDv7) int64 {
	return int64(id[0])<<40 | int64(id[1])<<32 | int64(id[2])<<24 | int64(id[3])<<16 | int64(id[4])<<8 | int64(id[5])
}
