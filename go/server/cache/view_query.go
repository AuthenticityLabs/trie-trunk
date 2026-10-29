package cache

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// QueryPlan is independent of candidate providers. Time, object and text
// indexes produce canonical source-row ranges; residual predicates intersect them.
// Numeric literals stay decimal strings so U64 never passes through float64/JS Number.
type QueryLiteral struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type QueryFilter struct {
	Field string       `json:"field"`
	Op    string       `json:"op"`
	Value QueryLiteral `json:"value"`
}
type QueryText struct {
	Text string `json:"text"`
	Mode string `json:"mode"`
}
type QueryPlan struct {
	Filters []QueryFilter `json:"filters"`
	Search  *QueryText    `json:"search,omitempty"`
	Order   string        `json:"order"`
}

var queryNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func validQueryNumber(v string) bool {
	if len(v) > 512 || !queryNumber.MatchString(v) {
		return false
	}
	if at := strings.IndexAny(v, "eE"); at >= 0 {
		n, e := strconv.Atoi(v[at+1:])
		if e != nil || n < -400 || n > 400 {
			return false
		}
	}
	return true
}
func parseQueryPlan(raw string) (*QueryPlan, string, error) {
	if raw == "" {
		return nil, "", nil
	}
	if len(raw) > 8192 {
		return nil, "", fmt.Errorf("query exceeds 8192 bytes")
	}
	var q QueryPlan
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&q); e != nil {
		return nil, "", fmt.Errorf("invalid query: %w", e)
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return nil, "", fmt.Errorf("unexpected query suffix")
	}
	if q.Order != "asc" && q.Order != "desc" {
		return nil, "", fmt.Errorf("order must be asc or desc")
	}
	if len(q.Filters) > 32 {
		return nil, "", fmt.Errorf("at most 32 predicates")
	}
	for _, f := range q.Filters {
		if f.Field == "" || len(f.Field) > 256 || !strings.Contains("|==|!=|>|>=|<|<=|contains|", "|"+f.Op+"|") {
			return nil, "", fmt.Errorf("invalid predicate")
		}
		switch f.Value.Kind {
		case "string":
		case "duration":
			if _, _, ok := durationLiteral(f.Value.Value); !ok {
				return nil, "", fmt.Errorf("invalid duration literal")
			}
		case "number":
			if !validQueryNumber(f.Value.Value) {
				return nil, "", fmt.Errorf("invalid numeric literal")
			}
		case "boolean":
			if f.Value.Value != "true" && f.Value.Value != "false" {
				return nil, "", fmt.Errorf("invalid boolean")
			}
		case "null":
			if f.Value.Value != "" {
				return nil, "", fmt.Errorf("invalid null")
			}
		default:
			return nil, "", fmt.Errorf("invalid literal kind")
		}
		if len(f.Value.Value) > 4096 {
			return nil, "", fmt.Errorf("literal too long")
		}
		if f.Op == "contains" && f.Value.Kind != "string" {
			return nil, "", fmt.Errorf("contains requires text")
		}
		if (f.Value.Kind == "boolean" || f.Value.Kind == "null") && f.Op != "==" && f.Op != "!=" {
			return nil, "", fmt.Errorf("boolean/null only support == and !=")
		}
	}
	if q.Search != nil && (!strings.Contains("|contains|terms|phrase|prefix|fuzzy|", "|"+q.Search.Mode+"|") || q.Search.Text == "" || len(q.Search.Text) > 4096) {
		return nil, "", fmt.Errorf("search requires nonempty text and a supported mode")
	}
	b, _ := json.Marshal(q)
	return &q, string(b), nil
}
func fieldIndex(l *Layout, name string) int {
	for i, f := range l.Definition.Fields {
		if f.Name == name {
			return i
		}
	}
	return -1
}
func validateQueryPlan(q *QueryPlan, sources []*viewSource) error {
	if q == nil {
		return nil
	}
	for _, f := range q.Filters {
		found := false
		for _, s := range sources {
			l := s.api.Store.layout
			i := fieldIndex(l, f.Field)
			if i < 0 {
				continue
			}
			found = true
			t := l.Definition.Fields[i].Type
			if i == l.Object {
				store := s.api.Store
				store.mu.Lock()
				enabled := store.config.ObjectIndex && store.objects != nil
				store.mu.Unlock()
				if !enabled {
					return ErrObjectIndexDisabled
				}
				if f.Op != "==" || f.Value.Kind == "null" {
					return fmt.Errorf("object identity searches require == and collection indexing")
				}
			}
			valid := f.Value.Kind == "duration" && i == l.Duration || f.Value.Kind == "null" || f.Value.Kind == "string" && (t == "STRING" || t == "UUID") || f.Value.Kind == "boolean" && t == "BOOL" || f.Value.Kind == "number" && (strings.HasPrefix(t, "TIMESTAMP_") || t == "REF16" || strings.Contains("|I8|I16|I32|I64|U8|U16|U32|U64|F32|F64|", "|"+t+"|"))
			if !valid {
				return fmt.Errorf("literal type does not match %s (%s); narrow the resource", f.Field, t)
			}
			if t == "UUID" && f.Op != "==" && f.Op != "!=" {
				return fmt.Errorf("UUID supports == and !=")
			}
		}
		if !found {
			return fmt.Errorf("unknown field %q", f.Field)
		}
	}
	return nil
}
func queryValue(l *Layout, i int, v Value) QueryLiteral {
	if v.State == Absent {
		return QueryLiteral{Kind: "absent"}
	}
	if v.State == Null {
		return QueryLiteral{Kind: "null"}
	}
	f := l.Definition.Fields[i]
	t := f.Type
	if t == "STRING" {
		if v.State == Direct && len(f.Values) > 0 {
			return QueryLiteral{"string", f.Values[unsigned(v.Bytes)]}
		}
		return QueryLiteral{"string", string(v.Bytes)}
	}
	if t == "UUID" {
		b := v.Bytes
		return QueryLiteral{"string", fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])}
	}
	if t == "BOOL" {
		return QueryLiteral{"boolean", strconv.FormatBool(unsigned(v.Bytes) != 0)}
	}
	var text string
	if t == "F32" {
		text = strconv.FormatFloat(float64(math.Float32frombits(order.Uint32(v.Bytes))), 'g', -1, 32)
	} else if t == "F64" {
		text = strconv.FormatFloat(math.Float64frombits(order.Uint64(v.Bytes)), 'g', -1, 64)
	} else if strings.HasPrefix(t, "I") || strings.HasPrefix(t, "TIMESTAMP_") {
		n := unsigned(v.Bytes)
		shift := uint(64 - len(v.Bytes)*8)
		text = strconv.FormatInt(int64(n<<shift)>>shift, 10)
	} else {
		text = strconv.FormatUint(unsigned(v.Bytes), 10)
	}
	return QueryLiteral{"number", text}
}
func compareQuery(actual, wanted QueryLiteral, op string) bool {
	if actual.Kind == "absent" {
		return false
	}
	if wanted.Kind == "null" {
		if op == "==" {
			return actual.Kind == "null"
		}
		return actual.Kind != "null"
	}
	if actual.Kind != wanted.Kind {
		return false
	}
	cmp := strings.Compare(actual.Value, wanted.Value)
	if actual.Kind == "number" {
		a, aok := new(big.Rat).SetString(actual.Value)
		b, bok := new(big.Rat).SetString(wanted.Value)
		if !aok || !bok {
			return false
		}
		cmp = a.Cmp(b)
	}
	switch op {
	case "==":
		return cmp == 0
	case "!=":
		return cmp != 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "contains":
		return strings.Contains(strings.ToLower(actual.Value), strings.ToLower(wanted.Value))
	}
	return false
}
func containsText(value any, needle string) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(strings.ToLower(v), needle)
	case []any:
		for _, x := range v {
			if containsText(x, needle) {
				return true
			}
		}
	case map[string]any:
		for _, x := range v {
			if containsText(x, needle) {
				return true
			}
		}
	case map[any]any:
		for _, x := range v {
			if containsText(x, needle) {
				return true
			}
		}
	}
	return false
}

