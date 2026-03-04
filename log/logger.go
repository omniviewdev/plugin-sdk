package log

import (
	"context"
	"fmt"
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
	return &logger{name: cfg.Name, fields: cfg.Fields, level: level, be: be, now: nowFn}
}

type Config struct {
	Name    string
	Fields  []Field
	Backend Backend
	Level   *LevelController
	Now     func() time.Time
}

func NewNop() Logger {
	return New(Config{Backend: NewHCLBackend(hclog.NewNullLogger())})
}

var defaultLogger atomic.Value

func init() {
	defaultLogger.Store(NewNop())
}

func Default() Logger { return defaultLogger.Load().(Logger) }

func SetDefault(l Logger) {
	if l == nil {
		defaultLogger.Store(NewNop())
		return
	}
	defaultLogger.Store(l)
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
	next.fields = append(append([]Field{}, l.fields...), fields...)
	return &next
}

func (l *logger) Enabled(_ context.Context, level Level) bool {
	return l.level.Enabled(level)
}

func (l *logger) Log(ctx context.Context, level Level, msg string, fields ...Field) {
	if !l.Enabled(ctx, level) {
		return
	}
	all := make([]Field, 0, len(l.fields)+len(fields)+8)
	all = append(all, l.fields...)
	all = append(all, enrichFromContext(ctx)...)
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

func kvFields(kv ...any) []Field {
	if len(kv) == 0 {
		return nil
	}
	out := make([]Field, 0, (len(kv)+1)/2)
	for i := 0; i < len(kv); i += 2 {
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
