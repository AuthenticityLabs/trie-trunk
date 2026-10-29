package logging

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"trie-trunk/go/codec"
	"trie-trunk/go/models"
)

// Excludes request construction and real I/O; includes correlation headers and
// the ordinary response wrapper, context, encoding and completion paths.
func BenchmarkHTTP(b *testing.B) {
	for _, failed := range []bool{false, true} {
		name := "Success"
		if failed {
			name = "Error"
		}
		b.Run(name, func(b *testing.B) {
			l := testLogger(b, io.Discard, Config{Format: codec.PIL, Service: codec.Ref(1)})
			failure := models.New(models.INVALID_INPUT)
			handler := l.Handle(func(w http.ResponseWriter, r *http.Request) error {
				if failed {
					return failure
				}
				w.WriteHeader(http.StatusNoContent)
				return nil
			}, HTTPOptions{})
			request := httptest.NewRequest("GET", "https://example.com/customer", nil)
			response := &benchmarkResponse{header: make(http.Header)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				handler.ServeHTTP(response, request)
			}
		})
	}
}

type benchmarkResponse struct{ header http.Header }

func (w *benchmarkResponse) Header() http.Header       { return w.header }
func (*benchmarkResponse) WriteHeader(int)             {}
func (*benchmarkResponse) Write(p []byte) (int, error) { return len(p), nil }
