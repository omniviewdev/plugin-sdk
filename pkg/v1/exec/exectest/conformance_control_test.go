package exectest

import (
	"sync/atomic"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// The cases of the control frames (resize, signals and Close, stdin EOF) drive a plugin over a transport, so the tests of the suite
// hold them to what they claim: each body passes on every shape of declaration an author may write, against the SDK's own server,
// and none calls the author's Exec. That each body fails when the server is wrong is shown against the server itself: the mutants
// of the SDK's control code (the receiver's frames, the acknowledgements, the signal queue) each fail one of these runs.

// controlBodies are the wire bodies of the control frames, by what the tests call them.
func controlBodies() []struct {
	name string
	body func(t checkT, target exec.Target, env *wireEnv)
} {
	return []struct {
		name string
		body func(t checkT, target exec.Target, env *wireEnv)
	}{
		{"case 6", caseWireResize},
		{"case 7", caseWireSignals},
		{"case 8", caseWireStdinEOF},
	}
}

// controlShapes are the shapes of the suite and the ones whose control frames differ: a terminal that cannot be resized, a target that
// cannot half-close its input, one that declares every signal and a host with no terminal that can half-close.
func controlShapes() []shape {
	base := func() exec.TargetInfo { return NewFakeTarget().Info }
	return append(shapes(),
		shape{"a terminal that cannot be resized", func() exec.TargetInfo {
			info := base()
			info.Capabilities.Resize = false
			return info
		}},
		shape{"a target that cannot half-close its input", func() exec.TargetInfo {
			info := base()
			info.Capabilities.StdinEOF = false
			return info
		}},
		shape{"a target that declares every signal", func() exec.TargetInfo {
			info := base()
			info.Capabilities.Signals = []exec.Signal{
				exec.SignalInt, exec.SignalQuit, exec.SignalTerm, exec.SignalKill, exec.SignalHup, exec.SignalUsr1, exec.SignalUsr2,
			}
			return info
		}},
		shape{"a host with no terminal that can half-close its input", func() exec.TargetInfo {
			info := base()
			info.Capabilities.TTY, info.Capabilities.Resize, info.Capabilities.InitialSize = false, false, false
			info.Capabilities.SeparateStderr = true
			return info
		}},
	)
}

func TestEveryControlBodyPassesOnEveryShapeAnAuthorDeclaresAndNeverCallsTheirExec(t *testing.T) {
	var calls atomic.Int64
	for _, s := range controlShapes() {
		info := s.info()
		for _, b := range controlBodies() {
			t.Run(s.name+"/"+b.name, func(t *testing.T) {
				rec := &recordT{}
				rec.run(func(r checkT) { b.body(r, declared(t, s, &calls), &wireEnv{resource: "thing-1"}) })
				if info.Session.IsZero() {
					if len(rec.skips) != 1 || len(rec.failed()) != 0 {
						t.Errorf("a target that serves no sessions: %d skips and failures %q, want one skip and no failure", len(rec.skips), rec.failed())
					}
					return
				}
				if len(rec.skips) != 0 {
					t.Errorf("the body skipped: %q: a target that serves sessions has something to drive", rec.skips)
				}
				if failures := rec.failed(); len(failures) != 0 {
					t.Errorf("the real plugin fails the body for a target declared as %s: %q", s.name, failures)
				}
			})
		}
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("the author's Exec was called %d times: the cases run a fake built from what the author declared", got)
	}
}

// The rows of the control frames are complete: their bodies are all of them, and the build lists what they drive.
func TestTheRowsOfTheControlFramesAreWrittenAndDriveTheFeaturesTheyNeed(t *testing.T) {
	for _, n := range []int{6, 7, 8} {
		c := caseNumbered(t, n)
		if c.step != "" || c.wire == nil {
			t.Errorf("case %d: step %q and a body %v: the row is written", n, c.step, c.wire != nil)
		}
	}
	if features := caseNumbered(t, 8).wireFeatures(); len(features) != 2 {
		t.Errorf("case 8 drives %v: it needs FEATURE_SESSION and FEATURE_STDIN_EOF", features)
	}
}
