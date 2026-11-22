package logs_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/logs/logtest"
)

const (
	backlogWait    = 5 * time.Second
	backlogSession = "6f1f9a3e-31c8-4f0e-9d44-0a7f5d1c2b10"
)

// tailLog is a source shaped like a kubelet's container log: lines in a file, a
// bounded read of the last n, and a follow that opens on the current file with its
// last n lines and then streams what is appended to that file. Rotate starts a new
// current file: a stream that is open keeps the file it opened on, a request made
// later is served from the new one.
type tailLog struct {
	mu       sync.Mutex
	files    []*logFile
	epoch    int // streams opened under an older epoch fail with breakErr
	breakErr error
	ended    bool
	hold     chan struct{} // when set, a stream's first Read waits for it to close
	waits    int           // times a stream blocked for more lines
	counts   []logs.LogStreamRequest
	opens    []logs.LogStreamRequest
	countErr []error // errors Backlog returns, first call first

	afterCount func() // runs inside Backlog after it counted, before it returns
	beforeOpen func() // runs inside Handler before it opens
}

type logFile struct {
	lines []string
	wake  chan struct{}
}

func newTailLog(lines ...string) *tailLog {
	l := &tailLog{files: []*logFile{{wake: make(chan struct{})}}}
	l.Append(lines...)
	return l
}

func (l *tailLog) current() *logFile { return l.files[len(l.files)-1] }

// Append writes lines to the current file.
func (l *tailLog) Append(lines ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.current()
	f.lines = append(f.lines, lines...)
	close(f.wake)
	f.wake = make(chan struct{})
}

// Rotate starts a new, empty current file.
func (l *tailLog) Rotate() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files = append(l.files, &logFile{wake: make(chan struct{})})
}

// Break fails every stream that is open with err.
func (l *tailLog) Break(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.epoch++
	l.breakErr = err
	for _, f := range l.files {
		close(f.wake)
		f.wake = make(chan struct{})
	}
}

// End makes streams return io.EOF once they have read their file.
func (l *tailLog) End() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ended = true
	for _, f := range l.files {
		close(f.wake)
		f.wake = make(chan struct{})
	}
}

// Hold makes a stream's first Read wait until the returned function is called.
func (l *tailLog) Hold() (release func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	gate := make(chan struct{})
	l.hold = gate
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func (l *tailLog) Counts() []logs.LogStreamRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.counts)
}

func (l *tailLog) Opens() []logs.LogStreamRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.opens)
}

func (l *tailLog) Waits() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waits
}

// tailLocked is the last n lines of the current file, all of them when n is not positive.
func (l *tailLog) tailLocked(n int64) []string {
	lines := l.current().lines
	if n > 0 && int64(len(lines)) > n {
		lines = lines[int64(len(lines))-n:]
	}
	return lines
}

// count is what a plugin's Backlog does for this source: a bounded read of the same
// tail, counted.
func (l *tailLog) count(_ *types.PluginContext, req logs.LogStreamRequest) (int64, error) {
	l.mu.Lock()
	l.counts = append(l.counts, req)
	var err error
	if len(l.countErr) > 0 {
		err, l.countErr = l.countErr[0], l.countErr[1:]
	}
	n := int64(len(l.tailLocked(req.TailLines)))
	hook := l.afterCount
	l.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if hook != nil {
		hook()
	}
	return n, nil
}

