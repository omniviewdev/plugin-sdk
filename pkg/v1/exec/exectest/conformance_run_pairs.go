package exectest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The pair cases: the same command, run two ways, must come out the same. The oracle of each is the other way: what one of them
// observed (the end, the bytes of each stream, whether the start was observed) is what the other must say, and not what a script
// says it should have been.

// pairArgv is the argv of the commands that the pair cases run.
var pairArgv = []string{"exectest", "pair"}

// ---------------------------------------------------------------------------------------------------------------
// pair P1: Run(argv) equals Open(argv, tty=false) collected to the end
// ---------------------------------------------------------------------------------------------------------------

// openedRun is a session without a terminal, collected to its end.
type openedRun struct {
	stdout, stderr []byte
	ended          *execv2.Ended
	problems       []string
}

// collectSession reads a session to its Ended and the end of the stream, and keeps what it wrote on each stream.
func collectSession(s *wireSession) openedRun {
	var got openedRun
	for got.ended == nil {
		frame := s.next("the end of the session")
		switch f := frame.GetFrame().(type) {
		case *execv2.ServerFrame_Output:
			if f.Output.GetStream() == execv2.Stream_STREAM_STDERR {
				got.stderr = append(got.stderr, f.Output.GetData()...)
			} else {
				got.stdout = append(got.stdout, f.Output.GetData()...)
			}
		case *execv2.ServerFrame_Dropped:
			got.problems = append(got.problems, "a Dropped range: nothing is lost to a host that reads")
		case *execv2.ServerFrame_Ended:
			got.ended = f.Ended
		}
	}
	if after, err := s.recv(); !errors.Is(err, io.EOF) {
		got.problems = append(got.problems, fmt.Sprintf("after Ended came %s and %v, want io.EOF", frameName(after), err))
	}
	got.problems = append(got.problems, s.grammar.problems...)
	return got
}

