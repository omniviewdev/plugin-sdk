package exectest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// Gate is a one-shot latch a test opens to let a scripted target go on: what a target waits for is an event the
// test causes, never a duration.
type Gate struct {
	once sync.Once
	ch   chan struct{}
}

// NewGate returns a closed gate (it blocks until Open).
func NewGate() *Gate { return &Gate{ch: make(chan struct{})} }

// Open lets every waiter go on; it is safe to call more than once.
func (g *Gate) Open() { g.once.Do(func() { close(g.ch) }) }

// Opened returns a channel that is closed when the gate is open.
func (g *Gate) Opened() <-chan struct{} { return g.ch }

// Step is one action of a scripted target. A script runs its steps in order; one that ends the command (Exit, Fail,
// Panic) or whose wait is cancelled stops it. A script that runs out of steps ends with exit code 0.
type Step interface {
	run(ctx context.Context, p *exec.Proc) (stop bool, exit exec.Exit, err error)
}

type stepFunc func(ctx context.Context, p *exec.Proc) (bool, exec.Exit, error)

func (f stepFunc) run(ctx context.Context, p *exec.Proc) (bool, exec.Exit, error) { return f(ctx, p) }

// WriteOut writes data to the stream the command writes to: stdout for a command without a terminal, the one
// terminal stream otherwise (the target writes to Proc.Stdout for it).
func WriteOut(data string) Step { return WriteChunks([]byte(data), 0) }

// WriteErr writes data to the command's standard error. The target must run without a terminal.
func WriteErr(data string) Step {
	return stepFunc(func(_ context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		if p.Stderr == nil {
			return true, exec.Exit{}, errors.New("exectest: WriteErr on a command that runs with a terminal")
		}
		if _, err := io.WriteString(p.Stderr, data); err != nil {
			return true, exec.Exit{}, err
		}
		return false, exec.Exit{}, nil
	})
}

// WriteChunks writes data in writes of at most chunk bytes (chunk <= 0 means one write), so that a test can pin
// the order and the framing of what the target wrote.
func WriteChunks(data []byte, chunk int) Step {
	return stepFunc(func(_ context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		// The cursor belongs to this execution: the step is shared by every Exec of the target, including
		// concurrent ones, and data stays as it was given.
		rest := data
		for len(rest) > 0 {
			n := len(rest)
			if chunk > 0 && chunk < n {
				n = chunk
			}
			if _, err := p.Stdout.Write(rest[:n]); err != nil {
				return true, exec.Exit{}, err
			}
			rest = rest[n:]
		}
		return false, exec.Exit{}, nil
	})
}

// WaitInput reads exactly n bytes of the command's stdin and discards them, waiting for them to arrive; it fails
// when stdin ends first.
func WaitInput(n int) Step {
	return stepFunc(func(ctx context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		if p.Stdin == nil {
			return true, exec.Exit{}, errors.New("exectest: WaitInput on a command with no stdin")
		}
		done := make(chan error, 1)
		go func() {
			_, err := io.CopyN(io.Discard, p.Stdin, int64(n))
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				return true, exec.Exit{}, err
			}
			return false, exec.Exit{}, nil
		case <-ctx.Done():
			return true, exec.Exit{}, ctx.Err()
		}
	})
}

// WaitGate blocks until the test opens the gate (or the command is cancelled).
func WaitGate(g *Gate) Step {
	return stepFunc(func(ctx context.Context, _ *exec.Proc) (bool, exec.Exit, error) {
		select {
		case <-g.Opened():
			return false, exec.Exit{}, nil
		case <-ctx.Done():
			return true, exec.Exit{}, ctx.Err()
		}
	})
}

// WaitResize blocks until a size arrives on Proc.Resize and discards it. A target that never reads Proc.Resize is
// one that ignores resizes: script no step for it.
func WaitResize() Step {
	return stepFunc(func(ctx context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		select {
		case _, ok := <-p.Resize:
			if !ok {
				return true, exec.Exit{}, errors.New("exectest: Proc.Resize closed")
			}
			return false, exec.Exit{}, nil
		case <-ctx.Done():
			return true, exec.Exit{}, ctx.Err()
		}
	})
}

