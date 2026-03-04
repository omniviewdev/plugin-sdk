package logtest

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
)

// TestLogSource is a programmable log source for testing.
// Feed lines via the Lines channel and inject events via the Events channel.
type TestLogSource struct {
	// Lines feeds log lines to the source. Close to end the stream.
	Lines chan string

	// Events injects stream events (reconnect, error, etc.).
	Events chan logs.LogStreamEvent

	// ErrOnConnect simulates a connection failure.
	ErrOnConnect error

	// ConnectDelay simulates a slow connection.
	ConnectDelay time.Duration
}

// NewTestLogSource creates a TestLogSource with buffered channels.
func NewTestLogSource() *TestLogSource {
	return &TestLogSource{
		Lines:  make(chan string, 100),
		Events: make(chan logs.LogStreamEvent, 100),
	}
}

// HandlerFunc returns a LogHandlerFunc that reads from this test source.
// The returned reader streams lines from the Lines channel, one per line.
// The reader respects context cancellation via the PluginContext so that
// goroutines unblock when the session is closed.
func (s *TestLogSource) HandlerFunc() logs.LogHandlerFunc {
	return func(pctx *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		if s.ConnectDelay > 0 {
			select {
			case <-pctx.Context.Done():
				return nil, pctx.Context.Err()
			case <-time.After(s.ConnectDelay):
			}
		}
		if s.ErrOnConnect != nil {
			return nil, s.ErrOnConnect
		}
		done := make(chan struct{})
		return &channelReader{lines: s.Lines, done: done, ctx: pctx.Context}, nil
	}
}

// ReaderFunc returns an io.ReadCloser that reads from this source's Lines channel.
// The reader respects the PluginContext for cancellation.
func (s *TestLogSource) ReaderFunc(pctx *types.PluginContext) (io.ReadCloser, error) {
	if s.ConnectDelay > 0 {
		select {
		case <-pctx.Context.Done():
			return nil, pctx.Context.Err()
		case <-time.After(s.ConnectDelay):
		}
	}
	if s.ErrOnConnect != nil {
		return nil, s.ErrOnConnect
	}
	return &channelReader{lines: s.Lines, done: make(chan struct{}), ctx: pctx.Context}, nil
}

// channelReader implements io.ReadCloser by reading lines from a channel.
// It respects context cancellation to avoid blocking forever.
type channelReader struct {
	lines  chan string
	buf    []byte
	closed bool
	done   chan struct{}
	ctx    context.Context
}

func (r *channelReader) Read(p []byte) (int, error) {
	// Drain leftover buffer first
	if len(r.buf) > 0 {
		n := copy(p, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}

	if r.closed {
		return 0, io.EOF
	}

	// Wait for next line or context cancellation
	select {
	case line, ok := <-r.lines:
		if !ok {
			r.closed = true
			return 0, io.EOF
		}
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		n := copy(p, []byte(line))
		if n < len(line) {
			r.buf = []byte(line[n:])
		}
		return n, nil
	case <-r.ctx.Done():
		r.closed = true
		return 0, io.EOF
	}
}

func (r *channelReader) Close() error {
	r.closed = true
	return nil
}

// SourceBuilder returns a SourceBuilderFunc that produces a single source
// with the given ID and labels.
func SingleSourceBuilder(sourceID string, labels map[string]string) logs.SourceBuilderFunc {
	return func(_ string, _ map[string]interface{}, _ logs.LogSessionOptions) []logs.LogSource {
		return []logs.LogSource{{ID: sourceID, Labels: labels}}
	}
}

// MultiSourceBuilder returns a SourceBuilderFunc that produces sources with
// the given IDs.
func MultiSourceBuilder(sourceIDs ...string) logs.SourceBuilderFunc {
	return func(_ string, _ map[string]interface{}, _ logs.LogSessionOptions) []logs.LogSource {
		sources := make([]logs.LogSource, len(sourceIDs))
		for i, id := range sourceIDs {
			sources[i] = logs.LogSource{ID: id, Labels: map[string]string{"source": id}}
		}
		return sources
	}
}

// Line creates a log line string with optional timestamp prefix.
func Line(content string, opts ...LineOption) string {
	cfg := lineConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.timestamp.IsZero() {
		return content
	}
	return cfg.timestamp.Format(time.RFC3339Nano) + " " + content
}

type lineConfig struct {
	timestamp time.Time
	level     logs.LogLevel
}

// LineOption configures a test log line.
type LineOption func(*lineConfig)

// WithTimestamp sets the timestamp for a test line.
func WithTimestamp(t time.Time) LineOption {
	return func(c *lineConfig) { c.timestamp = t }
}

// WithLevel is a documentation hint (level is set server-side, not in line content).
func WithLevel(_ logs.LogLevel) LineOption {
	return func(_ *lineConfig) {
		// Level is set on the LogLine struct by the handler, not embedded in content.
		// This option exists for API completeness in test DSL.
	}
}

// ContextWithCancel returns a PluginContext wrapping a cancellable context.
func ContextWithCancel(ctx context.Context) (*types.PluginContext, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	pctx := types.NewPluginContext(ctx, "log", nil, nil, nil)
	return pctx, cancel
}
