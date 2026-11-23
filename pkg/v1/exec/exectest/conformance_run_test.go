package exectest

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The cases of the bounded run drive a plugin over a transport, so the tests of the suite hold them to two things: each body passes on
// every shape of declaration that an author may write, and fails when the plugin is wrong. The wrongness is a faulty plugin, the real
// one served as it is with one fault added at the edge where the host reads it (a result that says something else, an event that comes
// in the wrong order, a status that carries what must stay local), given to the body through the serve seam of the wire environment.

// runBodies are the wire bodies of the bounded run, by what the tests call them.
func runBodies() []struct {
	name string
	body func(t checkT, target exec.Target, env *wireEnv)
} {
	return []struct {
		name string
		body func(t checkT, target exec.Target, env *wireEnv)
	}{
		{"case 13", caseWireErrorsRun},
		{"case 15", caseWireRun},
		{"case 16", caseWireLostRun},
		{"case 27", caseWireRunFailure},
		{"pair P1", casePairRunOpen},
		{"pair P3", casePairEngineWire},
	}
}

func runBody(t *testing.T, name string) func(checkT, exec.Target, *wireEnv) {
	t.Helper()
	for _, b := range runBodies() {
		if b.name == name {
			return b.body
		}
	}
	t.Fatalf("no run body %q", name)
	return nil
}

func TestEveryRunBodyPassesOnEveryShapeAnAuthorDeclaresAndNeverCallsTheirExec(t *testing.T) {
	var calls atomic.Int64
	for _, s := range shapes() {
		info := s.info()
		for _, b := range runBodies() {
			t.Run(s.name+"/"+b.name, func(t *testing.T) {
				rec := &recordT{}
				rec.run(func(r checkT) { b.body(r, declared(t, s, &calls), &wireEnv{resource: "thing-1"}) })
				servesRuns := !info.Run.IsZero()
				needsSessions := b.name == "pair P1"
				switch {
				case !servesRuns || needsSessions && info.Session.IsZero():
					if len(rec.skips) != 1 || len(rec.failed()) != 0 {
						t.Errorf("a target that serves %s: %d skips and failures %q, want one skip and no failure", servedText(info), len(rec.skips), rec.failed())
					}
				default:
					if len(rec.skips) != 0 {
						t.Errorf("the body skipped: %q: a target that declares runs has something to run", rec.skips)
					}
					if failures := rec.failed(); len(failures) != 0 {
						t.Errorf("the real plugin fails the body for a target declared as %s: %q", s.name, failures)
					}
				}
			})
		}
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("the author's Exec was called %d times: the cases run a fake built from what the author declared", got)
	}
}

func servedText(info exec.TargetInfo) string {
	switch {
	case info.Run.IsZero() && info.Session.IsZero():
		return "serves neither sessions nor runs"
	case info.Run.IsZero():
		return "serves no runs"
	}
	return "serves runs but not sessions"
}

// faultyRunClient is the client of a plugin whose results and events say something else than the plugin did.
type faultyRunClient struct {
	exec.Client
	// result changes the result of every run, whatever the request.
	result func(res *execv2.RunResult, req *execv2.RunRequest)
	// events changes the events of a stream, in order, and returns the ones the host gets: it may add, drop and reorder them.
	events func(events []*execv2.RunEvent, req *execv2.RunRequest) []*execv2.RunEvent
}

func (c faultyRunClient) Run(ctx context.Context, req *execv2.RunRequest) (*execv2.RunResult, error) {
	res, err := c.Client.Run(ctx, req)
	if res != nil && c.result != nil {
		res = proto.Clone(res).(*execv2.RunResult)
		c.result(res, req)
	}
	return res, err
}

func (c faultyRunClient) RunStream(ctx context.Context, req *execv2.RunRequest) (exec.RunConn, error) {
	conn, err := c.Client.RunStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &faultyRunConn{RunConn: conn, c: c, req: req}, nil
}

// faultyRunConn reads the stream to its end when it is first read, lets the fault change the events, and hands them out in order.
type faultyRunConn struct {
	exec.RunConn
	c       faultyRunClient
	req     *execv2.RunRequest
	loaded  bool
	pending []*execv2.RunEvent
	err     error
}

func (c *faultyRunConn) Recv() (*execv2.RunEvent, error) {
	if !c.loaded {
		c.loaded = true
		var events []*execv2.RunEvent
		for {
			event, err := c.RunConn.Recv()
			if err != nil {
				c.err = err
				break
			}
			if ended := event.GetEnded(); ended != nil && c.c.result != nil {
				event = proto.Clone(event).(*execv2.RunEvent)
				c.c.result(event.GetEnded().GetResult(), c.req)
			}
			events = append(events, event)
		}
		if c.c.events != nil {
			events = c.c.events(events, c.req)
		}
		c.pending = events
	}
	if len(c.pending) > 0 {
		event := c.pending[0]
		c.pending = c.pending[1:]
		return event, nil
	}
	return nil, c.err
}