// ReportStarted calls Proc.Started with s.
func ReportStarted(s exec.SessionFacts) Step {
	return stepFunc(func(_ context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		p.Started(s)
		return false, exec.Exit{}, nil
	})
}

// ReportLink calls Proc.Link with state.
func ReportLink(state exec.LinkState) Step {
	return stepFunc(func(_ context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		p.Link(state)
		return false, exec.Exit{}, nil
	})
}

// EndWith ends the command with e.
func EndWith(e exec.Exit) Step {
	return stepFunc(func(context.Context, *exec.Proc) (bool, exec.Exit, error) { return true, e, nil })
}

// Fail ends the command with an error, as a target that could not run it would.
func Fail(err error) Step {
	return stepFunc(func(context.Context, *exec.Proc) (bool, exec.Exit, error) { return true, exec.Exit{}, err })
}

// Panic panics with v: what a faulty author does.
func Panic(v any) Step {
	return stepFunc(func(context.Context, *exec.Proc) (bool, exec.Exit, error) { panic(v) })
}

// Block waits until the command is cancelled and returns the context error: a command that never ends by itself.
func Block() Step {
	return stepFunc(func(ctx context.Context, _ *exec.Proc) (bool, exec.Exit, error) {
		<-ctx.Done()
		return true, exec.Exit{}, ctx.Err()
	})
}

// CopyInput copies the command's stdin to its output until stdin ends (the host closed the input, or the session ended)
// or the command is cancelled: an echo, the command of a shell that only repeats what it is told. The step goes on when
// stdin ends. A write that fails because the session ended is the command's end, not a failure of the target.
func CopyInput() Step {
	return stepFunc(func(ctx context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		if p.Stdin == nil {
			return true, exec.Exit{}, errors.New("exectest: CopyInput on a command with no stdin")
		}
		done := make(chan error, 1)
		go func() {
			_, err := io.Copy(p.Stdout, p.Stdin)
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil && ctx.Err() == nil && !errors.Is(err, io.ErrClosedPipe) {
				return true, exec.Exit{}, err
			}
			return false, exec.Exit{}, nil
		case <-ctx.Done():
			return true, exec.Exit{}, ctx.Err()
		}
	})
}

// IgnoreCancel waits until the test opens the gate and looks at nothing else, the command's context included: a function
// that ignores its cancelled context. It is the stuck mode of a target; the engine gives up on such a function after its
// grace, and the test opens the gate afterwards so that the goroutine ends with the test.
func IgnoreCancel(g *Gate) Step {
	return stepFunc(func(context.Context, *exec.Proc) (bool, exec.Exit, error) {
		<-g.Opened()
		return false, exec.Exit{}, nil
	})
}

// LateWrites records what the goroutine of OutliveWith got when it used the command's stdio after the function returned.
type LateWrites struct {
	mu     sync.Mutex
	stdout error
	stderr error
	stdin  error
	nOut   int
	nErr   int
	done   chan struct{}
}

// NewLateWrites returns an empty record.
func NewLateWrites() *LateWrites { return &LateWrites{done: make(chan struct{})} }

// Done is closed when the goroutine has used the stdio.
func (l *LateWrites) Done() <-chan struct{} { return l.done }

// Stdout returns how many bytes the late write to stdout took and the error it got.
func (l *LateWrites) Stdout() (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nOut, l.stdout
}

// Stderr returns how many bytes the late write to stderr took and the error it got, or (0, nil) when the command has no
// stderr.
func (l *LateWrites) Stderr() (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nErr, l.stderr
}

// Stdin returns the error the late read of stdin got: io.EOF once the session has ended.
func (l *LateWrites) Stdin() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stdin
}

// OutliveWith starts a goroutine that keeps the command's stdio and, when the gate opens, writes data to stdout and to
// stderr and reads stdin once, recording what each returned in late. The step goes on at once, so that the steps after it
// end the command and the goroutine outlives its function: a function that returns before its process ends, and a
// goroutine that writes after the return.
func OutliveWith(g *Gate, data string, late *LateWrites) Step {
	return stepFunc(func(_ context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		go func() {
			defer close(late.done)
			<-g.Opened()
			n, err := io.WriteString(p.Stdout, data)
			late.mu.Lock()
			late.nOut, late.stdout = n, err
			late.mu.Unlock()
			if p.Stderr != nil {
				n, err = io.WriteString(p.Stderr, data)
				late.mu.Lock()
				late.nErr, late.stderr = n, err
				late.mu.Unlock()
			}
			if p.Stdin != nil {
				_, err = p.Stdin.Read(make([]byte, 1))
				late.mu.Lock()
				late.stdin = err
				late.mu.Unlock()
			}
			p.Started(exec.SessionFacts{})
			p.Link(exec.LinkGood)
		}()
		return false, exec.Exit{}, nil
	})
}

