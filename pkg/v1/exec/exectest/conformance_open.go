package exectest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The wire cases of the conformance suite: a host drives an interactive session over a real transport, as it does a plugin. Each
// case serves a second plugin next to the author's registration check: its one target is a fake that carries the description the
// author declared (the Info, never the Exec), and what it runs is a script of the case. What the author declared decides what the
// case expects: a terminal only when the target declares one, a signal only when it reports signals, the survival of a Closed end
// from the Kill it declares, a lossy flood only for a target that stalls lossily, the limits it declares.
//
// One fake serves every script of a case: the function it runs is the one the request's label names, so a case that needs two
// sessions that behave differently (a flood and an echo) serves them from one target, with the limits of one target kind.

const (
	// scriptLabel is the label of a request that names the script the fake runs for it.
	scriptLabel = "exectest-script"
	// maxOutputFrame is the largest Output frame the contract allows.
	maxOutputFrame = 64 << 10
	// fixedWindow is the flow-control window of the connection that a case dials when its host is to stop reading. A window that
	// is not below the default turns off gRPC's dynamic window, so what a host that does not read can leave in transit is this
	// window and no more.
	fixedWindow = 65535
	// noSessions is why a case skips for a target that serves no sessions.
	noSessions = "the target declares no sessions: it serves no Open"
)

// wireEnv is what a wire case is given besides the author's target.
type wireEnv struct {
	// resource is the id of the thing that the requests address (ConformanceResource).
	resource string
	// serve serves the targets of a case as a plugin over a transport; nil serves the real plugin. The tests of the suite give a
	// server that is wrong, to see each case fail.
	serve serveFunc
}

// serveFunc serves targets as a plugin behind a connection registry, and returns the host's side of it.
type serveFunc func(t checkT, targets []exec.Target, conns *exectestkit.Connections) *wireServer

// wireServer is the host's side of a plugin served over a transport.
type wireServer struct {
	client exec.Client
	raw    execv2.ExecClient
	// target is where a second connection to the same plugin dials.
	target string
	// plugin is the served plugin; its Shutdown stops the engine of the plugin, as the SDK does when the server stops.
	plugin *exec.Plugin
}

// serveReal serves the targets over a unix socket behind the SDK's own middleware.
func serveReal(t checkT, targets []exec.Target, conns *exectestkit.Connections) *wireServer {
	t.Helper()
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: targets, Connections: conns})
	return &wireServer{client: server.V2(t), raw: server.Raw(), target: server.Conn().Target(), plugin: server.Plugin}
}