// open is what a plugin's Handler does: one request, served as a stream.
func (l *tailLog) open(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
	l.mu.Lock()
	l.opens = append(l.opens, req)
	hook := l.beforeOpen
	l.mu.Unlock()
	if hook != nil {
		hook()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !req.Follow {
		return &heldBody{
			body: strings.NewReader(joinLines(l.tailLocked(req.TailLines))),
			hold: l.hold, ctx: pctx.Context, done: make(chan struct{}),
		}, nil
	}
	f := l.current()
	start := 0
	if n := req.TailLines; n > 0 && int64(len(f.lines)) > n {
		start = len(f.lines) - int(n)
	}
	return &tailStream{
		log: l, file: f, pos: start, epoch: l.epoch, hold: l.hold,
		ctx: pctx.Context, done: make(chan struct{}),
	}, nil
}

// handlers registers the source as the handler of resource "pod"; withBacklog says
// whether the plugin takes part in replay markers.
func (l *tailLog) handlers(withBacklog bool) map[string]logs.Handler {
	h := logs.Handler{
		Plugin: "test", Resource: "pod", Handler: l.open,
		SourceBuilder: logtest.SingleSourceBuilder("pod", map[string]string{"source": "pod"}),
	}
	if withBacklog {
		h.Backlog = l.count
	}
	return map[string]logs.Handler{"test/pod": h}
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

type tailStream struct {
	log       *tailLog
	file      *logFile
	pos       int
	epoch     int
	buf       []byte
	hold      chan struct{}
	held      bool
	ctx       context.Context
	done      chan struct{}
	closeOnce sync.Once
}

func (s *tailStream) Read(p []byte) (int, error) {
	if !s.held && s.hold != nil {
		select {
		case <-s.hold:
			s.held = true
		case <-s.done:
			return 0, io.EOF
		case <-s.ctx.Done():
			return 0, io.EOF
		}
	}
	for {
		if len(s.buf) > 0 {
			n := copy(p, s.buf)
			s.buf = s.buf[n:]
			return n, nil
		}
		l := s.log
		l.mu.Lock()
		switch {
		case l.epoch != s.epoch:
			err := l.breakErr
			l.mu.Unlock()
			return 0, err
		case s.pos < len(s.file.lines):
			s.buf = []byte(s.file.lines[s.pos] + "\n")
			s.pos++
			l.mu.Unlock()
			continue
		case l.ended:
			l.mu.Unlock()
			return 0, io.EOF
		}
		wake := s.file.wake
		l.waits++
		l.mu.Unlock()
		select {
		case <-wake:
		case <-s.done:
			return 0, io.EOF
		case <-s.ctx.Done():
			return 0, io.EOF
		}
	}
}

func (s *tailStream) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

// timeline is a sink that keeps lines and events in the one order they arrived in.
type timeline struct {
	mu      sync.Mutex
	items   []timelineItem
	changed chan struct{}
}

type timelineItem struct {
	line  *logs.LogLine
	event *logs.LogStreamEvent
}

func newTimeline() *timeline { return &timeline{changed: make(chan struct{})} }

func (t *timeline) add(it timelineItem) {
	t.mu.Lock()
	t.items = append(t.items, it)
	close(t.changed)
	t.changed = make(chan struct{})
	t.mu.Unlock()
}

func (t *timeline) OnLine(l logs.LogLine)                   { t.add(timelineItem{line: &l}) }
func (t *timeline) OnEvent(_ string, e logs.LogStreamEvent) { t.add(timelineItem{event: &e}) }

func (t *timeline) snapshot() []timelineItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.items)
}

func (t *timeline) lines() []logs.LogLine {
	var out []logs.LogLine
	for _, it := range t.snapshot() {
		if it.line != nil {
			out = append(out, *it.line)
		}
	}
	return out
}

func (t *timeline) events() []logs.LogStreamEvent {
	var out []logs.LogStreamEvent
	for _, it := range t.snapshot() {
		if it.event != nil {
			out = append(out, *it.event)
		}
	}
	return out
}

// until waits for cond to hold of the timeline, failing the test when it does not.
func (t *timeline) until(tb testing.TB, what string, cond func(items []timelineItem) bool) []timelineItem {
	tb.Helper()
	deadline := time.After(backlogWait)
	for {
		t.mu.Lock()
		items, changed := slices.Clone(t.items), t.changed
		t.mu.Unlock()
		if cond(items) {
			return items
		}
		select {
		case <-changed:
		case <-deadline:
			tb.Fatalf("timed out waiting for %s; timeline: %s", what, describe(items))
			return nil
		}
	}
}

func describe(items []timelineItem) string {
	parts := make([]string, len(items))
	for i, it := range items {
		switch {
		case it.line != nil && it.line.Replay:
			parts[i] = "R:" + it.line.Content
		case it.line != nil:
			parts[i] = "L:" + it.line.Content
		default:
			parts[i] = "E:" + it.event.Type.String() + "/" + it.event.SourceID
		}
	}
	return strings.Join(parts, " ")
}