// FakeTargetOption configures NewFakeTarget.
type FakeTargetOption func(*fakeTarget)

type fakeTarget struct {
	provider, resourceType string // set by FakeRouting
	info                   exec.TargetInfo
	options                exec.OptionsFunc
	classify               exec.ClassifyFunc
	script                 []Step
}

// FakeRouting sets the routing key of the target, its Info.Provider and Info.Type (default "fake" and "thing"). It
// applies over the routing key of a description given with FakeInfo, whatever the order of the options.
func FakeRouting(provider, resourceType string) FakeTargetOption {
	return func(f *fakeTarget) { f.provider, f.resourceType = provider, resourceType }
}

// FakeInfo replaces the target's description. The default describes a shell in a Pod-like target: a terminal,
// resize, signals, env and cwd, ends with a code or a signal, and sessions and runs under the SDK's default limits.
// A description that names no Provider and Type takes the routing key of FakeRouting, or "fake" and "thing".
func FakeInfo(info exec.TargetInfo) FakeTargetOption {
	return func(f *fakeTarget) { f.info = info }
}

// FakeOptions sets the callback that lists the choices of a resource.
func FakeOptions(fn exec.OptionsFunc) FakeTargetOption {
	return func(f *fakeTarget) { f.options = fn }
}

// FakeStaticOptions makes the target list the same options for every resource.
func FakeStaticOptions(options ...exec.Option) FakeTargetOption {
	return FakeOptions(func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		return options, nil
	})
}

// FakeClassify sets the target's own error classification.
func FakeClassify(fn exec.ClassifyFunc) FakeTargetOption {
	return func(f *fakeTarget) { f.classify = fn }
}

// FakeScript sets what the target's Exec does, step by step.
func FakeScript(steps ...Step) FakeTargetOption {
	return func(f *fakeTarget) { f.script = steps }
}

// NewFakeTarget returns a scriptable exec.Target: its Exec runs the script given with FakeScript against the
// command's stdio and returns the way the script ends. It is what a test of the engine, of a host provider or of
// the conformance suite itself runs against.
func NewFakeTarget(opts ...FakeTargetOption) exec.Target {
	f := &fakeTarget{
		info: exec.TargetInfo{
			Provider:     "fake",
			Type:         "thing",
			DisplayName:  "Fake",
			DefaultArgv:  []string{"/bin/sh"},
			Capabilities: defaultFakeCapabilities(),
			Session:      exec.DefaultSessionLimits(),
			Run:          exec.DefaultRunLimits(),
		},
	}
	for _, opt := range opts {
		opt(f)
	}
	info := f.info
	if f.provider != "" || f.resourceType != "" {
		info.Provider, info.Type = f.provider, f.resourceType
	}
	if info.Provider == "" {
		info.Provider = "fake"
	}
	if info.Type == "" {
		info.Type = "thing"
	}
	return exec.Target{
		Info:     info,
		Exec:     f.exec,
		Options:  f.options,
		Classify: f.classify,
	}
}

// defaultFakeCapabilities is what the default fake target declares: a shell in a Pod-like target. Every call returns
// a value that shares no storage with another.
func defaultFakeCapabilities() exec.Capabilities {
	return exec.Capabilities{
		TTY:            true,
		SeparateStderr: true,
		Resize:         true,
		InitialSize:    true,
		StdinEOF:       true,
		Signals:        []exec.Signal{exec.SignalInt, exec.SignalTerm, exec.SignalKill},
		Kill:           exec.KillTerminates,
		Env:            true,
		Cwd:            true,
		ExitReport:     exec.ExitReportCodeAndSignal,
		Stall:          exec.StallPropagates,
	}
}