// dial opens another connection to the plugin, with the options a host would not set by itself, and returns the clients on it.
func (w *wireServer) dial(t checkT, extra ...grpc.DialOption) (exec.Client, execv2.ExecClient) {
	t.Helper()
	opts := append(sdk.GRPCDialOptionsWithLogger(logging.NewNop()), grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient(w.target, append(opts, extra...)...)
	if err != nil {
		t.Fatalf("dial the plugin again: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := (&exec.Plugin{}).GRPCClient(context.Background(), nil, conn)
	if err != nil {
		t.Fatalf("build the exec client: %v", err)
	}
	return client.(*exec.PluginClient).V2(), execv2.NewExecClient(conn)
}

// wireRig is one plugin served over a transport whose target carries the author's declared description.
type wireRig struct {
	t        checkT
	info     exec.TargetInfo // what the fake serves: a copy of the declaration
	resource string
	conns    *exectestkit.Connections
	server   *wireServer
	client   exec.Client
	raw      execv2.ExecClient

	slowClient exec.Client // dialled on first use: a connection with a fixed window
	slowRaw    execv2.ExecClient

	mu      sync.Mutex
	scripts map[string]exec.ExecFunc
	next    int

	started  atomic.Int64 // functions that were called
	returned atomic.Int64 // functions that returned
}

// newWireRig serves a fake of the declared target.
func newWireRig(t checkT, env *wireEnv, target exec.Target) *wireRig {
	t.Helper()
	info := target.Info
	info.Capabilities = cloneCapabilities(info.Capabilities)
	info.DefaultArgv = slices.Clone(info.DefaultArgv)
	r := &wireRig{t: t, info: info, resource: env.resource, scripts: map[string]exec.ExecFunc{}}
	fake := NewFakeTarget(FakeInfo(info))
	fake.Exec = r.exec
	r.conns = exectestkit.NewConnections(info.Provider)
	serve := env.serve
	if serve == nil {
		serve = serveReal
	}
	r.server = serve(t, []exec.Target{fake}, r.conns)
	r.client, r.raw = r.server.client, r.server.raw
	t.Cleanup(r.teardown)
	return r
}

// teardown ends the sessions the plugin holds and retires the connection, so that no function outlives the case.
func (r *wireRig) teardown() {
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	_ = r.server.plugin.Shutdown(ctx)
	r.conns.Retire()
}

// exec is the function of the fake: the script the request names.
func (r *wireRig) exec(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	r.started.Add(1)
	defer r.returned.Add(1)
	name := p.Request.Labels[scriptLabel]
	r.mu.Lock()
	fn := r.scripts[name]
	r.mu.Unlock()
	if fn == nil {
		return exec.Exit{}, fmt.Errorf("exectest: no script %q", name)
	}
	return fn(ctx, p)
}

// script registers the steps of a script under a name.
func (r *wireRig) script(name string, steps ...Step) {
	r.scriptFunc(name, NewFakeTarget(FakeScript(steps...)).Exec)
}

// scriptFunc registers a function under a name.
func (r *wireRig) scriptFunc(name string, fn exec.ExecFunc) {
	r.mu.Lock()
	r.scripts[name] = fn
	r.mu.Unlock()
}

// newID returns an id for a session of the case.
func (r *wireRig) newID(prefix string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	return fmt.Sprintf("%s-%d", prefix, r.next)
}

// slow returns the client of a connection whose flow-control window is fixed and small: what a host that stops reading
// leaves in transit on it is bounded by the window.
func (r *wireRig) slow() exec.Client {
	if r.slowClient == nil {
		r.slowClient, r.slowRaw = r.server.dial(r.t, grpc.WithInitialWindowSize(fixedWindow), grpc.WithInitialConnWindowSize(fixedWindow))
	}
	return r.slowClient
}

// tty says that the declared target gives a command a terminal: a case asks for one only then.
func (r *wireRig) tty() bool { return r.info.Capabilities.TTY }

// request is the context a host's call arrives with: the connection and the catalog pin.
func (r *wireRig) request() context.Context { return r.conns.Context(context.Background()) }

// openRequest is the first frame of an Open for the declared target: a terminal only when the target declares one, the program
// of the target's default unless it has none, and the label that names the script.
func (r *wireRig) openRequest(id, script string, mods ...func(*execv2.Start)) *execv2.OpenRequest {
	r.t.Helper()
	target, err := wireTarget(r.conns, r.info.Type, r.resource)
	if err != nil {
		r.t.Fatalf("%v", err)
	}
	start := &execv2.Start{Target: target, Tty: r.tty()}
	if len(r.info.DefaultArgv) == 0 {
		start.Argv = []string{"exectest"}
	}
	if script != "" {
		start.Labels = []*execv2.NameValue{{Name: scriptLabel, Value: script}}
	}
	for _, mod := range mods {
		mod(start)
	}
	return &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}}
}

// open starts a session over the host's connection.
func (r *wireRig) open(id, script string, mods ...func(*execv2.Start)) (*wireSession, error) {
	r.t.Helper()
	return r.openOn(r.client, id, script, mods...)
}

// openOn starts a session over the connection of a client; its stream is closed when the case ends.
func (r *wireRig) openOn(client exec.Client, id, script string, mods ...func(*execv2.Start)) (*wireSession, error) {
	r.t.Helper()
	req := r.openRequest(id, script, mods...)
	conn, err := client.Open(r.request(), req)
	if err != nil {
		return nil, err
	}
	r.t.Cleanup(func() { _ = conn.Close() })
	return &wireSession{r: r, id: id, conn: conn, req: req}, nil
}

// mustOpen starts a session that has to be admitted.
func (r *wireRig) mustOpen(id, script string, mods ...func(*execv2.Start)) *wireSession {
	r.t.Helper()
	s, err := r.open(id, script, mods...)
	if err != nil {
		r.t.Fatalf("open %q for the script %q: %v", id, script, err)
	}
	return s
}

// mustOpenOn is mustOpen over the connection of a client.
func (r *wireRig) mustOpenOn(client exec.Client, id, script string, mods ...func(*execv2.Start)) *wireSession {
	r.t.Helper()
	s, err := r.openOn(client, id, script, mods...)
	if err != nil {
		r.t.Fatalf("open %q for the script %q: %v", id, script, err)
	}
	return s
}

// described returns what Describe says of the declared target.
func (r *wireRig) described() *execv2.TargetInfo {
	r.t.Helper()
	description, err := r.client.Describe(context.Background())
	if err != nil {
		r.t.Fatalf("describe: %v", err)
	}
	for _, candidate := range description.GetTargets() {
		if candidate.GetProvider() == r.info.Provider && candidate.GetType() == r.info.Type {
			return candidate
		}
	}
	r.t.Fatalf("Describe lists no target %s/%s", r.info.Provider, r.info.Type)
	return nil
}

// await polls cond until it holds, within the bound of a case. The condition is the oracle; the wait only keeps a bug from
// hanging the case.
func (r *wireRig) await(what string, cond func() bool) {
	r.t.Helper()
	if !poll(caseBound, cond) {
		r.t.Fatalf("timed out after %v waiting for %s", caseBound, what)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// a session as the host sees it
// ---------------------------------------------------------------------------------------------------------------

// wireSession is one Open stream, with the checks that every stream is held to made as its frames arrive.
type wireSession struct {
	r       *wireRig
	id      string
	conn    exec.Conn
	req     *execv2.OpenRequest
	grammar streamGrammar
	out     *wireOutput // fed by every frame when it is set
	ended   *execv2.Ended
	started *execv2.Started // the first Started the stream carried
}

// tty says that the session was asked for with a terminal.
func (s *wireSession) tty() bool { return s.req.GetStart().GetTty() }

// audit makes the output audit of the session, for a pattern.
func (s *wireSession) audit(byteAt func(uint64) byte) *wireOutput {
	s.out = &wireOutput{tty: s.tty(), byteAt: byteAt}
	return s.out
}

// recv reads the next frame of the stream, within the bound of a case: a stream that does not answer fails the case and does not
// hang it. The frame is held to the order of the stream and fed to the audit.
func (s *wireSession) recv() (*execv2.ServerFrame, error) {
	t := s.r.t
	t.Helper()
	var timedOut atomic.Bool
	timer := time.AfterFunc(caseBound, func() { timedOut.Store(true); _ = s.conn.Close() })
	frame, err := s.conn.Recv()
	timer.Stop()
	if timedOut.Load() {
		t.Fatalf("session %q: no frame arrived within %v", s.id, caseBound)
	}
	if err != nil {
		return nil, err
	}
	s.grammar.take(frame)
	if st := frame.GetStarted(); st != nil && s.started == nil {
		s.started = st
	}
	if s.out != nil {
		s.out.take(frame)
	}
	return frame, nil
}

// next reads the next frame, which has to exist.
func (s *wireSession) next(what string) *execv2.ServerFrame {
	t := s.r.t
	t.Helper()
	frame, err := s.recv()
	if err != nil {
		t.Fatalf("session %q: waiting for %s: %v", s.id, what, err)
	}
	return frame
}

// send sends a frame, within the bound of a case: a host that cannot send because the plugin does not take its frames waits, and a
// plugin that never does fails the case.
func (s *wireSession) send(frame *execv2.ClientFrame) error {
	t := s.r.t
	t.Helper()
	var timedOut atomic.Bool
	timer := time.AfterFunc(caseBound, func() { timedOut.Store(true); _ = s.conn.Close() })
	err := s.conn.Send(frame)
	timer.Stop()
	if timedOut.Load() {
		t.Fatalf("session %q: the plugin took no frame within %v", s.id, caseBound)
	}
	return err
}

// mustSend sends a frame that the plugin has to take.
func (s *wireSession) mustSend(frame *execv2.ClientFrame) {
	t := s.r.t
	t.Helper()
	if err := s.send(frame); err != nil {
		t.Fatalf("session %q: send: %v", s.id, err)
	}
}

// mustInput sends bytes as one input frame that the plugin has to take.
func (s *wireSession) mustInput(data []byte) {
	s.r.t.Helper()
	s.mustSend(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: data}})
}

// resize sends a new size.
func (s *wireSession) resize(seq, cols, rows uint32) error {
	return s.send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Resize{Resize: &execv2.Resize{Seq: seq, Size: &execv2.Size{Cols: cols, Rows: rows}}}})
}

