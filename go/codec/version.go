package codec

import (
	"fmt"
	"strconv"
	"strings"
)

// PackVersion encodes the sequenced 12-bit and 20-bit version components.
func PackVersion(major, minor uint32) (uint32, error) {
	if major >= 1<<12 || minor >= 1<<20 || major == 0 && minor == 0 {
		return 0, fmt.Errorf("invalid 12.20 schema version")
	}
	return major<<20 | minor, nil
}
func VersionString(v uint32) string { return fmt.Sprintf("%d.%d", v>>20, v&((1<<20)-1)) }
func ParseVersion(s string) (uint32, error) {
	a, b, ok := strings.Cut(s, ".")
	if !ok {
		return 0, fmt.Errorf("expected major.minor version")
	}
	x, e := strconv.ParseUint(a, 10, 12)
	if e != nil {
		return 0, e
	}
	y, e := strconv.ParseUint(b, 10, 20)
	if e != nil {
		return 0, e
	}
	v, e := PackVersion(uint32(x), uint32(y))
	if e == nil && VersionString(v) != s {
		e = fmt.Errorf("noncanonical version")
	}
	return v, e
}
