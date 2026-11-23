package exectest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// ---------------------------------------------------------------------------------------------------------------
// case 1: Opened is first and its facts agree with Describe
// ---------------------------------------------------------------------------------------------------------------

// openedWant is what Opened must say of a session.
type openedWant struct {
	id        string
	info      exec.TargetInfo
	described *execv2.TargetInfo
	resource  string
	argv      []string     // the argv the request carried; empty: the target's default
	tty       bool         // the request asked for a terminal
	size      *execv2.Size // the size the request carried
	now       time.Time
}

// openedProblems holds the Opened of a session to the facts the SDK knows before the author runs: the session, the target as
// declared and as Describe lists it, the program (the default filled in), no terminal fact (the target says it at Started), the
// size asked for or the default one for a terminal, the capabilities and limits as declared, when the Open was accepted, no pid, no
// transport and no TERM, no integration, and a replay of a new session: start = end = bytes = 0.
func openedProblems(o *execv2.Opened, w openedWant) []string {
	f := o.GetFacts()
	if f == nil {
		return []string{"Opened carries no facts"}
	}
	var found []string
	add := func(format string, args ...any) { found = append(found, fmt.Sprintf(format, args...)) }
	if f.GetSessionId() != w.id {
		add("session_id is %q, want %q", f.GetSessionId(), w.id)
	}
	target := f.GetTarget()
	if target.GetProvider() != w.info.Provider || target.GetType() != w.info.Type {
		add("the target is %s/%s, want %s/%s", target.GetProvider(), target.GetType(), w.info.Provider, w.info.Type)
	}
	if target.GetId() != w.resource {
		add("the target id is %q, want the resource %q", target.GetId(), w.resource)
	}
	if target.GetDisplayName() != w.info.DisplayName || target.GetDisplayName() != w.described.GetDisplayName() {
		add("the display name is %q, declared %q, described %q", target.GetDisplayName(), w.info.DisplayName, w.described.GetDisplayName())
	}
	if len(target.GetSelection()) != 0 {
		add("the selection is %v, the request sent none", target.GetSelection())
	}
	argv := w.argv
	if len(argv) == 0 {
		argv = w.info.DefaultArgv
	}
	if program := f.GetProgram(); !slices.Equal(program.GetArgv(), argv) || program.GetName() != baseName(argv) || program.GetPath() != "" {
		add("the program is %v (name %q, path %q), want the argv %v, its base name and no path: the SDK knows no path before the author runs", program.GetArgv(), program.GetName(), program.GetPath(), argv)
	}
	if f.Tty != nil {
		add("tty is present (%v): the target says whether it gave a terminal, at Started, and Opened leaves it absent", f.GetTty())
	}
	switch {
	case !w.tty && f.GetSize() != nil:
		add("a size %v for a session without a terminal", f.GetSize())
	case w.tty && w.size != nil && !proto.Equal(f.GetSize(), w.size):
		add("the size is %v, the request asked for %v", f.GetSize(), w.size)
	case w.tty && w.size == nil && (f.GetSize().GetCols() != 80 || f.GetSize().GetRows() != 24):
		add("the size is %v, want the default terminal of 80 x 24 for a request with no size", f.GetSize())
	}
	for _, diff := range capabilityDiffs(f.GetCapabilities(), w.info.Capabilities) {
		add("the capabilities differ from the ones declared: %s", diff)
	}
	if !proto.Equal(f.GetLimits(), w.described.GetSession()) {
		add("the limits are %v, Describe says %v", f.GetLimits(), w.described.GetSession())
	}
	if created := time.UnixMilli(f.GetCreatedAtUnixMs()); f.GetCreatedAtUnixMs() == 0 || created.Before(w.now.Add(-time.Minute)) || created.After(w.now.Add(time.Minute)) {
		add("created_at_unix_ms is %d, not when the Open was accepted (now is %d)", f.GetCreatedAtUnixMs(), w.now.UnixMilli())
	}
	if f.GetPid() != 0 || f.GetTransport() != "" || f.GetTerm() != "" {
		add("pid %d, transport %q, term %q: the target says them at Started, and Opened has none", f.GetPid(), f.GetTransport(), f.GetTerm())
	}
	if f.GetIntegration() != nil {
		add("integration facts %v for a request that asked for none", f.GetIntegration())
	}
	if r := o.GetReplay(); r == nil || r.GetStart() != 0 || r.GetEnd() != 0 || r.GetBytes() != 0 {
		add("the replay is %v, want one of a new session with start = end = bytes = 0", r)
	}
	return found
}