// closeSession asks for the end of the session. A stream that is over refuses the frame, which is no failure of the host.
func (s *wireSession) closeSession(mode execv2.CloseMode) error {
	return s.send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: mode}}})
}

// finish reads the stream to its end: the Ended must be the last frame, and the stream then ends with status OK (Recv returns
// io.EOF) and nothing follows. It returns the Ended.
func (s *wireSession) finish() *execv2.Ended {
	t := s.r.t
	t.Helper()
	for {
		frame, err := s.recv()
		if errors.Is(err, io.EOF) {
			t.Fatalf("session %q: the stream ended with status OK and no Ended after %d frames", s.id, s.grammar.frames)
		}
		if err != nil {
			t.Fatalf("session %q: the stream broke after %d frames: %v", s.id, s.grammar.frames, err)
		}
		ended := frame.GetEnded()
		if ended == nil {
			continue
		}
		if after, err := s.recv(); !errors.Is(err, io.EOF) {
			t.Fatalf("session %q: after Ended came %s and %v, want io.EOF: nothing follows Ended", s.id, frameName(after), err)
		}
		s.ended = ended
		for _, problem := range s.grammar.problems {
			t.Errorf("session %q: %s", s.id, problem)
		}
		if s.out != nil {
			for _, problem := range s.out.problems {
				t.Errorf("session %q: %s", s.id, problem)
			}
		}
		return ended
	}
}

