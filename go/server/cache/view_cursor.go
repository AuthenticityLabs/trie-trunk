package cache

// Cursor authentication binds pagination to the original query and snapshot.
import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

func viewSign(c viewCursor, auth string) (string, error) {
	b, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	m := hmac.New(sha256.New, viewSecret)
	m.Write([]byte(auth))
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(append(m.Sum(nil), b...)), nil
}
func viewParse(raw, auth string) (viewCursor, error) {
	var c viewCursor
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil || len(b) < 33 || len(b) > 65536 {
		return c, fmt.Errorf("invalid cursor")
	}
	m := hmac.New(sha256.New, viewSecret)
	m.Write([]byte(auth))
	m.Write(b[32:])
	if !hmac.Equal(b[:32], m.Sum(nil)) {
		return c, fmt.Errorf("invalid or expired cursor")
	}
	if e = json.Unmarshal(b[32:], &c); e != nil || time.Now().Unix() > c.Expires {
		return c, fmt.Errorf("cursor expired; restart search")
	}
	return c, nil
}
func viewDigest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