// faultyRaw is the generated client of a plugin whose statuses carry the canary.
type faultyRaw struct {
	execv2.ExecClient
}

func (faultyRaw) Run(ctx context.Context, req *execv2.RunRequest, opts ...grpc.CallOption) (*execv2.RunResult, error) {
	return nil, statusWithCanary()
}

func (faultyRaw) RunStream(context.Context, *execv2.RunRequest, ...grpc.CallOption) (execv2.Exec_RunStreamClient, error) {
	return nil, statusWithCanary()
}

// serveFaulty serves the real plugin and has the host read it through a client with the fault.
func serveFaulty(c faultyRunClient, raw bool) serveFunc {
	return func(t checkT, targets []exec.Target, conns *exectestkit.Connections) *wireServer {
		server := serveReal(t, targets, conns)
		c.Client = server.client
		server.client = c
		if raw {
			server.raw = faultyRaw{ExecClient: server.raw}
		}
		return server
	}
}

// exited changes the results of the runs that exited.
func exited(change func(res *execv2.RunResult)) func(*execv2.RunResult, *execv2.RunRequest) {
	return func(res *execv2.RunResult, _ *execv2.RunRequest) {
		if res.GetOutcome() == execv2.Outcome_OUTCOME_EXITED {
			change(res)
		}
	}
}

// everyResult changes every result.
func everyResult(change func(res *execv2.RunResult)) func(*execv2.RunResult, *execv2.RunRequest) {
	return func(res *execv2.RunResult, _ *execv2.RunRequest) { change(res) }
}

