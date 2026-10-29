package codec

import (
	"crypto/sha256"
	"fmt"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
)

// Revision identifies the complete connection-bound schema and value catalog.
// Digest detects catalog changes even if a caller forgets to bump Version.
type Revision struct {
	Version uint32   `json:"version" cbor:"0,keyasint" pil:"0,keyasint"`
	Digest  [32]byte `json:"digest" cbor:"1,keyasint" pil:"1,keyasint"`
}

// StoragePolicy is immutable routing metadata for native event files.
type StoragePolicy struct {
	Format   uint32 `json:"format" cbor:"0,keyasint" pil:"0,keyasint"`
	Boundary string `json:"boundary" cbor:"1,keyasint" pil:"1,keyasint"`
	Bucket   string `json:"bucket" cbor:"2,keyasint" pil:"2,keyasint"`
}

// Definition is initialization data, never a per-record envelope. Fields include
// nested error/checkpoint schemas and built-in enum values; Domains supplies the
// application values. Keep the accepted definition with the stream when storing
// or forwarding it. Decoding records without that association is ambiguous.
type Definition struct {
	Object       string                       `json:"object,omitempty" cbor:"8,keyasint,omitempty" pil:"8,keyasint,omitempty"`
	Storage      *StoragePolicy               `json:"storage,omitempty" cbor:"7,keyasint,omitempty" pil:"7,keyasint,omitempty"`
	Timezone     string                       `json:"timezone,omitempty" cbor:"4,keyasint,omitempty" pil:"4,keyasint,omitempty"`
	Path         []Key                        `json:"path,omitempty" cbor:"5,keyasint,omitempty" pil:"5,keyasint,omitempty"`
	Aggregations []string                     `json:"aggregations,omitempty" cbor:"6,keyasint,omitempty" pil:"6,keyasint,omitempty"`
	Version      uint32                       `json:"version" cbor:"0,keyasint" pil:"0,keyasint"`
	Fields       Schema                       `json:"fields" cbor:"1,keyasint" pil:"1,keyasint"`
	Dictionary   string                       `json:"dictionary,omitempty" cbor:"2,keyasint,omitempty" pil:"2,keyasint,omitempty"`
	Domains      map[string]map[uint16]string `json:"domains,omitempty" cbor:"3,keyasint,omitempty" pil:"3,keyasint,omitempty"`
}

// Define copies application domains once, at initialization. The caller owns
// fields; Prepare immediately serializes a stable snapshot before connecting.
func Define(version uint32, fields Schema, dictionary *Dictionary) Definition {
	d := Definition{Timezone: "UTC", Version: version, Fields: fields, Dictionary: dictionary.Name()}
	if dictionary != nil {
		d.Domains = make(map[string]map[uint16]string, len(dictionary.domains))
		for domain, values := range dictionary.domains {
			copy := make(map[uint16]string, len(values))
			for id, label := range values {
				copy[id] = label
			}
			d.Domains[domain] = copy
		}
	}
	return d
}

// Prepare hashes deterministic PIL once, not on individual log writes.
func (d Definition) Prepare() (Revision, []byte, error) {
	if d.Version == 0 || len(d.Fields) == 0 || d.Object != "" && (ValidateObject(d.Object) != nil || d.Version>>20 == 0) {
		return Revision{}, nil, fmt.Errorf("schema version and fields are required")
	}
	data, err := pil.MarshalObject(d)
	if err != nil {
		return Revision{}, nil, err
	}
	return Revision{Version: d.Version, Digest: sha256.Sum256(data)}, data, nil
}

// ParseDefinition is available for local stubs and future receiving adapters.
// It checks that the entire definition matches the acknowledged revision.
func ParseDefinition(data []byte, revision Revision) (Definition, error) {
	var d Definition
	if sha256.Sum256(data) != revision.Digest {
		return d, fmt.Errorf("schema digest mismatch")
	}
	if err := pil.UnmarshalObject(data, &d); err != nil {
		return d, err
	}
	if d.Version == 0 || d.Version != revision.Version || len(d.Fields) == 0 || d.Object != "" && (ValidateObject(d.Object) != nil || d.Version>>20 == 0) {
		return d, fmt.Errorf("schema version or fields mismatch")
	}
	return d, nil
}

// ValidateObject validates a stable, path-safe logical object name. Major and
// minor numbers are carried by Version; neither belongs in the object name.
func ValidateObject(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return fmt.Errorf("object name must be 1..64 characters")
	}
	for i, c := range []byte(name) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '_' || c == '-' || c == '.') {
			continue
		}
		return fmt.Errorf("object name must start with a lowercase letter or digit and contain only a-z, 0-9, '.', '_' or '-'")
	}
	return nil
}
