package logging

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	Keys "trie-trunk/go/logger/client/keys"

	"trie-trunk/go/codec"
	"trie-trunk/go/models"
)

// Correlation header names are case-insensitive; hyphens are significant.
const (
	HeaderTransactionId       = "transaction-id"
	HeaderClientTransactionId = "client-transaction-id"
	HeaderClientId            = "client-id"
)

// Handler returns errors to the transport boundary instead of logging them.
type Handler func(http.ResponseWriter, *http.Request) error

type HTTPOptions struct {
	// OnError reports sink/response write failures. It must not log back into the
	// same transaction. With nil, failures remain observable in Finish's result
	// for manual transactions only; HTTP callers should provide this callback.
	OnError func(error)
}

// Handle adapts error-returning handlers; Middleware supports ordinary handlers.
func (l *Logger) Handle(next Handler, options ...HTTPOptions) http.Handler {
	return l.serve(next, l.httpOptions(options))
}
func (l *Logger) Middleware(next http.Handler, options ...HTTPOptions) http.Handler {
	return l.serve(func(w http.ResponseWriter, r *http.Request) error { next.ServeHTTP(w, r); return nil }, l.httpOptions(options))
}

// Options override Config.OnError at handler construction, never per request.
// With multiple options the last wins; an empty option disables reporting.
func (l *Logger) httpOptions(options []HTTPOptions) HTTPOptions {
	if len(options) != 0 {
		return options[len(options)-1]
	}
	return HTTPOptions{OnError: l.config.OnError}
}

func (l *Logger) serve(next Handler, options HTTPOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, b := l.Begin(r.Context())
		r = r.WithContext(ctx)
		recorder := &responseWriter{ResponseWriter: w}
		wrapped := preserveInterfaces(recorder)
		if id := b.Id(); id.Valid() {
			w.Header().Set(HeaderTransactionId, id.String())
		}
		// Forwarded headers are untrusted unless a separate trusted-proxy layer
		// has validated them. Query strings and bodies are not collected.
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		b.mutate(func(e *Entry) {
			limit := l.config.MaxTextBytes
			e.ClientTransactionId = codec.LimitText(r.Header.Get(HeaderClientTransactionId), limit)
			e.ClientId = codec.LimitText(r.Header.Get(HeaderClientId), limit)
			e.Uri = codec.LimitText(r.URL.Path, limit)
			e.ClientIp = codec.LimitText(ip, limit)
			e.HttpMethod = methodSymbol(codec.LimitText(r.Method, limit))
		})
		report := func(err error) {
			if err != nil && options.OnError != nil {
				options.OnError(err)
			}
		}
		defer func() {
			p := recover()
			committedBeforePanic := recorder.committed || recorder.hijacked
			if p != nil {
				b.Error(models.New(models.UNEXPECTED))
				if l.config.Diagnostics {
					b.mutate(func(e *Entry) { e.Diagnostic = codec.LimitText(fmt.Sprint(p), l.config.MaxTextBytes) })
				}
				if p != http.ErrAbortHandler && !recorder.committed && !recorder.hijacked {
					report(models.WriteResponse(wrapped, models.New(models.UNEXPECTED).WithSource(l.config.Service), l.config.Dictionary))
				}
			}
			if recorder.writeErr != nil {
				b.Error(recorder.writeErr)
			}
			if ctx.Err() != nil {
				b.mutate(func(e *Entry) {
					if e.Error == nil {
						e.Error = models.Normalize(ctx.Err(), e.Service)
						e.Level = WARN
					}
				})
			}
			if recorder.status == 0 && !recorder.hijacked {
				// A recovered abort before any response has no actual status.
				if p != http.ErrAbortHandler {
					recorder.status = http.StatusOK
				}
			}
			b.mutate(func(e *Entry) {
				if recorder.status != 0 {
					e.HttpStatus = uint16(recorder.status)
					e.present |= 1 << Keys.HttpStatus
				}
				e.ResponseBytes = recorder.bytes
				e.present |= 1 << Keys.ResponseBytes
				if e.Error == nil && recorder.status >= 400 {
					typ := models.SERVICE
					if recorder.status >= 500 {
						typ = models.SYSTEM
					}
					e.Error = models.New(models.UNEXPECTED).WithType(typ).WithStatus(uint16(recorder.status)).WithSource(e.Service)
					if recorder.status >= 500 {
						e.Level = ERROR
					} else {
						e.Level = WARN
					}
				}
			})
			report(b.Finish())
			// Once headers are sent, a panic must abort the stream instead of
			// allowing net/http to present a truncated success as complete.
			if p == http.ErrAbortHandler || (p != nil && committedBeforePanic) {
				panic(http.ErrAbortHandler)
			}
		}()
		if err := next(wrapped, r); err != nil {
			failure := b.recordError(err)
			if !recorder.committed && !recorder.hijacked {
				report(models.WriteResponse(wrapped, failure, l.config.Dictionary))
			}
		}
	})
}

