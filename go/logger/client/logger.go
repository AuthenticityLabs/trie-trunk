package logging

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/logger/out"
	"trie-trunk/go/logger/pil"
	"trie-trunk/go/models"
)

var ErrFinalized = errors.New("transaction already finalized")

type Config struct {
	// SchemaVersion assigns an immutable catalog version; zero uses the built-in default.
	SchemaVersion uint32
	Format        codec.Format
	Dictionary    *codec.Dictionary
	Service       codec.Symbol
	Version       string
	// DetailLevel controls conditional enrichment, never completion emission.
	DetailLevel     Level
	MaxFields       int
	MaxCheckpoints  int
	MaxTextBytes    int
	SensitiveFields []string
	// Diagnostics opts into bounded internal error text in logs only.
	Diagnostics bool
	// OnError reports HTTP completion and response write failures.
	// It must not log into the same transaction; it may run concurrently.
	OnError func(error)
}

// Logger serializes writes to its writer. JSON uses newline-delimited records;
// Local PIL output is raw; negotiated connections add a two-byte length prefix.
type Logger struct {
	pil       *pil.Logger
	pilPlan   *pilEntryPlan
	session   *out.Writer
	writer    io.Writer
	config    Config
	sensitive map[string]bool
	writeMu   sync.Mutex
}

func New(writer io.Writer, config Config) (*Logger, error) {
	if writer == nil {
		return nil, errors.New("log writer is required")
	}
	l, err := newLogger(config)
	if err != nil {
		return nil, err
	}
	l.writer = writer
	return l, nil
}

func newLogger(config Config) (*Logger, error) {
	if config.Format > codec.PIL || config.Format == codec.CBOR {
		return nil, errors.New("unsupported log format")
	}
	if config.MaxFields <= 0 {
		config.MaxFields = 64
	}
	if config.MaxCheckpoints <= 0 {
		config.MaxCheckpoints = 32
	}
	if config.MaxTextBytes <= 0 {
		config.MaxTextBytes = 4096
	}
	if config.DetailLevel == 0 {
		config.DetailLevel = INFO
	}
	config.Service = config.Service.LimitText(config.MaxTextBytes)
	config.Version = codec.LimitText(config.Version, config.MaxTextBytes)
	sensitive := map[string]bool{"password": true, "pin": true, "ssn": true, "authorization": true}
	for _, field := range config.SensitiveFields {
		sensitive[strings.ToLower(field)] = true
	}
	config.SensitiveFields = nil
	return &Logger{config: config, sensitive: sensitive}, nil
}

type contextKey struct{}

func (l *Logger) Begin(ctx context.Context) (context.Context, *Builder) {
	start := time.Now()
	id, err := codec.NewUUIDv7(start)
	b := &Builder{state: &state{logger: l, started: start, initErr: err, entry: Entry{Id: id, Category: SUMMARY, Level: INFO, Service: l.config.Service, Version: l.config.Version}}}
	return context.WithValue(ctx, contextKey{}, b), b
}

// FromContext returns a no-op builder when no transaction is attached.
func FromContext(ctx context.Context) *Builder {
	if b, ok := ctx.Value(contextKey{}).(*Builder); ok {
		return b
	}
	return &Builder{}
}

type state struct {
	started    time.Time
	initErr    error
	mu         sync.Mutex
	logger     *Logger
	entry      Entry
	finished   bool
	finishOnce sync.Once
	finishErr  error
}

// Builder copies share the same transaction. Each setter is synchronized;
// fluent chains are not atomic groups. Late setters are ignored; Check reports
// ErrFinalized. Finish is idempotent and returns the first submission result.
type Builder struct {
	state    *state
	disabled bool
}

