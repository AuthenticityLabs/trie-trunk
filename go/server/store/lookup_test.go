package store

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func binaryTimeBound(rows []byte, key [6]byte) int {
	lo, hi := 0, len(rows)/indexRowSize
	for lo < hi {
		mid := lo + (hi-lo)/2
		offset := mid * indexRowSize
		if bytes.Compare(rows[offset:offset+len(key)], key[:]) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func boundedBinaryTimeBound(rows []byte, key [6]byte) int {
	count := len(rows) / indexRowSize
	if count == 0 || bytes.Compare(rows[:len(key)], key[:]) >= 0 {
		return 0
	}
	last := (count - 1) * indexRowSize
	if bytes.Compare(rows[last:last+len(key)], key[:]) < 0 {
		return count
	}
	return binaryTimeBound(rows, key)
}

func prefixTimeBound(rows []byte, key [6]byte) int {
	lo, hi := 0, len(rows)/indexRowSize
	for depth, target := range key {
		if lo == hi {
			return lo
		}
		if hi-lo == 1 {
			offset := lo * indexRowSize
			if bytes.Compare(rows[offset+depth:offset+len(key)], key[depth:]) < 0 {
				return hi
			}
			return lo
		}
		first := rows[lo*indexRowSize+depth]
		last := rows[(hi-1)*indexRowSize+depth]
		if target < first {
			return lo
		}
		if target > last {
			return hi
		}
		if first == last {
			continue
		}
		if target > first {
			left, right := lo, hi
			for left < right {
				mid := left + (right-left)/2
				if rows[mid*indexRowSize+depth] < target {
					left = mid + 1
				} else {
					right = mid
				}
			}
			lo = left
		}
		if rows[lo*indexRowSize+depth] != target {
			return lo
		}
		if target < last {
			left, right := lo, hi
			for left < right {
				mid := left + (right-left)/2
				if rows[mid*indexRowSize+depth] <= target {
					left = mid + 1
				} else {
					right = mid
				}
			}
			hi = left
		}
	}
	return lo
}

func timeBoundAlgorithms(rows []byte) []struct {
	name string
	find func([6]byte) int
} {
	columns := transposeTimeColumns(rows)
	return []struct {
		name string
		find func([6]byte) int
	}{
		{"binary", func(key [6]byte) int { return binaryTimeBound(rows, key) }},
		{"binary_bytes", func(key [6]byte) int { return bytewiseTimeBound(rows, key) }},
		{"binary_bounds", func(key [6]byte) int { return boundedBinaryTimeBound(rows, key) }},
		{"prefix", func(key [6]byte) int { return prefixTimeBound(rows, key) }},
		{"column_binary", func(key [6]byte) int { return binaryColumnTimeBound(columns, key) }},
		{"column_prefix", func(key [6]byte) int { return prefixColumnTimeBound(columns, key) }},
	}
}

func timeKey(millis uint64) (key [6]byte) {
	for i := range key {
		key[i] = byte(millis >> (8 * (len(key) - 1 - i)))
	}
	return key
}

func keyMillis(key [6]byte) uint64 {
	var value uint64
	for _, b := range key {
		value = value<<8 | uint64(b)
	}
	return value
}

func lookupRows(count int, timestamp func(int) uint64) []byte {
	random := rand.New(rand.NewPCG(12, 34))
	ids := make([][16]byte, count)
	for i := range ids {
		key := timeKey(timestamp(i))
		copy(ids[i][:6], key[:])
		ids[i][6] = 0x70 | byte(random.Uint32()&15)
		ids[i][7] = byte(random.Uint32())
		binary.BigEndian.PutUint64(ids[i][8:], random.Uint64())
		ids[i][8] = ids[i][8]&0x3f | 0x80
	}
	slices.SortFunc(ids, func(a, b [16]byte) int { return bytes.Compare(a[:], b[:]) })
	rows := make([]byte, count*indexRowSize)
	for i := range ids {
		copy(rows[i*indexRowSize:], ids[i][:])
	}
	return rows
}

func lookupQueries(rows []byte) [][6]byte {
	queries := make([][6]byte, 4096)
	count := len(rows) / indexRowSize
	if count == 0 {
		return queries
	}
	first := keyMillis([6]byte(rows[:6]))
	last := keyMillis([6]byte(rows[(count-1)*indexRowSize:][:6]))
	random := rand.New(rand.NewPCG(56, 78))
	for i := range queries {
		if i%2 == 0 {
			offset := random.IntN(count) * indexRowSize
			queries[i] = [6]byte(rows[offset : offset+6])
		} else {
			queries[i] = timeKey(first + random.Uint64N(last-first+1))
		}
	}
	return queries
}

func checkTimeBounds(t *testing.T, rows []byte) {
	t.Helper()
	algorithms := timeBoundAlgorithms(rows)
	queries := append(lookupQueries(rows), timeKey(0), timeKey(1<<48-1))
	for offset := 0; offset < len(rows); offset += indexRowSize {
		value := keyMillis([6]byte(rows[offset : offset+6]))
		queries = append(queries, timeKey(value))
		if value > 0 {
			queries = append(queries, timeKey(value-1))
		}
		if value < 1<<48-1 {
			queries = append(queries, timeKey(value+1))
		}
	}
	slices.SortFunc(queries, func(a, b [6]byte) int { return bytes.Compare(a[:], b[:]) })
	want := 0
	for _, key := range queries {
		for want < len(rows)/indexRowSize && bytes.Compare(rows[want*indexRowSize:want*indexRowSize+6], key[:]) < 0 {
			want++
		}
		for _, algorithm := range algorithms {
			if got := algorithm.find(key); got != want {
				t.Fatalf("%s key %x: got %d, want %d", algorithm.name, key, got, want)
			}
		}
	}
}

func readLookupFixture(tb testing.TB) []byte {
	tb.Helper()
	path := filepath.Join("..", "..", "..", "logs", "index-test", "1", "2026", "267", "log.idx")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		tb.Skip("local index-test fixture is absent")
	}
	if err != nil {
		tb.Fatal(err)
	}
	if len(data) < 20 || string(data[:4]) != "TIDX" || order.Uint32(data[4:8]) != storageFormat {
		tb.Fatal("invalid index fixture header")
	}
	if int(order.Uint32(data[8:12])) != len(data)-16 || crc32.Checksum(data[16:], crcTable) != order.Uint32(data[12:16]) {
		tb.Fatal("fixture must contain one complete checksummed run")
	}
	rows, err := decodeRun(data[16:])
	if err != nil || len(rows) != 10000 {
		tb.Fatalf("fixture has %d rows: %v", len(rows), err)
	}
	return data[20:]
}

func TestTimeBoundFixture(t *testing.T) {
	checkTimeBounds(t, readLookupFixture(t))
}

func TestTimeBoundDistributions(t *testing.T) {
	base := uint64(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC).UnixMilli())
	for _, test := range []struct {
		name string
		rows []byte
	}{
		{"empty", nil},
		{"single", lookupRows(1, func(int) uint64 { return base })},
		{"uniform", lookupRows(10000, func(i int) uint64 { return base + uint64(i)*8640 })},
		{"bursts", lookupRows(10000, func(i int) uint64 { return base + uint64(i/100)*600000 + uint64(i%100)/8 })},
		{"same_millisecond", lookupRows(10000, func(int) uint64 { return base })},
		{"epoch", lookupRows(256, func(i int) uint64 { return uint64(i / 8) })},
		{"last_timestamp", lookupRows(256, func(i int) uint64 { return 1<<48 - 1 - uint64(i/8) })},
	} {
		t.Run(test.name, func(t *testing.T) { checkTimeBounds(t, test.rows) })
	}
	for shift := 8; shift <= 40; shift += 8 {
		t.Run("carry_"+strconv.Itoa(shift), func(t *testing.T) {
			rows := lookupRows(256, func(i int) uint64 { return 1<<shift - 16 + uint64(i/8) })
			checkTimeBounds(t, rows)
		})
	}
}

