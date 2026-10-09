# Semantic definition API proposal

Draft design; this API is not implemented. The current log schema and
dictionary are defined in `../client/dictionary.go`.

```go
package definition
const (
    FieldErrorCode = "error_code"
    FieldService   = "service"
    FieldResource  = "resource"
    FieldOperation = "operation"

    ErrorRequestTimeout        = "request_timeout"
    ErrorUnsupportedMediaType   = "unsupported_media_type"
    ErrorInvalidInput           = "invalid_input"
)

var Definitions = logger.Definitions{
    logger.EnumField(
        FieldErrorCode,
        ErrorRequestTimeout,
        ErrorUnsupportedMediaType,
        ErrorInvalidInput,
    ),
    logger.StringField(FieldService),
    logger.StringField(FieldResource),
    logger.StringField(FieldOperation),
    logger.Uint16Field("http_status"),
    logger.Uint32Field("response_bytes"),
    logger.UUIDField("request_id"),
}

entry.Set(FieldErrorCode, ErrorInvalidInput)
entry.Set(FieldService, "orders")
entry.Set("http_status", uint16(400))
```

Yes. The client API should expose only **semantic field and value definitions**. The developer should not know about IDs, ordering, CBOR, trie layout, or negotiated schemas.

## Client-side registration

Use constants for names and values:

```go
const (
	FieldErrorCode = "error_code"
	FieldService   = "service"
	FieldResource  = "resource"
	FieldOperation = "operation"

	ErrorRequestTimeout        = "request_timeout"
	ErrorUnsupportedMediaType   = "unsupported_media_type"
	ErrorInvalidInput           = "invalid_input"
)
```

Then register the vocabulary:

```go
var Definitions = logger.Definitions{
	logger.EnumField(
		FieldErrorCode,
		ErrorRequestTimeout,
		ErrorUnsupportedMediaType,
		ErrorInvalidInput,
	),
	logger.StringField(FieldService),
	logger.StringField(FieldResource),
	logger.StringField(FieldOperation),
	logger.Uint16Field("http_status"),
	logger.Uint32Field("response_bytes"),
	logger.UUIDField("request_id"),
}
```

Go cannot have constant slices or maps, so the names and values can be constants while the registration list is a package-level value.

The developer then logs semantically:

```go
entry.Set(FieldErrorCode, ErrorInvalidInput)
entry.Set(FieldService, "orders")
entry.Set("http_status", uint16(400))
```

The client library resolves those names internally after negotiation.

## Negotiated schema

The client sends the definitions once. The server returns something conceptually like:

```text
schema_id: 42

error_code:
    sequence_id: 3
    request_timeout: 1
    unsupported_media_type: 2
    invalid_input: 3

service:
    sequence_id: 1

resource:
    sequence_id: 2

http_status:
    sequence_id: 4
```

The client then emits compact records such as:

```text
field_sequence_id : value_id
3                  : 3
1                  : 7
4                  : 400
```

An unregistered value uses a tagged full-value fallback:

```text
field_sequence_id : literal CBOR value
1                  : "orders"
```

That fallback must preserve the declared field type and be bounded in size.

## One important distinction

I would separate these concepts internally:

```text
FieldID   = stable identity of "error_code"
Sequence  = position chosen by the server for schema 42
ValueID   = dictionary identity of "invalid_input"
```

If `FieldID` and `Sequence` are literally the same number, reordering fields changes their identity. That is workable only if the number is scoped to a schema revision. Otherwise:

```text
schema 42: error_code = sequence 3
schema 43: error_code = sequence 5
```

would make the field appear to change identity.

The developer never needs to see that distinction. The client library can translate:

```text
"error_code" -> schema field sequence 3
"invalid_input" -> value ID 3
```

The public API stays semantic; the negotiated schema controls compact encoding and storage order.
