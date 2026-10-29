package cache

import "fmt"

const maxHourRecords = 1 << 24

// A process-internal ordering key, never a reconstruction of the ingress UUID.
// The timestamp and per-hour record ordinal are the entire retained identity.
// UUID framing keeps existing live-index and transport key interfaces usable.
func seedKey(ms int64, record uint32) ([16]byte, error) {
	var id [16]byte
	if ms < 0 || ms >= 1<<48 || record >= maxHourRecords {
		return id, fmt.Errorf("invalid Seed identity")
	}
	n := uint64(ms)
	for i := 5; i >= 0; i-- {
		id[i] = byte(n)
		n >>= 8
	}
	id[6] = 0x70
	id[8] = 0x80
	id[13] = byte(record >> 16)
	id[14] = byte(record >> 8)
	id[15] = byte(record)
	return id, nil
}
func seedIndexTime(low uint32, hour int64) (int64, error) {
	ms := (hour & ^int64(0xffffffff)) | int64(low)
	if ms < hour {
		ms += 1 << 32
	}
	if hour < 0 || ms >= hour+TimeHourMS || ms >= 1<<48 {
		return 0, fmt.Errorf("index timestamp outside hour")
	}
	return ms, nil
}