// caseWireOpened: Opened is the first frame and its facts agree with Describe; a start that is refused sends no frame at all.
func caseWireOpened(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	rig.script("idle", Block())
	described := rig.described()

	type mode struct {
		name string
		tty  bool
		size *execv2.Size
	}
	modes := []mode{{"without a terminal", false, nil}}
	if rig.tty() {
		modes = append(modes, mode{"with a terminal and no size", true, nil}, mode{"with a terminal of a size", true, &execv2.Size{Cols: 90, Rows: 30}})
	}
	for _, m := range modes {
		id := rig.newID("opened")
		s := rig.mustOpen(id, "idle", func(st *execv2.Start) { st.Tty, st.Size = m.tty, m.size })
		for _, problem := range openedProblems(s.conn.Opened(), openedWant{
			id: id, info: rig.info, described: described, resource: rig.resource, tty: m.tty, size: m.size, now: time.Now(),
		}) {
			t.Errorf("%s: %s", m.name, problem)
		}
		// Right after Opened: a terminal asked for with no size is told that the SDK picked one; anything else, nothing.
		if m.tty && m.size == nil {
			if got := s.next("the notice that the size was defaulted").GetNotice().GetKind(); got != execv2.NoticeKind_NOTICE_KIND_SIZE_DEFAULTED {
				t.Errorf("%s: the frame after Opened is a notice of kind %v, want SIZE_DEFAULTED", m.name, got)
			}
		}
		_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
		ended := s.finish()
		s.checkEnded(m.name, ended, wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, rig.info.Capabilities.Kill)})
		// A command that reports nothing and writes nothing gets no Started.
		if s.grammar.started != 0 {
			t.Errorf("%s: a Started came for a command that reported nothing and wrote nothing", m.name)
		}
	}

	// A refusal is a status and no frame: the first thing the stream gives is the error, and the author's function never ran.
	ran := rig.started.Load()
	frame, err := rig.rawFirst(rig.openRequest("", "idle"))
	if frame != nil || err == nil {
		t.Errorf("an Open with an invalid session id gave %s and %v: a refusal is a status, with no frame before it", frameName(frame), err)
	}
	if _, err := rig.open("", "idle"); err == nil || !isRefusal(err, exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified) {
		t.Errorf("an Open with an invalid session id was refused with %v, want INVALID_REQUEST", err)
	}
	if got := rig.started.Load(); got != ran {
		t.Errorf("the author's function ran %d times for requests that were refused: a refusal means nothing ran", got-ran)
	}
}

// rawFirst starts a stream with the generated client, sends the open frame and returns what the stream gives first: a frame, or the
// status that ended it.
func (r *wireRig) rawFirst(req *execv2.OpenRequest) (*execv2.ServerFrame, error) {
	r.t.Helper()
	ctx, err := resourceplugin.OutgoingSessionContext(r.request())
	if err != nil {
		r.t.Fatalf("the call's context: %v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, caseBound)
	r.t.Cleanup(cancel)
	stream, err := r.raw.Open(ctx)
	if err != nil {
		return nil, err
	}
	if err := stream.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: req}}); err != nil {
		// A stream that refused the first frame has ended: its status is the answer.
		_, recvErr := stream.Recv()
		return nil, recvErr
	}
	return stream.Recv()
}

// isRefusal reports whether err is the refusal of the exec lane with the class and reason.
func isRefusal(err error, class exec.ErrorClass, reason exec.ErrorReason) bool {
	var refusal *exec.Error
	return errors.As(err, &refusal) && refusal.Class == class && refusal.Reason == reason
}

