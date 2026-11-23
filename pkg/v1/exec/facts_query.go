package exec

import (
	"context"
	"slices"
	"sync/atomic"
	"time"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The facts of a running session, as the host asks for them (a Query) and as the target pushes them (Proc.Fact).
//
// A fact is something about the session that the host cannot read from its output: whether a command runs in the foreground of
// the shell, and the shell's current directory. The target's Capabilities say how it provides each (FactMode): not at all, when
// asked (PULL), or by itself on a change (PUSH, which the host may also ask for). The SDK holds the whole protocol and the
// author holds only the part only the target can do:
//
//   - a query for a fact the target does not provide is answered at once by the SDK, unknown and unsupported, and never reaches the
//     function: a function whose target provides nothing is not given Proc.Queries at all;
//   - a query for a fact the target provides is handed to the function through Proc.Queries, and its answer goes back to the host in
//     order with the stream's other frames. A function that does not answer within the deadline (5 seconds) has the SDK answer for it,
//     unknown and QUERY_FAILED, and an answer that comes later is ignored. At most eight queries of a session are waiting for the
//     function at once: another is answered unknown and QUERY_FAILED at once, so that a host cannot make a function that is slow
//     to answer hold more than that;
//   - the integration tier is the SDK's own fact, observed in the output; until the scanner that observes it is served it is
//     unknown, and a query for it never reaches the function;
//   - a function pushes a fact that changed with Proc.Fact, for a kind whose mode is PUSH. The SDK sends the newest value of each
//     kind at most ten times a second, and never sends a value the host already has.

// FactKind is a fact about a running session that the host can ask for and a target can push.
type FactKind int

// The facts a function provides. The integration tier is the SDK's own and is not listed: it never reaches a function.
const (
	FactKindUnspecified FactKind = iota // not a fact; a function never receives it, and one that reports it is refused
	FactKindForeground                  // whether a command runs in the foreground of the session (Capabilities.ForegroundFact)
	FactKindCwd                         // the current directory of the session (Capabilities.CwdFact)
)

var factKindPairs = []enumPair[FactKind, execv2.FactKind]{
	{FactKindForeground, execv2.FactKind_FACT_KIND_FOREGROUND},
	{FactKindCwd, execv2.FactKind_FACT_KIND_CWD},
}

func (k FactKind) toProto() execv2.FactKind { return enumToProto(factKindPairs, k) }

// String returns the wire name without its prefix.
func (k FactKind) String() string { return enumName(k.toProto(), "FACT_KIND_") }

// ForegroundState is whether a command runs in the foreground of the session.
type ForegroundState int

// Whether a command runs in the foreground.
const (
	ForegroundUnspecified ForegroundState = iota // unknown: Fact.Reason says why
	ForegroundIdle                               // only the shell is in the foreground
	ForegroundBusy                               // a command runs in the foreground
)

var foregroundStatePairs = []enumPair[ForegroundState, execv2.ForegroundState]{
	{ForegroundIdle, execv2.ForegroundState_FOREGROUND_STATE_IDLE},
	{ForegroundBusy, execv2.ForegroundState_FOREGROUND_STATE_BUSY},
}

func (s ForegroundState) toProto() execv2.ForegroundState {
	return enumToProto(foregroundStatePairs, s)
}

// String returns the wire name without its prefix.
func (s ForegroundState) String() string { return enumName(s.toProto(), "FOREGROUND_STATE_") }

// UnknownReason says why a fact is unknown.
type UnknownReason int

// Why a fact is unknown.
const (
	UnknownReasonUnspecified UnknownReason = iota // not said
	UnknownReasonUnsupported                      // the target cannot provide the fact
	UnknownReasonQueryFailed                      // the target tried and failed
	UnknownReasonRemote                           // the fact lives on the far side of a remote and is not observable
)

var unknownReasonPairs = []enumPair[UnknownReason, execv2.UnknownReason]{
	{UnknownReasonUnsupported, execv2.UnknownReason_UNKNOWN_REASON_UNSUPPORTED},
	{UnknownReasonQueryFailed, execv2.UnknownReason_UNKNOWN_REASON_QUERY_FAILED},
	{UnknownReasonRemote, execv2.UnknownReason_UNKNOWN_REASON_REMOTE},
}

func (r UnknownReason) toProto() execv2.UnknownReason { return enumToProto(unknownReasonPairs, r) }

// String returns the wire name without its prefix.
func (r UnknownReason) String() string { return enumName(r.toProto(), "UNKNOWN_REASON_") }

// Foreground is the fact FactKindForeground states.
type Foreground struct {
	// State is whether a command runs in the foreground. ForegroundUnspecified is unknown.
	State ForegroundState
	// Name is the foreground process's name, only for ForegroundBusy and only when known: plain text of at most 256 bytes. The SDK
	// drops a name that is not, and drops it for a state that is not busy.
	Name string
	// PGID is the foreground process group, only for ForegroundBusy and only when known; zero is unknown.
	PGID uint32
}

// Fact is a fact about the session that the target reports: the answer to a Query, or a value pushed with Proc.Fact.
type Fact struct {
	// Kind says which fact this is. In Query.Answer it may be left unspecified: the answer is then for the kind that was asked.
	Kind FactKind
	// Cwd is the current directory, for FactKindCwd: plain text of at most 4,096 bytes. Empty is unknown; the SDK also reads a path
	// that is not plain text, or is longer, as unknown (and QUERY_FAILED), and logs the author's bug.
	Cwd string
	// Foreground is the foreground process, for FactKindForeground. The zero value is unknown.
	Foreground Foreground
	// Reason says why the fact is unknown (an empty Cwd, a ForegroundUnspecified state), and is ignored for a fact that is known.
	// A function that does not say leaves it unspecified, which the host reads as "not said".
	Reason UnknownReason
}

// Query is the host's request for a fact, delivered through Proc.Queries.
type Query struct {
	// Kind is the fact that is asked for: FactKindForeground or FactKindCwd, a kind whose mode in the session's capabilities is PULL
	// or PUSH.
	Kind FactKind
	// Answer answers the query, with the fact, or with an unknown fact when the target cannot say (an empty Cwd, a Foreground whose
	// state is unspecified, and a Reason). It never blocks and may be called from any goroutine. Only the first call counts: a
	// second one is ignored and counted as the author's bug. One that comes after the deadline is ignored too: by then the SDK has
	// answered unknown and QUERY_FAILED for the function, and counted that. It is never nil in a Query the SDK delivers.
	Answer func(Fact)
}

// unknownFact is the wire fact that says kind is unknown, with the reason, as the answer to query id (or a push, for id zero). A
// kind that has no arm in the wire's Fact is answered with no value at all, which a reader reads as a kind it does not know.
func unknownFact(id uint32, kind FactKind, reason execv2.UnknownReason) *execv2.Fact {
	switch kind {
	case FactKindForeground:
		return &execv2.Fact{QueryId: id, Value: &execv2.Fact_Foreground{Foreground: &execv2.Foreground{Reason: reason}}}
	case FactKindCwd:
		return &execv2.Fact{QueryId: id, Value: &execv2.Fact_Cwd{Cwd: &execv2.Directory{Reason: reason}}}
	case FactKindUnspecified:
	}
	return &execv2.Fact{QueryId: id}
}

// tierFact is the SDK's answer to a query for the integration tier: the tier observed so far. Nothing observes it before the scanner
// of the output is served, so it is unspecified, which the wire defines as "not observed yet or not tracked".
func tierFact(id uint32) *execv2.Fact {
	return &execv2.Fact{QueryId: id, Value: &execv2.Fact_Tier{Tier: execv2.IntegrationTier_INTEGRATION_TIER_UNSPECIFIED}}
}

// toWire is the wire fact (for query id, or a push for id zero) that f states for kind, within the wire's bounds. What breaks a bound
// is dropped, never cut: the fact then says unknown, and the second result names what was wrong so that the caller tells the author.
func (f Fact) toWire(id uint32, kind FactKind) (*execv2.Fact, []string) {
	if f.Kind != FactKindUnspecified && f.Kind != kind {
		return unknownFact(id, kind, execv2.UnknownReason_UNKNOWN_REASON_QUERY_FAILED),
			[]string{"a Fact of kind " + f.Kind.String() + " was given for a fact of kind " + kind.String() + ": the answer is unknown"}
	}
	switch kind {
	case FactKindCwd:
		return f.cwdToWire(id)
	case FactKindForeground:
		return f.foregroundToWire(id)
	case FactKindUnspecified:
	}
	return unknownFact(id, kind, execv2.UnknownReason_UNKNOWN_REASON_QUERY_FAILED), []string{"a Fact whose kind is not a fact the function provides"}
}

func (f Fact) cwdToWire(id uint32) (*execv2.Fact, []string) {
	if f.Cwd == "" {
		return unknownFact(id, FactKindCwd, f.Reason.toProto()), nil
	}
	if len(f.Cwd) > maxCwdBytes || !validText(f.Cwd) {
		return unknownFact(id, FactKindCwd, execv2.UnknownReason_UNKNOWN_REASON_QUERY_FAILED),
			[]string{"Fact.Cwd is not plain text of at most 4096 bytes: the directory is unknown"}
	}
	return &execv2.Fact{QueryId: id, Value: &execv2.Fact_Cwd{Cwd: &execv2.Directory{Path: f.Cwd}}}, nil
}

func (f Fact) foregroundToWire(id uint32) (*execv2.Fact, []string) {
	fg := f.Foreground
	switch fg.State {
	case ForegroundUnspecified:
		return unknownFact(id, FactKindForeground, f.Reason.toProto()), nil
	case ForegroundIdle:
		return &execv2.Fact{QueryId: id, Value: &execv2.Fact_Foreground{Foreground: &execv2.Foreground{
			State: execv2.ForegroundState_FOREGROUND_STATE_IDLE,
		}}}, nil
	case ForegroundBusy:
		var problems []string
		name := fg.Name
		if name != "" && boundedFact(name) == "" {
			problems = append(problems, "Foreground.Name is not plain text of at most 256 bytes: the name is dropped")
			name = ""
		}
		return &execv2.Fact{QueryId: id, Value: &execv2.Fact_Foreground{Foreground: &execv2.Foreground{
			State: execv2.ForegroundState_FOREGROUND_STATE_BUSY, Name: name, Pgid: fg.PGID,
		}}}, problems
	}
	return unknownFact(id, FactKindForeground, execv2.UnknownReason_UNKNOWN_REASON_QUERY_FAILED),
		[]string{"Foreground.State is not a state of the foreground: the fact is unknown"}
}

// factKindFromProto is the kind of a fact a function provides that the wire names, or FactKindUnspecified for the integration tier and
// for a kind this build does not know.
func factKindFromProto(w execv2.FactKind) FactKind { return enumFromProto(factKindPairs, w) }

// factMode is how the session's target provides kind now: its effective capabilities, which a Started may have narrowed.
func (c *command) factMode(kind FactKind) FactMode {
	caps := c.gate.peek()
	switch kind {
	case FactKindForeground:
		return caps.ForegroundFact
	case FactKindCwd:
		return caps.CwdFact
	case FactKindUnspecified:
	}
	return FactModeNone
}

// The states of a query that the function was handed and the host waits for.
const (
	queryPending  uint32 = iota // the function has not answered and the deadline has not passed
	queryAnswered               // the function answered
	queryExpired                // the deadline passed and the SDK answered unknown
)

// pendingQuery is one query that the function was handed. Exactly one of the function's answer and the deadline settles it.
type pendingQuery struct {
	c      *command
	id     uint32
	kind   FactKind
	reply  func(*execv2.Fact) // where the answer goes: the stream that asked
	state  atomic.Uint32
	settle chan struct{} // closed when the query is settled
}

// claim settles the query for the caller, which must then answer it, and frees its place. It reports false when the function's
// answer or the deadline settled it first.
func (q *pendingQuery) claim(to uint32) bool {
	if !q.state.CompareAndSwap(queryPending, to) {
		return false
	}
	close(q.settle)
	q.c.ctl.released()
	return true
}

// answer is Query.Answer.
func (q *pendingQuery) answer(f Fact) {
	if !q.claim(queryAnswered) {
		if q.state.Load() == queryAnswered {
			q.c.violate(violation{violationQueryTwice, "Query.Answer was called twice for one query: the first answer stands"})
		} else {
			q.c.log.Debugw(context.Background(), "exec query answered after its deadline: ignored", "kind", q.kind.String())
		}
		return
	}
	wire, problems := f.toWire(q.id, q.kind)
	for _, problem := range problems {
		q.c.violate(violation{violationFactInvalid, problem})
	}
	q.reply(wire)
}

// expire is the deadline: the function has not answered, so the SDK does, unknown, and says so.
func (q *pendingQuery) expire() {
	if !q.claim(queryExpired) {
		return
	}
	q.c.violate(violation{violationQueryUnanswered, "the function did not answer a query within the deadline: the host was answered unknown, and a late answer is ignored"})
	q.reply(unknownFact(q.id, q.kind, execv2.UnknownReason_UNKNOWN_REASON_QUERY_FAILED))
}

// watch runs the deadline of the query. It ends when the query is settled or the session ends.
func (q *pendingQuery) watch() {
	timeout, stop := q.c.e.timer(q.c.e.cfg.queryDeadline)
	defer stop()
	select {
	case <-timeout:
		q.expire()
	case <-q.settle:
	case <-q.c.entry.Ended():
	}
}

// AskFact is a host's query for a fact of a kind the function provides. The result is the answer to send at once, which is non-nil
// when the SDK answers itself: the target does not provide the fact, or the function already holds as many queries as it may.
// Otherwise the query was handed to the function, and reply is called, once, with the answer: from the goroutine that settles the
// query, the function's when it answers and the deadline's when it does not. reply must not block.
func (c *command) AskFact(id uint32, kind FactKind, reply func(*execv2.Fact)) (immediate *execv2.Fact) {
	if c.factMode(kind) == FactModeNone {
		return unknownFact(id, kind, execv2.UnknownReason_UNKNOWN_REASON_UNSUPPORTED)
	}
	q := &pendingQuery{c: c, id: id, kind: kind, reply: reply, settle: make(chan struct{})}
	if !c.ctl.deliverQuery(q) {
		return unknownFact(id, kind, execv2.UnknownReason_UNKNOWN_REASON_QUERY_FAILED)
	}
	if !c.entry.Go(q.watch) {
		// The session ended while the query was handed over: nothing is left to answer for, and the stream is ending.
		q.claim(queryExpired)
	}
	return nil
}

// minPushInterval is how often the SDK sends the newest value of one kind of pushed fact at most.
const minPushInterval = 100 * time.Millisecond

// pushFact is Proc.Fact: a fact that changed, pushed by the target. It never blocks, and a fact the session's target does not push
// is ignored and counted as the author's bug.
func (c *command) pushFact(f Fact) {
	if c.callsRefused() {
		c.violate(violation{violationLateCall, "Proc.Fact was called after the function returned or the session ended: ignored"})
		return
	}
	kind := f.Kind
	if kind != FactKindForeground && kind != FactKindCwd {
		c.violate(violation{violationFactInvalid, "Proc.Fact with a Kind that is not a fact the function provides: ignored"})
		return
	}
	if c.factMode(kind) != FactModePush {
		c.violate(violation{violationFactUndeclared, "Proc.Fact for " + kind.String() + " on a target that does not push it (Capabilities." + factCapabilityName(kind) + " is not FactModePush): ignored"})
		return
	}
	wire, problems := f.toWire(0, kind)
	for _, problem := range problems {
		c.violate(violation{violationFactInvalid, problem})
	}
	c.ctl.push(kind, wire)
}

// factCapabilityName names the capability that says how the target provides kind, for a message.
func factCapabilityName(kind FactKind) string {
	if kind == FactKindForeground {
		return "ForegroundFact"
	}
	return "CwdFact"
}

// factKinds are the kinds a function pushes, in the order the stream's slots hold them.
var factKinds = [...]FactKind{FactKindForeground, FactKindCwd}

// factSlot is the place of a kind among the pushed facts.
func factSlot(kind FactKind) int { return slices.Index(factKinds[:], kind) }