// responseWriter records final headers and actual bytes accepted by the writer.
// ResponseWriter itself is not safe for concurrent use, matching net/http.
type responseWriter struct {
	http.ResponseWriter
	status    int
	bytes     uint64
	committed bool
	hijacked  bool
	writeErr  error
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Track controller-only flushing as well as legacy http.Flusher. A writer that
// rejects flushing must not accidentally commit a 200 response.
func (w *responseWriter) FlushError() error {
	if w.hijacked {
		return http.ErrHijacked
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if !errors.Is(err, http.ErrNotSupported) {
		if !w.committed {
			w.status, w.committed = http.StatusOK, true
		}
		if err != nil {
			w.writeErr = err
		}
	}
	return err
}
func (w *responseWriter) WriteHeader(status int) {
	if w.committed || w.hijacked {
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.ResponseWriter.WriteHeader(status)
	w.status, w.committed = status, true
}
func (w *responseWriter) Write(data []byte) (int, error) {
	if w.hijacked {
		return 0, http.ErrHijacked
	}
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += uint64(n)
	if err != nil {
		w.writeErr = err
	}
	return n, err
}

type flushAdapter struct{ *responseWriter }

func (f flushAdapter) Flush() { _ = f.FlushError() }

type hijackAdapter struct{ *responseWriter }

func (h hijackAdapter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(h.ResponseWriter).Hijack()
	if err == nil {
		h.hijacked = true
	}
	return conn, rw, err
}

type pushAdapter struct{ *responseWriter }

func (p pushAdapter) Push(target string, options *http.PushOptions) error {
	if v, ok := p.ResponseWriter.(http.Pusher); ok {
		return v.Push(target, options)
	}
	return http.ErrNotSupported
}

// Preserve legacy interface assertions as well as ResponseController.Unwrap.
type unwrappingWriter interface {
	http.ResponseWriter
	Unwrap() http.ResponseWriter
	FlushError() error
}

func preserveInterfaces(w *responseWriter) http.ResponseWriter {
	_, f := w.ResponseWriter.(http.Flusher)
	_, h := w.ResponseWriter.(http.Hijacker)
	_, p := w.ResponseWriter.(http.Pusher)
	flags := 0
	if f {
		flags |= 1
	}
	if h {
		flags |= 2
	}
	if p {
		flags |= 4
	}
	switch flags {
	case 1:
		return struct {
			unwrappingWriter
			http.Flusher
		}{w, flushAdapter{w}}
	case 2:
		return struct {
			unwrappingWriter
			http.Hijacker
		}{w, hijackAdapter{w}}
	case 3:
		return struct {
			unwrappingWriter
			http.Flusher
			http.Hijacker
		}{w, flushAdapter{w}, hijackAdapter{w}}
	case 4:
		return struct {
			unwrappingWriter
			http.Pusher
		}{w, pushAdapter{w}}
	case 5:
		return struct {
			unwrappingWriter
			http.Flusher
			http.Pusher
		}{w, flushAdapter{w}, pushAdapter{w}}
	case 6:
		return struct {
			unwrappingWriter
			http.Hijacker
			http.Pusher
		}{w, hijackAdapter{w}, pushAdapter{w}}
	case 7:
		return struct {
			unwrappingWriter
			http.Flusher
			http.Hijacker
			http.Pusher
		}{w, flushAdapter{w}, hijackAdapter{w}, pushAdapter{w}}
	default:
		return w
	}
}

func methodSymbol(method string) codec.Symbol {
	switch method {
	case "GET":
		return codec.Ref(uint16(GET))
	case "POST":
		return codec.Ref(uint16(POST))
	case "PUT":
		return codec.Ref(uint16(PUT))
	case "PATCH":
		return codec.Ref(uint16(PATCH))
	case "DELETE":
		return codec.Ref(uint16(DELETE))
	case "HEAD":
		return codec.Ref(uint16(HEAD))
	case "OPTIONS":
		return codec.Ref(uint16(OPTIONS))
	case "CONNECT":
		return codec.Ref(uint16(CONNECT))
	case "TRACE":
		return codec.Ref(uint16(TRACE_METHOD))
	default:
		return codec.Text(method)
	}
}
