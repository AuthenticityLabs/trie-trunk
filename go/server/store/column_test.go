package store

import (
	"bytes"
	"strconv"
	"testing"
	"time"
)

func transposeTimeColumns(rows []byte) []byte {
	count := len(rows) / indexRowSize
	columns := make([]byte, 6*count)
	for i := 0; i < count; i++ {
		for depth := 0; depth < 6; depth++ {
			columns[depth*count+i] = rows[i*indexRowSize+depth]
		}
	}
	return columns
}

func bytewiseTimeBound(rows []byte, key [6]byte) int {
	lo, hi := 0, len(rows)/indexRowSize
	for lo < hi {
		mid := lo + (hi-lo)/2
		offset := mid * indexRowSize
		less := false
		for depth, target := range key {
			value := rows[offset+depth]
			if value != target {
				less = value < target
				break
			}
		}
		if less {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func binaryColumnTimeBound(columns []byte, key [6]byte) int {
	count := len(columns) / len(key)
	lo, hi := 0, count
	for lo < hi {
		mid := lo + (hi-lo)/2
		less := false
		for depth, target := range key {
			value := columns[depth*count+mid]
			if value != target {
				less = value < target
				break
			}
		}
		if less {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func prefixColumnTimeBound(columns []byte, key [6]byte) int {
	count := len(columns) / len(key)
	lo, hi := 0, count
	for depth, target := range key {
		if lo == hi {
			return lo
		}
		column := columns[depth*count : (depth+1)*count]
		first, last := column[lo], column[hi-1]
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
				if column[mid] < target {
					left = mid + 1
				} else {
					right = mid
				}
			}
			lo = left
		}
		if column[lo] != target {
			return lo
		}
		if target < last {
			left, right := lo, hi
			for left < right {
				mid := left + (right-left)/2
				if column[mid] <= target {
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

func TestTransposeTimeColumns(t *testing.T) {
	rows := make([]byte, 3*indexRowSize)
	copy(rows, []byte{1, 2, 3, 4, 5, 6})
	copy(rows[indexRowSize:], []byte{7, 8, 9, 10, 11, 12})
	copy(rows[2*indexRowSize:], []byte{13, 14, 15, 16, 17, 18})
	want := []byte{1, 7, 13, 2, 8, 14, 3, 9, 15, 4, 10, 16, 5, 11, 17, 6, 12, 18}
	if got := transposeTimeColumns(rows); !bytes.Equal(got, want) {
		t.Fatalf("columns %v, want %v", got, want)
	}
}

var timeColumnResult []byte

func BenchmarkTimeColumns(b *testing.B) {
	base := uint64(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC).UnixMilli())
	for _, count := range []int{10000, 100000, 1000000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			rows := lookupRows(count, func(i int) uint64 { return base + uint64(i)*86400000/uint64(count) })
			b.ReportAllocs()
			b.SetBytes(int64(len(rows)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				timeColumnResult = transposeTimeColumns(rows)
			}
		})
	}
}
