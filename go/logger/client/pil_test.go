package logging

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"trie-trunk/go/codec"
	Keys "trie-trunk/go/logger/client/keys"
	"trie-trunk/go/logger/pil"
	"trie-trunk/go/models"
	"trie-trunk/go/server/cache"
)

type pilRoundTrip func(*http.Request) (*http.Response, error)

func (f pilRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func builderMap(t testing.TB) ([]byte, *codec.Dictionary) {
	t.Helper()
	dictionary, err := codec.NewDictionary("payment/v1", map[string]map[uint16]string{
		"service": {17: "payment"}, "resource": {8: "transactions"}, "operation": {23: "authorize"}, "checkpoint": {41: "db"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := BuildPILDefinition(pil.Binding{Service: "payment", Resource: "requests", Major: 1, Timezone: "UTC", Path: []string{"level", "service", "resource", "operation"}}, dictionary, "16")
	if err != nil {
		t.Fatal(err)
	}
	return raw, dictionary
}

func TestPILBuilderNegotiatedCompletion(t *testing.T) {
	raw, dict := builderMap(t)
	source, _ := cache.ParseDefinition(raw)
	d := source.Definition
	d.Resource.Minor++
	d.Version++
	d.Fields = append(d.Fields, pil.Field{Name: "attempts", Type: "U16"})
	// Different order and dictionary ordinal assignment must preserve values.
	d.Fields[0], d.Fields[3] = d.Fields[3], d.Fields[0]
	for i := range d.Fields {
		if d.Fields[i].Name == "level" {
			d.Fields[i].Values = []string{"WARN", "TRACE", "INFO", "ERROR", "DEBUG"}
		}
	}
	target, err := cache.Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := cache.OpenEndpoint(cache.Config{Root: t.TempDir(), Cache: "demo", Tenant: "local", Layout: target})
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	var frames [][]byte
	var branches []string
	client := &http.Client{Transport: pilRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/ingest" {
			if r.Header.Get("Content-Type") != "application/pil" {
				t.Fatal("not PIL")
			}
			body, e := io.ReadAll(r.Body)
			if e != nil {
				return nil, e
			}
			frames = append(frames, body)
			branches = append(branches, r.URL.Query().Get("tap"))
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		w := httptest.NewRecorder()
		endpoint.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	logger, err := NewPIL(context.Background(), pil.Config{URL: "https://test", Cache: "demo", Branch: "read", Definition: raw, HTTPClient: client}, Config{Dictionary: dict, Service: codec.Ref(17), Diagnostics: true})
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	_, entry := logger.Begin(context.Background())
	entry.Resource(8).Operation(23).HttpMethod(GET).HttpStatus(200).ResponseBytes(0).
		Field("attempts", codec.Uint(3)).Field("password", codec.String("secret")).Field("nullable", codec.Value{})
	entry.Checkpoint(41, time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := entry.Finish(); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if len(frames) != 1 {
		t.Fatalf("got %d writes", len(frames))
	}
	var f cache.Frame
	if err := target.Scan(frames[0], &f); err != nil {
		t.Fatal(err)
	}
	values := map[string]cache.Value{}
	for i, field := range target.Definition.Fields {
		values[field.Name] = f.Values[i]
	}
	eventID := entry.Id()
	if !bytes.Equal(values["event_id"].Bytes, eventID[:]) {
		t.Fatal("identity changed")
	}
	if !bytes.Equal(values["attempts"].Bytes, []byte{3}) {
		t.Fatal("not promoted/shortened", values["attempts"])
	}
	if values["response_bytes"].State != cache.Direct || values["error"].State != cache.Absent {
		t.Fatal("presence lost")
	}
	if values["level"].Bytes[0] != 2 {
		t.Fatal("dictionary translation")
	}
	var extras map[string]any
	if err := cbor.Unmarshal(values["@fields"].Bytes, &extras); err != nil {
		t.Fatal(err)
	}
	if extras["password"] != "*******" || extras["nullable"] != nil || len(extras) != 2 {
		t.Fatal(extras)
	}
	var checkpoints []map[string]any
	if err := cbor.Unmarshal(values["checkpoints"].Bytes, &checkpoints); err != nil {
		t.Fatal(err)
	}
	if checkpoints[0]["name"] != "db" || checkpoints[0]["duration"] != uint64(1000) {
		t.Fatal(checkpoints)
	}
	if entry.Field("late", codec.Bool(true)).Check() != ErrFinalized {
		t.Fatal("builder reopened")
	}
	// Reconnect retains the original producer contract.
	if err := logger.Reconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, failed := logger.Begin(context.Background())
	failed.Error(models.New(models.INVALID_INPUT)).HttpStatus(400)
	if err := failed.Finish(); err != nil {
		t.Fatal(err)
	}
	if branches[0] != "demo/payment/requests.v1/read" || branches[1] != "demo/payment/requests.v1/error" {
		t.Fatal("branch separation", branches)
	}
	if err := target.Scan(frames[1], &f); err != nil {
		t.Fatal(err)
	}
	for i, field := range target.Definition.Fields {
		if field.Name == "access" && (f.Values[i].State != cache.Direct || len(f.Values[i].Bytes) != 1 || f.Values[i].Bytes[0] != 0) {
			t.Fatal("failed read lost read access", f.Values[i])
		}
		if field.Name == "error" {
			var failure map[string]any
			if err := cbor.Unmarshal(f.Values[i].Bytes, &failure); err != nil {
				t.Fatal(err)
			}
			if failure["code"] != "INVALID_INPUT" {
				t.Fatal(failure)
			}
		}
	}
	// Storage routing must not erase the request's access classification.
	for _, tc := range []struct {
		method HttpMethod
		level  Level
		route  string
		access byte
	}{
		{POST, INFO, "write", 1}, {GET, WARN, "debug", 0}, {POST, WARN, "debug", 1}, {GET, DEBUG, "debug", 0}, {POST, DEBUG, "debug", 1},
		{GET, TRACE, "trace", 0}, {POST, TRACE, "trace", 1}, {POST, ERROR, "error", 1},
	} {
		_, b := logger.Begin(context.Background())
		b.HttpMethod(tc.method).Level(tc.level)
		if err := b.Finish(); err != nil {
			t.Fatal(err)
		}
		if got := branches[len(branches)-1]; got != "demo/payment/requests.v1/"+tc.route {
			t.Fatal("wrong route", got)
		}
		if err := target.Scan(frames[len(frames)-1], &f); err != nil {
			t.Fatal(err)
		}
		for i, field := range target.Definition.Fields {
			if field.Name == "level" && (f.Values[i].State != cache.Direct || len(f.Values[i].Bytes) != 1 || field.Values[int(f.Values[i].Bytes[0])] != BuildSchema()[Keys.Level].Enums[uint16(tc.level)]) {
				t.Fatalf("%v/%v changed level during routing", tc.method, tc.level)
			}
			if field.Name == "access" && (f.Values[i].State != cache.Direct || !bytes.Equal(f.Values[i].Bytes, []byte{tc.access})) {
				t.Fatalf("%v/%v lost access", tc.method, tc.level)
			}
		}
	}

}

func TestPILBuilderRejectsAmbiguousFields(t *testing.T) {
	raw, dict := builderMap(t)
	layout, _ := cache.ParseDefinition(raw)
	plan, err := compilePILEntry(layout, dict)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := newLogger(Config{Dictionary: dict})
	_, b := l.Begin(context.Background())
	b.Service(99)
	e := b.Snapshot()
	if _, err := plan.values(&e); err == nil {
		t.Fatal("unknown reference accepted")
	}
	b.Service(17).Field("event_id", codec.String("overwrite"))
	e = b.Snapshot()
	if _, err := plan.values(&e); err == nil {
		t.Fatal("identity shadow accepted")
	}
	d := layout.Definition
	d.Fields = append(d.Fields, pil.Field{Name: "attempts", Type: "U16"})
	layout, err = cache.Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = compilePILEntry(layout, dict)
	if err != nil {
		t.Fatal(err)
	}
	b.Remove("event_id").Field("attempts", codec.Uint(65536))
	e = b.Snapshot()
	if _, err := plan.values(&e); err == nil {
		t.Fatal("overflow accepted")
	}
	b.Field("attempts", codec.Uint(0))
	e = b.Snapshot()
	values, err := plan.values(&e)
	if err != nil {
		t.Fatal(err)
	}
	if values[len(values)-1].State != pil.Direct {
		t.Fatal("zero lost")
	}
}

// Serialization-only controls: same request entry and precompiled dictionaries.
// No HTTP, durable staging, or whole-object map conversion in either path.
var requestEncodingSink []byte

func BenchmarkRequestEncoding(b *testing.B) {
	raw, dict := builderMap(b)
	layout, err := cache.ParseDefinition(raw)
	if err != nil {
		b.Fatal(err)
	}
	plan, err := compilePILEntry(layout, dict)
	if err != nil {
		b.Fatal(err)
	}
	logger, _ := newLogger(Config{Dictionary: dict, Service: codec.Ref(17)})
	_, entry := logger.Begin(context.Background())
	entry.Resource(8).Operation(23).HttpMethod(GET).HttpStatus(200).Message(SuccessfulTransaction)
	event := entry.Snapshot()
	event.Duration = 250 * time.Microsecond
	b.Run("LegacyCBOR", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			raw, e := event.Encode(codec.CBOR, dict)
			if e != nil {
				b.Fatal(e)
			}
			requestEncodingSink = raw
			b.SetBytes(int64(len(raw)))
		}
	})
	b.Run("PIL", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			values, e := plan.values(&event)
			if e != nil {
				b.Fatal(e)
			}
			raw, e := layout.Encode(make([]byte, 0, 128), values)
			if e != nil {
				b.Fatal(e)
			}
			requestEncodingSink = raw
			b.SetBytes(int64(len(raw)))
		}
	})
}

func TestPILAccessSurvivesEveryLevel(t *testing.T) {
	raw, dict := builderMap(t)
	layout, err := cache.ParseDefinition(raw)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compilePILEntry(layout, dict)
	if err != nil {
		t.Fatal(err)
	}
	plan.access = "read"
	logger, _ := newLogger(Config{Dictionary: dict})
	for _, method := range []HttpMethod{GET, POST} {
		for _, level := range []Level{INFO, ERROR, WARN, DEBUG, TRACE} {
			_, b := logger.Begin(context.Background())
			b.HttpMethod(method).Level(level)
			e := b.Snapshot()
			values, err := plan.values(&e)
			if err != nil {
				t.Fatal(err)
			}
			access := "read"
			if method == POST {
				access = "write"
			}
			i := plan.core[slotAccess]
			want := plan.text(i, access)
			if values[i].State != want.State || !bytes.Equal(values[i].Bytes, want.Bytes) {
				t.Fatalf("%v/%v lost access", method, level)
			}
			li := plan.core[slotLevel]
			if !bytes.Equal(values[li].Bytes, plan.refs[li][uint16(level)].Bytes) {
				t.Fatal("access changed level")
			}
		}
	}
	// Non-HTTP operations retain the explicit routing access, including failures.
	plan.access = "write"
	_, b := logger.Begin(context.Background())
	b.Level(ERROR)
	e := b.Snapshot()
	if plan.accessFor(&e) != "write" {
		t.Fatal("failed write lost access")
	}
}
