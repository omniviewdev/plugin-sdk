package exectest

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The session half of the stress is shown to fail the way the first half is (stress_test.go): the real engine with one fault added at its edge,
// the streams of a session, the list, the calls that manage sessions, and what the stress says of it. Each fault is one thing a plausible
// engine gets wrong about sessions that outlive their stream, and each is meant to be found by one check.

type sessionFault int

const (
	sfSecondEnded       sessionFault = iota + 1 // a stream that left is told Ended once the session ended: two streams are
	sfByteTwice                                 // an attach opens one byte before the offset it was asked from: a byte the host has is sent again
	sfGapNotAnnounced                           // an attach skips a byte and says it starts where it was asked from
	sfTombstoneForever                          // the list keeps every tombstone it ever listed
	sfLeakedFollower                            // the stream that read to the end leaves a goroutine behind
	sfStaleCloseEnds                            // the Close of a stream that left ends the session
	sfEndSessionIgnores                         // EndSession ends a session that has a stream attached, instead of CONFLICT
	sfUndeclaredQueued                          // a signal the session does not declare is taken
)

// sfEngine is the real engine with one fault at its edge.
type sfEngine struct {
	exechook.Engine
	fault   sessionFault
	release chan struct{}

	mu         sync.Mutex
	cmds       map[string]exechook.Command
	remembered map[string]*execv2.SessionInfo
}

func sessionFaultFactory(t testing.TB, fault sessionFault) engineFactory {
	return func(targets []exec.Target, cfg exechook.EngineConfig) (exechook.Engine, error) {
		real, err := realEngine(targets, cfg)
		if err != nil {
			return nil, err
		}
		e := &sfEngine{Engine: real, fault: fault, release: make(chan struct{}), cmds: map[string]exechook.Command{}, remembered: map[string]*execv2.SessionInfo{}}
		t.Cleanup(func() { close(e.release) })
		return e, nil
	}
}

func (e *sfEngine) command(id string) exechook.Command {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cmds[id]
}

func (e *sfEngine) Open(ctx context.Context, req *execv2.OpenRequest) (exechook.Command, error) {
	cmd, err := e.Engine.Open(ctx, req)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.cmds[req.GetSessionId()] = cmd
	e.mu.Unlock()
	return &sfCommand{Command: cmd, e: e}, nil
}

func (e *sfEngine) Attach(ctx context.Context, req *execv2.OpenRequest) (exechook.Stream, error) {
	from := req.GetAttach().GetFromOffset()
	asked := req
	if from > 0 && from < beyondOutput {
		switch e.fault {
		case sfByteTwice:
			asked = &execv2.OpenRequest{SessionId: req.GetSessionId(), Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: from - 1}}}
		case sfGapNotAnnounced:
			asked = &execv2.OpenRequest{SessionId: req.GetSessionId(), Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: from + 1}}}
		}
	}
	inner, err := e.Engine.Attach(ctx, asked)
	if err != nil {
		return nil, err
	}
	s := &sfStream{Stream: inner, e: e, cmd: e.command(req.GetSessionId())}
	if e.fault == sfGapNotAnnounced && asked != req {
		rep := inner.Replay()
		s.lie = &exechook.Replay{Start: from, End: rep.End, Bytes: rep.End - from}
	}
	return s, nil
}

