package logtest

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
)

const replayLineStep = time.Millisecond

type replayEntry struct {
	at   time.Time
	line string
}

// ReplaySource is a programmable log source with a history, for exercising the
// replay contract of [logs.Handler]. Like a real source it keeps timestamped
// lines, serves a bounded read of them (its Replay function) and a follow that
// starts at or after a timestamp (its Handler function, which may return lines
// the SDK already read: the SDK drops them), and it keeps producing while no
// reader is attached.
type ReplaySource struct {
	mu       sync.Mutex
	entries  []replayEntry
	next     time.Time
	changed  chan struct{} // closed and replaced on every change
	requests []logs.LogStreamRequest

	ended         bool
	sameStamp     bool // appended lines reuse the previous line's timestamp
	waits         int  // times a follow stream blocked for more lines
	epoch         int  // follow readers opened under an older epoch fail with breakErr
	breakErr      error
	hang          bool
	breakAfter    int // when positive, the next replay fails after this many lines
	breakAfterErr error
}

// NewReplaySource creates a source whose history already holds the given lines.
func NewReplaySource(backlog ...string) *ReplaySource {
	s := &ReplaySource{
		next:    time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		changed: make(chan struct{}),
	}
	s.Append(backlog...)
	return s
}

// Append adds lines to the history, each stamped one millisecond after the last,
// and returns the timestamp of the final one.
func (s *ReplaySource) Append(bodies ...string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendLocked(bodies)
	s.wakeLocked()
	return s.next
}

// SameStamp sets whether lines appended from now on reuse the timestamp of the
// line before them, as a source with a coarse clock writes many lines to one
// timestamp. The first line of an empty history takes the first timestamp.
func (s *ReplaySource) SameStamp(same bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sameStamp = same
}

// Waits counts how many times a follow stream has come back to wait for a new
// line. A stream does so only after the reader it feeds has consumed what it was
// given, so a rise after an Append says the line was read and dealt with.
func (s *ReplaySource) Waits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waits
}

// AppendAndBreak adds lines and, in the same instant, fails every follow stream
// that is open: the lines arrive just as the connection drops, so only a resumed
// read can deliver them.
func (s *ReplaySource) AppendAndBreak(err error, bodies ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendLocked(bodies)
	s.epoch++
	s.breakErr = err
	s.wakeLocked()
}

// BreakFollow fails every follow stream that is open with err.
func (s *ReplaySource) BreakFollow(err error) { s.AppendAndBreak(err) }

// End finishes the source: follow streams return io.EOF once they have read the
// history, as a terminated container's do.
func (s *ReplaySource) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	s.wakeLocked()
}

// HangReplay makes replay streams block until closed, as a handler that never
// reaches the end of its backlog does.
func (s *ReplaySource) HangReplay(hang bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hang = hang
}

// BreakReplayAfter makes the next replay stream fail with err after n lines.
func (s *ReplaySource) BreakReplayAfter(n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.breakAfter, s.breakAfterErr = n, err
}

// Lines returns the history as the source writes it: one timestamped line each.
func (s *ReplaySource) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := make([]string, len(s.entries))
	for i, e := range s.entries {
		lines[i] = e.line
	}
	return lines
}

// Requests returns the requests the source has served, in order.
func (s *ReplaySource) Requests() []logs.LogStreamRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Handler returns a log handler for one source, wired with this source's replay
// and follow functions.
func (s *ReplaySource) Handler(sourceID string) logs.Handler {
	return logs.Handler{
		Plugin:        "test",
		Resource:      sourceID,
		Handler:       s.FollowFunc(),
		Replay:        s.ReplayFunc(),
		SourceBuilder: SingleSourceBuilder(sourceID, map[string]string{"source": sourceID}),
	}
}

// ReplayFunc returns the source's [logs.Handler.Replay] function. It rejects a
// request with Follow set, which the contract forbids.
func (s *ReplaySource) ReplayFunc() logs.LogHandlerFunc {
	return func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests = append(s.requests, req)
		if req.Follow {
			return nil, errors.New("logtest: replay was asked to follow")
		}
		if s.hang {
			return &hangReader{ctx: pctx.Context, done: make(chan struct{})}, nil
		}
		lines := s.selectLocked(req)
		if n, err := s.breakAfter, s.breakAfterErr; n > 0 {
			s.breakAfter, s.breakAfterErr = 0, nil
			return &failingReader{
				data: strings.NewReader(joinLines(lines[:min(n, len(lines))])),
				err:  err,
			}, nil
		}
		return io.NopCloser(strings.NewReader(joinLines(lines))), nil
	}
}

