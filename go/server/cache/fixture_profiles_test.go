package cache

import (
	"bytes"
	"math"
	"testing"
)

func TestMeasurementIntegerWidths(t *testing.T) {
	for _, tc := range []struct {
		max  uint64
		kind string
	}{{0, "U8"}, {255, "U8"}, {256, "U16"}, {65535, "U16"}, {65536, "U32"}, {math.MaxUint32, "U32"}, {uint64(math.MaxUint32) + 1, "U64"}, {math.MaxUint64, "U64"}} {
		if got := smallestUnsigned(tc.max); got != tc.kind {
			t.Fatalf("bound %d: got %s, want %s", tc.max, got, tc.kind)
		}
	}
	// A larger collection must widen revisions instead of truncating them.
	if smallestUnsigned(measurementIntegerBounds(2550000)[2]) != "U8" || smallestUnsigned(measurementIntegerBounds(2550001)[2]) != "U16" {
		t.Fatal("revision range did not follow collection size")
	}
}

func TestMeasurementProfiles(t *testing.T) {
	for _, profile := range []string{"range-sized", "full-u64"} {
		t.Run(profile, func(t *testing.T) {
			const n = 2048
			l, full, _, _, hash := measurementFixtureProfile(t, n, false, profile)
			_, sparse, _, _, _ := measurementFixtureProfile(t, n, true, profile)
			_, _, _, _, repeat := measurementFixtureProfile(t, n, false, profile)
			if hash != repeat {
				t.Fatal("fixture is not reproducible")
			}
			if l.Duration != -1 || len(l.Definition.Resource.Aggregations) != 0 {
				t.Fatal("codec fixture enabled a duration aggregation constraint")
			}
			for field, max := range measurementIntegerBounds(n) {
				want := smallestUnsigned(max)
				if profile == "full-u64" {
					want = "U64"
				}
				if l.Definition.Fields[field].Type != want {
					t.Fatalf("field %d has wrong range width", field)
				}
			}
			for _, field := range []int{4, 5, 6} {
				if l.Widths[field] != 1 {
					t.Fatal("dictionary ordinal was widened")
				}
			}
			direct, err := CompilePositionalCBOR(l)
			if err != nil {
				t.Fatal(err)
			}
			var a, b Frame
			var decoded CBORFrame
			highHalf := map[int]int{}
			for i, raw := range full {
				if err = l.Scan(raw, &a); err != nil {
					t.Fatal(err)
				}
				if err = l.Scan(sparse[i], &b); err != nil {
					t.Fatal(err)
				}
				for field, max := range measurementIntegerBounds(n) {
					value := unsigned(a.Values[field].Bytes)
					if profile == "range-sized" && value > max {
						t.Fatal("fixture exceeded declared generator range")
					}
					if value >= 1<<63 {
						highHalf[field]++
					}
				}
				for field, value := range b.Values {
					want := field < 3 || field == 6 || field == l.Time || field == 8 && i%2 == 0
					if (value.State != Absent) != want {
						t.Fatal("sparse fixture changed its field membership")
					}
					if want && !bytes.Equal(value.Bytes, a.Values[field].Bytes) {
						t.Fatal("full/sparse fixture values differ")
					}
				}
				// Both decoders must preserve every value, including high U64 bits
				// and each newly narrowed integer, for both full and PATCH frames.
				for _, frame := range []Frame{a, b} {
					record, err := fixtureCBOR(l, frame.Values)
					if err != nil {
						t.Fatal(err)
					}
					cbor, err := marshalBlindCBOR(record)
					if err != nil {
						t.Fatal(err)
					}
					if err = direct.Scan(cbor, &decoded); err != nil {
						t.Fatal(err)
					}
					got, err := l.Encode(nil, decoded.Values)
					if err != nil || !bytes.Equal(got, frame.Bytes) {
						t.Fatalf("CBOR/PIL profile round trip differs: %v", err)
					}
				}
			}
			if profile == "full-u64" {
				for field := range measurementIntegerBounds(n) {
					if highHalf[field] < n/4 || highHalf[field] > 3*n/4 {
						t.Fatalf("field %d is not sampling across the U64 range", field)
					}
				}
			}
		})
	}
}
