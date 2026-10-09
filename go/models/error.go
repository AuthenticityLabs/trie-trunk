// Package models implements structured errors independently of logging.
package models

import (
	"context"
	"errors"
	"fmt"

	"trie-trunk/go/codec"
)

type ErrorCode uint16
type ErrorType uint16

const (
	UNEXPECTED                ErrorCode = 1
	REQUEST_TIMEOUT           ErrorCode = 2
	UNSUPPORTED_MEDIA_TYPE    ErrorCode = 3
	MEDIA_TYPE_NOT_ACCEPTABLE ErrorCode = 4
	MISSING_PATH_VARIABLE     ErrorCode = 5
	MISSING_REQUEST_PARAMETER ErrorCode = 6
	INVALID_INPUT             ErrorCode = 7
	BAD_REQUEST               ErrorCode = 8
	METHOD_NOT_ALLOWED        ErrorCode = 9
	RESOURCE_NOT_FOUND        ErrorCode = 10
	CANCELED                  ErrorCode = 11
)
const (
	SYSTEM     ErrorType = 1
	SERVICE    ErrorType = 2
	VALIDATION ErrorType = 3
)

type Definition struct {
	Code    ErrorCode
	Type    ErrorType
	Status  uint16
	Message string
}

func Lookup(code ErrorCode) Definition {
	switch code {
	case REQUEST_TIMEOUT:
		return Definition{code, SYSTEM, 503, "Request timed out."}
	case UNSUPPORTED_MEDIA_TYPE:
		return Definition{code, SERVICE, 415, "Unsupported media type."}
	case MEDIA_TYPE_NOT_ACCEPTABLE:
		return Definition{code, SERVICE, 406, "Requested media type is not acceptable."}
	case MISSING_PATH_VARIABLE:
		return Definition{code, VALIDATION, 400, "Required path variable is missing."}
	case MISSING_REQUEST_PARAMETER:
		return Definition{code, VALIDATION, 400, "Required request parameter is missing."}
	case INVALID_INPUT:
		return Definition{code, VALIDATION, 400, "Invalid input."}
	case BAD_REQUEST:
		return Definition{code, SERVICE, 400, "Bad request."}
	case METHOD_NOT_ALLOWED:
		return Definition{code, SERVICE, 405, "Method not allowed."}
	case RESOURCE_NOT_FOUND:
		return Definition{code, SERVICE, 404, "Resource not found."}
	case CANCELED:
		return Definition{code, SERVICE, 408, "Request canceled."}
	default:
		return Definition{code, SYSTEM, 500, "Unexpected failure has occurred."}
	}
}

// Error is immutable: fluent methods return modified copies. Message is public;
// cause is retained for errors.Is/As and is never serialized into the response.
type Error struct {
	code    codec.Symbol
	typ     codec.Symbol
	source  codec.Symbol
	status  uint16
	message codec.Symbol
	cause   error
}

func New(code ErrorCode) *Error {
	e := FromDefinition(Lookup(code))
	if code >= UNEXPECTED && code <= CANCELED {
		e.message = codec.Ref(uint16(code))
	}
	return e
}
func FromDefinition(d Definition) *Error {
	status := d.Status
	if status < 400 || status > 599 {
		status = 500
	}
	code := d.Code
	if code == 0 {
		code = UNEXPECTED
	}
	typ := d.Type
	if typ == 0 {
		typ = SYSTEM
	}
	return &Error{code: codec.Ref(uint16(code)), typ: codec.Ref(uint16(typ)), status: status, message: codec.Text(d.Message)}
}
func (e *Error) WithCode(code ErrorCode) *Error        { n := *e; n.code = codec.Ref(uint16(code)); return &n }
func (e *Error) WithCodeText(code string) *Error       { n := *e; n.code = codec.Text(code); return &n }
func (e *Error) WithType(typ ErrorType) *Error         { n := *e; n.typ = codec.Ref(uint16(typ)); return &n }
func (e *Error) WithTypeText(typ string) *Error        { n := *e; n.typ = codec.Text(typ); return &n }
func (e *Error) WithSource(source codec.Symbol) *Error { n := *e; n.source = source; return &n }
func (e *Error) WithStatus(status uint16) *Error {
	n := *e
	if status >= 400 && status <= 599 {
		n.status = status
	}
	return &n
}
func (e *Error) WithMessage(message string) *Error {
	n := *e
	n.message = codec.Text(message)
	return &n
}
func (e *Error) WithMessageRef(id uint16) *Error { n := *e; n.message = codec.Ref(id); return &n }
func (e *Error) WithCause(cause error) *Error    { n := *e; n.cause = cause; return &n }
func (e *Error) Code() codec.Symbol              { return e.code }
func (e *Error) Type() codec.Symbol              { return e.typ }
func (e *Error) Source() codec.Symbol            { return e.source }
func (e *Error) Status() uint16                  { return e.status }
func (e *Error) Message() codec.Symbol           { return e.message }