// readBytes reads output until n more bytes have arrived, and returns them. Frames of other kinds are taken as they come.
func (s *wireSession) readBytes(n int) []byte {
	t := s.r.t
	t.Helper()
	var got []byte
	for len(got) < n {
		frame := s.next(fmt.Sprintf("%d more bytes of output (%d read)", n-len(got), len(got)))
		if ended := frame.GetEnded(); ended != nil {
			t.Fatalf("session %q: the session ended with %d of %d bytes read", s.id, len(got), n)
		}
		got = append(got, frame.GetOutput().GetData()...)
	}
	return got
}

// roundTrip sends bytes and reads them back from an echo: they must come back as they were sent.
func (s *wireSession) roundTrip(data []byte) []byte {
	t := s.r.t
	t.Helper()
	s.mustInput(data)
	got := s.readBytes(len(data))
	if !bytes.Equal(got, data) {
		t.Errorf("session %q: an echo of %q came back as %q", s.id, data, got)
	}
	return got
}

// frameName names the kind of a frame, for a message.
func frameName(f *execv2.ServerFrame) string {
	switch f.GetFrame().(type) {
	case *execv2.ServerFrame_Opened:
		return "an Opened frame"
	case *execv2.ServerFrame_Output:
		return "an Output frame"
	case *execv2.ServerFrame_Resized:
		return "a Resized frame"
	case *execv2.ServerFrame_Mark:
		return "a Mark frame"
	case *execv2.ServerFrame_Fact:
		return "a Fact frame"
	case *execv2.ServerFrame_Dropped:
		return "a Dropped frame"
	case *execv2.ServerFrame_Notice:
		return "a Notice frame"
	case *execv2.ServerFrame_Ended:
		return "an Ended frame"
	case *execv2.ServerFrame_Started:
		return "a Started frame"
	case *execv2.ServerFrame_Link:
		return "a Link frame"
	case *execv2.ServerFrame_Detached:
		return "a Detached frame"
	case nil:
		return "no frame"
	}
	return "a frame of a kind this reader does not know"
}

