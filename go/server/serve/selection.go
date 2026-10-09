package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"trie-trunk/go/server/search"
)

// GET collection selections have no body. Repeated where clauses retain OR
// semantics; all other scalar parameters must be unambiguous.
func decodeSelection(r *http.Request, c *search.Config) error {
	if r.ContentLength > 0 {
		return fmt.Errorf("GET /collect does not accept a request body")
	}
	// HTTP/3 represents an empty request stream as unknown length (-1).
	if r.ContentLength < 0 || len(r.TransferEncoding) != 0 {
		var byte [1]byte
		n, err := r.Body.Read(byte[:])
		if n != 0 || err != io.EOF {
			return fmt.Errorf("GET /collect requires an empty request stream")
		}
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return err
	}
	for key, entries := range values {
		if len(entries) != 1 && key != "where" && key != "headers" {
			return fmt.Errorf("duplicate parameter %q", key)
		}
		value := entries[0]
		switch key {
		case "tap":
			c.Tap = value
		case "from", "to":
			timestamp, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return fmt.Errorf("invalid %s timestamp: %w", key, err)
			}
			if key == "from" {
				c.Start = timestamp
			} else {
				c.End = timestamp
			}
		case "limit":
			if value == "all" {
				c.Limit = nil
				continue
			}
			count, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid limit")
			}
			c.Limit = &count
		case "tail", "follow":
			if value != "true" && value != "false" {
				return fmt.Errorf("invalid %s boolean", key)
			}
			if key == "tail" {
				c.Tail = value == "true"
			} else {
				c.Follow = value == "true"
			}
		case "where":
			c.Where = entries
		case "headers":
			c.Headers = []string{}
			for _, entry := range entries {
				if entry != "" {
					c.Headers = append(c.Headers, entry)
				}
			}
		default:
			return fmt.Errorf("unknown parameter %q", key)
		}
	}
	return nil
}

// A bounded cache hint lets a cold session reuse immutable definitions saved on
// disk, even when its aggregate version is absent or belongs to an old server.
func knownDefinitions(headers []string) ([]string, error) {
	var known []string
	for _, header := range headers {
		for _, value := range strings.Split(header, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			digest, err := hex.DecodeString(value)
			if err != nil || len(digest) != sha256.Size || len(known) == 128 {
				return nil, fmt.Errorf("Definition-Known requires at most 128 SHA-256 digests")
			}
			known = append(known, hex.EncodeToString(digest))
		}
	}
	return known, nil
}