// LimitText bounds literal metadata for a log record without changing the
// original public error. Dictionary references are unchanged.
func (e *Error) LimitText(max int) *Error {
	n := *e
	n.code = n.code.LimitText(max)
	n.typ = n.typ.LimitText(max)
	n.source = n.source.LimitText(max)
	n.message = n.message.LimitText(max)
	if n.code == e.code && n.typ == e.typ && n.source == e.source && n.message == e.message {
		return e
	}
	return &n
}
func (e *Error) Unwrap() error { return e.cause }
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if id := e.message.Id(); id >= uint16(UNEXPECTED) && id <= uint16(CANCELED) {
		return Lookup(ErrorCode(id)).Message
	}
	return fmt.Sprint(e.message.Raw())
}

// Normalize preserves explicit structured errors through wrapping. Unknown
// errors become SYSTEM/500, without exposing their text to clients.
func Normalize(err error, source codec.Symbol) *Error {
	if err == nil {
		return nil
	}
	var structured *Error
	if errors.As(err, &structured) && structured != nil {
		if structured.source.IsZero() {
			return structured.WithSource(source)
		}
		return structured
	}
	code := UNEXPECTED
	if errors.Is(err, context.DeadlineExceeded) {
		code = REQUEST_TIMEOUT
	} else if errors.Is(err, context.Canceled) {
		code = CANCELED
	}
	return New(code).WithSource(source).WithCause(err)
}

const (
	KeyCode    codec.Key = 0
	KeyType    codec.Key = 1
	KeySource  codec.Key = 2
	KeyStatus  codec.Key = 3
	KeyMessage codec.Key = 4
)

func BuildSchema() codec.Schema {
	return codec.Schema{
		KeyCode:   {Name: "code", Domain: "error_code", Enums: map[uint16]string{1: "UNEXPECTED", 2: "REQUEST_TIMEOUT", 3: "UNSUPPORTED_MEDIA_TYPE", 4: "MEDIA_TYPE_NOT_ACCEPTABLE", 5: "MISSING_PATH_VARIABLE", 6: "MISSING_REQUEST_PARAMETER", 7: "INVALID_INPUT", 8: "BAD_REQUEST", 9: "METHOD_NOT_ALLOWED", 10: "RESOURCE_NOT_FOUND", 11: "CANCELED"}},
		KeyType:   {Name: "type", Domain: "error_type", Enums: map[uint16]string{1: "SYSTEM", 2: "SERVICE", 3: "VALIDATION"}},
		KeySource: {Name: "source", Domain: "service"}, KeyStatus: {Name: "status"}, KeyMessage: {Name: "message", Domain: "error_message", Enums: errorMessages()},
	}
}

func (e *Error) Record() codec.Record {
	r := codec.Record{KeyCode: e.code, KeyType: e.typ, KeyStatus: e.status, KeyMessage: e.message}
	if !e.source.IsZero() {
		r[KeySource] = e.source
	}
	return r
}

func (e *Error) Encode(format codec.Format, dictionary *codec.Dictionary) ([]byte, error) {
	if format != codec.ReadableJSON {
		return codec.MarshalCompact(e.Wire(), format)
	}
	return codec.Encode(e.Record(), errorSchema, dictionary, format)
}

var errorSchema = BuildSchema()

func errorMessages() map[uint16]string {
	m := make(map[uint16]string, 11)
	for code := UNEXPECTED; code <= CANCELED; code++ {
		m[uint16(code)] = Lookup(code).Message
	}
	return m
}

// WireError is a fixed-layout encoding projection. Use Error's builder methods
// to construct errors; these tags are permanent wire assignments.
type WireError struct {
	Code    any    `json:"0" cbor:"0,keyasint"`
	Type    any    `json:"1" cbor:"1,keyasint"`
	Source  any    `json:"2,omitempty" cbor:"2,keyasint,omitempty"`
	Status  uint16 `json:"3" cbor:"3,keyasint"`
	Message any    `json:"4" cbor:"4,keyasint"`
}

func (e *Error) Wire() WireError {
	w := WireError{Code: e.code.Raw(), Type: e.typ.Raw(), Status: e.status, Message: e.message.Raw()}
	if !e.source.IsZero() {
		w.Source = e.source.Raw()
	}
	return w
}

var Codes = struct {
	UNEXPECTED                uint16
	REQUEST_TIMEOUT           uint16
	UNSUPPORTED_MEDIA_TYPE    uint16
	MEDIA_TYPE_NOT_ACCEPTABLE uint16
	MISSING_PATH_VARIABLE     uint16
	MISSING_REQUEST_PARAMETER uint16
	INVALID_INPUT             uint16
	BAD_REQUEST               uint16
	METHOD_NOT_ALLOWED        uint16
	RESOURCE_NOT_FOUND        uint16
	CANCELED                  uint16
}{
	UNEXPECTED:                1,
	REQUEST_TIMEOUT:           2,
	UNSUPPORTED_MEDIA_TYPE:    3,
	MEDIA_TYPE_NOT_ACCEPTABLE: 4,
	MISSING_PATH_VARIABLE:     5,
	MISSING_REQUEST_PARAMETER: 6,
	INVALID_INPUT:             7,
	BAD_REQUEST:               8,
	METHOD_NOT_ALLOWED:        9,
	RESOURCE_NOT_FOUND:        10,
	CANCELED:                  11,
}