// ---------------------------------------------------------------------------------------------------------------
// what every stream is held to
// ---------------------------------------------------------------------------------------------------------------

// streamGrammar holds the frames of one stream to the order the contract states: Opened first (the client takes it before it returns
// the stream), then Notice, Started (at most one, before the first output), Output, Dropped and Link frames, and Ended last, once, with
// nothing after it. This build keeps no session past its stream, so a Detached has no place on one.
type streamGrammar struct {
	frames   int
	output   bool // an Output or a Dropped frame has come
	started  int
	ended    bool
	problems []string
}

func (g *streamGrammar) problem(format string, args ...any) {
	if len(g.problems) < 20 {
		g.problems = append(g.problems, fmt.Sprintf(format, args...))
	}
}

// take checks one frame of the stream.
func (g *streamGrammar) take(f *execv2.ServerFrame) {
	g.frames++
	if g.ended {
		g.problem("%s came after Ended: nothing follows Ended", frameName(f))
	}
	switch f.GetFrame().(type) {
	case *execv2.ServerFrame_Opened:
		g.problem("a second Opened: Opened is the first frame and is not repeated")
	case *execv2.ServerFrame_Started:
		if g.started++; g.started > 1 {
			g.problem("a second Started: a session has at most one")
		}
		if g.output {
			g.problem("a Started after the first output: Started always comes before it")
		}
	case *execv2.ServerFrame_Output, *execv2.ServerFrame_Dropped:
		g.output = true
	case *execv2.ServerFrame_Notice:
		if g.output {
			g.problem("a Notice after the first output: a Notice comes before it")
		}
	case *execv2.ServerFrame_Ended:
		g.ended = true
	case *execv2.ServerFrame_Detached:
		g.problem("a Detached frame: this build keeps no session past its stream, and the host sent no Detach")
	}
}

// wireSegment is a run of an output sequence that one stream wrote.
type wireSegment struct {
	start, end uint64
	stream     execv2.Stream
}

// wireOutput follows the output of one session as its host reads it, to find what a reader that does not trust the plugin would find:
// a hole that was not reported as dropped, a byte delivered twice, a frame over 64 KiB, a stream the command cannot have, a byte that
// is not what the command wrote at its offset.
type wireOutput struct {
	tty    bool
	byteAt func(offset uint64) byte // what the command wrote at an offset; nil: the bytes are not compared
	layout []wireSegment            // which stream wrote each run; nil: any stream the mode allows
	keep   bool                     // keep every byte read

	next, read, dropped uint64
	frames, drops       int
	largest             int
	reasons             map[execv2.DropReason]int
	data                []byte
	problems            []string
}

func (o *wireOutput) problem(format string, args ...any) {
	if len(o.problems) < 20 {
		o.problems = append(o.problems, fmt.Sprintf(format, args...))
	}
}