func (f *fakeTarget) exec(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	for _, step := range f.script {
		stop, exit, err := step.run(ctx, p)
		if err != nil {
			return exec.Exit{}, err
		}
		if stop {
			return exit, nil
		}
	}
	return exec.ExitCode(0), nil
}

// ProcRig runs an exec.ExecFunc without an engine: it builds the exec.Proc the SDK would, with stdio the test
// controls, and records what the function reports. An author unit-tests an ExecFunc with it; the conformance suite
// and the tests of the engine's building blocks use it too.
//
// The rig opens the command as the SDK does. The capabilities of its session start as the kind's declared set (see
// RigCapabilities) narrowed by the Capabilities of the choice that each option takes in the request's selection (see
// RigOptions): that is what the host reads in Opened (see Opened) and what Proc.Capabilities starts as. The mode of a
// session is declared on its choice, and the function does not report it.
//
// What the function reports with Proc.Started is held to the contract by the SDK's own rule, which the rig uses and
// does not copy: the host receives at most one Started, and always before the first output. A report before the first
// output is taken, once; a command that writes output without having reported gets the Started the SDK makes from the
// facts of Opened; a report after the first output, or a second one, is refused. Started returns what the host would
// receive. A session may only narrow what it was opened with: a field that a report puts above the opened value is
// clamped to it. For each of these violations the rig tells the test with Errorf, at the author's call, where the SDK
// logs the author's bug. Effective returns the resulting capabilities.
type ProcRig struct {
	t    testing.TB
	proc *exec.Proc

	opened exec.Capabilities // what the SDK puts in Opened: the rig's own copy, never handed out
	gate   exechook.StartGate[exec.SessionFacts, exec.Capabilities]

	stdin *io.PipeWriter

	mu        sync.Mutex
	cond      *sync.Cond
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	started   []exec.SessionFacts // what the host would receive: at most one
	startedAt []int               // for each of them, the bytes of output (both streams) recorded before it
	links     []exec.LinkState
	resize    chan exec.Size
	closed    bool // Proc.Resize is closed: the command has ended
}

// ProcRigOption configures NewProcRig.
type ProcRigOption func(*rigConfig)

type rigConfig struct {
	declared exec.Capabilities
	options  []exec.Option
}

// RigCapabilities sets the capabilities of the kind the command runs in, its upper bound: the rig opens the session
// with them, narrowed by the choices of the selection (see RigOptions), hands the command a deep copy of the result as
// Proc.Capabilities and holds what the command reports to it. A test of an author's function gives the declared
// capabilities of the author's own target; the default is the default fake target's.
func RigCapabilities(declared exec.Capabilities) ProcRigOption {
	return func(c *rigConfig) { c.declared = cloneCapabilities(declared) }
}

// RigOptions sets the options of the resource the command runs in: what the target's OptionsFunc returns for it. The
// rig resolves the request's selection against them as the SDK does at admission: for each option, the choice the
// selection names, or the default choice (IsDefault) of an option it omits, narrows the kind's capabilities by its own
// Capabilities. A value that names no choice, an option the options do not have and a choice with no Capabilities
// narrow nothing. The result is what Opened returns and what Proc.Capabilities starts as.
//
// A choice that declares a field above the kind's (see RigCapabilities) is the author's bug in Options: the SDK serves
// the kind's value for it and logs it, and the rig opens the command with the kind's value and tells the test with
// Errorf, once for each field, when the rig is built. Without RigOptions the session is opened with the kind's
// capabilities.
func RigOptions(options ...exec.Option) ProcRigOption {
	return func(c *rigConfig) { c.options = options }
}