// FollowFunc returns the source's [logs.Handler.Handler] function: a stream of
// the lines at or after the request's SinceTime (all of them when a resumed
// request has none, nothing old when the request is not a resume) that stays open
// until the source ends, breaks or the reader is closed.
func (s *ReplaySource) FollowFunc() logs.LogHandlerFunc {
	return func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests = append(s.requests, req)
		if !req.Follow {
			return nil, errors.New("logtest: follow was asked for a bounded read")
		}
		cursor := len(s.entries)
		switch {
		case req.Resume && req.SinceTime != nil:
			cursor = s.firstAtOrAfterLocked(*req.SinceTime)
		case req.Resume:
			// Nothing was read: the whole window, which a source with no window
			// bounds of its own serves from the start.
			cursor = 0
		}
		return &followReader{src: s, ctx: pctx.Context, cursor: cursor, epoch: s.epoch, done: make(chan struct{})}, nil
	}
}

func (s *ReplaySource) appendLocked(bodies []string) {
	for _, body := range bodies {
		if !s.sameStamp || len(s.entries) == 0 {
			s.next = s.next.Add(replayLineStep)
		}
		s.entries = append(s.entries, replayEntry{at: s.next, line: s.next.Format(time.RFC3339Nano) + " " + body})
	}
}

func (s *ReplaySource) wakeLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// firstAtOrAfterLocked is the index of the first line stamped at or after t.
func (s *ReplaySource) firstAtOrAfterLocked(t time.Time) int {
	i, _ := slices.BinarySearchFunc(s.entries, t, func(e replayEntry, t time.Time) int {
		return e.at.Compare(t)
	})
	return i
}

// selectLocked applies a replay request's bounds to the history.
func (s *ReplaySource) selectLocked(req logs.LogStreamRequest) []string {
	from := 0
	if req.SinceTime != nil {
		from = s.firstAtOrAfterLocked(*req.SinceTime)
	}
	picked := s.entries[from:]
	if req.TailLines > 0 && int64(len(picked)) > req.TailLines {
		picked = picked[int64(len(picked))-req.TailLines:]
	}
	lines := make([]string, len(picked))
	for i, e := range picked {
		lines[i] = e.line
	}
	return lines
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// hangReader blocks every Read until it is closed or its context ends.
type hangReader struct {
	ctx       context.Context
	done      chan struct{}
	closeOnce sync.Once
}

func (r *hangReader) Read([]byte) (int, error) {
	select {
	case <-r.done:
	case <-r.ctx.Done():
	}
	return 0, io.EOF
}

func (r *hangReader) Close() error {
	r.closeOnce.Do(func() { close(r.done) })
	return nil
}

// failingReader returns its data, then err.
type failingReader struct {
	data io.Reader
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	n, err := r.data.Read(p)
	if errors.Is(err, io.EOF) {
		return n, r.err
	}
	return n, err
}

func (r *failingReader) Close() error { return nil }

// followReader streams history entries from a cursor and then new ones.
type followReader struct {
	src       *ReplaySource
	ctx       context.Context
	cursor    int
	epoch     int
	buf       []byte
	done      chan struct{}
	closeOnce sync.Once
}

func (r *followReader) Read(p []byte) (int, error) {
	for {
		if len(r.buf) > 0 {
			n := copy(p, r.buf)
			r.buf = r.buf[n:]
			return n, nil
		}
		r.src.mu.Lock()
		switch {
		case r.src.epoch != r.epoch:
			err := r.src.breakErr
			r.src.mu.Unlock()
			return 0, err
		case r.cursor < len(r.src.entries):
			r.buf = []byte(r.src.entries[r.cursor].line + "\n")
			r.cursor++
			r.src.mu.Unlock()
			continue
		case r.src.ended:
			r.src.mu.Unlock()
			return 0, io.EOF
		}
		changed := r.src.changed
		r.src.waits++
		r.src.mu.Unlock()
		select {
		case <-changed:
		case <-r.done:
			return 0, io.EOF
		case <-r.ctx.Done():
			return 0, io.EOF
		}
	}
}

func (r *followReader) Close() error {
	r.closeOnce.Do(func() { close(r.done) })
	return nil
}
