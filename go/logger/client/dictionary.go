// Package logging builds transaction logs and defines their wire schema and dictionary.
package logging

import (
	"trie-trunk/go/codec"
	Keys "trie-trunk/go/logger/client/keys"
	"trie-trunk/go/models"
)

type Category uint8

const (
	SUMMARY Category = 1
	SERVICE Category = 2
	DATA    Category = 3
	CACHE   Category = 4
	CONFIG  Category = 5
)

type Level uint8

const (
	TRACE Level = 1
	DEBUG Level = 2
	INFO  Level = 3
	WARN  Level = 4
	ERROR Level = 5
)

type HttpMethod uint8

const (
	GET          HttpMethod = 1
	POST         HttpMethod = 2
	PUT          HttpMethod = 3
	PATCH        HttpMethod = 4
	DELETE       HttpMethod = 5
	HEAD         HttpMethod = 6
	OPTIONS      HttpMethod = 7
	CONNECT      HttpMethod = 8
	TRACE_METHOD HttpMethod = 9
)

type ServiceId uint16
type ResourceId uint16
type OperationId uint16
type CheckpointId uint16
type MessageId uint16

const (
	SuccessfulTransaction MessageId = 1
	FailedTransaction     MessageId = 2
)

// SchemaVersion 5 keeps message values in the per-event payload, outside the trie.
const SchemaVersion uint32 = 5

// BuildSchema returns a fresh field schema for encoding and inspection.
func BuildSchema() codec.Schema {
	return codec.Schema{
		Keys.Category: {Name: "category", Enums: map[uint16]string{1: "SUMMARY", 2: "SERVICE", 3: "DATA", 4: "CACHE", 5: "CONFIG"}},
		Keys.Level:    {Name: "level", Enums: map[uint16]string{1: "TRACE", 2: "DEBUG", 3: "INFO", 4: "WARN", 5: "ERROR"}},
		Keys.Service:  {Name: "service", Domain: "service"}, Keys.Version: {Name: "version"},
		Keys.Resource: {Name: "resource", Domain: "resource"}, Keys.Operation: {Name: "operation", Domain: "operation"},
		Keys.Id: {Name: "id", Encoding: "uuidv7"}, Keys.ClientTransactionId: {Name: "client_transaction_id"}, Keys.ClientId: {Name: "client_id"},
		Keys.Duration:      {Name: "duration", Unit: "us"},
		Keys.HttpMethod:    {Name: "http_method", Enums: map[uint16]string{1: "GET", 2: "POST", 3: "PUT", 4: "PATCH", 5: "DELETE", 6: "HEAD", 7: "OPTIONS", 8: "CONNECT", 9: "TRACE"}},
		Keys.HttpStatus:    {Name: "http_status"},
		Keys.Message:       {Name: "message", Domain: "message", Enums: map[uint16]string{1: "Successful transaction.", 2: "Transaction failed."}},
		Keys.Error:         {Name: "error", Object: models.BuildSchema()},
		Keys.Checkpoints:   {Name: "checkpoints", Array: codec.Schema{0: {Name: "name", Domain: "checkpoint"}, 1: {Name: "duration", Unit: "us"}, 2: {Name: "count"}}},
		Keys.ResponseBytes: {Name: "response_bytes"}, Keys.Uri: {Name: "uri"}, Keys.ClientIp: {Name: "client_ip"},
		Keys.Fields: {Name: "fields"}, Keys.Diagnostic: {Name: "diagnostic"}, Keys.Dropped: {Name: "dropped"},
	}
}

// BuildDefinition builds the complete schema sent when the connected peer is out of
// sync: field names, units, built-in enums, nested schemas, and application values.
func BuildDefinition(dictionary *codec.Dictionary) codec.Definition {
	d := codec.Define(SchemaVersion, BuildSchema(), dictionary)
	d.Path = []codec.Key{Keys.Level, Keys.HttpStatus, Keys.Service, Keys.Resource, Keys.Operation, Keys.Category, Keys.HttpMethod}
	d.Aggregations = []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}
	return d
}