// ---------------------------------------------------------------------------------------------------------------
// case 2: input and resize frames reach the author
// ---------------------------------------------------------------------------------------------------------------

// resizeSizes are three sizes of a terminal that the target's largest dimension allows, from small to large.
func resizeSizes(maxDimension uint32) (small, medium, large exec.Size) {
	md := max(maxDimension, 80)
	return exec.Size{Cols: uint16(max(20, md/4)), Rows: uint16(max(10, md/8))},
		exec.Size{Cols: uint16(max(40, md/2)), Rows: uint16(max(20, md/4))},
		exec.Size{Cols: uint16(md), Rows: uint16(max(40, md/2))}
}

// caseWireInput: the input frames of a host reach the author in the order they were sent, byte for byte however they are split (many
// small frames and one of the largest size the target takes), and the resize frames are forwarded: the author sees the size the
// session was asked for first and then the newest valid one, never an invalid one. There is no acknowledgement of a resize in this
// build, so the case does not order a resize against the input.
func caseWireInput(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	described := rig.described()
	maxFrame := int(described.GetSession().GetMaxInputFrame())
	maxDimension := described.GetSession().GetMaxDimension()
	small, medium, large := resizeSizes(maxDimension)
	sizes := make(chan exec.Size, 64)
	rig.script("echo", observeSizes(sizes), CopyInput())

	tty := rig.tty()
	s := rig.mustOpen(rig.newID("input"), "echo", func(st *execv2.Start) {
		if tty {
			st.Size = &execv2.Size{Cols: uint32(small.Cols), Rows: uint32(small.Rows)}
		}
	})
	audit := s.audit(patternByte)

	// Sizes first: the size the session was asked for is the first value of Proc.Resize, and a later valid size follows it.
	if tty {
		if first := awaitSize(t, sizes); first != small {
			t.Errorf("the author's first size is %v, want the one the session was asked for, %v", first, small)
		}
		if info.Capabilities.Resize {
			frames := []struct{ cols, rows uint32 }{
				{uint32(medium.Cols), uint32(medium.Rows)},
				{0, 5},                // a zero dimension: refused, and changes nothing
				{maxDimension + 1, 5}, // above the target's limit: the same
				{uint32(large.Cols), uint32(large.Rows)},
			}
			for i, f := range frames {
				if err := s.resize(uint32(i+1), f.cols, f.rows); err != nil {
					t.Fatalf("send a resize: %v", err)
				}
			}
			seen := []exec.Size{}
			for len(seen) == 0 || seen[len(seen)-1] != large {
				seen = append(seen, awaitSize(t, sizes))
			}
			rank := map[exec.Size]int{medium: 1, large: 2}
			last := 0
			for _, size := range seen {
				r, valid := rank[size]
				if !valid {
					t.Errorf("the author was handed the size %v: a size that is not valid must change nothing", size)
				}
				if r < last {
					t.Errorf("the author saw the sizes %v: a newer size is never followed by an older one", seen)
				}
				last = r
			}
		}
	}

	// Input: many small frames, one of the largest size, then more small ones; echoed back, they are the same bytes in the same order.
	var frames [][]byte
	var total uint64
	add := func(n int) {
		frames = append(frames, patternBytes(total, n))
		total += uint64(n)
	}
	for i := range 100 {
		add(i%37 + 1)
	}
	add(maxFrame)
	for i := range 100 {
		add(i%11 + 1)
	}
	sent := make(chan error, 1)
	go func() {
		for _, frame := range frames {
			if err := s.conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: frame}}); err != nil {
				sent <- err
				return
			}
		}
		sent <- nil
	}()
	for audit.read < total {
		if s.next(fmt.Sprintf("the echo of %d more bytes", total-audit.read)).GetEnded() != nil {
			t.Fatalf("the session ended with %d of %d bytes echoed", audit.read, total)
		}
	}
	select {
	case err := <-sent:
		if err != nil {
			t.Errorf("the host could not send its input: %v", err)
		}
	case <-time.After(caseBound):
		t.Fatalf("the host's input frames were not all taken within %v", caseBound)
	}
	if audit.next != total {
		t.Errorf("the echo ended at offset %d, the host sent %d bytes", audit.next, total)
	}
	// A session that cannot be resized is handed no size but the first: nothing was submitted to it.
	if !tty || !info.Capabilities.Resize {
		_ = s.resize(1, uint32(medium.Cols), uint32(medium.Rows))
		s.roundTrip(patternBytes(total, 16))
		select {
		case size := <-sizes:
			t.Errorf("the author was handed the size %v by a target that declares no resize", size)
		default:
		}
	}
	_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	ended := s.finish()
	sentTotal := total
	if !tty || !info.Capabilities.Resize {
		sentTotal += 16
	}
	s.checkEnded("the echo", ended, wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, info.Capabilities.Kill), started: true, bytesIn: sentTotal})
}

