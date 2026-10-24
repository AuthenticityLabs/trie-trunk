package cache

// A file chooses one fixed width for each column. Expanding a column rewrites
// that file, not the variable-length datastore it addresses. No varint traversal.
func uintWidth(n uint64, minimum int) int {
	for minimum < 8 && n>>(minimum*8) != 0 {
		minimum++
	}
	return minimum
}
func putUintWidth(out []byte, n uint64) {
	for i := range out {
		out[i] = byte(n)
		n >>= 8
	}
	if n != 0 {
		panic("address width overflow")
	}
}
func getUintWidth(in []byte) uint64 {
	var n uint64
	for i := len(in) - 1; i >= 0; i-- {
		n = n<<8 | uint64(in[i])
	}
	return n
}
