// Package pil writes caller-defined PIL events after registering the immutable
// definition. It is independent of the historical logger's fixed log vocabulary.
package pil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	pilcodec "github.com/AuthenticityLabs/pill-packer/go/codec"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/cache"
)

// These aliases expose the existing unified PIL/resource map and primitive values;
// no second definition compiler or CBOR event conversion is introduced.
type Definition = cache.Definition
type Field = cache.Field
type ReferenceTranslation = cache.ReferenceTranslation
type Binding = cache.Binding
type Roles = cache.Roles
type Value = cache.Value

const (
	Absent        = cache.Absent
	Null          = cache.Null
	Direct        = cache.Direct
	Offset        = cache.Offset
	StorageFormat = cache.Format
)

func CompileDefinition(d Definition) ([]byte, error) {
	l, e := cache.Compile(d)
	if e != nil {
		return nil, e
	}
	return append([]byte(nil), l.Bytes...), nil
}

type Config struct {
	// URL is the listener origin, e.g. https://host:5140. Cache/tenant are bound by
	// the listener; service/resource/version come from the supplied definition.
	URL        string
	Cache      string
	Branch     string
	Definition []byte
	Token      string
	// Supply an HTTP/3 client here when desired. The default client negotiates
	// HTTP/2 over TLS. The caller owns any supplied client and transport.
	HTTPClient *http.Client
	Timeout    time.Duration
	// DisableCompression disables negotiated HTTP/per-event compression.
	DisableCompression bool
	// CompressionLevel accepts 1 (default, low CPU) or 6 (smaller output).
	CompressionLevel int
	// BatchMilliseconds is the maximum intentional linger for concurrent writes.
	// Zero sends immediately. Write calls still wait for durable acknowledgement.
	BatchMilliseconds int
}
type Logger struct {
	batch           timedBatch
	mu              sync.Mutex
	config          Config
	layout          *cache.Layout
	source          *cache.Layout
	projection      *cache.Projection
	definitions     map[string]*cache.Layout
	client          *http.Client
	origin          *url.URL
	digest          string
	ready, closed   bool
	compression     bool
	httpCompression bool
}