func (e *sfEngine) ListSessions(ctx context.Context) ([]*execv2.SessionInfo, error) {
	infos, err := e.Engine.ListSessions(ctx)
	if err != nil || e.fault != sfTombstoneForever {
		return infos, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, info := range infos {
		if info.GetEndedWith() != nil {
			e.remembered[info.GetFacts().GetSessionId()] = info
		}
	}
	listed := make(map[string]bool, len(infos))
	for _, info := range infos {
		listed[info.GetFacts().GetSessionId()] = true
	}
	for id, info := range e.remembered {
		if !listed[id] {
			infos = append(infos, info)
		}
	}
	slices.SortFunc(infos, func(a, b *execv2.SessionInfo) int {
		return strings.Compare(a.GetFacts().GetSessionId(), b.GetFacts().GetSessionId())
	})
	return infos, nil
}

func (e *sfEngine) EndSession(ctx context.Context, req *execv2.EndSessionRequest) error {
	err := e.Engine.EndSession(ctx, req)
	if e.fault == sfEndSessionIgnores && err != nil && refusalName(err) == "CONFLICT" {
		if cmd := e.command(req.GetSessionId()); cmd != nil {
			cmd.Close(false)
		}
		return nil
	}
	return err
}

// sfCommand is a command of the real engine with the fault of the engine that made it.
type sfCommand struct {
	exechook.Command
	e *sfEngine
}

func (c *sfCommand) Stream() exechook.Stream {
	inner := c.Command.Stream()
	if inner == nil {
		return nil
	}
	return &sfStream{Stream: inner, e: c.e, cmd: c.Command}
}

func (c *sfCommand) Signal(sig execv2.Signal) exechook.SignalDisposition {
	d := c.Command.Signal(sig)
	if c.e.fault == sfUndeclaredQueued && d == exechook.SignalUndeclared {
		return exechook.SignalQueued
	}
	return d
}

// sfStream is a stream of the real engine with the fault of the engine that made it.
type sfStream struct {
	exechook.Stream
	e      *sfEngine
	cmd    exechook.Command
	lie    *exechook.Replay
	leaked atomic.Bool
}

func (s *sfStream) Replay() exechook.Replay {
	if s.lie != nil {
		return *s.lie
	}
	return s.Stream.Replay()
}

// ended says that the session has ended, as far as the stream can tell.
func (s *sfStream) ended() bool {
	if s.cmd == nil {
		return false
	}
	select {
	case <-s.cmd.Done():
		return true
	default:
		return false
	}
}

func (s *sfStream) Read(ctx context.Context, buf []byte) (exechook.Chunk, error) {
	chunk, err := s.Stream.Read(ctx, buf)
	switch {
	case s.e.fault == sfSecondEnded && errors.Is(err, exechook.ErrStreamLeft) && s.ended():
		return exechook.Chunk{Offset: chunk.Offset}, io.EOF
	case s.e.fault == sfLeakedFollower && errors.Is(err, io.EOF) && s.leaked.CompareAndSwap(false, true):
		go func() { <-s.e.release }()
	}
	return chunk, err
}

func (s *sfStream) Final() exechook.Final {
	final := s.Stream.Final()
	if s.e.fault == sfSecondEnded && final.Detached && s.ended() {
		return exechook.Final{}
	}
	return final
}

func (s *sfStream) Close(kill bool) {
	if s.e.fault == sfStaleCloseEnds && s.Stream.Final().Detached && s.cmd != nil {
		s.cmd.Close(kill) // a stream that left ends the session
		return
	}
	s.Stream.Close(kill)
}

// said reports whether the failures hold every phrase of one of the alternatives.
func (r *recordT) saidAny(alternatives ...[]string) bool {
	for _, phrases := range alternatives {
		if r.said(phrases...) {
			return true
		}
	}
	return false
}

func TestTheStressFindsWhatAWrongEngineGetsWrongAboutSessionsThatOutliveTheirStream(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fault        sessionFault
		alternatives [][]string // the failure holds every phrase of one of them
	}{
		{"a stream that left is told Ended", sfSecondEnded, [][]string{{"streams were told Ended"}, {"had left the session", "eof true"}, {"ended with no stream attached"}}},
		{"a byte is sent twice across an attach", sfByteTwice, [][]string{{"a byte the host has would be sent again"}, {"bytes read twice"}}},
		{"a gap that is not announced", sfGapNotAnnounced, [][]string{{"a hole that was not reported as dropped"}}},
		{"a tombstone that never expires", sfTombstoneForever, [][]string{{"the store still lists", "a tombstone is gone when its time to live has run out"}}},
		{"a follower goroutine that is leaked", sfLeakedFollower, [][]string{{"goroutines", "after the lane was closed"}}},
		{"the Close of a stream that left ends the session", sfStaleCloseEnds, [][]string{{"canary", "the echo never came"}, {"nothing that can end it was asked of it"}}},
		{"EndSession of a session with a stream", sfEndSessionIgnores, [][]string{{"want CONFLICT"}}},
		{"a signal that is not declared is taken", sfUndeclaredQueued, [][]string{{"does not declare", "only a declared one is taken"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordT{}
			keeps := regimeKeeps
			runStress(rec, StressConfig{Workers: 4, Duration: 600 * time.Millisecond, Seed: 7}, stressHooks{
				engine: sessionFaultFactory(t, tc.fault), regime: &keeps,
				closeBound: 300 * time.Millisecond, settle: 300 * time.Millisecond, abandonBound: 300 * time.Millisecond,
			})
			rec.finish()
			if !rec.saidAny(tc.alternatives...) {
				t.Errorf("the stress did not say any of %q of an engine with the fault %q; it said %q", tc.alternatives, tc.name, rec.failed())
			}
		})
	}
}
