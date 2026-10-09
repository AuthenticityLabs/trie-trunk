package models

import (
	"encoding/json"
	"net/http"

	"trie-trunk/go/codec"
)

// WriteResponse renders readable JSON only. Compact error records are embedded
// in logs; clients do not need the logging dictionary. Unknown references remain
// numeric. The caller must invoke this before committing the response.
func WriteResponse(w http.ResponseWriter, failure *Error, dictionary *codec.Dictionary) error {
	if failure == nil {
		return nil
	}
	body := codec.Expand(failure.Record(), errorSchema, dictionary)
	delete(body, "status") // Match Java's status exclusion from the response body.
	data, err := json.Marshal(struct {
		Error any `json:"error"`
	}{body})
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(int(failure.status))
	_, err = w.Write(append(data, '\n'))
	return err
}
