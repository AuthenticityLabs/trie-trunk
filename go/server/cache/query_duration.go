package cache

import (
	"strconv"
	"strings"
)

// Duration-role storage is integer microseconds. Query defaults are milliseconds;
// explicit units remain in the public plan and are normalized per Definition.
func durationLiteral(value string) (string, int, bool) {
	for _, unit := range []struct {
		suffix string
		power  int
	}{{"ns", -3}, {"us", 0}, {"µs", 0}, {"μs", 0}, {"ms", 3}, {"s", 6}} {
		if strings.HasSuffix(value, unit.suffix) {
			number := strings.TrimSuffix(value, unit.suffix)
			return number, unit.power, validQueryNumber(number)
		}
	}
	return "", 0, false
}

func durationQueryValue(l *Layout, index int, value QueryLiteral) QueryLiteral {
	if index < 0 || index != l.Duration || (value.Kind != "number" && value.Kind != "duration") {
		return value
	}
	number, power := value.Value, 3
	if value.Kind == "duration" {
		var ok bool
		number, power, ok = durationLiteral(value.Value)
		if !ok {
			return value
		}
	}
	// Decimal exponent adjustment avoids rounding large U64 values or fractional
	// microseconds. Candidate pushdown and residual comparisons use the same value.
	if at := strings.IndexAny(number, "eE"); at >= 0 {
		exponent, _ := strconv.Atoi(number[at+1:])
		power += exponent
		number = number[:at]
	}
	return QueryLiteral{"number", number + "e" + strconv.Itoa(power)}
}