func (b *Builder) enabled() bool { return b != nil && b.state != nil && !b.disabled }
func (b *Builder) mutate(fn func(*Entry)) *Builder {
	if !b.enabled() {
		return b
	}
	s := b.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.finished {
		fn(&s.entry)
	}
	return b
}
func (b *Builder) Check() error {
	if !b.enabled() {
		return nil
	}
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	if b.state.initErr != nil {
		return b.state.initErr
	}
	if b.state.finished {
		return ErrFinalized
	}
	return nil
}
func (b *Builder) If(level Level) *Builder {
	if !b.enabled() {
		return &Builder{}
	}
	return &Builder{state: b.state, disabled: level < b.state.logger.config.DetailLevel}
}
func (b *Builder) IfDebug() *Builder { return b.If(DEBUG) }
func (b *Builder) IfTrace() *Builder { return b.If(TRACE) }
func (b *Builder) Field(key string, value codec.Value) *Builder {
	return b.mutate(func(e *Entry) {
		l := b.state.logger
		if len(key) == 0 || len(key) > l.config.MaxTextBytes {
			e.Dropped++
			return
		}
		if _, exists := e.Fields[key]; !exists && len(e.Fields) >= l.config.MaxFields {
			e.Dropped++
			return
		}
		if e.Fields == nil {
			e.Fields = make(map[string]codec.Value)
		}
		if l.sensitive[strings.ToLower(key)] {
			value = codec.String("*******")
		} else {
			value = value.Sanitize(l.config.MaxTextBytes, l.config.MaxFields, 8, l.sensitive)
		}
		e.Fields[key] = value
	})
}

// FieldLazy evaluates only when detail collection is enabled and still open.
// The callback runs outside the lock; it may safely call other builder methods.
func (b *Builder) FieldLazy(key string, value func() codec.Value) *Builder {
	if !b.enabled() || b.Check() != nil || value == nil {
		return b
	}
	return b.Field(key, value())
}
func (b *Builder) Remove(key string) *Builder {
	return b.mutate(func(e *Entry) { delete(e.Fields, key) })
}
func (b *Builder) Error(err error) *Builder {
	b.recordError(err)
	return b
}

// Return the normalized public error while bounding only its log representation.
func (b *Builder) recordError(err error) *models.Error {
	if err == nil {
		return nil
	}
	var failure *models.Error
	b.mutate(func(e *Entry) {
		failure = models.Normalize(err, e.Service)
		e.Error = failure
		if e.Error != nil {
			e.Error = e.Error.LimitText(b.state.logger.config.MaxTextBytes)
			// A failed outcome is an error even when the caller caused it.
			// Warnings are supplemental diagnostics, never failed completions.
			e.Level = ERROR
			if b.state.logger.config.Diagnostics {
				e.Diagnostic = codec.LimitText(err.Error(), b.state.logger.config.MaxTextBytes)
			}
		}
	})
	return failure
}
func (b *Builder) StartCheckpoint(id CheckpointId) func() {
	return b.startCheckpoint(codec.Ref(uint16(id)))
}
func (b *Builder) StartCheckpointText(name string) func() {
	if !b.enabled() {
		return func() {}
	}
	return b.startCheckpoint(codec.Text(codec.LimitText(name, b.state.logger.config.MaxTextBytes)))
}
func (b *Builder) startCheckpoint(name codec.Symbol) func() {
	if !b.enabled() || b.Check() != nil {
		return func() {}
	}
	start := time.Now()
	var once sync.Once
	return func() {
		once.Do(func() {
			elapsed := time.Since(start)
			b.checkpoint(name, elapsed)
		})
	}
}

