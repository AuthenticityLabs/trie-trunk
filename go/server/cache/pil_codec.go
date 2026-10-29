package cache

import (
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"io"
)

type State = pil.State
type Value = pil.Value
type Frame = pil.Frame
type PAC = pil.PAC
type PACExtension = pil.PACExtension
type SealedGzip = pil.SealedGzip
type GzipPACWriter = pil.GzipPACWriter
type PositionalCBOR = pil.PositionalCBOR
type CBORFrame = pil.CBORFrame

const (
	Absent                  = pil.Absent
	Null                    = pil.Null
	Direct                  = pil.Direct
	Offset                  = pil.Offset
	PACHeaderSize           = pil.PACHeaderSize
	PACCompressedHeaderSize = pil.PACCompressedHeaderSize
	PACCommon               = pil.PACCommon
	PACGzip                 = pil.PACGzip
	PACSource               = pil.PACSource
	PACNative               = uint32(0x5454524b)
	PACMaximum              = pil.PACMaximum
	PACMaximumMembers       = pil.PACMaximumMembers
)

func Uint(n uint64, w int) Value                                { return pil.Uint(n, w) }
func Text(s string) Value                                       { return pil.Text(s) }
func unsigned(b []byte) uint64                                  { return pil.Unsigned(b) }
func readExtended(b []byte, at *int) (uint64, error)            { return pil.ReadExtended(b, at) }
func extended(b []byte, n uint64) []byte                        { return pil.Extended(b, n) }
func width(s string) (int, error)                               { return pil.Width(s) }
func validateArray(s string, b []byte) error                    { return pil.ValidateArray(s, b) }
func integerCode(b []byte, signed bool) byte                    { return pil.IntegerCode(b, signed) }
func integer16Code(b []byte, signed bool) byte                  { return pil.Integer16Code(b, signed) }
func putWidthCode(b []byte, bit, bits int, c byte)              { pil.PutWidthCode(b, bit, bits, c) }
func getWidthCode(b []byte, bit, bits int) byte                 { return pil.GetWidthCode(b, bit, bits) }
func SealGzip(b []byte) (*SealedGzip, error)                    { return pil.SealGzip(b) }
func SealGzipLevel(b []byte, level int) (*SealedGzip, error)    { return pil.SealGzipLevel(b, level) }
func RestoreSealedGzip(b []byte, n uint64) (*SealedGzip, error) { return pil.RestoreSealedGzip(b, n) }
func NewGzipPACWriter(w io.Writer) *GzipPACWriter               { return pil.NewGzipPACWriter(w) }
func combineCRC32(a, b uint32, n uint64) uint32                 { return pil.CombineCRC32(a, b, n) }
func compressPAC(b []byte, level int) ([]byte, error)           { return pil.CompressPAC(b, level) }
func expandPAC(b []byte) ([]byte, error)                        { return pil.ExpandPAC(b) }
func validatePlainPACEnvelope(b []byte) error                   { return pil.ValidatePlainPACEnvelope(b) }
func fieldObject(f Field, v Value) ([]byte, error)              { return pil.FieldObject(f, v) }
func valueFromObject(f Field, w int, b []byte) (Value, error)   { return pil.ValueFromObject(f, w, b) }

func PackPAC(l *Layout, f [][]byte, e []PACExtension, p bool) (*PAC, error) {
	return pil.PackPAC(l.Layout, f, e, p)
}
func factorPAC(l *Layout, f [][]byte, e []PACExtension, p bool) (*PAC, error) {
	return pil.FactorPAC(l.Layout, f, e, p)
}
func ParsePAC(l *Layout, b []byte, s map[uint32]bool) (*PAC, error) {
	return pil.ParsePAC(l.Layout, b, s)
}
func ResolvePAC(l *Layout, c, m []byte) ([]byte, error) { return pil.ResolvePAC(l.Layout, c, m) }
func CompilePositionalCBOR(l *Layout) (*PositionalCBOR, error) {
	if l == nil {
		return pil.CompilePositionalCBOR(nil)
	}
	return pil.CompilePositionalCBOR(l.Layout)
}

func isNullObject(raw []byte) bool { var v any; return pil.UnmarshalObject(raw, &v) == nil && v == nil }
func emptyObject() []byte          { b, _ := pil.MarshalObject(map[string]any{}); return b }
