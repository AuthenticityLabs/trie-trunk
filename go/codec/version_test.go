package codec

import "testing"

func TestVersion1220(t *testing.T) {
	for _, v := range []uint32{1, 3<<20 | 42, ^uint32(0)} {
		s := VersionString(v)
		got, e := ParseVersion(s)
		if e != nil || got != v {
			t.Fatal(s, got, e)
		}
	}
	for _, s := range []string{"0.0", "4096.0", "0.1048576", "3.042", "3/42", "-1.1"} {
		if _, e := ParseVersion(s); e == nil {
			t.Fatal(s)
		}
	}
}