// casePairRunOpen: Run(argv) equals Open(argv, tty=false) collected to the end. For each way a command ends by itself, the result of
// a run says what the session said: the same bytes on each stream, the same exit or signal or failure or loss, the same survival,
// whether the start was observed, and the same completeness of the output. The result is held to what the session observed, not to
// what the script was told to do.
func casePairRunOpen(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	switch {
	case info.Run.IsZero():
		t.Skipf(noRuns)
	case info.Session.IsZero():
		t.Skipf(noSessions)
	}
	rig := newRunRig(t, env, target, nil)
	if !rig.lists(execv2.Feature_FEATURE_SESSION) {
		t.Skipf("the SDK build does not list FEATURE_SESSION: it does not serve Open")
	}
	defaultCap := int(rig.limits.GetDefaultOutputBytes())
	for _, ns := range naturalScripts(rig.info.Capabilities) {
		name := rig.newID("pair")
		script := rig.register(name, ns.script)
		call := rig.run(rig.runRequest("", name, func(req *execv2.RunRequest) { req.Argv = pairArgv }))
		session := rig.mustOpen(rig.newID("pair"), name, func(st *execv2.Start) {
			st.Tty = false
			st.Argv = pairArgv
		})
		opened := collectSession(session)
		for _, problem := range opened.problems {
			t.Errorf("%s: the session: %s", ns.name, problem)
		}
		want := wantRun{
			end: opened.ended.GetEnd(), started: opened.ended.GetStartObserved(), reported: script.report,
			out: streamWant{written: opened.stdout, cap: defaultCap}, err: streamWant{written: opened.stderr, cap: defaultCap},
		}
		rig.expect(ns.name, call, want)
		if call.res == nil {
			continue
		}
		// What Ended counts is what the result's accounts count, and a command that lost nothing says the same of its output.
		total := call.res.GetStdoutAccount().GetTotalBytes() + call.res.GetStderrAccount().GetTotalBytes()
		if opened.ended.GetBytesOut() != total {
			t.Errorf("%s: the session wrote %d bytes and the accounts of the run say %d", ns.name, opened.ended.GetBytesOut(), total)
		}
		if opened.ended.GetDroppedBytes() == 0 && call.res.GetStdoutAccount().GetDroppedBytes() == 0 && call.res.GetStderrAccount().GetDroppedBytes() == 0 {
			for stream, account := range map[string]*execv2.OutputAccount{"standard output": call.res.GetStdoutAccount(), "standard error": call.res.GetStderrAccount()} {
				if account.GetCompleteness() != opened.ended.GetOutput() {
					t.Errorf("%s: the output of %s is %s in the result of the run and %s in the Ended of the session", ns.name, stream, account.GetCompleteness(), opened.ended.GetOutput())
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// pair P3: the engine over gRPC equals the engine in process
// ---------------------------------------------------------------------------------------------------------------

// runStep is the step of a fake target that runs a script: the function of the script, which is the whole of its target.
func runStep(s runScript) Step {
	fn := s.fn()
	return stepFunc(func(ctx context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		exit, err := fn(ctx, p)
		return true, exit, err
	})
}

// inProcessRun is a command of the engine, run in this process, collected to its end.
type inProcessRun struct {
	stdout, stderr []byte
	result         exechook.Result
}

// startRun starts a run for a request in this process: the request is the one that goes over the wire, with the target of this
// rig's connection.
func (r *caseRig) startRun(req *execv2.RunRequest) exechook.Command {
	r.t.Helper()
	target, err := wireTarget(r.conns, r.info.Type, r.resource)
	if err != nil {
		r.t.Fatalf("%v", err)
	}
	req = proto.Clone(req).(*execv2.RunRequest)
	req.Target = target
	cmd, err := r.eng.Run(r.conns.Context(context.Background()), req)
	if err != nil {
		r.t.Fatalf("run in process: %v", err)
	}
	return cmd
}

// collectRun reads the output of a command of the engine to its end, by stream, and takes its result.
func (r *caseRig) collectRun(cmd exechook.Command) inProcessRun {
	r.t.Helper()
	var got inProcessRun
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	buf := make([]byte, 1024)
	for {
		chunk, err := cmd.Read(ctx, buf)
		if len(chunk.Dropped) != 0 {
			r.t.Errorf("command %q: output was dropped (%v) from a command that wrote a few bytes", cmd.ID(), chunk.Dropped)
		}
		if chunk.Stream == execv2.Stream_STREAM_STDERR {
			got.stderr = append(got.stderr, buf[:chunk.N]...)
		} else {
			got.stdout = append(got.stdout, buf[:chunk.N]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			r.t.Fatalf("command %q: reading its output: %v", cmd.ID(), err)
		}
	}
	got.result = r.await(cmd)
	return got
}

// casePairEngineWire: the engine over gRPC equals the engine in process. The same script runs under the engine in this process and
// behind the served plugin, and the result that the host gets says what the engine said: its end becomes the outcome of the result (an
// exit, a signal, a failure with its class, reason, facts and stage, a loss), its bytes are what the stream carried, whether the start
// was observed, the survival, and the account of the output. The ends the plugin makes itself are the same too, a shutdown and a
// retired connection. The result is held to what the engine observed, not to what the script was told to do.
func casePairEngineWire(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Run.IsZero() {
		t.Skipf(noRuns)
	}
	inEnv := &inProcessEnv{resource: env.resource}
	rig := newRunRig(t, env, target, nil)
	defaultCap := int(rig.limits.GetDefaultOutputBytes())
	type scenario struct {
		name   string
		script runScript
		mod    func(*execv2.RunRequest)
	}
	var scenarios []scenario
	for _, ns := range naturalScripts(rig.info.Capabilities) {
		scenarios = append(scenarios, scenario{name: ns.name, script: ns.script})
	}
	scenarios = append(scenarios, scenario{
		name:   "output beyond the caps",
		script: runScript{writes: []runWrite{{n: 100, chunk: 7}, {stderr: true, n: 30, chunk: 3}}, exit: exec.ExitCode(0)},
		mod: func(req *execv2.RunRequest) {
			req.Output = &execv2.OutputLimits{MaxStdoutBytes: 11, MaxStderrBytes: 4, Policy: execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL}
		},
	})
	for _, sc := range scenarios {
		name := rig.newID("pair")
		script := rig.register(name, sc.script)
		var mods []func(*execv2.RunRequest)
		mods = append(mods, func(req *execv2.RunRequest) { req.Argv = pairArgv })
		if sc.mod != nil {
			mods = append(mods, sc.mod)
		}
		req := rig.runRequest("", name, mods...)

		inRig := newCaseRig(t, inEnv, target, runStep(script))
		observed := inRig.collectRun(inRig.startRun(req))
		rig.checkAgainstEngine(sc.name, req, script, observed, defaultCap)
	}
	for _, sc := range []struct {
		name  string
		cause func(rig *runRig, in *caseRig)
	}{
		{"the connection is retired", func(rig *runRig, in *caseRig) {
			in.conns.Retire()
			rig.conns.Retire()
		}},
		{"the plugin shuts down", func(rig *runRig, in *caseRig) {
			ctx, cancel := context.WithTimeout(context.Background(), caseBound)
			defer cancel()
			if err := in.eng.Close(ctx); err != nil {
				t.Errorf("close the engine in process: %v", err)
			}
			if err := rig.server.plugin.Shutdown(ctx); err != nil {
				t.Errorf("shut the plugin down: %v", err)
			}
		}},
	} {
		rig := newRunRig(t, env, target, nil)
		up := make(chan struct{}, 2)
		script := rig.register("held", runScript{writes: []runWrite{{n: 5}, {stderr: true, n: 2}}, block: true, written: func() { up <- struct{}{} }})
		req := rig.runRequest("", "held", func(req *execv2.RunRequest) { req.Argv = pairArgv })
		inRig := newCaseRig(t, inEnv, target, runStep(script))
		cmd := inRig.startRun(req)
		wire := make(chan *runCall, 1)
		go func() { wire <- rig.run(req) }()
		// Both functions have written all they write, and wait: the engine in process and the plugin each hold one.
		for range 2 {
			select {
			case <-up:
			case <-time.After(caseBound):
				t.Fatalf("%s: a function did not write within %v", sc.name, caseBound)
			}
		}
		sc.cause(rig, inRig)
		observed := inRig.collectRun(cmd)
		select {
		case call := <-wire:
			want := wantRunOf(observed, script, defaultCap)
			rig.expect(sc.name, call, want)
			rig.compareWithEngine(sc.name, call, observed)
		case <-time.After(caseBound):
			t.Errorf("%s: the run over the wire did not end within %v", sc.name, caseBound)
		}
	}
}

// wantRunOf is what a run must say of a command that the engine in process ended as observed says.
func wantRunOf(observed inProcessRun, s runScript, cap int) wantRun {
	return wantRun{
		end: observed.result.End, started: observed.result.StartObserved, reported: s.report,
		out: streamWant{written: observed.stdout, cap: cap}, err: streamWant{written: observed.stderr, cap: cap},
	}
}

// checkAgainstEngine runs the request over the wire, with Run and with RunStream when the build lists it, and holds each to what the
// engine in process observed.
func (r *runRig) checkAgainstEngine(name string, req *execv2.RunRequest, s runScript, observed inProcessRun, defaultCap int) {
	r.t.Helper()
	want := wantRunOf(observed, s, defaultCap)
	if limits := req.GetOutput(); limits != nil {
		want.out.cap, want.out.policy = int(limits.GetMaxStdoutBytes()), limits.GetPolicy()
		want.err.cap, want.err.policy = int(limits.GetMaxStderrBytes()), limits.GetPolicy()
	}
	call := r.run(req)
	r.expect(name, call, want)
	r.compareWithEngine(name, call, observed)
	if r.lists(execv2.Feature_FEATURE_RUN_STREAM) {
		stream := proto.Clone(req).(*execv2.RunRequest)
		r.expectStream(name, r.callStream(stream, &s), want)
	}
}

// compareWithEngine holds what a result counts to what the engine counted: the bytes it wrote, and that nothing was lost to the
// reader of the engine.
func (r *runRig) compareWithEngine(name string, call *runCall, observed inProcessRun) {
	r.t.Helper()
	if call.res == nil {
		return
	}
	total := call.res.GetStdoutAccount().GetTotalBytes() + call.res.GetStderrAccount().GetTotalBytes()
	if total != observed.result.BytesOut {
		r.t.Errorf("%s: the accounts of the run total %d bytes and the engine in process counted %d", name, total, observed.result.BytesOut)
	}
	if observed.result.DroppedBytes != 0 {
		r.t.Errorf("%s: the engine in process dropped %d bytes from a command that wrote a few", name, observed.result.DroppedBytes)
	}
}
