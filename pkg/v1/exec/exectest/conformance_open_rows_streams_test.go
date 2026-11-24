package exectest

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The halves of cases 9 and 25 that need a session that outlives its stream (the tombstone, and the streams that leave) are held, as the
// other bodies of the interactive session are, by the table of faults of conformance_open_rows_faulty_test.go and by the real plugin; the
// tests here hold what those cannot see: the grammar of one stream, what the scenarios of case 9 cover, the lease window the host holds
// the plugin to, and the body of case 5, which the end of a session with its stream used to hold.

// server frames, by kind, for the tests of the grammar.
var (
	frameOutput   = &execv2.ServerFrame{Frame: &execv2.ServerFrame_Output{Output: &execv2.Output{Data: []byte("x")}}}
	frameStarted  = &execv2.ServerFrame{Frame: &execv2.ServerFrame_Started{Started: &execv2.Started{}}}
	frameNotice   = &execv2.ServerFrame{Frame: &execv2.ServerFrame_Notice{Notice: &execv2.Notice{}}}
	frameEnded    = &execv2.ServerFrame{Frame: &execv2.ServerFrame_Ended{Ended: &execv2.Ended{}}}
	frameDetached = &execv2.ServerFrame{Frame: &execv2.ServerFrame_Detached{Detached: &execv2.Detached{Reason: execv2.DetachReason_DETACH_REASON_REQUESTED}}}
	frameOpened   = &execv2.ServerFrame{Frame: &execv2.ServerFrame_Opened{Opened: &execv2.Opened{}}}
)

// A stream ends with exactly one of Ended or Detached, and nothing follows it. The grammar says what is wrong with the frames of a stream
// that does not: a last frame of either kind is no problem, and every frame after it, of whatever kind, and a second last frame, are.
func TestTheStreamGrammarAcceptsEndedOrDetachedAsTheLastFrameAndHoldsNothingAfterIt(t *testing.T) {
	for _, tc := range []struct {
		name              string
		frames            []*execv2.ServerFrame
		endeds, detacheds int
		problems          []string // what each problem says, in order; none for a stream that is as the contract says
	}{
		{"a stream that ends with the end of its session", []*execv2.ServerFrame{frameStarted, frameOutput, frameEnded}, 1, 0, nil},
		{"a stream that ends with Detached", []*execv2.ServerFrame{frameStarted, frameOutput, frameDetached}, 0, 1, nil},
		{"a stream that left before it had anything", []*execv2.ServerFrame{frameNotice, frameDetached}, 0, 1, nil},
		{"output after Ended", []*execv2.ServerFrame{frameOutput, frameEnded, frameOutput}, 1, 0, []string{"an Output frame came after Ended or Detached"}},
		{"output after Detached", []*execv2.ServerFrame{frameOutput, frameDetached, frameOutput}, 0, 1, []string{"an Output frame came after Ended or Detached"}},
		{"Ended after Detached", []*execv2.ServerFrame{frameDetached, frameEnded}, 1, 1, []string{"an Ended frame came after Ended or Detached"}},
		{"Detached after Ended", []*execv2.ServerFrame{frameEnded, frameDetached}, 1, 1, []string{"a Detached frame came after Ended or Detached"}},
		{"Detached twice", []*execv2.ServerFrame{frameDetached, frameDetached}, 0, 2, []string{"a Detached frame came after Ended or Detached"}},
		{"Ended twice", []*execv2.ServerFrame{frameEnded, frameEnded}, 2, 0, []string{"an Ended frame came after Ended or Detached"}},
		{"a Started after the first output", []*execv2.ServerFrame{frameOutput, frameStarted, frameDetached}, 0, 1, []string{"a Started after the first output"}},
		{"a second Started", []*execv2.ServerFrame{frameStarted, frameStarted, frameEnded}, 1, 0, []string{"a second Started"}},
		{"a Notice after the first output", []*execv2.ServerFrame{frameOutput, frameNotice, frameEnded}, 1, 0, []string{"a Notice after the first output"}},
		{"a second Opened", []*execv2.ServerFrame{frameOpened, frameEnded}, 1, 0, []string{"a second Opened"}},
		{"a stream with no last frame", []*execv2.ServerFrame{frameOutput}, 0, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var g streamGrammar
			for _, f := range tc.frames {
				g.take(f)
			}
			if g.endeds != tc.endeds || g.detacheds != tc.detacheds {
				t.Errorf("the grammar counted %d Ended and %d Detached frames, want %d and %d", g.endeds, g.detacheds, tc.endeds, tc.detacheds)
			}
			if wantOver := tc.endeds+tc.detacheds > 0; g.over() != wantOver {
				t.Errorf("the grammar says the last frame has come: %v, want %v", g.over(), wantOver)
			}
			if len(g.problems) != len(tc.problems) {
				t.Fatalf("the grammar found %q, want %d problems saying %q", g.problems, len(tc.problems), tc.problems)
			}
			for i, want := range tc.problems {
				if !strings.Contains(g.problems[i], want) {
					t.Errorf("problem %d is %q, want it to say %q", i, g.problems[i], want)
				}
			}
		})
	}
}