// Checkpoint records an already-measured duration without a closure or timer
// allocation. StartCheckpoint is an optional convenience with additional cost.
func (b *Builder) Checkpoint(id CheckpointId, duration time.Duration) *Builder {
	return b.checkpoint(codec.Ref(uint16(id)), duration)
}
func (b *Builder) checkpoint(name codec.Symbol, duration time.Duration) *Builder {
	if duration < 0 {
		return b
	}
	return b.mutate(func(e *Entry) {
		for i := range e.Checkpoints {
			if e.Checkpoints[i].Name == name {
				e.Checkpoints[i].Duration += duration
				e.Checkpoints[i].Count++
				return
			}
		}
		if len(e.Checkpoints) >= b.state.logger.config.MaxCheckpoints {
			e.Dropped++
			return
		}
		e.Checkpoints = append(e.Checkpoints, Checkpoint{Name: name, Duration: duration, Count: 1})
	})
}
func (b *Builder) Snapshot() Entry {
	if !b.enabled() {
		return Entry{}
	}
	s := b.state
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entry.clone()
	if !s.finished {
		e.Duration = time.Since(s.started)
	}
	return e
}
func (b *Builder) Finish() error {
	if !b.enabled() {
		return nil
	}
	s := b.state
	s.finishOnce.Do(func() {
		s.mu.Lock()
		s.finished = true
		s.entry.Duration = time.Since(s.started)
		if s.entry.Message.IsZero() {
			message := SuccessfulTransaction
			if s.entry.Error != nil || s.entry.HttpStatus >= 400 {
				message = FailedTransaction
			}
			s.entry.Message = codec.Ref(uint16(message))
		}
		// Finalization seals every mutation path. Transfer the frozen containers
		// to the encoder; only public Snapshot needs defensive copies.
		e := s.entry
		s.mu.Unlock()
		if s.initErr != nil {
			s.finishErr = s.initErr
			return
		}
		if s.logger.pil != nil {
			s.logger.writeMu.Lock()
			defer s.logger.writeMu.Unlock()
			values, err := s.logger.pilPlan.values(&e)
			if err == nil {
				// Completion must remain deliverable after request cancellation;
				// the PIL transport enforces its own bounded delivery timeout.
				branch := s.logger.pilPlan.accessFor(&e)
				if e.Error != nil || e.HttpStatus >= 400 || e.Level == ERROR {
					branch = "error"
				} else if e.Level == DEBUG || e.Level == WARN {
					branch = "debug"
				} else if e.Level == TRACE {
					branch = "trace"
				}
				err = s.logger.pil.WriteValuesTo(context.Background(), branch, values)
			}
			s.finishErr = err
			return
		}
		data, err := e.Encode(s.logger.config.Format, s.logger.config.Dictionary)
		if err != nil {
			s.finishErr = err
			return
		}
		if s.logger.config.Format != codec.PIL {
			data = append(data, '\n')
		}
		if s.logger.session != nil {
			_, s.finishErr = s.logger.session.Write(data)
			return
		}
		s.logger.writeMu.Lock()
		defer s.logger.writeMu.Unlock()
		n, err := s.logger.writer.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		s.finishErr = err
	})
	return s.finishErr
}

// Id remains stable throughout the transaction and after finalization.
func (b *Builder) Id() codec.UUIDv7 {
	if !b.enabled() {
		return codec.UUIDv7{}
	}
	return b.state.entry.Id
}

var ErrLocalWriter = errors.New("logger uses a local writer, not a negotiated connection")

// NewStream initializes and negotiates before returning a usable Logger. It
// always selects PIL; Config.Format applies only to New's local output mode.
// Connector is the transport seam; out.WebSocketConnector supplies WebSocket I/O.
func NewStream(ctx context.Context, connector out.Connector, config Config) (*Logger, error) {
	config.Format = codec.PIL
	l, err := newLogger(config)
	if err != nil {
		return nil, err
	}
	definition := BuildDefinition(l.config.Dictionary)
	if config.SchemaVersion != 0 {
		definition.Version = config.SchemaVersion
	}
	session, err := out.Open(ctx, connector, definition)
	if err != nil {
		return nil, err
	}
	l.session = session
	return l, nil
}

func (l *Logger) Reconnect(ctx context.Context) error {
	if l.pil != nil {
		return l.pil.Register(ctx)
	}
	if l.session == nil {
		return ErrLocalWriter
	}
	return l.session.Reconnect(ctx)
}

// Close owns negotiated connections only. Local io.Writers remain caller-owned.
func (l *Logger) Close() error {
	if l.pil != nil {
		return l.pil.Close()
	}
	if l.session == nil {
		return nil
	}
	return l.session.Close()
}