// awaitSize takes the next size the author was handed, within the bound of a case.
func awaitSize(t checkT, sizes <-chan exec.Size) exec.Size {
	t.Helper()
	select {
	case size := <-sizes:
		return size
	case <-time.After(caseBound):
		t.Fatalf("the author was handed no size within %v", caseBound)
		return exec.Size{}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// case 3: output is exact
// ---------------------------------------------------------------------------------------------------------------

// wireWrite is one write of a command: to standard error or to the output, n bytes of the unit.
type wireWrite struct {
	stderr bool
	n      int
}

// outputLayout is what case 3's command writes: small writes that no frame boundary respects, one of 200 KiB that frames of 64 KiB cut
// four times, and a second stream when the target has one.
func outputLayout(separateStderr bool) []wireWrite {
	layout := []wireWrite{{false, 1}, {false, 3}, {true, 5}, {false, 7}, {false, 200 << 10}, {true, 11}, {false, 64 << 10}, {true, 64<<10 + 5}, {false, 13}}
	if !separateStderr {
		for i := range layout {
			layout[i].stderr = false
		}
	}
	return layout
}

// layoutSegments are the runs of the output that each stream wrote, in the order the writes were made.
func layoutSegments(layout []wireWrite, tty bool) ([]wireSegment, uint64) {
	var segments []wireSegment
	var offset uint64
	for _, w := range layout {
		stream := execv2.Stream_STREAM_STDOUT
		switch {
		case tty:
			stream = execv2.Stream_STREAM_TTY
		case w.stderr:
			stream = execv2.Stream_STREAM_STDERR
		}
		segments = append(segments, wireSegment{start: offset, end: offset + uint64(w.n), stream: stream})
		offset += uint64(w.n)
	}
	return segments, offset
}

// layoutScript is the command of case 3: it writes the layout, from one goroutine so that the order of the writes is the order of the
// output, after it reported what it knows (report) or without reporting, and ends with the exit of a process.
func layoutScript(layout []wireWrite, report bool, exit exec.Exit) exec.ExecFunc {
	return func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		if report {
			tty := p.Request.TTY
			p.Started(exec.SessionFacts{
				Program: exec.Program{Argv: p.Request.Argv, Name: baseName(p.Request.Argv), Path: "/usr/bin/" + baseName(p.Request.Argv)},
				PID:     4242, TTY: &tty, Term: "xterm-256color", Transport: "exectest",
			})
		}
		var offset uint64
		for _, w := range layout {
			out := p.Stdout
			if w.stderr && p.Stderr != nil {
				out = p.Stderr
			}
			if _, err := out.Write(unitBytes(offset, w.n)); err != nil {
				return exec.Exit{}, err
			}
			offset += uint64(w.n)
		}
		return exit, nil
	}
}

// caseWireOutput: the output arrives with offsets that are contiguous, in frames of at most 64 KiB, with every byte untouched (escape
// sequences and invalid UTF-8 that a frame boundary cuts through are delivered as they were written), on the stream that wrote it (the
// terminal's with a terminal, standard output and standard error without), and the Started comes before the first of it: the author's
// own when it reported, the one the SDK makes from the facts of Opened when it did not.
func caseWireOutput(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	exit, end := processExit(info.Capabilities, 0)
	lossy := info.Capabilities.Stall == exec.StallLossy
	modes := []bool{false}
	if rig.tty() {
		modes = append(modes, true)
	}
	for _, tty := range modes {
		layout := outputLayout(!tty && info.Capabilities.SeparateStderr)
		segments, total := layoutSegments(layout, tty)
		for _, report := range []bool{true, false} {
			name := fmt.Sprintf("tty %v, the author reports %v", tty, report)
			script := fmt.Sprintf("layout-%v-%v", tty, report)
			rig.scriptFunc(script, layoutScript(layout, report, exit))
			s := rig.mustOpen(rig.newID("output"), script, func(st *execv2.Start) { st.Tty = tty })
			audit := s.audit(unitByte)
			audit.layout = segments
			ended := s.finish()
			s.checkEnded(name, ended, wantEnded{end: end, started: true})
			if audit.next != total {
				t.Errorf("%s: the output ends at offset %d, the command wrote %d bytes", name, audit.next, total)
			}
			if lossy {
				if audit.read+audit.dropped != total {
					t.Errorf("%s: read %d and dropped %d of %d bytes written", name, audit.read, audit.dropped, total)
				}
			} else if audit.read != total || audit.dropped != 0 {
				t.Errorf("%s: read %d and dropped %d of %d bytes written by a target whose stalled reader slows the process: nothing may be lost to a host that reads", name, audit.read, audit.dropped, total)
			}
			if audit.largest > maxOutputFrame {
				t.Errorf("%s: a frame of %d bytes", name, audit.largest)
			}
			if s.grammar.started != 1 {
				t.Errorf("%s: %d Started frames for a command that wrote output: exactly one precedes it", name, s.grammar.started)
				continue
			}
			s.checkStartedFacts(name, report, tty)
		}
	}
}

// checkStartedFacts holds the Started of a session to what it says: what the author reported, or, when it reported nothing, exactly the
// facts of Opened.
func (s *wireSession) checkStartedFacts(name string, reported, tty bool) {
	t := s.r.t
	t.Helper()
	facts := s.started.GetFacts()
	opened := s.conn.Opened().GetFacts()
	if !reported {
		if !proto.Equal(facts, opened) {
			t.Errorf("%s: the Started the SDK made carries %v, want the facts of Opened, %v", name, facts, opened)
		}
		return
	}
	argv := opened.GetProgram().GetArgv()
	switch {
	case facts.GetPid() != 4242 || facts.GetTransport() != "exectest" || facts.GetTerm() != "xterm-256color":
		t.Errorf("%s: Started says pid %d, transport %q, term %q, the author reported 4242, \"exectest\" and \"xterm-256color\"", name, facts.GetPid(), facts.GetTransport(), facts.GetTerm())
	case facts.Tty == nil || facts.GetTty() != tty:
		t.Errorf("%s: Started says tty %v (present %v), the author reported %v", name, facts.GetTty(), facts.Tty != nil, tty)
	case facts.GetProgram().GetPath() != "/usr/bin/"+baseName(argv) || !slices.Equal(facts.GetProgram().GetArgv(), argv):
		t.Errorf("%s: Started says the program is %v, want the argv %v at /usr/bin/%s", name, facts.GetProgram(), argv, baseName(argv))
	case facts.GetSessionId() != opened.GetSessionId() || !proto.Equal(facts.GetTarget(), opened.GetTarget()) || !proto.Equal(facts.GetLimits(), opened.GetLimits()) ||
		!proto.Equal(facts.GetCapabilities(), opened.GetCapabilities()):
		t.Errorf("%s: Started changed the session, the target, the limits or the capabilities of Opened: %v, Opened carried %v", name, facts, opened)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// cases 4 and 18: a host that stops reading
// ---------------------------------------------------------------------------------------------------------------

const (
	// floodBound is how many bytes a command may have written ahead of what its host has read, when the host's window is fixed: the
	// queue of the output (256 KiB), the frame in the plugin's hand, the window and the quota of the transport are about 450 KiB, and a
	// plugin that holds nothing back would let the command write everything.
	floodBound = 2 << 20
	// floodTotal is what the command writes: twice the bound.
	floodTotal = 2 * floodBound
	// readingPause is how long the host does not read while the command runs ahead.
	readingPause = 150 * time.Millisecond
)

// floodExit is the exit of the flood: the status of a process.
func floodScript(total int, accepted *atomic.Int64, exit exec.Exit) []Step {
	return []Step{floodPattern(total, accepted), EndWith(exit)}
}

// caseWireSlowConsumer: a host that does not read cannot make the plugin hold more than a bound: the command of a target that stalls
// the process (PROPAGATES) is held in its Write once the queue and the window of the transport are full, never gets further ahead of
// the host than that bound, and when the host reads again every byte arrives, none is dropped and the output is complete. A target that
// stalls lossily never holds its command back and is case 18's.
func caseWireSlowConsumer(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	switch {
	case info.Session.IsZero():
		t.Skipf(noSessions)
	case info.Capabilities.Stall != exec.StallPropagates:
		t.Skipf("the target declares STALL %s: only a target whose stalled reader slows the process holds its command back (a lossy one drops, case 18)", info.Capabilities.Stall)
	}
	rig := newWireRig(t, env, target)
	exit, end := processExit(info.Capabilities, 0)
	var accepted atomic.Int64
	rig.script("flood", floodScript(floodTotal, &accepted, exit)...)
	s := rig.mustOpenOn(rig.slow(), rig.newID("flood"), "flood")
	audit := s.audit(patternByte)

	// The host does not read. The command runs ahead until the plugin holds it, and no further than the bound.
	if pauseUntil(readingPause, func() bool { return accepted.Load() > floodBound }) {
		t.Errorf("the command wrote %d bytes while its host read nothing, more than the bound of %d: the plugin does not hold back a command whose host does not read", accepted.Load(), floodBound)
	}
	// Then it reads, frame by frame; at every frame the command is no further ahead of the host than the bound. The count of the
	// command is read before the host's own: the command can only have been ahead of what the host had read before it looked.
	var delivered uint64
	for {
		ahead := accepted.Load()
		frame := s.next("the output of the flood")
		if ahead-int64(delivered) > floodBound { //nolint:gosec // both are bounded by the flood
			t.Errorf("the command had written %d bytes when its host had read %d: more than %d ahead", ahead, delivered, floodBound)
			break
		}
		if ended := frame.GetEnded(); ended != nil {
			s.checkEnded("the flood", ended, wantEnded{end: end, started: true})
			if audit.read != floodTotal || audit.dropped != 0 {
				t.Errorf("the host read %d and was told %d dropped of %d written by a target whose stalled reader slows the process", audit.read, audit.dropped, floodTotal)
			}
			break
		}
		delivered = audit.read
	}
	if got := accepted.Load(); got != floodTotal {
		t.Errorf("the command wrote %d of %d bytes and ended: a command held back is released when its host reads", got, floodTotal)
	}
	for _, problem := range audit.problems {
		t.Errorf("%s", problem)
	}
	for _, problem := range s.grammar.problems {
		t.Errorf("%s", problem)
	}
}

// caseWireCompleteness: what Ended says of the output. A target that stalls lossily never holds its command back: a command that floods
// while its host does not read runs to its end, the queue keeps the newest bytes, the host is told exactly what was lost (Dropped
// ranges before the bytes that follow, and dropped_bytes in Ended), read plus dropped is what was written, and the output is TRUNCATED.
// A target whose stalled reader slows the process loses nothing to a host that reads, and its output is COMPLETE.
func caseWireCompleteness(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	described := rig.described()
	exit, end := processExit(info.Capabilities, 0)
	var accepted atomic.Int64
	lossy := info.Capabilities.Stall == exec.StallLossy
	ring := int(described.GetSession().GetReplayBytes())
	total := 1 << 20
	if lossy {
		// The queue holds a replay ring; the command writes more than the ring and the window can hold.
		total = ring + 1<<20
		if ring > 8<<20 {
			t.Logf("the declared replay ring is %d bytes, too large to overflow cheaply: the flood of %d bytes does not", ring, 4<<20)
			total = 4 << 20
		}
	}
	rig.script("flood", floodScript(total, &accepted, exit)...)
	s := rig.mustOpenOn(rig.slow(), rig.newID("flood"), "flood")
	audit := s.audit(patternByte)
	if lossy {
		// The host reads nothing, and the command is never held back: it writes every byte and returns.
		rig.await(fmt.Sprintf("a command that stalls lossily to write all %d bytes while its host reads nothing", total), func() bool { return rig.returned.Load() == 1 })
		if got := accepted.Load(); got != int64(total) {
			t.Errorf("the command wrote %d of %d bytes and returned", got, total)
		}
	}
	ended := s.finish()
	s.checkEnded("the flood", ended, wantEnded{end: end, started: true})
	switch {
	case audit.next != uint64(total):
		t.Errorf("the output ends at offset %d, the command wrote %d bytes", audit.next, total)
	case !lossy && (audit.dropped != 0 || audit.read != uint64(total)):
		t.Errorf("a target whose stalled reader slows the process lost %d bytes of %d to a host that reads", audit.dropped, total)
	case lossy && total > ring+(1<<20)-1 && audit.dropped == 0:
		t.Errorf("a command that wrote %d bytes into a ring of %d while its host read nothing lost none: the output is not bounded", total, ring)
	}
	if lossy {
		for reason, n := range audit.reasons {
			if reason != execv2.DropReason_DROP_REASON_EAGER_DRAIN_OVERFLOW {
				t.Errorf("%d Dropped ranges say %s, want EAGER_DRAIN_OVERFLOW for a target drained eagerly", n, reason)
			}
		}
		if audit.read+audit.dropped != uint64(total) {
			t.Errorf("read %d plus dropped %d is not the %d bytes written", audit.read, audit.dropped, total)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// case 5: a session that does not read does not move another's echo
// ---------------------------------------------------------------------------------------------------------------

// caseWireIsolation: a session whose host does not read (its command floods, and is held in its Write or runs on dropping) does not move
// another session's echo: it answers every time within a generous bound, and ending the stuck session releases its function.
func caseWireIsolation(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	switch {
	case info.Session.IsZero():
		t.Skipf(noSessions)
	case effectiveLimit(info) < 2:
		t.Skipf("the target serves one session at a time: there is no other session to move")
	}
	rig := newWireRig(t, env, target)
	exit, _ := processExit(info.Capabilities, 0)
	var accepted atomic.Int64
	rig.script("flood", floodScript(floodTotal, &accepted, exit)...)
	rig.script("echo", CopyInput())
	stuck := rig.mustOpenOn(rig.slow(), rig.newID("stuck"), "flood")
	_ = stuck
	rig.await("the flood to fill the queue and the window", func() bool { return accepted.Load() >= 320<<10 })
	echo := rig.mustOpen(rig.newID("echo"), "echo")
	const answerBound = 2 * time.Second
	for i := range 20 {
		began := time.Now()
		echo.roundTrip([]byte(fmt.Sprintf("ping-%02d", i)))
		if took := time.Since(began); took > answerBound {
			t.Errorf("an echo took %v while another session's host read nothing: a stuck session moved another's echo (bound %v)", took, answerBound)
		}
	}
	_ = echo.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	ended := echo.finish()
	echo.checkEnded("the echo", ended, wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, info.Capabilities.Kill), started: true, bytesIn: 20 * 7, bytesOut: 20 * 7})
	// The host that stopped reading goes away: its session ends and its function returns, however far the flood had got.
	_ = stuck.conn.Close()
	rig.await("the function of the session whose host went away to return", func() bool { return rig.returned.Load() == 2 })
}

var _ = math.MaxInt
var _ = status.Code