func TestEveryRunBodyFailsWhenThePluginIsWrong(t *testing.T) {
	notTheEnd := func(events []*execv2.RunEvent, _ *execv2.RunRequest) []*execv2.RunEvent {
		for i, event := range events {
			if event.GetEnded() != nil && i > 0 {
				events[0], events[i] = events[i], events[0] // RunEnded first
				break
			}
		}
		return events
	}
	for _, tc := range []struct {
		name   string
		body   string
		fault  faultyRunClient
		raw    bool
		phrase string // what a failure of the body says
	}{
		{"an exit code of 0 for every exit", "case 15", faultyRunClient{result: exited(func(res *execv2.RunResult) { res.ExitCode = new(int64) })}, false, "exit_code is 0"},
		{"no exit code for an exit", "case 15", faultyRunClient{result: exited(func(res *execv2.RunResult) { res.ExitCode = nil })}, false, "exit_code is absent"},
		{"every exit says it is the host's", "case 15", faultyRunClient{result: exited(func(res *execv2.RunResult) { res.ExitSource = execv2.ExitSource_EXIT_SOURCE_HOST })}, false, "exit_source is"},
		{"the request id is lost", "case 15", faultyRunClient{result: everyResult(func(res *execv2.RunResult) { res.RequestId = "" })}, false, "request_id is"},
		{"a result that says the process may survive an exit", "case 15", faultyRunClient{result: exited(func(res *execv2.RunResult) { res.RemoteMaySurvive = new(true) })}, false, "remote_may_survive is true"},
		{"every run says its start was observed", "case 16", faultyRunClient{result: everyResult(func(res *execv2.RunResult) { res.StartObserved = true })}, false, "start_observed is true"},
		{"the output is repaired into UTF-8", "case 15", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			res.Stdout = []byte(strings.ToValidUTF8(string(res.Stdout), "\ufffd"))
		})}, false, "the bytes are"},
		{"valid_utf8 is always true", "case 15", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.StdoutAccount != nil {
				res.StdoutAccount.ValidUtf8 = true
			}
		})}, false, "the account is"},
		{"a dropped count that is one short", "case 15", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.StdoutAccount.GetDroppedBytes() > 0 {
				res.StdoutAccount.DroppedBytes--
			}
		})}, false, "the account is"},
		{"a truncated output that says COMPLETE", "case 15", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.StdoutAccount.GetTruncated() {
				res.StdoutAccount.Completeness = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
			}
		})}, false, "the account is"},
		{"the head and the tail are swapped", "case 15", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if acct := res.StdoutAccount; acct.GetTruncated() && acct.GetHeadBytes() > 0 && acct.GetTailBytes() > 0 {
				head := int(acct.GetHeadBytes())
				res.Stdout = append(append([]byte(nil), res.Stdout[head:]...), res.Stdout[:head]...)
			}
		})}, false, "the bytes are"},
		{"a timed out run that says it was cancelled", "case 15", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.GetOutcome() == execv2.Outcome_OUTCOME_TIMED_OUT {
				res.Outcome = execv2.Outcome_OUTCOME_CANCELLED
			}
		})}, false, "the outcome is OUTCOME_CANCELLED"},
		{"a run with no duration", "case 15", faultyRunClient{result: everyResult(func(res *execv2.RunResult) { res.DurationMs = 0 })}, false, "duration_ms is 0"},
		{"a stream whose RunEnded is first", "case 15", faultyRunClient{events: notTheEnd}, false, "RunEnded"},
		{"a stream whose RunEnded carries the payloads", "case 15", faultyRunClient{events: func(events []*execv2.RunEvent, _ *execv2.RunRequest) []*execv2.RunEvent {
			for _, event := range events {
				if ended := event.GetEnded(); ended != nil {
					ended.Result.Stdout = []byte("not streamed")
				}
			}
			return events
		}}, false, "its payloads are empty"},
		{"a stream whose end is repeated", "case 15", faultyRunClient{events: func(events []*execv2.RunEvent, _ *execv2.RunRequest) []*execv2.RunEvent {
			return append(events, events[len(events)-1])
		}}, false, "came after RunEnded"},
		{"a stream that sends RunStarted after the output", "case 15", faultyRunClient{events: func(events []*execv2.RunEvent, _ *execv2.RunRequest) []*execv2.RunEvent {
			for i, event := range events {
				if event.GetStarted() != nil && i+1 < len(events) {
					events[i], events[i+1] = events[i+1], events[i]
					break
				}
			}
			return events
		}}, false, "RunStarted after the first Output"},
		{"a lost run that says it exited with 0", "case 16", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.GetOutcome() == execv2.Outcome_OUTCOME_LOST {
				res.Outcome, res.ExitCode, res.RemoteMaySurvive = execv2.Outcome_OUTCOME_EXITED, new(int64), new(false)
			}
		})}, false, "the outcome is OUTCOME_EXITED"},
		{"a lost run that says the process is gone", "case 16", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.GetOutcome() == execv2.Outcome_OUTCOME_LOST {
				res.RemoteMaySurvive = new(false)
			}
		})}, false, "remote_may_survive is false"},
		{"a lost run with a made-up start", "case 16", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.GetOutcome() == execv2.Outcome_OUTCOME_LOST && !res.GetStartObserved() {
				res.Target = &execv2.TargetFacts{Provider: "made-up"}
			}
		})}, false, "never made up"},
		{"a failed run with no failure", "case 27", faultyRunClient{result: everyResult(func(res *execv2.RunResult) { res.Failed = nil })}, false, "failed is present"},
		{"a failed run at the wrong stage", "case 27", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.GetFailed() != nil {
				res.Failed.Stage = execv2.FailureStage_FAILURE_STAGE_STREAM
			}
		})}, false, "failed is"},
		{"a failed run that says the process cannot survive", "case 27", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.GetFailed() != nil {
				res.RemoteMaySurvive = new(false)
			}
		})}, false, "remote_may_survive is false"},
		{"a failed run that lost its partial output", "case 27", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.GetFailed() != nil {
				res.Stdout, res.Stderr = nil, nil
			}
		})}, false, "the bytes are"},
		{"a failed run that says COMPLETE", "case 27", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.GetFailed() != nil && res.StdoutAccount != nil {
				res.StdoutAccount.Completeness = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
			}
		})}, false, "the account is"},
		{"a failure whose public message carries the canary", "case 13", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.GetFailed() != nil {
				res.Failed.PublicMessage = runCanary
			}
		})}, false, "carries the canary"},
		{"a failure that lost its facts", "case 13", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if res.GetFailed() != nil {
				res.Failed.Facts = nil
			}
		})}, false, "failed is"},
		{"a status that carries the canary", "case 13", faultyRunClient{}, true, "carries the canary"},
		{"a run whose exit is not the one the session had", "pair P1", faultyRunClient{result: exited(func(res *execv2.RunResult) { res.ExitCode = new(int64(99)) })}, false, "exit_code is 99"},
		{"a run that is more complete than the session says", "pair P1", faultyRunClient{result: exited(func(res *execv2.RunResult) {
			res.StdoutAccount.Completeness = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
		})}, false, "the account is"},
		{"a result that is another end than the engine's", "pair P3", faultyRunClient{result: exited(func(res *execv2.RunResult) { res.ExitCode = new(int64(98)) })}, false, "exit_code is 98"},
		{"a result whose output is not what the engine wrote", "pair P3", faultyRunClient{result: everyResult(func(res *execv2.RunResult) {
			if len(res.Stderr) > 0 {
				res.Stderr[0] ^= 0xff
			}
		})}, false, "the bytes are"},
	} {
		t.Run(tc.body+" with "+tc.name, func(t *testing.T) {
			var calls atomic.Int64
			rec := &recordT{}
			env := &wireEnv{resource: "thing-1", serve: serveFaulty(tc.fault, tc.raw)}
			rec.run(func(r checkT) { runBody(t, tc.body)(r, declared(t, shapes()[0], &calls), env) })
			if !rec.said(tc.phrase) {
				t.Errorf("the body did not fail with %q on a plugin with the fault %q: it failed with %q (skipped: %q)", tc.phrase, tc.name, rec.failed(), rec.skips)
			}
		})
	}
}

// statusWithCanary is the status of a plugin that lets the local cause of an error cross in the text of the status.
func statusWithCanary() error { return status.Error(codes.Internal, runCanary) }