type boundQueryFilter struct {
	index  int
	filter QueryFilter
	number *big.Rat
}

func bindQuery(l *Layout, q *QueryPlan) []boundQueryFilter {
	out := make([]boundQueryFilter, len(q.Filters))
	for i, f := range q.Filters {
		f.Value = durationQueryValue(l, fieldIndex(l, f.Field), f.Value)
		if j := fieldIndex(l, f.Field); j >= 0 && l.Definition.Fields[j].Type == "UUID" {
			f.Value.Value = strings.ToLower(f.Value.Value)
		}
		out[i] = boundQueryFilter{index: fieldIndex(l, f.Field), filter: f}
		if f.Value.Kind == "number" {
			out[i].number, _ = new(big.Rat).SetString(f.Value.Value)
		}
	}
	return out
}
func (b boundQueryFilter) matches(l *Layout, v Value) bool {
	if b.index < 0 {
		return false
	}
	actual := QueryLiteral{Kind: "present"}
	if b.filter.Value.Kind != "null" || v.State < Direct {
		actual = queryValue(l, b.index, v)
	}
	if b.number != nil && actual.Kind == "number" {
		a, ok := new(big.Rat).SetString(actual.Value)
		if !ok {
			return false
		}
		n := a.Cmp(b.number)
		switch b.filter.Op {
		case "==":
			return n == 0
		case "!=":
			return n != 0
		case ">":
			return n > 0
		case ">=":
			return n >= 0
		case "<":
			return n < 0
		case "<=":
			return n <= 0
		}
		return false
	}
	return compareQuery(actual, b.filter.Value, b.filter.Op)
}
func matchQuery(l *Layout, frame *Frame, q *QueryPlan) bool {
	return matchBoundQuery(l, frame, q, bindQuery(l, q))
}
func matchBoundQuery(l *Layout, frame *Frame, q *QueryPlan, bound []boundQueryFilter) bool {
	for _, b := range bound {
		if b.index < 0 || !b.matches(l, frame.Values[b.index]) {
			return false
		}
	}
	if q.Search == nil {
		return true
	}
	needle := strings.ToLower(q.Search.Text)
	for i, f := range l.Definition.Fields {
		v := frame.Values[i]
		if v.State < Direct {
			continue
		}
		if f.Type == "STRING" && len(f.Values) == 0 && strings.Contains(strings.ToLower(string(v.Bytes)), needle) {
			return true
		}
		if f.Type == "ARRAY<STRING>" || f.Type == "ARRAY<OBJECT>" {
			at := 0
			n, e := readExtended(v.Bytes, &at)
			if e != nil {
				continue
			}
			ends := make([]uint64, int(n))
			for j := range ends {
				ends[j], _ = readExtended(v.Bytes, &at)
			}
			start := uint64(0)
			for _, end := range ends {
				raw := v.Bytes[at+int(start) : at+int(end)]
				start = end
				if f.Type == "ARRAY<STRING>" {
					if strings.Contains(strings.ToLower(string(raw)), needle) {
						return true
					}
				} else {
					var value any
					if decoder.Unmarshal(raw, &value) == nil && containsText(value, needle) {
						return true
					}
				}
			}
		}
		if f.Type == "OBJECT" {
			var value any
			if decoder.Unmarshal(v.Bytes, &value) == nil && containsText(value, needle) {
				return true
			}
		}
	}
	return false
}
func (s *viewSource) filterQuery(ctx context.Context, q *QueryPlan) error {
	if q == nil || len(q.Filters) == 0 && q.Search == nil {
		return nil
	}
	if e := s.objectQueryCandidates(q); e != nil {
		return e
	}
	var err error
	q, err = s.indexedCandidates(ctx, q)
	if err != nil {
		return err
	}
	if len(q.Filters) == 0 && q.Search == nil {
		return nil
	}
	var matches []cursor
	l := s.api.Store.layout
	allBound := bindQuery(l, q)
	var liveBound, sealedBound, identityBound []boundQueryFilter
	for _, f := range allBound {
		if f.index < 0 {
			s.ranges = nil
			return nil
		}
		if f.index == l.Event {
			identityBound = append(identityBound, f)
			continue
		}
		liveBound = append(liveBound, f)
		if f.index != l.Object {
			sealedBound = append(sealedBound, f)
		}
	}
	var f Frame
	for _, span := range s.ranges {
		for row := span.at; row < span.end; row++ {
			if e := ctx.Err(); e != nil {
				return e
			}
			bound := sealedBound
			if row >= s.binding.Tail {
				bound = liveBound
			}
			identityMatch := true
			for _, f := range identityBound {
				raw, err := s.fullFrame(row)
				if err != nil {
					return err
				}
				var identityFrame Frame
				if err = l.Scan(raw, &identityFrame); err != nil {
					return err
				}
				if !f.matches(l, identityFrame.Values[l.Event]) {
					identityMatch = false
					break
				}
			}
			if !identityMatch {
				continue
			}
			matched := len(bound) == 0 && q.Search == nil
			if !matched {
				var raw []byte
				if row >= s.binding.Tail {
					raw = s.pending[row-s.binding.Tail].frame
				} else {
					raw = s.resident(row)
				}
				if raw == nil {
					return fmt.Errorf("predicate requires local evaluation after collecting the complete hour; sealed PAC inspection is disabled")
				}
				if e := l.Scan(raw, &f); e != nil {
					return e
				}
				matched = matchBoundQuery(l, &f, q, bound)
			}
			if matched {
				n := len(matches)
				if n > 0 && matches[n-1].end == row {
					matches[n-1].end++
				} else {
					matches = append(matches, cursor{row, row + 1})
				}
			}
		}
	}
	s.ranges = matches
	return nil
}
func filteredSummary(summary *ViewSummary, sources []*viewSource) {
	for _, s := range sources {
		if !s.branch.Aggregates() {
			continue
		}
		for _, span := range s.ranges {
			for row := span.at; row < span.end; row++ {
				i := (eventTime(s.orderingKey(row)) - summary.From) / summary.Step
				if i >= 0 && i < int64(len(summary.Counts)) {
					summary.Count++
					summary.Counts[i]++
					if s.branch == Error {
						summary.Errors[i]++
					}
				}
			}
		}
	}
}

