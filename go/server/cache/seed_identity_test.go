package cache

import (
	"testing"
)

func TestSeedOrdinalKey(t *testing.T) {
	for _, ms := range []int64{0, 1, 1<<32 - 1, 1 << 32, 1793232000000, 1<<48 - 1} {
		for _, ordinal := range []uint32{0, 1, 255, 256, 65535, 65536, 0xffffff} {
			id, err := seedKey(ms, ordinal)
			if err != nil || eventTime(id[:]) != ms || uint32(id[13])<<16|uint32(id[14])<<8|uint32(id[15]) != ordinal {
				t.Fatalf("invalid key %x: %v", id, err)
			}
		}
	}
	if _, err := seedKey(0, 1<<24); err == nil {
		t.Fatal("accepted overflowing record")
	}
}
func TestSeedIndexTimeRollover(t *testing.T) {
	const boundary = int64(417) << 32
	hour := boundary / TimeHourMS * TimeHourMS
	for _, ms := range []int64{hour, boundary - 1, boundary, boundary + 1, hour + TimeHourMS - 1} {
		got, err := seedIndexTime(uint32(ms), hour)
		if err != nil || got != ms {
			t.Fatalf("%d != %d: %v", got, ms, err)
		}
	}
	if _, err := seedIndexTime(uint32(hour+TimeHourMS), hour); err == nil {
		t.Fatal("accepted next hour")
	}
}