// Case 9 runs a session to every kind of end the contract names, with the stream attached and with the host gone: a status of the process,
// a code that only the transport relayed (whose start is observed and whose survival is not known), a signal where the kind reports one, a
// stream that ended with no status, a link that died, a failure, a close by the host, a retired connection, the plugin's shutdown and,
// for a session with no stream, the lease that ran out, once for the Kill of the kind and once for each narrower Kill a session of it may
// report; and a session that its host left for a lease that it asked for, which it reads and ends. A kind of end that is not in the table
// is one that no host reads from the tombstone.
func TestCaseNineEndsASessionEveryWayItCanEndWithNoStreamAttached(t *testing.T) {
	for _, s := range openShapes() {
		info := s.info()
		if info.Session.IsZero() {
			continue
		}
		caps := info.Capabilities
		t.Run(s.name, func(t *testing.T) {
			seen := map[string]endScenario{}
			var leases []*execv2.End
			asked := 0
			for _, sc := range endScenarios(caps) {
				if sc.detached == nil {
					if sc.detachedOnly {
						t.Errorf("%s runs only with the host gone and has no such form", sc.name)
					}
					continue
				}
				arm, _ := endArm(sc.end)
				key := arm
				if by := sc.end.GetClosed().GetBy(); by != execv2.ClosedBy_CLOSED_BY_UNSPECIFIED {
					key += " by " + by.String()
				}
				if _, again := seen[key]; !again {
					seen[key] = sc
				}
				switch lapses := sc.detached.lease == leaseRunsOut; {
				case sc.end.GetClosed().GetBy() == execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT:
					if !sc.detachedOnly || !lapses || !sc.detached.runs {
						t.Errorf("%s ends by its lease and has to run only with the host gone, with a lease that runs out, for a function that runs: only a lease ends it, and nothing else may", sc.name)
					}
					leases = append(leases, sc.end)
				case lapses:
					t.Errorf("%s leaves its session with a lease that runs out and ends it another way: the lease is the end of that session", sc.name)
				case sc.detachedOnly && sc.detached.lease == leaseAsked:
					asked++
					if !sc.detached.runs || sc.detached.end == nil {
						t.Errorf("%s leaves its session for a lease that the host asked for, which outlives the case: the function has to run, and the host has to end the session", sc.name)
					}
				case sc.detachedOnly:
					t.Errorf("%s runs only with the host gone, and its lease is neither the one that runs out nor one that the host asked for", sc.name)
				}
			}
			if asked != 1 {
				t.Errorf("%d scenarios leave a session for a lease that the host asked for, want one: the lease of a Detach and of a stream that broke is read for a lease that outlives the case", asked)
			}
			wantKinds := []string{
				"exited", "failed", "lost", "closed by CLOSED_BY_REQUEST", "closed by CLOSED_BY_DETACH_TIMEOUT",
				"closed by CLOSED_BY_PLUGIN_SHUTDOWN", "closed by CLOSED_BY_CONNECTION_RETIRED",
			}
			if reportsSignals(caps) {
				wantKinds = append(wantKinds, "signaled")
			}
			for _, want := range wantKinds {
				if _, ok := seen[want]; !ok {
					t.Errorf("no scenario ends a session as %q with the host gone: its tombstone is never read", want)
				}
			}
			// The lease that ran out is run for the Kill of the kind (once plainly, and once for a session that reports it in Started) and
			// for each narrower one that a session of the kind may report.
			var wantLeases []*execv2.End
			for _, k := range movesKill {
				if slices.Index(movesKill, k) <= slices.Index(movesKill, caps.Kill) {
					wantLeases = append(wantLeases, endClosed(execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT, k))
				}
			}
			if len(leases) != len(wantLeases)+1 {
				t.Errorf("the lease runs out in %d scenarios, want the plain one and one for each of the %d Kills the kind allows", len(leases), len(wantLeases))
			}
			for _, want := range wantLeases {
				if !slices.ContainsFunc(leases, func(end *execv2.End) bool { return proto.Equal(end, want) }) {
					t.Errorf("no scenario runs a lease out on a session whose Closed end is %s", endText(want))
				}
			}
			// A code that only the transport relayed ends Exited{TRANSPORT}, with its start observed and its survival not known: a host that read
			// it from the tombstone as unstarted, or as gone, would read it wrongly.
			transport, ok := scenarioNamed(endScenarios(caps), "a code only the transport relayed")
			if !ok {
				t.Fatalf("no scenario ends with a code that only the transport relayed")
			}
			exited := transport.end.GetExited()
			if exited.GetSource() != execv2.ExitSource_EXIT_SOURCE_TRANSPORT || exited.GetExitCode() != 0 || transport.end.RemoteMaySurvive != nil || !transport.started || transport.detached == nil {
				t.Errorf("the transport's end is %s, started %v, want Exited{0, TRANSPORT} with the survival absent and its start observed, run with the host gone as well", endText(transport.end), transport.started)
			}
		})
	}
}

