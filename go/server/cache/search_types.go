package cache

// Internal typed inputs to the native ABI, not a serialized wire protocol.
// Large persistent indexes and native result buffers belong to Rust.
type searchCell struct {
	State uint64
	K     int
	T     []string
	E     []string
	C     []string
	U     []uint64
	I     []int64
	F     []float64
	B     []bool
}
type searchDoc struct {
	SourceRow uint64
	ID        string
	Row       uint64
	Cells     []searchCell
}
type searchFilter struct {
	Code  *uint64
	K     int
	Kind  string
	Op    string
	Value string
}
type searchRequest struct {
	FirstRef uint64
	IDs      []string
	Op       string
	Path     string
	Binding  string
	Fields   []string
	Base     uint64
	Tail     uint64
	Docs     []searchDoc
	Ranges   [][2]uint64
	Filters  []searchFilter
	Text     string
	Mode     string
}
type searchResponse struct {
	IDs  []string
	Tail uint64
	Rows []uint64
}