// take checks one frame; frames that are not output are not its business.
func (o *wireOutput) take(f *execv2.ServerFrame) {
	switch frame := f.GetFrame().(type) {
	case *execv2.ServerFrame_Output:
		out := frame.Output
		data, offset := out.GetData(), out.GetOffset()
		o.frames++
		o.largest = max(o.largest, len(data))
		switch {
		case len(data) == 0:
			o.problem("an Output frame at offset %d carries no bytes", offset)
		case len(data) > maxOutputFrame:
			o.problem("an Output frame of %d bytes, over the limit of %d", len(data), maxOutputFrame)
		}
		if offset != o.next {
			o.problem("an Output frame at offset %d, the reader is at %d: a hole that was not reported as dropped, or bytes delivered twice", offset, o.next)
		}
		if !streamFits(o.tty, out.GetStream()) {
			o.problem("an Output frame of the stream %s from a command %s", out.GetStream(), streamRule(o.tty))
		}
		o.checkLayout(offset, uint64(len(data)), out.GetStream())
		if o.byteAt != nil {
			for i, b := range data {
				if want := o.byteAt(offset + uint64(i)); b != want {
					o.problem("the byte at offset %d is %#02x, not what was written there (%#02x)", offset+uint64(i), b, want)
					break
				}
			}
		}
		if o.keep {
			o.data = append(o.data, data...)
		}
		o.next = offset + uint64(len(data))
		o.read += uint64(len(data))
	case *execv2.ServerFrame_Dropped:
		d := frame.Dropped
		o.drops++
		if d.GetFromOffset() != o.next {
			o.problem("a Dropped range starts at %d, the reader is at %d: bytes were lost without being reported, or reported twice", d.GetFromOffset(), o.next)
		}
		if d.GetBytes() == 0 {
			o.problem("an empty Dropped range at offset %d", d.GetFromOffset())
		}
		if !streamFits(o.tty, d.GetStream()) {
			o.problem("a Dropped range of the stream %s from a command %s", d.GetStream(), streamRule(o.tty))
		}
		if o.reasons == nil {
			o.reasons = map[execv2.DropReason]int{}
		}
		o.reasons[d.GetReason()]++
		o.next = d.GetFromOffset() + d.GetBytes()
		o.dropped += d.GetBytes()
	}
}

// checkLayout holds a frame to the streams that wrote the bytes it carries: a frame has one stream, and every byte of it was written to it.
func (o *wireOutput) checkLayout(offset, n uint64, stream execv2.Stream) {
	if o.layout == nil {
		return
	}
	for _, seg := range o.layout {
		if seg.end <= offset || seg.start >= offset+n {
			continue
		}
		if seg.stream != stream {
			o.problem("an Output frame of the stream %s at offset %d holds bytes that the command wrote to %s", stream, offset, seg.stream)
			return
		}
	}
}

// completenessFor is what Ended.output must say of a session that ended with end after dropping some bytes: TRUNCATED when any were
// dropped, COMPLETE when nothing was and the end is a status of the process, and UNSPECIFIED otherwise (the SDK cannot prove it).
func completenessFor(end *execv2.End, dropped uint64) execv2.OutputCompleteness {
	switch {
	case dropped > 0:
		return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED
	case end.GetSignaled() != nil && end.GetSignaled().GetSource() != execv2.ExitSource_EXIT_SOURCE_TRANSPORT:
		return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
	case end.GetExited() != nil && end.GetExited().GetSource() != execv2.ExitSource_EXIT_SOURCE_TRANSPORT:
		return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
	}
	return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
}

// wantEnded is what the Ended of a session must say.
type wantEnded struct {
	end      *execv2.End
	started  bool   // start_observed
	bytesIn  uint64 // what the host sent
	bytesOut uint64 // what the command wrote, when the output was not audited
}

