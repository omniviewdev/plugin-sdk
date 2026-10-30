package log

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Level is the logging severity level.
type Level int32

const (
	// LevelTrace enables the most detailed diagnostic records.
	LevelTrace Level = iota
	// LevelDebug enables debugging records and more severe records.
	LevelDebug
	// LevelInfo enables informational records and more severe records.
	LevelInfo
	// LevelWarn enables warning records and more severe records.
	LevelWarn
	// LevelError enables error and fatal records.
	LevelError
	// LevelFatal marks fatal conditions without requiring process termination.
	LevelFatal
)

const errorLevelName = "error"

// String returns the lowercase severity name, or "unknown" for an invalid level.
func (l Level) String() string {
	switch l {
	case LevelTrace:
		return "trace"
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return errorLevelName
	case LevelFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// ParseLevel accepts case-insensitive severity names and surrounding whitespace.
// Empty input selects info; "warning" aliases warn. Invalid input returns info and an error.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return LevelDebug, nil
	case "info", "":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case errorLevelName:
		return LevelError, nil
	case "fatal":
		return LevelFatal, nil
	default:
		return LevelInfo, fmt.Errorf("invalid log level %q", s)
	}
}

// LevelController manages the effective log level at runtime.
type LevelController struct {
	level atomic.Int32
}

// NewLevelController creates a concurrency-safe threshold controller.
func NewLevelController(initial Level) *LevelController {
	c := &LevelController{}
	c.Set(initial)
	return c
}

// Level returns the current severity threshold.
func (c *LevelController) Level() Level {
	return Level(c.level.Load())
}

// Set atomically replaces the severity threshold.
func (c *LevelController) Set(level Level) {
	c.level.Store(int32(level))
}

// Enabled reports whether level meets the current severity threshold.
func (c *LevelController) Enabled(level Level) bool {
	return level >= c.Level()
}
