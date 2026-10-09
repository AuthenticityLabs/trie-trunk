// Package keys defines the negotiated log field identifiers.
package keys

import "trie-trunk/go/codec"

type Key = codec.Key

// Keys 0 and 1 are reserved for legacy schema and dictionary metadata.
const (
	Category            Key = 2
	Level               Key = 3
	Service             Key = 4
	Version             Key = 5
	Resource            Key = 6
	Operation           Key = 7
	Id                  Key = 8
	ClientTransactionId Key = 9
	ClientId            Key = 10
	Duration            Key = 12
	HttpMethod          Key = 13
	HttpStatus          Key = 14
	Message             Key = 15
	Error               Key = 16
	Checkpoints         Key = 17
	ResponseBytes       Key = 18
	Uri                 Key = 19
	ClientIp            Key = 20
	Fields              Key = 21
	Diagnostic          Key = 22
	Dropped             Key = 23
)
