package exec

import (
	"strings"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// This file holds the closed vocabularies that an author writes (proto/exec/v2/exec.proto) as Go enums. The enums a
// host reads from a stream (how a session ended, what a mark means, ...) are the wire's own; Go code uses the
// generated types for them.
//
// An enum that an author declares follows one rule. Its named values map one to one to named wire values, so that
// every wire value an author can mean has a Go value and no Go value means "unspecified". Where the wire has an
// explicit NONE (FactMode, Survival, IntegrationMode, ExitReport) the Go value for it is the one the author wrote: an
// author who leaves FactMode at its zero value declared "none", and the SDK writes NONE, never UNSPECIFIED, which is
// the wire's word for "the producer did not say".

// enumPair relates a named Go value to its wire value.
type enumPair[G ~int, W ~int32] struct {
	g G
	w W
}

// enumToProto returns the wire value of g, or the wire zero (UNSPECIFIED) for a Go value that has no pair.
func enumToProto[G ~int, W ~int32](pairs []enumPair[G, W], g G) W {
	for _, p := range pairs {
		if p.g == g {
			return p.w
		}
	}
	return 0
}

// enumFromProto returns the Go value of w, or the Go zero value for UNSPECIFIED and for any wire value that has no
// pair (a value of a newer peer).
func enumFromProto[G ~int, W ~int32](pairs []enumPair[G, W], w W) G {
	for _, p := range pairs {
		if p.w == w {
			return p.g
		}
	}
	return 0
}

// enumName is the wire enum's value name without its type prefix: "TERMINATES" for KILL_BEHAVIOR_TERMINATES.
func enumName(wire interface{ String() string }, prefix string) string {
	return strings.TrimPrefix(wire.String(), prefix)
}

// Signal is a process signal the host may ask a target to deliver.
type Signal int

// The deliverable signals.
const (
	SignalUnspecified Signal = iota // unknown; never deliverable
	SignalInt
	SignalQuit
	SignalTerm
	SignalKill
	SignalHup
	SignalUsr1
	SignalUsr2
)

var signalPairs = []enumPair[Signal, execv2.Signal]{
	{SignalInt, execv2.Signal_SIGNAL_INT},
	{SignalQuit, execv2.Signal_SIGNAL_QUIT},
	{SignalTerm, execv2.Signal_SIGNAL_TERM},
	{SignalKill, execv2.Signal_SIGNAL_KILL},
	{SignalHup, execv2.Signal_SIGNAL_HUP},
	{SignalUsr1, execv2.Signal_SIGNAL_USR1},
	{SignalUsr2, execv2.Signal_SIGNAL_USR2},
}

func (s Signal) toProto() execv2.Signal { return enumToProto(signalPairs, s) }

// String returns the wire name without its prefix.
func (s Signal) String() string { return enumName(s.toProto(), "SIGNAL_") }

// FactMode says how a target provides a fact about a running session. The zero value, FactModeNone, is the author's
// statement that the target provides no such fact, and is written as NONE.
type FactMode int

// How a fact is provided.
const (
	FactModeNone FactMode = iota // no fact: the consumer shows nothing and never probes
	FactModePull                 // the consumer may ask
	FactModePush                 // the target pushes the fact when it changes; the consumer may also ask
)

var factModePairs = []enumPair[FactMode, execv2.FactMode]{
	{FactModeNone, execv2.FactMode_FACT_MODE_NONE},
	{FactModePull, execv2.FactMode_FACT_MODE_PULL},
	{FactModePush, execv2.FactMode_FACT_MODE_PUSH},
}

func (m FactMode) toProto() execv2.FactMode { return enumToProto(factModePairs, m) }

// String returns the wire name without its prefix.
func (m FactMode) String() string { return enumName(m.toProto(), "FACT_MODE_") }

// KillBehavior says what ending a session does to the process the target runs.
type KillBehavior int

// What ending a session does to the remote process.
const (
	KillUnspecified      KillBehavior = iota // the author did not say; read as "the process may survive"
	KillTerminates                           // ending the session terminates the process
	KillBestEffort                           // ending the session usually terminates the process
	KillClosesStreamOnly                     // ending the session only closes the stream; the process may keep running
)

var killBehaviorPairs = []enumPair[KillBehavior, execv2.KillBehavior]{
	{KillTerminates, execv2.KillBehavior_KILL_BEHAVIOR_TERMINATES},
	{KillBestEffort, execv2.KillBehavior_KILL_BEHAVIOR_BEST_EFFORT},
	{KillClosesStreamOnly, execv2.KillBehavior_KILL_BEHAVIOR_CLOSES_STREAM_ONLY},
}

func (k KillBehavior) toProto() execv2.KillBehavior { return enumToProto(killBehaviorPairs, k) }

// String returns the wire name without its prefix.
func (k KillBehavior) String() string { return enumName(k.toProto(), "KILL_BEHAVIOR_") }

// Survival says what outlives a restart of the host. The zero value, SurvivalNone, is the author's statement that
// nothing survives, and is written as NONE.
type Survival int

// What outlives a restart of the host.
const (
	SurvivalNone          Survival = iota // nothing
	SurvivalTargetProcess                 // the process survives and a new session can reach it; the scrollback does not
	SurvivalTargetSession                 // the process and its scrollback survive (tmux new -A)
)

var survivalPairs = []enumPair[Survival, execv2.Survival]{
	{SurvivalNone, execv2.Survival_SURVIVAL_NONE},
	{SurvivalTargetProcess, execv2.Survival_SURVIVAL_TARGET_PROCESS},
	{SurvivalTargetSession, execv2.Survival_SURVIVAL_TARGET_SESSION},
}

func (s Survival) toProto() execv2.Survival { return enumToProto(survivalPairs, s) }

// String returns the wire name without its prefix.
func (s Survival) String() string { return enumName(s.toProto(), "SURVIVAL_") }

// ExitReport says what a target can ever say about how a command ended.
type ExitReport int

// What a target can report about the end of a command.
const (
	ExitReportUnspecified   ExitReport = iota // the author did not say; registration refuses it for a target that runs commands
	ExitReportNone                            // no status at all (Kubernetes attach)
	ExitReportCode                            // an exit code (Kubernetes exec)
	ExitReportCodeAndSignal                   // an exit code or a signal (a pty, ssh)
)

var exitReportPairs = []enumPair[ExitReport, execv2.ExitReport]{
	{ExitReportNone, execv2.ExitReport_EXIT_REPORT_NONE},
	{ExitReportCode, execv2.ExitReport_EXIT_REPORT_CODE},
	{ExitReportCodeAndSignal, execv2.ExitReport_EXIT_REPORT_CODE_AND_SIGNAL},
}

func (r ExitReport) toProto() execv2.ExitReport { return enumToProto(exitReportPairs, r) }

// String returns the wire name without its prefix.
func (r ExitReport) String() string { return enumName(r.toProto(), "EXIT_REPORT_") }

// StallBehavior says what a reader that stops reading does to the target.
type StallBehavior int

// What a stalled reader does to the target.
const (
	StallUnspecified StallBehavior = iota // the author did not say; registration refuses it for a target that runs commands
	StallPropagates                       // the process slows: Write blocks and nothing is lost (a pty, ssh)
	StallLossy                            // every channel stalls and the tail is lost: the SDK drains eagerly into the ring (Kubernetes, Docker)
)

var stallBehaviorPairs = []enumPair[StallBehavior, execv2.StallBehavior]{
	{StallPropagates, execv2.StallBehavior_STALL_BEHAVIOR_PROPAGATES},
	{StallLossy, execv2.StallBehavior_STALL_BEHAVIOR_LOSSY},
}

func (s StallBehavior) toProto() execv2.StallBehavior { return enumToProto(stallBehaviorPairs, s) }

// String returns the wire name without its prefix.
func (s StallBehavior) String() string { return enumName(s.toProto(), "STALL_BEHAVIOR_") }

// IntegrationMode says how shell integration reaches a target. The zero value, IntegrationOff, is "none" and is
// written as NONE; it is never one of the modes a target lists in Capabilities.Integration.
type IntegrationMode int

// How shell integration reaches a target.
const (
	IntegrationOff         IntegrationMode = iota // none; the default everywhere
	IntegrationArgvWrapper                        // the SDK wraps the argv in a fixed bootstrap script; never without the host's consent
)

var integrationModePairs = []enumPair[IntegrationMode, execv2.IntegrationMode]{
	{IntegrationOff, execv2.IntegrationMode_INTEGRATION_MODE_NONE},
	{IntegrationArgvWrapper, execv2.IntegrationMode_INTEGRATION_MODE_ARGV_WRAPPER},
}

func (m IntegrationMode) toProto() execv2.IntegrationMode {
	return enumToProto(integrationModePairs, m)
}

// String returns the wire name without its prefix.
func (m IntegrationMode) String() string { return enumName(m.toProto(), "INTEGRATION_MODE_") }

// ErrorClass is the closed vocabulary of why something failed.
type ErrorClass int

// The error classes. ClassUnspecified (an unknown class) reads as ClassInternal.
const (
	ClassUnspecified    ErrorClass = iota
	ClassForbidden                 // the target's own authorization refused
	ClassUnauthorized              // the credentials the plugin holds were missing or refused
	ClassNotFound                  // the target, the executable or the session does not exist
	ClassConflict                  // the request conflicts with the current state
	ClassUnavailable               // the target cannot serve now
	ClassConnection                // the connection to the target failed or was retired
	ClassTimeout                   // a bound on time was reached
	ClassInvalidRequest            // the request is malformed or breaks a bound
	ClassUnsupported               // the plugin, the SDK build or the target does not support the request
	ClassLimit                     // a declared limit was reached
	ClassCancelled                 // the call or the command was cancelled
	ClassInternal                  // an unclassifiable failure, or a broken contract
)

var errorClassPairs = []enumPair[ErrorClass, execv2.ErrorClass]{
	{ClassForbidden, execv2.ErrorClass_ERROR_CLASS_FORBIDDEN},
	{ClassUnauthorized, execv2.ErrorClass_ERROR_CLASS_UNAUTHORIZED},
	{ClassNotFound, execv2.ErrorClass_ERROR_CLASS_NOT_FOUND},
	{ClassConflict, execv2.ErrorClass_ERROR_CLASS_CONFLICT},
	{ClassUnavailable, execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE},
	{ClassConnection, execv2.ErrorClass_ERROR_CLASS_CONNECTION},
	{ClassTimeout, execv2.ErrorClass_ERROR_CLASS_TIMEOUT},
	{ClassInvalidRequest, execv2.ErrorClass_ERROR_CLASS_INVALID_REQUEST},
	{ClassUnsupported, execv2.ErrorClass_ERROR_CLASS_UNSUPPORTED},
	{ClassLimit, execv2.ErrorClass_ERROR_CLASS_LIMIT},
	{ClassCancelled, execv2.ErrorClass_ERROR_CLASS_CANCELLED},
	{ClassInternal, execv2.ErrorClass_ERROR_CLASS_INTERNAL},
}

func (c ErrorClass) toProto() execv2.ErrorClass { return enumToProto(errorClassPairs, c) }

// String returns the wire name without its prefix.
func (c ErrorClass) String() string { return enumName(c.toProto(), "ERROR_CLASS_") }

// ErrorReason says why, within a class; closed, and valid only beside the classes listed in reasonClasses.
type ErrorReason int

// The reasons. ReasonUnspecified pairs with every class.
const (
	ReasonUnspecified       ErrorReason = iota
	ReasonTargetDenied                  // Forbidden
	ReasonTargetGone                    // NotFound
	ReasonTargetReplaced                // Conflict
	ReasonNotRunning                    // Unavailable
	ReasonNoSuchExecutable              // NotFound
	ReasonTTYUnsupported                // Unsupported
	ReasonTooManySessions               // Limit
	ReasonTooManyRuns                   // Limit
	ReasonInputTooLarge                 // Limit
	ReasonConnectionRetired             // Connection
	ReasonSessionExists                 // Conflict
	ReasonSessionUnknown                // NotFound
	ReasonConsentRequired               // InvalidRequest
	ReasonInputBacklog                  // Limit
)

var errorReasonPairs = []enumPair[ErrorReason, execv2.ErrorReason]{
	{ReasonTargetDenied, execv2.ErrorReason_ERROR_REASON_TARGET_DENIED},
	{ReasonTargetGone, execv2.ErrorReason_ERROR_REASON_TARGET_GONE},
	{ReasonTargetReplaced, execv2.ErrorReason_ERROR_REASON_TARGET_REPLACED},
	{ReasonNotRunning, execv2.ErrorReason_ERROR_REASON_NOT_RUNNING},
	{ReasonNoSuchExecutable, execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE},
	{ReasonTTYUnsupported, execv2.ErrorReason_ERROR_REASON_TTY_UNSUPPORTED},
	{ReasonTooManySessions, execv2.ErrorReason_ERROR_REASON_TOO_MANY_SESSIONS},
	{ReasonTooManyRuns, execv2.ErrorReason_ERROR_REASON_TOO_MANY_RUNS},
	{ReasonInputTooLarge, execv2.ErrorReason_ERROR_REASON_INPUT_TOO_LARGE},
	{ReasonConnectionRetired, execv2.ErrorReason_ERROR_REASON_CONNECTION_RETIRED},
	{ReasonSessionExists, execv2.ErrorReason_ERROR_REASON_SESSION_EXISTS},
	{ReasonSessionUnknown, execv2.ErrorReason_ERROR_REASON_SESSION_UNKNOWN},
	{ReasonConsentRequired, execv2.ErrorReason_ERROR_REASON_CONSENT_REQUIRED},
	{ReasonInputBacklog, execv2.ErrorReason_ERROR_REASON_INPUT_BACKLOG},
}

func (r ErrorReason) toProto() execv2.ErrorReason { return enumToProto(errorReasonPairs, r) }

// String returns the wire name without its prefix.
func (r ErrorReason) String() string { return enumName(r.toProto(), "ERROR_REASON_") }

// ChoiceState is what a choice can do now.
type ChoiceState int

// The states of a choice.
const (
	ChoiceStateUnspecified ChoiceState = iota // the producer does not know; never read as ready or stopped
	ChoiceStateReady                          // the choice can run a command now
	ChoiceStateStarting                       // the choice is starting and will probably be ready soon
	ChoiceStateStopped                        // the choice cannot run a command now
)

var choiceStatePairs = []enumPair[ChoiceState, execv2.ChoiceState]{
	{ChoiceStateReady, execv2.ChoiceState_CHOICE_STATE_READY},
	{ChoiceStateStarting, execv2.ChoiceState_CHOICE_STATE_STARTING},
	{ChoiceStateStopped, execv2.ChoiceState_CHOICE_STATE_STOPPED},
}

func (s ChoiceState) toProto() execv2.ChoiceState { return enumToProto(choiceStatePairs, s) }

func choiceStateFromProto(w execv2.ChoiceState) ChoiceState {
	return enumFromProto(choiceStatePairs, w)
}

// String returns the wire name without its prefix.
func (s ChoiceState) String() string { return enumName(s.toProto(), "CHOICE_STATE_") }

// ExitSource says whose status an exit code or a signal is. The zero value, SourceUnspecified, is what an author
// leaves unsaid: beside a code or a signal the SDK reads it as SourceProcess (see Exit), and it never sends it.
type ExitSource int

// Whose status an exit code is.
const (
	SourceUnspecified  ExitSource = iota // not said; read as the process's own status when an author gives it
	SourceProcess                        // the target relayed the process's own status
	SourceSignalFolded                   // a death by signal the target folded into 128+n: the SDK claims no signal
	SourceTransport                      // the transport said the stream ended; the process's own status was not relayed
	SourceHost                           // a bounded run that the SDK ended: a code, if any, is the aftermath of that kill
)

var exitSourcePairs = []enumPair[ExitSource, execv2.ExitSource]{
	{SourceProcess, execv2.ExitSource_EXIT_SOURCE_PROCESS},
	{SourceSignalFolded, execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED},
	{SourceTransport, execv2.ExitSource_EXIT_SOURCE_TRANSPORT},
	{SourceHost, execv2.ExitSource_EXIT_SOURCE_HOST},
}

func (s ExitSource) toProto() execv2.ExitSource { return enumToProto(exitSourcePairs, s) }

// String returns the wire name without its prefix.
func (s ExitSource) String() string { return enumName(s.toProto(), "EXIT_SOURCE_") }

// LostCause says why the SDK has no status for a command. The zero value, LostUnspecified, is "not lost" in an Exit.
type LostCause int

// Why a command has no status.
const (
	LostUnspecified LostCause = iota // not said (in an Exit: not lost)
	LostNoStatus                     // the stream to the target closed with nothing on its status channel
	LostLinkDead                     // the transport's keepalive gave up
)

var lostCausePairs = []enumPair[LostCause, execv2.LostCause]{
	{LostNoStatus, execv2.LostCause_LOST_CAUSE_NO_STATUS},
	{LostLinkDead, execv2.LostCause_LOST_CAUSE_LINK_DEAD},
}

func (c LostCause) toProto() execv2.LostCause { return enumToProto(lostCausePairs, c) }

// String returns the wire name without its prefix.
func (c LostCause) String() string { return enumName(c.toProto(), "LOST_CAUSE_") }

// LinkState is the health of the transport between the plugin and the target.
type LinkState int

// The health of the transport.
const (
	LinkUnspecified LinkState = iota // not said
	LinkGood
	LinkDegraded // slow or lossy, still moving
	LinkStalled  // nothing has moved for longer than the author's threshold
)

var linkStatePairs = []enumPair[LinkState, execv2.LinkState]{
	{LinkGood, execv2.LinkState_LINK_STATE_GOOD},
	{LinkDegraded, execv2.LinkState_LINK_STATE_DEGRADED},
	{LinkStalled, execv2.LinkState_LINK_STATE_STALLED},
}

func (s LinkState) toProto() execv2.LinkState { return enumToProto(linkStatePairs, s) }

// String returns the wire name without its prefix.
func (s LinkState) String() string { return enumName(s.toProto(), "LINK_STATE_") }