func FuzzTimeBound(f *testing.F) {
	f.Add([]byte{0, 1, 255, 0, 8}, uint64(1<<32-16), uint64(1<<32))
	f.Add([]byte{1, 1, 1, 1}, uint64(0), uint64(2))
	f.Add([]byte{}, uint64(1<<48-1), uint64(1<<48-1))
	f.Fuzz(func(t *testing.T, samples []byte, base, target uint64) {
		samples = samples[:min(len(samples), 256)]
		base %= 1<<48 - 256
		rows := lookupRows(len(samples), func(i int) uint64 { return base + uint64(samples[i]) })
		if target&1 == 0 {
			target = base + target%257
		}
		key := timeKey(target & (1<<48 - 1))
		want := 0
		for want < len(rows)/indexRowSize && bytes.Compare(rows[want*indexRowSize:want*indexRowSize+6], key[:]) < 0 {
			want++
		}
		for _, algorithm := range timeBoundAlgorithms(rows) {
			if got := algorithm.find(key); got != want {
				t.Fatalf("%s key %x: got %d, want %d", algorithm.name, key, got, want)
			}
		}
	})
}

var timeBoundResult int

func benchmarkTimeBounds(b *testing.B, rows []byte) {
	queries := lookupQueries(rows)
	for _, algorithm := range timeBoundAlgorithms(rows) {
		b.Run(algorithm.name, func(b *testing.B) {
			b.ReportAllocs()
			result := 0
			for i := 0; i < b.N; i++ {
				result += algorithm.find(queries[i&(len(queries)-1)])
			}
			timeBoundResult = result
		})
	}
}

func BenchmarkTimeBound(b *testing.B) {
	base := uint64(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC).UnixMilli())
	b.Run("fixture_10000", func(b *testing.B) { benchmarkTimeBounds(b, readLookupFixture(b)) })
	b.Run("bursts_10000", func(b *testing.B) {
		benchmarkTimeBounds(b, lookupRows(10000, func(i int) uint64 { return base + uint64(i/100)*600000 + uint64(i%100)/8 }))
	})
	b.Run("same_millisecond_10000", func(b *testing.B) {
		benchmarkTimeBounds(b, lookupRows(10000, func(int) uint64 { return base }))
	})
	b.Run("carry_10000", func(b *testing.B) {
		boundary := ((base >> 32) + 1) << 32
		benchmarkTimeBounds(b, lookupRows(10000, func(i int) uint64 { return boundary - 5000 + uint64(i) }))
	})
	for _, count := range []int{1000, 10000, 100000, 1000000} {
		b.Run("uniform_"+strconv.Itoa(count), func(b *testing.B) {
			benchmarkTimeBounds(b, lookupRows(count, func(i int) uint64 { return base + uint64(i)*86400000/uint64(count) }))
		})
	}
}
