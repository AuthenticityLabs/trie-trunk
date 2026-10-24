package cache

import (
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Integer range predicates accept decimal literals without float rounding.
// Convert an open boundary to the nearest inclusive integer boundary.
func integerPredicate(k int, kind string, f QueryFilter) (searchFilter, bool) {
	r, ok := new(big.Rat).SetString(f.Value.Value)
	if !ok {
		return searchFilter{}, false
	}
	floor := new(big.Int).Quo(r.Num(), r.Denom())
	rem := new(big.Int).Rem(r.Num(), r.Denom())
	if r.Sign() < 0 && rem.Sign() != 0 {
		floor.Sub(floor, big.NewInt(1))
	}
	ceil := new(big.Int).Set(floor)
	if rem.Sign() != 0 {
		ceil.Add(ceil, big.NewInt(1))
	}
	min := new(big.Int)
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
	if kind == "i" {
		min.Neg(new(big.Int).Lsh(big.NewInt(1), 63))
		max.Sub(new(big.Int).Lsh(big.NewInt(1), 63), big.NewInt(1))
	}
	out := searchFilter{K: k, Kind: kind, Op: f.Op}
	all := func() searchFilter { return searchFilter{K: k, Kind: "p", Op: "==", Value: "2"} }
	none := func() searchFilter { return searchFilter{K: k, Kind: "never"} }
	var n *big.Int
	switch f.Op {
	case "==", "!=":
		if rem.Sign() != 0 || floor.Cmp(min) < 0 || floor.Cmp(max) > 0 {
			if f.Op == "!=" {
				return all(), true
			}
			return none(), true
		}
		n = floor
	case ">":
		n = new(big.Int).Add(floor, big.NewInt(1))
		out.Op = ">="
	case ">=":
		n = ceil
	case "<":
		n = new(big.Int).Sub(ceil, big.NewInt(1))
		out.Op = "<="
	case "<=":
		n = floor
	default:
		return searchFilter{}, false
	}
	if strings.HasPrefix(out.Op, ">") {
		if n.Cmp(max) > 0 {
			return none(), true
		}
		if n.Cmp(min) <= 0 {
			return all(), true
		}
	}
	if strings.HasPrefix(out.Op, "<") {
		if n.Cmp(min) < 0 {
			return none(), true
		}
		if n.Cmp(max) >= 0 {
			return all(), true
		}
	}
	out.Value = n.String()
	return out, true
}

func floatPredicate(k, bits int, f QueryFilter) (searchFilter, bool) {
	literal, ok := new(big.Rat).SetString(f.Value.Value)
	if !ok {
		return searchFilter{}, false
	}
	n, e := strconv.ParseFloat(f.Value.Value, bits)
	out := searchFilter{K: k, Kind: "f", Op: f.Op, Value: strconv.FormatFloat(n, 'g', -1, 64)}
	none := searchFilter{K: k, Kind: "never"}
	all := searchFilter{K: k, Kind: "finite"}
	if math.IsInf(n, 0) {
		if f.Op == "!=" || n > 0 && (f.Op == "<" || f.Op == "<=") || n < 0 && (f.Op == ">" || f.Op == ">=") {
			return all, true
		}
		return none, true
	}
	if e != nil || math.IsNaN(n) {
		return searchFilter{}, false
	}
	canonical, _ := new(big.Rat).SetString(strconv.FormatFloat(n, 'g', -1, bits))
	cmp := canonical.Cmp(literal)
	switch f.Op {
	case "==":
		if cmp != 0 {
			return none, true
		}
	case "!=":
		if cmp != 0 {
			return all, true
		}
	case ">":
		if cmp > 0 {
			out.Op = ">="
		} else {
			out.Op = ">"
		}
	case ">=":
		if cmp >= 0 {
			out.Op = ">="
		} else {
			out.Op = ">"
		}
	case "<":
		if cmp < 0 {
			out.Op = "<="
		} else {
			out.Op = "<"
		}
	case "<=":
		if cmp <= 0 {
			out.Op = "<="
		} else {
			out.Op = "<"
		}
	default:
		return searchFilter{}, false
	}
	return out, true
}