// endedProblems holds an Ended to the contract: the end is the one expected, the accounts are exact (bytes_out is what the command
// wrote, dropped bytes included; last_offset is bytes_out; dropped_bytes is what the reader was told was lost), the completeness is
// the one the end allows, and a session without a terminal carries its empty mode reset.
func endedProblems(e *execv2.Ended, tty bool, want wantEnded, audit *wireOutput) []string {
	var problems []string
	if _, ok := endArm(e.GetEnd()); !ok {
		problems = append(problems, "the end has no arm: exactly one of Exited, Signaled, Closed, Failed and Lost must be set")
	} else if !proto.Equal(e.GetEnd(), want.end) {
		problems = append(problems, fmt.Sprintf("the end is %s, want %s", endText(e.GetEnd()), endText(want.end)))
	}
	if e.GetStartObserved() != want.started {
		problems = append(problems, fmt.Sprintf("start_observed is %v, want %v", e.GetStartObserved(), want.started))
	}
	wroteOut, dropped := want.bytesOut, e.GetDroppedBytes()
	if audit != nil {
		wroteOut, dropped = audit.next, audit.dropped
		if audit.dropped != e.GetDroppedBytes() {
			problems = append(problems, fmt.Sprintf("the reader was told %d bytes were dropped and Ended says %d", audit.dropped, e.GetDroppedBytes()))
		}
	}
	if e.GetBytesOut() != wroteOut {
		problems = append(problems, fmt.Sprintf("bytes_out is %d, the command wrote %d", e.GetBytesOut(), wroteOut))
	}
	if e.GetBytesIn() != want.bytesIn {
		problems = append(problems, fmt.Sprintf("bytes_in is %d, the host sent %d", e.GetBytesIn(), want.bytesIn))
	}
	if e.GetLastOffset() != e.GetBytesOut() {
		problems = append(problems, fmt.Sprintf("last_offset is %d, bytes_out is %d: they are one past the last output byte", e.GetLastOffset(), e.GetBytesOut()))
	}
	if got, wantC := e.GetOutput(), completenessFor(want.end, dropped); got != wantC {
		problems = append(problems, fmt.Sprintf("output is %s, want %s (dropped %d bytes, end %s)", got, wantC, dropped, endText(want.end)))
	}
	if !tty && (e.ModeReset == nil || len(e.ModeReset) != 0) {
		problems = append(problems, fmt.Sprintf("mode_reset is %q (present: %v), want present and empty for a session without a terminal", e.GetModeReset(), e.ModeReset != nil))
	}
	return problems
}

// checkEnded holds the Ended of a session to what the case expects of it.
func (s *wireSession) checkEnded(name string, e *execv2.Ended, want wantEnded) {
	t := s.r.t
	t.Helper()
	for _, problem := range endedProblems(e, s.tty(), want, s.out) {
		t.Errorf("%s: session %q: %s", name, s.id, problem)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the steps of the scripts
// ---------------------------------------------------------------------------------------------------------------

// outputUnit is what case 3's commands write over and over: an escape sequence of CSI and one of OSC, a three-byte character, and
// bytes that are no UTF-8 at all. Its length is such that the first frame of 64 KiB ends in the middle of a CSI.
const outputUnit = "\x1b[38;5;196m\xe2\x82\xac\xff\xfe\x80\x1b]0;t\x07"

// unitByte is the byte of the unit at an offset of the output.
func unitByte(offset uint64) byte { return outputUnit[offset%uint64(len(outputUnit))] }

// unitBytes returns n bytes of the unit from an offset.
func unitBytes(offset uint64, n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = unitByte(offset + uint64(i))
	}
	return data
}

// floodPattern is a step that writes total bytes of the stress's pattern to the command's output in writes of 32 KiB and counts
// each byte once its write has returned, which is once the plugin has taken it.
func floodPattern(total int, accepted *atomic.Int64) Step {
	return stepFunc(func(ctx context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		for offset := 0; offset < total; {
			if err := ctx.Err(); err != nil {
				return true, exec.Exit{}, err
			}
			n := min(32<<10, total-offset)
			if _, err := p.Stdout.Write(patternBytes(uint64(offset), n)); err != nil {
				return true, exec.Exit{}, err
			}
			offset += n
			accepted.Store(int64(offset))
		}
		return false, exec.Exit{}, nil
	})
}

// observeSizes is a step that hands every value of Proc.Resize to a channel, from a goroutine that ends when the channel of the
// command closes, and goes on.
func observeSizes(sizes chan<- exec.Size) Step {
	return stepFunc(func(_ context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		go func() {
			for size := range p.Resize {
				sizes <- size
			}
		}()
		return false, exec.Exit{}, nil
	})
}

// poll waits until cond holds, for at most bound. The condition is the oracle; the wait only keeps a bug from hanging a case.
// (The stress has the same wait; this one is for the cases, and says nothing but whether cond held.)
func pauseUntil(window time.Duration, hit func() bool) bool {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if hit() {
			return true
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	return hit()
}

// baseName is the base name of the program an argv names, as Opened reports it.
func baseName(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return path.Base(strings.ReplaceAll(argv[0], `\`, "/"))
}