func contents(lines []logs.LogLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Content
	}
	return out
}

func countLines(items []timelineItem) int {
	n := 0
	for _, it := range items {
		if it.line != nil {
			n++
		}
	}
	return n
}

func countMarkers(items []timelineItem, source string) int {
	n := 0
	for _, it := range items {
		if it.event != nil && it.event.Type == logs.StreamEventSourceCaughtUp && it.event.SourceID == source {
			n++
		}
	}
	return n
}

// signature is the lines and markers of one source in the order they arrived: R:text
// for a backlog line, L:text for a live one, MARK for the caught-up event.
func signature(items []timelineItem, source string) string {
	var parts []string
	for _, it := range items {
		switch {
		case it.line != nil && it.line.SourceID == source && it.line.Replay:
			parts = append(parts, "R:"+it.line.Content)
		case it.line != nil && it.line.SourceID == source:
			parts = append(parts, "L:"+it.line.Content)
		case it.event != nil && it.event.Type == logs.StreamEventSourceCaughtUp && it.event.SourceID == source:
			parts = append(parts, "MARK")
		}
	}
	return strings.Join(parts, " ")
}

// rig is a real manager over the sources a test registers.
type rig struct {
	t   *testing.T
	tl  *timeline
	mgr *logs.Manager
	ctx *types.PluginContext
}

func newRig(t *testing.T, handlers map[string]logs.Handler, tune ...func(*logs.ManagerConfig)) *rig {
	t.Helper()
	tl := newTimeline()
	cfg := logs.ManagerConfig{Handlers: handlers, Sink: tl, Clock: timeutil.NewInstantFakeClock()}
	for _, f := range tune {
		f(&cfg)
	}
	mgr := logs.NewManager(cfg)
	t.Cleanup(mgr.Close)
	return &rig{t: t, tl: tl, mgr: mgr, ctx: types.NewPluginContext(context.Background(), "test", nil, nil, nil)}
}

// asking makes first-open options that ask for replay markers.
func asking(o logs.LogSessionOptions) logs.LogSessionOptions {
	params := map[string]string{logs.ParamReplayMarkers: logs.ReplayMarkersOn}
	for k, v := range o.Params {
		params[k] = v
	}
	o.Params = params
	return o
}

func (r *rig) open(o logs.LogSessionOptions) *logs.LogSession { return r.openKey("pod", o) }

func (r *rig) openKey(key string, o logs.LogSessionOptions) *logs.LogSession {
	r.t.Helper()
	session, err := r.mgr.CreateResourceSession(r.ctx, logs.CreateSessionOptions{
		SessionID: backlogSession, ResourceKey: key, ResourceID: "web-1", Options: o,
	})
	if err != nil {
		r.t.Fatalf("CreateResourceSession: %v", err)
	}
	return session
}

// marker waits for the n-th marker of a source.
func (r *rig) marker(source string, n int) {
	r.t.Helper()
	r.tl.until(r.t, "the marker", func(items []timelineItem) bool { return countMarkers(items, source) >= n })
}

func (r *rig) linesUntil(n int) []logs.LogLine {
	r.t.Helper()
	r.tl.until(r.t, "lines", func(items []timelineItem) bool { return countLines(items) >= n })
	return r.tl.lines()
}

var errBoom = errors.New("connection reset")

// heldBody serves a finite response, but only once the log's hold is released, so a
// test can close a finite session while its stream is still unread.
type heldBody struct {
	body      io.Reader
	hold      chan struct{}
	held      bool
	ctx       context.Context
	done      chan struct{}
	closeOnce sync.Once
}

func (b *heldBody) Read(p []byte) (int, error) {
	if !b.held && b.hold != nil {
		select {
		case <-b.hold:
			b.held = true
		case <-b.done:
			return 0, io.EOF
		case <-b.ctx.Done():
			return 0, io.EOF
		}
	}
	return b.body.Read(p)
}

func (b *heldBody) Close() error {
	b.closeOnce.Do(func() { close(b.done) })
	return nil
}