// compareEvent orders canonical references, independent of the index providing them.
func compareEvent(sources []*viewSource, a, b viewHit) int {
	if n := bytes.Compare(sources[a.source].orderingKey(a.row), sources[b.source].orderingKey(b.row)); n != 0 {
		return n
	}
	if a.source < b.source {
		return -1
	}
	if a.source > b.source {
		return 1
	}
	if a.row < b.row {
		return -1
	}
	if a.row > b.row {
		return 1
	}
	return 0
}

// Object identity equality uses the optional collection index, never a payload
// scan to discover object membership. Pending records retain provisional locators.
func (s *viewSource) objectQueryCandidates(q *QueryPlan) error {
	l := s.api.Store.layout
	if l.Object < 0 {
		return nil
	}
	for _, filter := range q.Filters {
		if filter.Field != l.Definition.Resource.Roles.Object {
			continue
		}
		if filter.Op != "==" || filter.Value.Kind == "null" {
			return fmt.Errorf("object identity searches require == and collection indexing")
		}
		store := s.api.Store
		store.mu.Lock()
		if !store.config.ObjectIndex || store.objects == nil {
			store.mu.Unlock()
			return ErrObjectIndexDisabled
		}
		if s.branch != Write {
			store.mu.Unlock()
			s.ranges = nil
			return nil
		}
		var value Value
		kind := l.Definition.Fields[l.Object].Type
		if kind == "UUID" {
			raw, e := hex.DecodeString(strings.ReplaceAll(filter.Value.Value, "-", ""))
			if e != nil || len(raw) != 16 {
				store.mu.Unlock()
				return fmt.Errorf("invalid UUID")
			}
			value = Value{Direct, raw}
		} else if kind == "STRING" {
			value = Text(filter.Value.Value)
		} else {
			n, e := strconv.ParseUint(filter.Value.Value, 10, 64)
			if e != nil {
				store.mu.Unlock()
				return e
			}
			width := l.Widths[l.Object]
			if width < 8 && n >= 1<<uint(width*8) {
				store.mu.Unlock()
				return fmt.Errorf("object ID out of range")
			}
			value = Uint(n, width)
		}
		b := store.branches[s.branch]
		if b == nil || b.generation != s.snap.generation || b.baseRows != s.snap.baseRows {
			store.mu.Unlock()
			return fmt.Errorf("object snapshot expired")
		}
		rows, e := store.objectRows(b, value, time.UnixMilli(0), time.UnixMilli(1<<48))
		store.mu.Unlock()
		if e != nil {
			return e
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i] < rows[j] })
		var selected []cursor
		for _, span := range s.ranges {
			i := sort.Search(len(rows), func(i int) bool { return rows[i] >= span.at })
			for ; i < len(rows) && rows[i] < span.end && rows[i] < s.binding.Tail; i++ {
				selected = append(selected, cursor{rows[i], rows[i] + 1})
			}
			if span.end > s.binding.Tail {
				var frame Frame
				for row := max(span.at, s.binding.Tail); row < span.end; row++ {
					if err := l.Scan(s.pending[row-s.binding.Tail].frame, &frame); err != nil {
						return err
					}
					if frame.Values[l.Revision].State == Direct {
						selected = append(selected, cursor{row, row + 1})
					}
				}
			}
		}
		s.ranges = selected
	}
	return nil
}
