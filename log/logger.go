package log

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
)

// Logger is the SDK-wide logging interface for plugin runtime logs.
type Logger interface {
	Named(name string) Logger
	With(fields ...Field) Logger
	Enabled(ctx context.Context, level Level) bool
	Log(ctx context.Context, level Level, msg string, fields ...Field)
	Trace(ctx context.Context, msg string, fields ...Field)
	Debug(ctx context.Context, msg string, fields ...Field)
	Info(ctx context.Context, msg string, fields ...Field)
	Warn(ctx context.Context, msg string, fields ...Field)
	Error(ctx context.Context, msg string, fields ...Field)
	Fatal(ctx context.Context, msg string, fields ...Field)
	Tracew(ctx context.Context, msg string, kv ...any)
	Debugw(ctx context.Context, msg string, kv ...any)
	Infow(ctx context.Context, msg string, kv ...any)
	Warnw(ctx context.Context, msg string, kv ...any)
	Errorw(ctx context.Context, msg string, kv ...any)
	Fatalw(ctx context.Context, msg string, kv ...any)
	Sync(ctx context.Context) error
}

type logger struct {
	name   string
	fields []Field
	level  *LevelController
	be     Backend
	now    func() time.Time
}

// New creates a logger with an info threshold, null backend, and [time.Now] by default.
// Fields are copied; Backend, Level, and values stored in fields remain shared.
func New(cfg Config) Logger {
	be := cfg.Backend
	if be == nil {
		be = NewHCLBackend(hclog.NewNullLogger())
	}
	level := cfg.Level
	if level == nil {
		level = NewLevelController(LevelInfo)
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	fields := slices.Clone(cfg.Fields)
	return &logger{name: cfg.Name, fields: fields, level: level, be: be, now: nowFn}
}

// Config selects the logger's name, persistent fields, output, threshold, and clock.
type Config struct {
	Name    string
	Fields  []Field
	Backend Backend
	Level   *LevelController
	Now     func() time.Time
}

// NewNop returns a logger whose backend discards every record.
func NewNop() Logger {
	return New(Config{Backend: NewHCLBackend(hclog.NewNullLogger())})
}

// ContextEnricher extracts structured fields from a context.
// Registered via RegisterContextEnricher to break import cycles.
type ContextEnricher func(context.Context) []Field

// enricherHolder allows an atomic pointer to publish a context enricher.
type enricherHolder struct{ fn ContextEnricher }

//nolint:gochecknoglobals // The public registration API shares one race-safe enricher across all loggers.
var contextEnricher atomic.Pointer[enricherHolder]

// RegisterContextEnricher sets the global context enricher used by all loggers.
// Typically called from an init() in the package that owns PluginContext.
func RegisterContextEnricher(fn ContextEnricher) {
	contextEnricher.Store(&enricherHolder{fn: fn})
}

// loggerHolder allows an atomic pointer to publish any Logger implementation.
type loggerHolder struct{ l Logger }

//nolint:gochecknoglobals // Default and SetDefault intentionally expose one process-wide logger.
var defaultLogger atomic.Pointer[loggerHolder]

// Default returns the process-wide logger, initially a logger that discards records.
func Default() Logger {
	if h := defaultLogger.Load(); h != nil {
		return h.l
	}
	h := &loggerHolder{l: NewNop()}
	if defaultLogger.CompareAndSwap(nil, h) {
		return h.l
	}
	return defaultLogger.Load().l
}

// SetDefault atomically replaces the process-wide logger; nil resets it to NewNop.
func SetDefault(l Logger) {
	if l == nil {
		l = NewNop()
	}
	defaultLogger.Store(&loggerHolder{l: l})
}

func (l *logger) Named(name string) Logger {
	if name == "" {
		return l
	}
	next := *l
	if next.name == "" {
		next.name = name
	} else {
		next.name = next.name + "." + name
	}
	return &next
}

func (l *logger) With(fields ...Field) Logger {
	if len(fields) == 0 {
		return l
	}
	next := *l
	next.fields = append(slices.Clone(l.fields), fields...)
	return &next
}

func (l *logger) Enabled(_ context.Context, level Level) bool {
	return l.level.Enabled(level)
}

func (l *logger) Log(ctx context.Context, level Level, msg string, fields ...Field) {
	if !l.Enabled(ctx, level) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	enriched := enrichFromContext(ctx)
	all := make([]Field, 0, len(l.fields)+len(fields)+len(enriched))
	all = append(all, l.fields...)
	all = append(all, enriched...)
	all = append(all, fields...)
	rec := Record{Timestamp: l.now(), Level: level, Logger: l.name, Message: msg, Fields: all}
	_ = l.be.Write(ctx, rec)
}

func (l *logger) Trace(ctx context.Context, msg string, fields ...Field) {
	l.Log(ctx, LevelTrace, msg, fields...)
}

func (l *logger) Debug(ctx context.Context, msg string, fields ...Field) {
	l.Log(ctx, LevelDebug, msg, fields...)
}

func (l *logger) Info(ctx context.Context, msg string, fields ...Field) {
	l.Log(ctx, LevelInfo, msg, fields...)
}

func (l *logger) Warn(ctx context.Context, msg string, fields ...Field) {
	l.Log(ctx, LevelWarn, msg, fields...)
}

func (l *logger) Error(ctx context.Context, msg string, fields ...Field) {
	l.Log(ctx, LevelError, msg, fields...)
}

func (l *logger) Fatal(ctx context.Context, msg string, fields ...Field) {
	l.Log(ctx, LevelFatal, msg, fields...)
}

func (l *logger) Tracew(ctx context.Context, msg string, kv ...any) {
	l.Log(ctx, LevelTrace, msg, kvFields(kv...)...)
}

func (l *logger) Debugw(ctx context.Context, msg string, kv ...any) {
	l.Log(ctx, LevelDebug, msg, kvFields(kv...)...)
}

func (l *logger) Infow(ctx context.Context, msg string, kv ...any) {
	l.Log(ctx, LevelInfo, msg, kvFields(kv...)...)
}

func (l *logger) Warnw(ctx context.Context, msg string, kv ...any) {
	l.Log(ctx, LevelWarn, msg, kvFields(kv...)...)
}

func (l *logger) Errorw(ctx context.Context, msg string, kv ...any) {
	l.Log(ctx, LevelError, msg, kvFields(kv...)...)
}

func (l *logger) Fatalw(ctx context.Context, msg string, kv ...any) {
	l.Log(ctx, LevelFatal, msg, kvFields(kv...)...)
}

func (l *logger) Sync(ctx context.Context) error { return l.be.Sync(ctx) }

// keyValueWidth is the number of arguments in each structured key/value pair.
const keyValueWidth = 2

func kvFields(kv ...any) []Field {
	if len(kv) == 0 {
		return nil
	}
	out := make([]Field, 0, (len(kv)+1)/keyValueWidth)
	for i := 0; i < len(kv); i += keyValueWidth {
		if i+1 >= len(kv) {
			out = append(out, Any("arg", kv[i]))
			break
		}
		key, ok := kv[i].(string)
		if !ok {
			key = fmt.Sprint(kv[i])
		}
		out = append(out, Any(key, kv[i+1]))
	}
	return out
}
