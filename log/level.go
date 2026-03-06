package log

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Level is the logging severity level.
type Level int32

const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

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
		return "error"
	case LevelFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

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
	case "error":
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

func NewLevelController(initial Level) *LevelController {
	c := &LevelController{}
	c.Set(initial)
	return c
}

func (c *LevelController) Level() Level {
	return Level(c.level.Load())
}

func (c *LevelController) Set(level Level) {
	c.level.Store(int32(level))
}

func (c *LevelController) Enabled(level Level) bool {
	return level >= c.Level()
}