// The lease that a host asks for, and reads while the session lives with no stream, outlives a case by far: no pace of the machine runs it
// out, so what the host reads of it is the same for every pace. The shortest lease is the one that the pace decides.
func TestTheLeaseThatTheHostAsksForAndReadsOutlivesACaseByFar(t *testing.T) {
	if got := time.Duration(askedLeaseMs) * time.Millisecond; got < 5*caseBound {
		t.Errorf("the lease the host asks for and reads is %v, want one that outlives the bound of a case, %v, by far", got, caseBound)
	}
}

func scenarioNamed(scenarios []endScenario, name string) (endScenario, bool) {
	for _, sc := range scenarios {
		if sc.name == name {
			return sc, true
		}
	}
	return endScenario{}, false
}

// A lease that the plugin says ends at some moment is held to the window the host's own clock gives it: applied between the host's two
// readings of the clock, it ends that many milliseconds after one of them and before that many after the other, and a lease that ends a
// millisecond outside of the window is wrong. The plugin and the host share the clock, so the host holds the plugin to what it asked for.
// The window is arithmetic on the host's two readings and holds at any pace, so the host reads it of the lease a Detach is answered with
// whatever its length; what a listing says of a lease depends on the pace, and the host reads it only of a lease that outlives the case.
func TestTheLeaseWindowIsTheMillisecondsAskedForAfterTheHostsReadingsOfItsClock(t *testing.T) {
	before := time.UnixMilli(1_000_000)
	after := time.UnixMilli(1_000_040)
	for _, tc := range []struct {
		name    string
		expires int64
		ms      uint32
		want    bool
	}{
		{"applied at the first reading", 1_000_000 + 300, 300, true},
		{"applied at the second reading", 1_000_040 + 300, 300, true},
		{"applied between them", 1_000_020 + 300, 300, true},
		{"one millisecond before the window", 1_000_000 + 300 - 1, 300, false},
		{"one millisecond after the window", 1_000_040 + 300 + 1, 300, false},
		{"a lease that was not applied", 0, 300, false},
		{"an hour too long", 1_000_020 + 300 + 3_600_000, 300, false},
		{"a lease of the millisecond that was asked for", 1_000_020 + 1, 1, true},
		{"a lease of a minute that the host asked for", 1_000_020 + 60_000, 60_000, true},
		{"a lease of a minute that was applied as the default of five", 1_000_020 + 300_000, 60_000, false},
		{"a lease of a minute that was applied as a millisecond", 1_000_020 + 1, 60_000, false},
	} {
		if got := leaseWindow(tc.expires, before, after, tc.ms); got != tc.want {
			t.Errorf("%s: a lease of %d ms that ends at %d is in the window: %v, want %v", tc.name, tc.ms, tc.expires, got, tc.want)
		}
	}
}

// A host that stops reading ends its session with a Close frame, and the function that the flood holds in its Write returns while the
// host still reads nothing. A stream that is only closed leaves the session running for its lease (an implicit detach), so the body that
// used to close the stream and wait for the function to return passed for the wrong reason: the flood ran out into the ring. The body of
// case 5 holds a target that serves two sessions to this, and one that serves one is skipped: it has no other session to move.
func TestCaseFiveClosesTheSessionOfTheHostThatStoppedReadingAndSeesItsFunctionReturn(t *testing.T) {
	shapesOf := openShapes()
	var calls atomic.Int64
	for i, s := range shapesOf {
		info := s.info()
		if info.Session.IsZero() {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			rec := &recordT{}
			rec.run(func(r checkT) { caseWireIsolation(r, declared(t, shapesOf[i], &calls), &wireEnv{resource: "thing-1"}) })
			switch {
			case effectiveLimit(info) < 2:
				if len(rec.skips) != 1 || len(rec.failed()) != 0 {
					t.Errorf("a target that serves one session at a time: %d skips and failures %q, want one skip and no failure", len(rec.skips), rec.failed())
				}
			case len(rec.skips) != 0 || len(rec.failed()) != 0:
				t.Errorf("the real plugin fails the case 5 for a target declared as %s: %q (skips %q)", s.name, rec.failed(), rec.skips)
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("the author's Exec was called %d times: the case runs a fake built from what the author declared", got)
	}
}