// NewProcRig returns a rig for a command with the given request. A request with TTY set has no Stderr, as in the
// SDK; Proc.Resize is primed with Request.Size when it is not zero. Proc.Capabilities is a deep copy of the
// capabilities the session is opened with, as the SDK's is: the declared capabilities of the kind (the default fake
// target's, a shell in a Pod-like target, unless RigCapabilities says otherwise) narrowed by the choices that the
// request's selection takes in the options of RigOptions (none by default).
func NewProcRig(t testing.TB, req exec.Request, opts ...ProcRigOption) *ProcRig {
	t.Helper()
	cfg := rigConfig{declared: defaultFakeCapabilities()}
	for _, opt := range opts {
		opt(&cfg)
	}
	tellWidenedChoices(t, cfg.declared, cfg.options)
	opened := exechook.OpenedCapabilities(cfg.declared, cfg.options, req.Selection)
	stdinReader, stdinWriter := io.Pipe()
	t.Cleanup(func() { _ = stdinWriter.Close(); _ = stdinReader.Close() })
	r := &ProcRig{
		t: t, opened: opened, gate: exechook.NewStartGate[exec.Request, exec.SessionFacts](req, opened),
		stdin: stdinWriter, resize: make(chan exec.Size, 1),
	}
	r.cond = sync.NewCond(&r.mu)
	r.proc = &exec.Proc{
		Context:      &types.PluginContext{Context: context.Background()},
		Request:      req,
		Capabilities: cloneCapabilities(opened),
		Stdin:        stdinReader,
		Stdout:       rigWriter{r: r, buffer: &r.stdout},
		Resize:       r.resize,
		Started:      r.report,
		Link: func(state exec.LinkState) {
			r.mu.Lock()
			r.links = append(r.links, state)
			r.cond.Broadcast()
			r.mu.Unlock()
		},
	}
	if !req.TTY {
		r.proc.Stderr = rigWriter{r: r, buffer: &r.stderr}
	}
	if !req.Size.IsZero() {
		r.resize <- req.Size
	}
	return r
}

// tellWidenedChoices tells the test, with Errorf, each field that a choice of the options declares above the kind's: what
// the SDK serves the kind's value for and logs when it serves the options. The report is a test helper, so that the failure
// points at the call that built the rig and not into it.
func tellWidenedChoices(t testing.TB, declared exec.Capabilities, options []exec.Option) {
	t.Helper()
	for i := range options {
		for j := range options[i].Choices {
			choice := &options[i].Choices[j]
			if choice.Capabilities == nil {
				continue
			}
			_, violations := exechook.NarrowCapabilities(declared, *choice.Capabilities)
			for _, violation := range violations {
				t.Errorf("RigOptions: option %q choice %q declares capabilities above the kind's, and the SDK serves the kind's value for the field and logs it: %s",
					options[i].Name, choice.Value, violation)
			}
		}
	}
}

// cloneCapabilities returns a deep copy: the lists are not shared with the original.
func cloneCapabilities(c exec.Capabilities) exec.Capabilities {
	c.Signals = slices.Clone(c.Signals)
	c.Integration = slices.Clone(c.Integration)
	return c
}

// cloneFacts returns a deep copy of the facts: the TTY pointer, the capabilities with their lists and the program's argv
// are not shared with the original.
func cloneFacts(s exec.SessionFacts) exec.SessionFacts {
	if s.TTY != nil {
		s.TTY = new(*s.TTY)
	}
	if s.Capabilities != nil {
		s.Capabilities = new(cloneCapabilities(*s.Capabilities))
	}
	s.Program.Argv = slices.Clone(s.Program.Argv)
	return s
}

// report is Proc.Started. It hands the facts to the SDK's start gate, which takes a report that comes before the first
// output, once, held to the capabilities the session was opened with, and refuses one that comes late or a second. What
// the host would receive is recorded, and each violation that the gate names is told to the test: the author broke the
// contract, which the SDK would log. The report is a test helper, so that the failure points at the author's call of
// Proc.Started and not into the rig.
func (r *ProcRig) report(s exec.SessionFacts) {
	r.t.Helper()
	r.mu.Lock()
	violations := r.gate.Report(s, r.record)
	r.mu.Unlock()
	for _, violation := range violations {
		r.t.Errorf("%s", violation)
	}
}

// record keeps what the host would receive in a Started frame, and how much output the rig had recorded when it did. The
// rig's lock is held.
func (r *ProcRig) record(facts exec.SessionFacts) {
	r.started = append(r.started, facts)
	r.startedAt = append(r.startedAt, r.stdout.Len()+r.stderr.Len())
	r.cond.Broadcast()
}

// Opened returns the capabilities the SDK puts in Opened for this command: the declared capabilities of the kind (see
// RigCapabilities) narrowed by the choice that each option of RigOptions takes in the request's selection. It is also what
// Proc.Capabilities starts as. The result is the caller's own copy.
func (r *ProcRig) Opened() exec.Capabilities { return cloneCapabilities(r.opened) }