// Open registers before returning a usable logger. Definition can come from a
// static compiler or an external generator (e.g. Postgres table metadata).
func Open(ctx context.Context, c Config) (*Logger, error) {
	if c.BatchMilliseconds < 0 || c.BatchMilliseconds > 60000 {
		return nil, fmt.Errorf("batch milliseconds must be between 0 and 60000")
	}
	if c.CompressionLevel == 0 {
		c.CompressionLevel = 1
	}
	if c.CompressionLevel != 1 && c.CompressionLevel != 6 {
		return nil, fmt.Errorf("compression level must be 1 or 6")
	}
	l, e := cache.ParseDefinition(c.Definition)
	if e != nil {
		return nil, e
	}
	u, e := url.Parse(c.URL)
	if e != nil {
		return nil, e
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("URL must be a listener origin")
	}
	if u.Scheme != "https" {
		if u.Scheme != "http" || u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback() {
			return nil, fmt.Errorf("remote PIL logging requires HTTPS")
		}
	}
	if c.Cache == "" {
		return nil, fmt.Errorf("cache required")
	}
	if c.Branch == "" {
		c.Branch = "write"
	}
	if _, e = cache.ParseBranch(c.Branch); e != nil {
		return nil, e
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.Timeout < 0 {
		return nil, fmt.Errorf("positive timeout required")
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	// Do not redirect registrations, credentials or event bodies to another path.
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.Definition = nil // Layout owns a defensive copy of the definition bytes.
	w := &Logger{config: c, layout: l, source: l, definitions: map[string]*cache.Layout{fmt.Sprintf("%x", l.Digest): l}, client: &clone, origin: u, digest: fmt.Sprintf("%x", l.Digest)}
	if e = w.Register(ctx); e != nil {
		return nil, e
	}
	return w, nil
}
func (w *Logger) request(ctx context.Context, method, path string, body io.Reader, branches ...string) (*http.Response, error) {
	u := *w.origin
	u.Path = path
	if path == "/ingest" {
		n := w.layout.Definition.Resource
		branch := w.config.Branch
		if len(branches) > 0 {
			branch = branches[0]
		}
		q := url.Values{"tap": {fmt.Sprintf("%s/%s/%s.v%d/%s", w.config.Cache, n.Service, n.Resource, n.Major, branch)}}
		u.RawQuery = q.Encode()
	}
	r, e := http.NewRequestWithContext(ctx, method, u.String(), body)
	if e != nil {
		return nil, e
	}
	announced := w.layout
	if path == "/definitions" && method != "POST" {
		announced = w.source
	}
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("Definition-Version", fmt.Sprintf("%x", announced.Digest))
	r.Header.Set("Definition-Identity", announced.Definition.Identity)
	r.Header.Set("Definition-Revision", fmt.Sprint(announced.Definition.Version))
	// This client implements both fixed and adaptive layouts, regardless of the
	// producer's initial choice. The server may select either supported profile.
	r.Header.Set("PIL-Features", "adaptive-integers,"+cache.CompressionFeature+","+cache.HTTPCompressionFeature)
	if w.config.Token != "" {
		r.Header.Set("Authorization", "Bearer "+w.config.Token)
	}
	if method == "PUT" {
		r.Header.Set("Content-Type", "application/cbor")
	} else if method == "POST" {
		r.Header.Set("Content-Type", "application/pil")
		if compressed, ok := body.(*compressedRequest); ok {
			r.Header.Set("Content-Encoding", "gzip")
			r.ContentLength = int64(compressed.Len())
		}
	}
	return w.client.Do(r)
}
func responseError(r *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	return fmt.Errorf("PIL request rejected (%d, accepted=%s): %s", r.StatusCode, r.Header.Get("Trunk-Received"), strings.TrimSpace(string(b)))
}
func (w *Logger) ack(r *http.Response) error {
	defer r.Body.Close()
	if r.StatusCode != 204 {
		return responseError(r)
	}
	if r.Header.Get("PIL-Version") != "2" || r.Header.Get("Definition-Version") != w.digest {
		return fmt.Errorf("server acknowledged a different PIL definition")
	}
	w.compression = !w.config.DisableCompression && strings.Contains(","+r.Header.Get("PIL-Features")+",", ","+cache.CompressionFeature+",")
	w.httpCompression = !w.config.DisableCompression && strings.Contains(","+r.Header.Get("PIL-Features")+",", ","+cache.HTTPCompressionFeature+",")
	return nil
}

// Register can be called again after a delivery error or server restart. It
// preserves the producer definition while adopting a compatible preferred layout.
// It never retries an event.
func (w *Logger) Register(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.registerLocked(ctx)
}
func (w *Logger) registerLocked(ctx context.Context) error {
	if w.closed {
		return errors.New("logger closed")
	}
	w.ready = false
	ctx, cancel := context.WithTimeout(ctx, w.config.Timeout)
	defer cancel()
	method := "HEAD"
	r, e := w.request(ctx, method, "/definitions", nil)
	if e != nil {
		return e
	}
	if r.StatusCode == 404 {
		r.Body.Close()
		method = "PUT"
		r, e = w.request(ctx, method, "/definitions", bytes.NewReader(w.source.Bytes))
		if e != nil {
			return e
		}
	}
	if r.StatusCode == 200 {
		digest := r.Header.Get("Definition-Version")
		target := w.definitions[digest]
		if target == nil {
			if method == "HEAD" {
				r.Body.Close()
				r, e = w.request(ctx, "GET", "/definitions", nil)
				if e != nil {
					return e
				}
			}
			raw, readErr := io.ReadAll(io.LimitReader(r.Body, cache.MaxFrame+1))
			r.Body.Close()
			if readErr != nil {
				return readErr
			}
			if r.StatusCode != 200 || r.Header.Get("PIL-Version") != "2" {
				return fmt.Errorf("invalid preferred definition response")
			}
			target, e = cache.ParseDefinition(raw)
			if e != nil {
				return e
			}
			if fmt.Sprintf("%x", target.Digest) != digest || r.Header.Get("Definition-Version") != digest {
				return fmt.Errorf("preferred definition digest mismatch")
			}
		} else {
			r.Body.Close()
		}
		if target.Definition.Version < w.layout.Definition.Version {
			return fmt.Errorf("server definition rollback requires a new logger")
		}
		projection, e := cache.CompileProjection(w.source, target)
		if e != nil {
			return e
		}
		w.layout, w.digest, w.projection = target, digest, projection
		w.definitions[digest] = target
		r, e = w.request(ctx, "POST", "/definitions", nil)
		if e != nil {
			return e
		}
	} else if r.StatusCode == 204 {
		// A server reverting to the original definition must not cause us to label
		// bytes with the previously negotiated layout.
		if w.layout.Digest != w.source.Digest {
			r.Body.Close()
			return fmt.Errorf("server definition rollback requires a new logger")
		}
	}
	if e = w.ack(r); e != nil {
		return e
	}
	w.ready = true
	return nil
}

// WriteValues encodes positional values directly into PIL. An absent event_id
// gets a UUIDv7; a supplied ID stays stable for caller-controlled retries.
func (w *Logger) WriteValues(ctx context.Context, values []Value) error {
	return w.WriteValuesTo(ctx, w.config.Branch, values)
}

// WriteValuesTo selects an explicit event branch without another logger session.
// Definition negotiation remains collection-scoped; errors need not pollute the
// configured successful-operation branch.
func (w *Logger) WriteValuesTo(ctx context.Context, branch string, values []Value) error {
	if w.config.BatchMilliseconds > 0 {
		if _, err := cache.ParseBranch(branch); err != nil {
			return err
		}
		w.mu.Lock()
		if w.closed || !w.ready {
			w.mu.Unlock()
			return errors.New("PIL logger is not registered")
		}
		if len(values) != len(w.source.Definition.Fields) {
			w.mu.Unlock()
			return fmt.Errorf("field count mismatch")
		}
		if values[w.source.Event].State == Absent {
			values = append([]Value(nil), values...)
			id, err := codec.NewUUIDv7(time.Now())
			if err != nil {
				w.mu.Unlock()
				return err
			}
			values[w.source.Event] = Value{State: Direct, Bytes: id[:]}
		}
		err := w.source.ValidateValues(values)
		var raw []byte
		if err == nil {
			raw, err = w.source.Encode(nil, values)
		}
		w.mu.Unlock()
		if err != nil {
			return err
		}
		return w.enqueue(ctx, branch, [][]byte{raw})
	}
	if _, e := cache.ParseBranch(branch); e != nil {
		return e
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || !w.ready {
		return errors.New("PIL logger is not registered")
	}
	if len(values) != len(w.source.Definition.Fields) {
		return fmt.Errorf("field count mismatch")
	}
	if values[w.source.Event].State == Absent {
		values = append([]Value(nil), values...)
		id, e := codec.NewUUIDv7(time.Now())
		if e != nil {
			return e
		}
		values[w.source.Event] = Value{State: Direct, Bytes: id[:]}
	}
	v := values[w.source.Event]
	if v.State != Direct || len(v.Bytes) != 16 || !codec.UUIDv7(v.Bytes).Valid() {
		return fmt.Errorf("event_id must be UUIDv7")
	}
	if e := w.source.ValidateValues(values); e != nil {
		return e
	}
	encodedValues := values
	if w.projection != nil {
		mapped, e := w.projection.Values(values)
		if e != nil {
			return e
		}
		encodedValues = mapped
	}
	frame, e := w.layout.Encode(make([]byte, 0, 128), encodedValues)
	if e != nil {
		return e
	}
	return w.write(ctx, [][]byte{frame}, branch)
}

// WriteFrames accepts complete frames in the original producer layout. Matching
// layouts pass through unchanged; negotiated changes use the compiled projection.
// Batch requests amortize HTTP overhead. Frames must retain their
// event IDs across retries. Caller retains ownership after this method returns.
func (w *Logger) WriteFrames(ctx context.Context, frames ...[]byte) error {
	if w.config.BatchMilliseconds > 0 && len(frames) > 0 {
		return w.enqueue(ctx, w.config.Branch, frames)
	}
	return w.writeFramesNow(ctx, frames, w.config.Branch)
}
func (w *Logger) writeFramesNow(ctx context.Context, frames [][]byte, branch string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || !w.ready {
		return errors.New("PIL logger is not registered")
	}
	var f cache.Frame
	var converted [][]byte
	if w.projection != nil {
		converted = make([][]byte, len(frames))
	}
	for i, raw := range frames {
		if e := w.source.Scan(raw, &f); e != nil {
			return e
		}
		v := f.Values[w.source.Event]
		if v.State != Direct || len(v.Bytes) != 16 || !codec.UUIDv7(v.Bytes).Valid() {
			return fmt.Errorf("event_id must be UUIDv7")
		}
		if w.projection != nil {
			mapped, e := w.projection.Values(f.Values)
			if e != nil {
				return e
			}
			encoded, e := w.layout.Encode(make([]byte, 0, 128), mapped)
			if e != nil {
				return e
			}
			converted[i] = encoded
		}
	}
	if converted != nil {
		frames = converted
	}
	return w.write(ctx, frames, branch)
}
func (w *Logger) writeOnce(ctx context.Context, frames [][]byte, branch string) error {
	if len(frames) == 0 {
		return nil
	}
	readers := make([]io.Reader, len(frames))
	for i, b := range frames {
		if w.compression && !w.httpCompression {
			var err error
			b, err = w.layout.CompressEventLevel(b, w.config.CompressionLevel)
			if err != nil {
				return err
			}
		}
		readers[i] = bytes.NewReader(b)
	}
	var body io.Reader = io.MultiReader(readers...)
	if w.httpCompression {
		// Compress the batch once, including every PIL header. Ingress removes
		// this coding before staging; there is no per-event recompression here.
		var packed bytes.Buffer
		if err := pilcodec.Gzip(&packed, body, w.config.CompressionLevel); err != nil {
			return err
		}
		plainSize := 0
		for _, frame := range frames {
			plainSize += len(frame)
		}
		if packed.Len() < plainSize {
			body = &compressedRequest{bytes.NewReader(packed.Bytes())}
		} else {
			for i, frame := range frames {
				readers[i] = bytes.NewReader(frame)
			}
			body = io.MultiReader(readers...)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, w.config.Timeout)
	defer cancel()
	r, e := w.request(ctx, "POST", "/ingest", body, branch)
	if e != nil {
		w.ready = false
		return e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		w.ready = false
		if r.StatusCode == http.StatusPreconditionFailed && r.Header.Get("PIL-Capacity-Rollover") == "1" {
			n, err := strconv.Atoi(r.Header.Get("Trunk-Received"))
			if err != nil || n < 0 || n > len(frames) {
				return fmt.Errorf("invalid rollover acknowledgement")
			}
			return capacityRollover{n}
		}
		return responseError(r)
	}
	if r.Header.Get("PIL-Version") != "2" || r.Header.Get("Definition-Version") != w.digest || r.Header.Get("Trunk-Acknowledgement") != "durable-staging" || r.Header.Get("Trunk-Received") != strconv.Itoa(len(frames)) {
		w.ready = false
		return fmt.Errorf("missing or inconsistent durable PIL acknowledgement")
	}
	_, e = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
	if e != nil {
		w.ready = false
	}
	return e
}

type compressedRequest struct{ *bytes.Reader }

func (w *Logger) Close() error {
	w.batch.mu.Lock()
	w.batch.closed = true
	w.batch.mu.Unlock()
	_ = w.Flush(context.Background())
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	w.ready = false
	w.batch.mu.Lock()
	err := w.batch.lastErr
	w.batch.mu.Unlock()
	return err
}

// AcceptedDefinition returns a copy for inspection or generated encoders. The
// WriteValues/WriteFrames input contract remains the original producer layout.
func (w *Logger) AcceptedDefinition() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.layout.Bytes...)
}

// A capacity transition explicitly acknowledges a prefix. Only its unaccepted
// suffix is projected/retried; other delivery failures retain existing semantics.
type capacityRollover struct{ accepted int }

func (e capacityRollover) Error() string { return "PIL index capacity rollover" }
func (w *Logger) write(ctx context.Context, frames [][]byte, branch string) error {
	ctx, cancel := context.WithTimeout(ctx, w.config.Timeout)
	defer cancel()
	for len(frames) > 0 {
		previous := w.layout
		err := w.writeOnce(ctx, frames, branch)
		var rollover capacityRollover
		if !errors.As(err, &rollover) {
			return err
		}
		if err = w.registerLocked(ctx); err != nil {
			return err
		}
		if w.layout.Digest == previous.Digest {
			return fmt.Errorf("capacity rollover did not advance definition")
		}
		frames = frames[rollover.accepted:]
		projection, err := cache.CompileProjection(previous, w.layout)
		if err != nil {
			return err
		}
		mapped := make([][]byte, len(frames))
		var f cache.Frame
		for i, raw := range frames {
			if err = previous.Scan(raw, &f); err != nil {
				return err
			}
			values, err := projection.Values(f.Values)
			if err != nil {
				return err
			}
			mapped[i], err = w.layout.Encode(nil, values)
			if err != nil {
				return err
			}
		}
		frames = mapped
	}
	return nil
}