// Effective returns the capabilities of the session as the SDK would take them: the ones it was opened with (see Opened),
// narrowed by the Started that the command reported, a field above the opened value clamped to it. A command that
// reported nothing, a report that carries no capabilities and a report that was refused leave them as they are. The
// result is the caller's own copy.
func (r *ProcRig) Effective() exec.Capabilities {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gate.Effective()
}

// rigWriter appends to one of the rig's buffers and wakes the waiters. Its first byte, on either stream, is the first
// output of the command: it goes through the start gate before it is recorded, so that the Started the host would
// receive is recorded first.
type rigWriter struct {
	r      *ProcRig
	buffer *bytes.Buffer
}

// Write fails with io.ErrClosedPipe once the command has ended, as the SDK's Proc.Stdout does, and then records
// nothing. A write of no bytes is not output.
func (w rigWriter) Write(p []byte) (int, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	if w.r.closed {
		return 0, io.ErrClosedPipe
	}
	if len(p) > 0 {
		w.r.gate.Output(w.r.record)
	}
	w.r.cond.Broadcast()
	return w.buffer.Write(p)
}

// Proc returns the Proc to hand to an ExecFunc.
func (r *ProcRig) Proc() *exec.Proc { return r.proc }

// Input returns the command's stdin as the test writes it; closing it is the command's stdin EOF.
func (r *ProcRig) Input() io.WriteCloser { return r.stdin }

// Resize sends a terminal size; the newest pending size replaces an older one the command has not taken, as the
// SDK's Proc.Resize does, and the call never blocks. After the command has ended it does nothing.
func (r *ProcRig) Resize(size exec.Size) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	select {
	case <-r.resize:
	default:
	}
	r.resize <- size
}

// Stdout returns a copy of what the command wrote to its standard output (or terminal) so far.
func (r *ProcRig) Stdout() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return bytes.Clone(r.stdout.Bytes())
}

// Stderr returns a copy of what the command wrote to its standard error so far.
func (r *ProcRig) Stderr() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return bytes.Clone(r.stderr.Bytes())
}

// Started returns the Started that the host would receive, at most one, recorded before the first output: the facts the
// command reported with Proc.Started, with the capabilities of the session in them (narrowed as the SDK narrows them), or,
// when the command wrote output without having reported, the Started the SDK makes from the facts of Opened (the requested
// program and the opened capabilities). A command that reported nothing and wrote nothing has none. The result is the
// caller's own deep copy: nothing in it is shared with the rig's record or with the next call.
func (r *ProcRig) Started() []exec.SessionFacts {
	r.mu.Lock()
	defer r.mu.Unlock()
	facts := make([]exec.SessionFacts, len(r.started))
	for i, s := range r.started {
		facts[i] = cloneFacts(s)
	}
	return facts
}

// Links returns the link states the command reported, in order.
func (r *ProcRig) Links() []exec.LinkState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]exec.LinkState(nil), r.links...)
}

// WaitStdout blocks until the command has written at least n bytes to its standard output, and returns what it has
// written. It returns the context's error if ctx ends first.
func (r *ProcRig) WaitStdout(ctx context.Context, n int) ([]byte, error) {
	stop := context.AfterFunc(ctx, func() {
		r.mu.Lock()
		r.cond.Broadcast()
		r.mu.Unlock()
	})
	defer stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.stdout.Len() < n {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.cond.Wait()
	}
	return bytes.Clone(r.stdout.Bytes()), nil
}

// Run calls fn with the rig's Proc and returns what it returns. It closes Proc.Resize and ends Proc.Stdin (a read
// returns io.EOF once what the test wrote is read) afterwards, as the SDK does at the end of a command, and turns a
// panic of fn into an error instead of crashing the test binary. The input's final disposal stays with the test's
// cleanup.
func (r *ProcRig) Run(ctx context.Context, fn exec.ExecFunc) (exit exec.Exit, err error) {
	defer func() {
		r.mu.Lock()
		r.closed = true
		close(r.resize)
		r.mu.Unlock()
		_ = r.stdin.Close()
		if recovered := recover(); recovered != nil {
			exit, err = exec.Exit{}, fmt.Errorf("exec function panicked: %v", recovered)
		}
	}()
	return fn(ctx, r.proc)
}
