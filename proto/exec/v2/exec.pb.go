// The exec lane of the Omniview plugin SDK: run a command in a target (a Pod, a container, a host, a VM)
// either interactively (Open: one stream per session) or as one bounded, non-interactive run (Run).
//
// This file is a public contract. A released message, field number, enum value or RPC never changes meaning and is
// never reused. The reviewed listing of the contract (proto/exec/v2/exec.contract.golden, which a test compares with
// this file's descriptor) and buf breaking against the last release tag in CI fail a change that is not additive.
//
// Conventions that hold for every message below:
//
//   - ABSENCE IS UNKNOWN, one rule for the whole file. A fact that a producer may not know is written so that "not
//     known" is a value of its own and never the zero value of its type: a bool fact is `optional bool`; a numeric
//     fact whose zero is a real value is `optional`; an enum fact has an UNSPECIFIED zero; a message fact is absent; a
//     list whose emptiness is a fact ("no signals") is wrapped in a message so that absent differs from empty. A field
//     that is none of these is one of: a request the host makes (its zero is the documented default); a limit or a
//     size whose zero is not a real value; an identifier or a sequence number; a counter the SDK itself observed (zero
//     is a count); or a field of an arm that exists only when the fact is known. The field's comment says which. A
//     consumer never turns "unknown" into false, zero, none or exit 0.
//   - An UNSPECIFIED enum value is "unknown", never the nearest named value. Where it has a documented safe reading
//     for a decision (CLOSE_MODE_UNSPECIFIED is read as GRACEFUL, MARK_TRUST_UNSPECIFIED as UNVERIFIED,
//     KILL_BEHAVIOR_UNSPECIFIED as "the process may survive") the enum's comment says so. A value that a reader does
//     not know (a newer peer) is read as UNSPECIFIED.
//   - Classes, reasons and facts cross this wire; free text does not. The host writes the words. The one bounded
//     exception, Failed.public_message, is optional, reviewed-classes-only and never authority.
//   - Requests (every message the host sends to ask or to start something, and the first frame of Open) are STRICT:
//     a plugin refuses a request that carries a field it does not know, at any depth and inside the elements of a
//     repeated message too (UNSUPPORTED), because a request field can change what a command may do. For that reason a
//     request carries no map: a name/value list is a repeated NameValue, whose encoding is identical to a map entry's
//     and whose unknown fields can be seen. A host therefore sends a field an older plugin may not know only after
//     DescribeResponse.features lists the feature that gates it. Frames and responses are LENIENT: a field or a
//     frame kind a reader does not know is ignored, and counted.
//   - Capabilities have two levels: the target kind's UPPER BOUND in Describe, and each session's EFFECTIVE set in its
//     facts, which may only narrow it (see Capabilities).
//   - A session has exactly one end, and a stream exactly one last frame (see ServerFrame and Ended).
//   - Names: a message or field that the Go author API also names carries the same word there (pkg/v1/exec).
//
// A REFUSAL is a gRPC status that the SDK sends itself, before it hands a command to the author's function: the answer
// to a request it did not accept, so nothing ran and a host may retry (Open, Run and every other RPC that starts or
// ends a command). It carries a google.rpc.ErrorInfo with domain "omniview.sdk.exec.v2", reason = the ErrorClass name
// without its prefix ("FORBIDDEN"), and metadata keys "reason" (the ErrorReason name without its prefix), "program",
// "selection_name", "selection_value" and "retry_after_ms" for the ErrorFacts that are set; a bounded
// google.rpc.LocalizedMessage (locale "en") carries Error.Message for a reviewed class. Options is a read, it starts no
// command: a failure of the author's callback is also a status (the classified class, INTERNAL when the callback broke
// the contract) and a host may retry it. EVERYTHING AFTER A COMMAND WAS HANDED OVER IS DATA, never a status: once the
// command was handed to the author's function, how it ended is part of the result, because the command may have run
// and may still be running. A session ends with Ended (an End: exited, signaled, closed, failed or lost); a run ends
// with RunResult (an Outcome: exited, signaled, timed out, cancelled, failed or lost), which keeps the output read so
// far and says whether the process may survive. A refusal establishes that nothing ran; whether a retry can succeed
// depends on its class and reason (CONSENT_REQUIRED, FORBIDDEN and INVALID_REQUEST stay refused until the request or
// the host's state changes; LIMIT, UNAVAILABLE and CONNECTION may pass), so a host retries a refusal only when its
// class and reason say a retry can help, and never a failed or lost end. A status that is not a refusal in this sense
// (the call was cancelled, its deadline expired, the connection broke, the plugin died, or any gRPC status without
// this ErrorInfo) can arrive after the command was handed over and says nothing about whether it ran: it never
// authorizes an automatic retry of a command.

// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.11
// 	protoc        (unknown)
// source: proto/exec/v2/exec.proto

package execv2

import (
	v2 "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// Stream says which of a command's output streams a chunk of bytes belongs to.
type Stream int32

const (
	// Unknown. Never produced by the SDK.
	Stream_STREAM_UNSPECIFIED Stream = 0
	// The one combined stream of a command that runs with a terminal (a tty merges stdout and stderr).
	Stream_STREAM_TTY Stream = 1
	// The standard output of a command that runs without a terminal.
	Stream_STREAM_STDOUT Stream = 2
	// The standard error of a command that runs without a terminal.
	Stream_STREAM_STDERR Stream = 3
)

// Enum value maps for Stream.
var (
	Stream_name = map[int32]string{
		0: "STREAM_UNSPECIFIED",
		1: "STREAM_TTY",
		2: "STREAM_STDOUT",
		3: "STREAM_STDERR",
	}
	Stream_value = map[string]int32{
		"STREAM_UNSPECIFIED": 0,
		"STREAM_TTY":         1,
		"STREAM_STDOUT":      2,
		"STREAM_STDERR":      3,
	}
)

func (x Stream) Enum() *Stream {
	p := new(Stream)
	*p = x
	return p
}

func (x Stream) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (Stream) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[0].Descriptor()
}

func (Stream) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[0]
}

func (x Stream) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use Stream.Descriptor instead.
func (Stream) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{0}
}

// Signal is a process signal the host may ask a target to deliver (ClientFrame.signal). Ctrl+C in a terminal is
// the byte 0x03 sent as input, not this.
type Signal int32

const (
	// Unknown or not set. Never deliverable.
	Signal_SIGNAL_UNSPECIFIED Signal = 0
	Signal_SIGNAL_INT         Signal = 1
	Signal_SIGNAL_QUIT        Signal = 2
	Signal_SIGNAL_TERM        Signal = 3
	Signal_SIGNAL_KILL        Signal = 4
	Signal_SIGNAL_HUP         Signal = 5
	Signal_SIGNAL_USR1        Signal = 6
	Signal_SIGNAL_USR2        Signal = 7
)

// Enum value maps for Signal.
var (
	Signal_name = map[int32]string{
		0: "SIGNAL_UNSPECIFIED",
		1: "SIGNAL_INT",
		2: "SIGNAL_QUIT",
		3: "SIGNAL_TERM",
		4: "SIGNAL_KILL",
		5: "SIGNAL_HUP",
		6: "SIGNAL_USR1",
		7: "SIGNAL_USR2",
	}
	Signal_value = map[string]int32{
		"SIGNAL_UNSPECIFIED": 0,
		"SIGNAL_INT":         1,
		"SIGNAL_QUIT":        2,
		"SIGNAL_TERM":        3,
		"SIGNAL_KILL":        4,
		"SIGNAL_HUP":         5,
		"SIGNAL_USR1":        6,
		"SIGNAL_USR2":        7,
	}
)

func (x Signal) Enum() *Signal {
	p := new(Signal)
	*p = x
	return p
}

func (x Signal) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (Signal) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[1].Descriptor()
}

func (Signal) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[1]
}

func (x Signal) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use Signal.Descriptor instead.
func (Signal) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{1}
}

// FactMode says how a target provides a fact about a running session (the foreground process, the current
// directory).
type FactMode int32

const (
	// The producer did not say. A consumer treats the fact as unavailable for every decision and never probes it, and
	// does not claim that the target cannot provide it.
	FactMode_FACT_MODE_UNSPECIFIED FactMode = 0
	// The target provides no such fact: the consumer shows nothing and never probes.
	FactMode_FACT_MODE_NONE FactMode = 1
	// The consumer may ask (ClientFrame.query) and the target answers; the SDK never pushes it.
	FactMode_FACT_MODE_PULL FactMode = 2
	// The target pushes the fact when it changes (a Fact frame with query_id 0); the consumer may also ask.
	FactMode_FACT_MODE_PUSH FactMode = 3
)

// Enum value maps for FactMode.
var (
	FactMode_name = map[int32]string{
		0: "FACT_MODE_UNSPECIFIED",
		1: "FACT_MODE_NONE",
		2: "FACT_MODE_PULL",
		3: "FACT_MODE_PUSH",
	}
	FactMode_value = map[string]int32{
		"FACT_MODE_UNSPECIFIED": 0,
		"FACT_MODE_NONE":        1,
		"FACT_MODE_PULL":        2,
		"FACT_MODE_PUSH":        3,
	}
)

func (x FactMode) Enum() *FactMode {
	p := new(FactMode)
	*p = x
	return p
}

func (x FactMode) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (FactMode) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[2].Descriptor()
}

func (FactMode) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[2]
}

func (x FactMode) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use FactMode.Descriptor instead.
func (FactMode) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{2}
}

// KillBehavior says what ending a session (Close, a detach timeout, a retired connection) does to the process the
// target runs.
type KillBehavior int32

const (
	// The producer did not say. Read as "the process may survive".
	KillBehavior_KILL_BEHAVIOR_UNSPECIFIED KillBehavior = 0
	// Ending the session terminates the process (a pty hangup, an ssh channel close).
	KillBehavior_KILL_BEHAVIOR_TERMINATES KillBehavior = 1
	// Ending the session usually terminates the process; the target cannot promise it.
	KillBehavior_KILL_BEHAVIOR_BEST_EFFORT KillBehavior = 2
	// Ending the session only closes the stream; the process may keep running (Kubernetes exec, Docker exec).
	KillBehavior_KILL_BEHAVIOR_CLOSES_STREAM_ONLY KillBehavior = 3
)

// Enum value maps for KillBehavior.
var (
	KillBehavior_name = map[int32]string{
		0: "KILL_BEHAVIOR_UNSPECIFIED",
		1: "KILL_BEHAVIOR_TERMINATES",
		2: "KILL_BEHAVIOR_BEST_EFFORT",
		3: "KILL_BEHAVIOR_CLOSES_STREAM_ONLY",
	}
	KillBehavior_value = map[string]int32{
		"KILL_BEHAVIOR_UNSPECIFIED":        0,
		"KILL_BEHAVIOR_TERMINATES":         1,
		"KILL_BEHAVIOR_BEST_EFFORT":        2,
		"KILL_BEHAVIOR_CLOSES_STREAM_ONLY": 3,
	}
)

func (x KillBehavior) Enum() *KillBehavior {
	p := new(KillBehavior)
	*p = x
	return p
}

func (x KillBehavior) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (KillBehavior) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[3].Descriptor()
}

func (KillBehavior) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[3]
}

func (x KillBehavior) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use KillBehavior.Descriptor instead.
func (KillBehavior) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{3}
}

// Survival says what outlives a restart of the host (not a reload of its window or a break of its stream, which
// every session survives while the plugin process lives, for its lease).
type Survival int32

const (
	// The producer did not say. A consumer never offers to reattach, and does not claim that nothing survives.
	Survival_SURVIVAL_UNSPECIFIED Survival = 0
	// Nothing: a restart of the host ends the command with its session.
	Survival_SURVIVAL_NONE Survival = 1
	// The command's process survives and a new session can reach it; the scrollback does not.
	Survival_SURVIVAL_TARGET_PROCESS Survival = 2
	// The process and its scrollback survive (the command is a terminal multiplexer such as tmux new -A).
	Survival_SURVIVAL_TARGET_SESSION Survival = 3
)

// Enum value maps for Survival.
var (
	Survival_name = map[int32]string{
		0: "SURVIVAL_UNSPECIFIED",
		1: "SURVIVAL_NONE",
		2: "SURVIVAL_TARGET_PROCESS",
		3: "SURVIVAL_TARGET_SESSION",
	}
	Survival_value = map[string]int32{
		"SURVIVAL_UNSPECIFIED":    0,
		"SURVIVAL_NONE":           1,
		"SURVIVAL_TARGET_PROCESS": 2,
		"SURVIVAL_TARGET_SESSION": 3,
	}
)

func (x Survival) Enum() *Survival {
	p := new(Survival)
	*p = x
	return p
}

func (x Survival) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (Survival) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[4].Descriptor()
}

func (Survival) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[4]
}

func (x Survival) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use Survival.Descriptor instead.
func (Survival) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{4}
}

// ExitReport says what a target can ever say about how a command ended, so that a consumer neither waits for nor
// invents a status the target cannot produce.
type ExitReport int32

const (
	// The producer did not say. A consumer reads no status as promised. The SDK refuses to register a target that
	// can run commands and leaves this unspecified.
	ExitReport_EXIT_REPORT_UNSPECIFIED ExitReport = 0
	// The target reports no status of the process at all (Kubernetes attach): the session ends Closed, Lost or Failed,
	// or, when the transport itself relays a code of its own, Exited with EXIT_SOURCE_TRANSPORT, whose code is the
	// transport's and never the command's. Never Exited from the process (PROCESS or SIGNAL_FOLDED), never Signaled.
	ExitReport_EXIT_REPORT_NONE ExitReport = 1
	// The target reports an exit code (Kubernetes exec): a command that ended with a status ends Exited, never
	// Signaled; a session whose connection was retired, whose status channel was lost before a code arrived or that
	// failed ends Closed, Lost or Failed, and a consumer never waits for a code that did not arrive.
	ExitReport_EXIT_REPORT_CODE ExitReport = 2
	// The target reports an exit code or a signal (a pty, ssh).
	ExitReport_EXIT_REPORT_CODE_AND_SIGNAL ExitReport = 3
)

// Enum value maps for ExitReport.
var (
	ExitReport_name = map[int32]string{
		0: "EXIT_REPORT_UNSPECIFIED",
		1: "EXIT_REPORT_NONE",
		2: "EXIT_REPORT_CODE",
		3: "EXIT_REPORT_CODE_AND_SIGNAL",
	}
	ExitReport_value = map[string]int32{
		"EXIT_REPORT_UNSPECIFIED":     0,
		"EXIT_REPORT_NONE":            1,
		"EXIT_REPORT_CODE":            2,
		"EXIT_REPORT_CODE_AND_SIGNAL": 3,
	}
)

func (x ExitReport) Enum() *ExitReport {
	p := new(ExitReport)
	*p = x
	return p
}

func (x ExitReport) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ExitReport) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[5].Descriptor()
}

func (ExitReport) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[5]
}

func (x ExitReport) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ExitReport.Descriptor instead.
func (ExitReport) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{5}
}

// StallBehavior says what a reader that stops reading does to the target.
type StallBehavior int32

const (
	// The producer did not say. The SDK refuses to register a target that can run commands and leaves this
	// unspecified.
	StallBehavior_STALL_BEHAVIOR_UNSPECIFIED StallBehavior = 0
	// A stalled reader slows the process (a pty, ssh with per-channel windows): the SDK blocks the author's Write
	// and nothing is lost.
	StallBehavior_STALL_BEHAVIOR_PROPAGATES StallBehavior = 1
	// A stalled reader stalls every channel and loses the tail (Kubernetes, Docker): the SDK drains the target
	// eagerly into the replay ring, counts what the ring overwrites and never blocks the target on the host.
	StallBehavior_STALL_BEHAVIOR_LOSSY StallBehavior = 2
)

// Enum value maps for StallBehavior.
var (
	StallBehavior_name = map[int32]string{
		0: "STALL_BEHAVIOR_UNSPECIFIED",
		1: "STALL_BEHAVIOR_PROPAGATES",
		2: "STALL_BEHAVIOR_LOSSY",
	}
	StallBehavior_value = map[string]int32{
		"STALL_BEHAVIOR_UNSPECIFIED": 0,
		"STALL_BEHAVIOR_PROPAGATES":  1,
		"STALL_BEHAVIOR_LOSSY":       2,
	}
)

func (x StallBehavior) Enum() *StallBehavior {
	p := new(StallBehavior)
	*p = x
	return p
}

func (x StallBehavior) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (StallBehavior) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[6].Descriptor()
}

func (StallBehavior) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[6]
}

func (x StallBehavior) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use StallBehavior.Descriptor instead.
func (StallBehavior) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{6}
}

// IntegrationMode says how shell integration (OSC 133/633/7 marks) reaches a target that has no argv or env
// control of its shell. A helper binary inside the target is not offered.
type IntegrationMode int32

const (
	// Not said. In a request: no integration was asked for. In facts: not known what was applied.
	IntegrationMode_INTEGRATION_MODE_UNSPECIFIED IntegrationMode = 0
	// No integration (in IntegrationFacts.mode: none was applied; in a request: none is asked for).
	IntegrationMode_INTEGRATION_MODE_NONE IntegrationMode = 1
	// The SDK rewrites the command to [sh, -c, <fixed bootstrap script>, --, <the requested argv...>]; the script
	// loads the integration and then execs the requested shell. Nothing is typed into the user's terminal. Never
	// applied without IntegrationRequest.consent.
	IntegrationMode_INTEGRATION_MODE_ARGV_WRAPPER IntegrationMode = 2
)

// Enum value maps for IntegrationMode.
var (
	IntegrationMode_name = map[int32]string{
		0: "INTEGRATION_MODE_UNSPECIFIED",
		1: "INTEGRATION_MODE_NONE",
		2: "INTEGRATION_MODE_ARGV_WRAPPER",
	}
	IntegrationMode_value = map[string]int32{
		"INTEGRATION_MODE_UNSPECIFIED":  0,
		"INTEGRATION_MODE_NONE":         1,
		"INTEGRATION_MODE_ARGV_WRAPPER": 2,
	}
)

func (x IntegrationMode) Enum() *IntegrationMode {
	p := new(IntegrationMode)
	*p = x
	return p
}

func (x IntegrationMode) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (IntegrationMode) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[7].Descriptor()
}

func (IntegrationMode) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[7]
}

func (x IntegrationMode) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use IntegrationMode.Descriptor instead.
func (IntegrationMode) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{7}
}

// Feature is a part of the contract that a plugin's SDK build serves. DescribeResponse.features lists the features a
// build serves; a consumer uses a feature only when the build lists it AND, where the feature depends on the target,
// the target's Capabilities allow it. Start.features lists the opt-in features a session asks for.
type Feature int32

const (
	// Unknown. Never listed.
	Feature_FEATURE_UNSPECIFIED Feature = 0
	// Typed Mark frames from OSC 133/633/7 in the output, bytes untouched. Opt-in per session (Start.features).
	Feature_FEATURE_MARKS Feature = 1
	// Sessions that outlive their stream: Attach, Detach, Detached, ReadOutput, ListSessions, EndSession, the replay
	// ring, the lease and the tombstone.
	Feature_FEATURE_SESSION_MANAGEMENT Feature = 2
	// RunStream.
	Feature_FEATURE_RUN_STREAM Feature = 3
	// Start.env and RunRequest.env are honored (for a target whose Capabilities.env is set).
	Feature_FEATURE_ENV Feature = 4
	// Start.cwd and RunRequest.cwd are honored (for a target whose Capabilities.cwd is set).
	Feature_FEATURE_CWD Feature = 5
	// ClientFrame.close_input (stdin EOF) is honored (for a target whose Capabilities.stdin_eof is set).
	Feature_FEATURE_STDIN_EOF Feature = 6
	// Terminal-mode tracking for the attach reply and an exact mode reset. Opt-in per session (Start.features).
	Feature_FEATURE_MODES Feature = 7
	// Start.integration is honored (for a target whose Capabilities.integration lists the mode).
	Feature_FEATURE_INTEGRATION Feature = 8
	// Open: interactive sessions are served.
	Feature_FEATURE_SESSION Feature = 9
	// Run: bounded runs are served.
	Feature_FEATURE_RUN Feature = 10
)

// Enum value maps for Feature.
var (
	Feature_name = map[int32]string{
		0:  "FEATURE_UNSPECIFIED",
		1:  "FEATURE_MARKS",
		2:  "FEATURE_SESSION_MANAGEMENT",
		3:  "FEATURE_RUN_STREAM",
		4:  "FEATURE_ENV",
		5:  "FEATURE_CWD",
		6:  "FEATURE_STDIN_EOF",
		7:  "FEATURE_MODES",
		8:  "FEATURE_INTEGRATION",
		9:  "FEATURE_SESSION",
		10: "FEATURE_RUN",
	}
	Feature_value = map[string]int32{
		"FEATURE_UNSPECIFIED":        0,
		"FEATURE_MARKS":              1,
		"FEATURE_SESSION_MANAGEMENT": 2,
		"FEATURE_RUN_STREAM":         3,
		"FEATURE_ENV":                4,
		"FEATURE_CWD":                5,
		"FEATURE_STDIN_EOF":          6,
		"FEATURE_MODES":              7,
		"FEATURE_INTEGRATION":        8,
		"FEATURE_SESSION":            9,
		"FEATURE_RUN":                10,
	}
)

func (x Feature) Enum() *Feature {
	p := new(Feature)
	*p = x
	return p
}

func (x Feature) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (Feature) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[8].Descriptor()
}

func (Feature) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[8]
}

func (x Feature) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use Feature.Descriptor instead.
func (Feature) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{8}
}

// ErrorClass is the SDK's closed vocabulary of why something failed, the subset the exec lane uses. The host words
// the failure from the class, the ErrorReason and the ErrorFacts.
type ErrorClass int32

const (
	// Unknown class. Read as INTERNAL.
	ErrorClass_ERROR_CLASS_UNSPECIFIED ErrorClass = 0
	// The target's own authorization refused (cluster RBAC on pods/exec).
	ErrorClass_ERROR_CLASS_FORBIDDEN ErrorClass = 1
	// The credentials the plugin holds for the target are missing or were refused.
	ErrorClass_ERROR_CLASS_UNAUTHORIZED ErrorClass = 2
	// The target, the executable or the session does not exist.
	ErrorClass_ERROR_CLASS_NOT_FOUND ErrorClass = 3
	// The request conflicts with the current state (the incarnation changed, the session id is in use).
	ErrorClass_ERROR_CLASS_CONFLICT ErrorClass = 4
	// The target cannot serve now (the container is not running).
	ErrorClass_ERROR_CLASS_UNAVAILABLE ErrorClass = 5
	// The connection to the target failed or was retired.
	ErrorClass_ERROR_CLASS_CONNECTION ErrorClass = 6
	// A bound on time was reached.
	ErrorClass_ERROR_CLASS_TIMEOUT ErrorClass = 7
	// The request is malformed or breaks a bound it declares.
	ErrorClass_ERROR_CLASS_INVALID_REQUEST ErrorClass = 8
	// The request asks for something this plugin, SDK build or target does not support.
	ErrorClass_ERROR_CLASS_UNSUPPORTED ErrorClass = 9
	// A declared limit was reached (too many sessions, too many runs, input too large).
	ErrorClass_ERROR_CLASS_LIMIT ErrorClass = 10
	// The call or the command was cancelled.
	ErrorClass_ERROR_CLASS_CANCELLED ErrorClass = 11
	// The plugin failed in a way it cannot classify, or broke this contract.
	ErrorClass_ERROR_CLASS_INTERNAL ErrorClass = 12
)

// Enum value maps for ErrorClass.
var (
	ErrorClass_name = map[int32]string{
		0:  "ERROR_CLASS_UNSPECIFIED",
		1:  "ERROR_CLASS_FORBIDDEN",
		2:  "ERROR_CLASS_UNAUTHORIZED",
		3:  "ERROR_CLASS_NOT_FOUND",
		4:  "ERROR_CLASS_CONFLICT",
		5:  "ERROR_CLASS_UNAVAILABLE",
		6:  "ERROR_CLASS_CONNECTION",
		7:  "ERROR_CLASS_TIMEOUT",
		8:  "ERROR_CLASS_INVALID_REQUEST",
		9:  "ERROR_CLASS_UNSUPPORTED",
		10: "ERROR_CLASS_LIMIT",
		11: "ERROR_CLASS_CANCELLED",
		12: "ERROR_CLASS_INTERNAL",
	}
	ErrorClass_value = map[string]int32{
		"ERROR_CLASS_UNSPECIFIED":     0,
		"ERROR_CLASS_FORBIDDEN":       1,
		"ERROR_CLASS_UNAUTHORIZED":    2,
		"ERROR_CLASS_NOT_FOUND":       3,
		"ERROR_CLASS_CONFLICT":        4,
		"ERROR_CLASS_UNAVAILABLE":     5,
		"ERROR_CLASS_CONNECTION":      6,
		"ERROR_CLASS_TIMEOUT":         7,
		"ERROR_CLASS_INVALID_REQUEST": 8,
		"ERROR_CLASS_UNSUPPORTED":     9,
		"ERROR_CLASS_LIMIT":           10,
		"ERROR_CLASS_CANCELLED":       11,
		"ERROR_CLASS_INTERNAL":        12,
	}
)

func (x ErrorClass) Enum() *ErrorClass {
	p := new(ErrorClass)
	*p = x
	return p
}

func (x ErrorClass) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ErrorClass) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[9].Descriptor()
}

func (ErrorClass) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[9]
}

func (x ErrorClass) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ErrorClass.Descriptor instead.
func (ErrorClass) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{9}
}

// ErrorReason says why, within a class; closed, and always paired with the class(es) listed beside it. Any other
// pairing is a bug of the producer (the SDK replaces it with INTERNAL). ERROR_REASON_UNSPECIFIED pairs with every
// class.
type ErrorReason int32

const (
	// No reason beyond the class.
	ErrorReason_ERROR_REASON_UNSPECIFIED ErrorReason = 0
	// FORBIDDEN: the target's own authorization denied the exec.
	ErrorReason_ERROR_REASON_TARGET_DENIED ErrorReason = 1
	// NOT_FOUND: the target is gone (the Pod was deleted).
	ErrorReason_ERROR_REASON_TARGET_GONE ErrorReason = 2
	// CONFLICT: the target's incarnation changed since the host observed it.
	ErrorReason_ERROR_REASON_TARGET_REPLACED ErrorReason = 3
	// UNAVAILABLE: the target exists and cannot run commands now (the container is not running).
	ErrorReason_ERROR_REASON_NOT_RUNNING ErrorReason = 4
	// NOT_FOUND: the program to run does not exist in the target (no shell in the image); ErrorFacts.program names it.
	ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE ErrorReason = 5
	// UNSUPPORTED: the target cannot give the command a terminal.
	ErrorReason_ERROR_REASON_TTY_UNSUPPORTED ErrorReason = 6
	// LIMIT: the plugin holds SessionLimits.max_sessions sessions.
	ErrorReason_ERROR_REASON_TOO_MANY_SESSIONS ErrorReason = 7
	// LIMIT: the plugin runs RunLimits.max_concurrent runs.
	ErrorReason_ERROR_REASON_TOO_MANY_RUNS ErrorReason = 8
	// LIMIT: an input frame or stdin exceeds its declared maximum.
	ErrorReason_ERROR_REASON_INPUT_TOO_LARGE ErrorReason = 9
	// CONNECTION: the connection the call arrived on was retired.
	ErrorReason_ERROR_REASON_CONNECTION_RETIRED ErrorReason = 10
	// CONFLICT: the session id is in use by a live session.
	ErrorReason_ERROR_REASON_SESSION_EXISTS ErrorReason = 11
	// NOT_FOUND: no such session (never existed, or its tombstone expired).
	ErrorReason_ERROR_REASON_SESSION_UNKNOWN ErrorReason = 12
	// INVALID_REQUEST: a mode that needs the host's consent was requested without it.
	ErrorReason_ERROR_REASON_CONSENT_REQUIRED ErrorReason = 13
	// LIMIT: the command's input queue is full and the stream's window is closed; the host sends again when the
	// window opens. Raised by the plugin, and by a host's client that applies the same bound locally.
	ErrorReason_ERROR_REASON_INPUT_BACKLOG ErrorReason = 14
	// UNAVAILABLE: every place the target has for this call is held by author code that the plugin gave up on and that has
	// not returned (a function that ignores its cancelled context, an Options callback that never returns). Such code may
	// never return: restart the plugin, retrying does not help. It is not a shutdown (UNAVAILABLE with no reason) and not a
	// target that is full of live work (LIMIT).
	ErrorReason_ERROR_REASON_TARGET_WEDGED ErrorReason = 15
)

// Enum value maps for ErrorReason.
var (
	ErrorReason_name = map[int32]string{
		0:  "ERROR_REASON_UNSPECIFIED",
		1:  "ERROR_REASON_TARGET_DENIED",
		2:  "ERROR_REASON_TARGET_GONE",
		3:  "ERROR_REASON_TARGET_REPLACED",
		4:  "ERROR_REASON_NOT_RUNNING",
		5:  "ERROR_REASON_NO_SUCH_EXECUTABLE",
		6:  "ERROR_REASON_TTY_UNSUPPORTED",
		7:  "ERROR_REASON_TOO_MANY_SESSIONS",
		8:  "ERROR_REASON_TOO_MANY_RUNS",
		9:  "ERROR_REASON_INPUT_TOO_LARGE",
		10: "ERROR_REASON_CONNECTION_RETIRED",
		11: "ERROR_REASON_SESSION_EXISTS",
		12: "ERROR_REASON_SESSION_UNKNOWN",
		13: "ERROR_REASON_CONSENT_REQUIRED",
		14: "ERROR_REASON_INPUT_BACKLOG",
		15: "ERROR_REASON_TARGET_WEDGED",
	}
	ErrorReason_value = map[string]int32{
		"ERROR_REASON_UNSPECIFIED":        0,
		"ERROR_REASON_TARGET_DENIED":      1,
		"ERROR_REASON_TARGET_GONE":        2,
		"ERROR_REASON_TARGET_REPLACED":    3,
		"ERROR_REASON_NOT_RUNNING":        4,
		"ERROR_REASON_NO_SUCH_EXECUTABLE": 5,
		"ERROR_REASON_TTY_UNSUPPORTED":    6,
		"ERROR_REASON_TOO_MANY_SESSIONS":  7,
		"ERROR_REASON_TOO_MANY_RUNS":      8,
		"ERROR_REASON_INPUT_TOO_LARGE":    9,
		"ERROR_REASON_CONNECTION_RETIRED": 10,
		"ERROR_REASON_SESSION_EXISTS":     11,
		"ERROR_REASON_SESSION_UNKNOWN":    12,
		"ERROR_REASON_CONSENT_REQUIRED":   13,
		"ERROR_REASON_INPUT_BACKLOG":      14,
		"ERROR_REASON_TARGET_WEDGED":      15,
	}
)

func (x ErrorReason) Enum() *ErrorReason {
	p := new(ErrorReason)
	*p = x
	return p
}

func (x ErrorReason) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ErrorReason) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[10].Descriptor()
}

func (ErrorReason) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[10]
}

func (x ErrorReason) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ErrorReason.Descriptor instead.
func (ErrorReason) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{10}
}

// ChoiceState is what a choice can do now.
type ChoiceState int32

const (
	// The producer does not know (a container's state was not observed). Never read as ready or stopped.
	ChoiceState_CHOICE_STATE_UNSPECIFIED ChoiceState = 0
	// The choice can run a command now.
	ChoiceState_CHOICE_STATE_READY ChoiceState = 1
	// The choice is starting and will probably be ready soon.
	ChoiceState_CHOICE_STATE_STARTING ChoiceState = 2
	// The choice cannot run a command now.
	ChoiceState_CHOICE_STATE_STOPPED ChoiceState = 3
)

// Enum value maps for ChoiceState.
var (
	ChoiceState_name = map[int32]string{
		0: "CHOICE_STATE_UNSPECIFIED",
		1: "CHOICE_STATE_READY",
		2: "CHOICE_STATE_STARTING",
		3: "CHOICE_STATE_STOPPED",
	}
	ChoiceState_value = map[string]int32{
		"CHOICE_STATE_UNSPECIFIED": 0,
		"CHOICE_STATE_READY":       1,
		"CHOICE_STATE_STARTING":    2,
		"CHOICE_STATE_STOPPED":     3,
	}
)

func (x ChoiceState) Enum() *ChoiceState {
	p := new(ChoiceState)
	*p = x
	return p
}

func (x ChoiceState) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ChoiceState) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[11].Descriptor()
}

func (ChoiceState) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[11]
}

func (x ChoiceState) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ChoiceState.Descriptor instead.
func (ChoiceState) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{11}
}

// CloseMode says how a session is ended.
type CloseMode int32

const (
	// Not set. Read as GRACEFUL: nothing destructive is ever the reading of an unset value.
	CloseMode_CLOSE_MODE_UNSPECIFIED CloseMode = 0
	// Cancel the author's context and wait the declared grace.
	CloseMode_CLOSE_MODE_GRACEFUL CloseMode = 1
	// Cancel the author's context and do not wait.
	CloseMode_CLOSE_MODE_KILL CloseMode = 2
)

// Enum value maps for CloseMode.
var (
	CloseMode_name = map[int32]string{
		0: "CLOSE_MODE_UNSPECIFIED",
		1: "CLOSE_MODE_GRACEFUL",
		2: "CLOSE_MODE_KILL",
	}
	CloseMode_value = map[string]int32{
		"CLOSE_MODE_UNSPECIFIED": 0,
		"CLOSE_MODE_GRACEFUL":    1,
		"CLOSE_MODE_KILL":        2,
	}
)

func (x CloseMode) Enum() *CloseMode {
	p := new(CloseMode)
	*p = x
	return p
}

func (x CloseMode) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (CloseMode) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[12].Descriptor()
}

func (CloseMode) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[12]
}

func (x CloseMode) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use CloseMode.Descriptor instead.
func (CloseMode) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{12}
}

// FactKind is a fact the host can ask for or the target can push.
type FactKind int32

const (
	// Unknown. Never valid in a Query.
	FactKind_FACT_KIND_UNSPECIFIED FactKind = 0
	// Whether a command is running in the foreground of the session (Capabilities.foreground_fact).
	FactKind_FACT_KIND_FOREGROUND FactKind = 1
	// The current directory of the session (Capabilities.cwd_fact).
	FactKind_FACT_KIND_CWD FactKind = 2
	// The integration tier observed so far (SessionFacts.integration).
	FactKind_FACT_KIND_INTEGRATION_TIER FactKind = 3
)

// Enum value maps for FactKind.
var (
	FactKind_name = map[int32]string{
		0: "FACT_KIND_UNSPECIFIED",
		1: "FACT_KIND_FOREGROUND",
		2: "FACT_KIND_CWD",
		3: "FACT_KIND_INTEGRATION_TIER",
	}
	FactKind_value = map[string]int32{
		"FACT_KIND_UNSPECIFIED":      0,
		"FACT_KIND_FOREGROUND":       1,
		"FACT_KIND_CWD":              2,
		"FACT_KIND_INTEGRATION_TIER": 3,
	}
)

func (x FactKind) Enum() *FactKind {
	p := new(FactKind)
	*p = x
	return p
}

func (x FactKind) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (FactKind) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[13].Descriptor()
}

func (FactKind) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[13]
}

func (x FactKind) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use FactKind.Descriptor instead.
func (FactKind) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{13}
}

// DetachReason says why a stream ended while its session lives on.
type DetachReason int32

const (
	// Not said: the stream ended and the session lives on.
	DetachReason_DETACH_REASON_UNSPECIFIED DetachReason = 0
	// The host sent Detach: the session lives for the lease.
	DetachReason_DETACH_REASON_REQUESTED DetachReason = 1
	// Another stream attached and took the session over: the session continues, attached to it, with no lease. It is
	// not an end: no Ended is sent for it.
	DetachReason_DETACH_REASON_SUPERSEDED DetachReason = 2
)

// Enum value maps for DetachReason.
var (
	DetachReason_name = map[int32]string{
		0: "DETACH_REASON_UNSPECIFIED",
		1: "DETACH_REASON_REQUESTED",
		2: "DETACH_REASON_SUPERSEDED",
	}
	DetachReason_value = map[string]int32{
		"DETACH_REASON_UNSPECIFIED": 0,
		"DETACH_REASON_REQUESTED":   1,
		"DETACH_REASON_SUPERSEDED":  2,
	}
)

func (x DetachReason) Enum() *DetachReason {
	p := new(DetachReason)
	*p = x
	return p
}

func (x DetachReason) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (DetachReason) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[14].Descriptor()
}

func (DetachReason) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[14]
}

func (x DetachReason) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use DetachReason.Descriptor instead.
func (DetachReason) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{14}
}

// IntegrationTier is how much shell integration the SDK has observed in the output. Observed, not promised.
type IntegrationTier int32

const (
	// Not observed yet or not tracked (no FEATURE_MARKS).
	IntegrationTier_INTEGRATION_TIER_UNSPECIFIED IntegrationTier = 0
	// No marks seen.
	IntegrationTier_INTEGRATION_TIER_NONE IntegrationTier = 1
	// Prompt marks only.
	IntegrationTier_INTEGRATION_TIER_BASIC IntegrationTier = 2
	// Prompt, input, start and end marks carrying the session's nonce.
	IntegrationTier_INTEGRATION_TIER_RICH IntegrationTier = 3
)

// Enum value maps for IntegrationTier.
var (
	IntegrationTier_name = map[int32]string{
		0: "INTEGRATION_TIER_UNSPECIFIED",
		1: "INTEGRATION_TIER_NONE",
		2: "INTEGRATION_TIER_BASIC",
		3: "INTEGRATION_TIER_RICH",
	}
	IntegrationTier_value = map[string]int32{
		"INTEGRATION_TIER_UNSPECIFIED": 0,
		"INTEGRATION_TIER_NONE":        1,
		"INTEGRATION_TIER_BASIC":       2,
		"INTEGRATION_TIER_RICH":        3,
	}
)

func (x IntegrationTier) Enum() *IntegrationTier {
	p := new(IntegrationTier)
	*p = x
	return p
}

func (x IntegrationTier) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (IntegrationTier) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[15].Descriptor()
}

func (IntegrationTier) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[15]
}

func (x IntegrationTier) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use IntegrationTier.Descriptor instead.
func (IntegrationTier) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{15}
}

// MouseTracking is the one mouse-reporting mode in force: a mode, not a bit set.
type MouseTracking int32

const (
	// Not known. A consumer never reads it as no reporting.
	MouseTracking_MOUSE_TRACKING_UNSPECIFIED MouseTracking = 0
	// No mouse reporting.
	MouseTracking_MOUSE_TRACKING_NONE MouseTracking = 1
	// X10 compatibility (?9): button presses only.
	MouseTracking_MOUSE_TRACKING_X10 MouseTracking = 2
	// Normal tracking (?1000): presses and releases.
	MouseTracking_MOUSE_TRACKING_NORMAL MouseTracking = 3
	// Button-event tracking (?1002): and motion while a button is held.
	MouseTracking_MOUSE_TRACKING_BUTTON MouseTracking = 4
	// Any-event tracking (?1003): and all motion.
	MouseTracking_MOUSE_TRACKING_ANY MouseTracking = 5
)

// Enum value maps for MouseTracking.
var (
	MouseTracking_name = map[int32]string{
		0: "MOUSE_TRACKING_UNSPECIFIED",
		1: "MOUSE_TRACKING_NONE",
		2: "MOUSE_TRACKING_X10",
		3: "MOUSE_TRACKING_NORMAL",
		4: "MOUSE_TRACKING_BUTTON",
		5: "MOUSE_TRACKING_ANY",
	}
	MouseTracking_value = map[string]int32{
		"MOUSE_TRACKING_UNSPECIFIED": 0,
		"MOUSE_TRACKING_NONE":        1,
		"MOUSE_TRACKING_X10":         2,
		"MOUSE_TRACKING_NORMAL":      3,
		"MOUSE_TRACKING_BUTTON":      4,
		"MOUSE_TRACKING_ANY":         5,
	}
)

func (x MouseTracking) Enum() *MouseTracking {
	p := new(MouseTracking)
	*p = x
	return p
}

func (x MouseTracking) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (MouseTracking) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[16].Descriptor()
}

func (MouseTracking) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[16]
}

func (x MouseTracking) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use MouseTracking.Descriptor instead.
func (MouseTracking) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{16}
}

// MouseEncoding is how mouse reports are encoded.
type MouseEncoding int32

const (
	// Not known.
	MouseEncoding_MOUSE_ENCODING_UNSPECIFIED MouseEncoding = 0
	// The default X10 encoding.
	MouseEncoding_MOUSE_ENCODING_X10 MouseEncoding = 1
	// UTF-8 (?1005).
	MouseEncoding_MOUSE_ENCODING_UTF8 MouseEncoding = 2
	// SGR (?1006).
	MouseEncoding_MOUSE_ENCODING_SGR MouseEncoding = 3
	// urxvt (?1015).
	MouseEncoding_MOUSE_ENCODING_URXVT MouseEncoding = 4
)

// Enum value maps for MouseEncoding.
var (
	MouseEncoding_name = map[int32]string{
		0: "MOUSE_ENCODING_UNSPECIFIED",
		1: "MOUSE_ENCODING_X10",
		2: "MOUSE_ENCODING_UTF8",
		3: "MOUSE_ENCODING_SGR",
		4: "MOUSE_ENCODING_URXVT",
	}
	MouseEncoding_value = map[string]int32{
		"MOUSE_ENCODING_UNSPECIFIED": 0,
		"MOUSE_ENCODING_X10":         1,
		"MOUSE_ENCODING_UTF8":        2,
		"MOUSE_ENCODING_SGR":         3,
		"MOUSE_ENCODING_URXVT":       4,
	}
)

func (x MouseEncoding) Enum() *MouseEncoding {
	p := new(MouseEncoding)
	*p = x
	return p
}

func (x MouseEncoding) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (MouseEncoding) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[17].Descriptor()
}

func (MouseEncoding) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[17]
}

func (x MouseEncoding) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use MouseEncoding.Descriptor instead.
func (MouseEncoding) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{17}
}

// ResizeResult says what became of a Resize.
type ResizeResult int32

const (
	// Not reported.
	ResizeResult_RESIZE_RESULT_UNSPECIFIED ResizeResult = 0
	// The author consumed the size.
	ResizeResult_RESIZE_RESULT_APPLIED ResizeResult = 1
	// The target has no resize capability, or a newer resize superseded this one before the author took it.
	ResizeResult_RESIZE_RESULT_IGNORED ResizeResult = 2
	// The target failed to apply it.
	ResizeResult_RESIZE_RESULT_FAILED ResizeResult = 3
)

// Enum value maps for ResizeResult.
var (
	ResizeResult_name = map[int32]string{
		0: "RESIZE_RESULT_UNSPECIFIED",
		1: "RESIZE_RESULT_APPLIED",
		2: "RESIZE_RESULT_IGNORED",
		3: "RESIZE_RESULT_FAILED",
	}
	ResizeResult_value = map[string]int32{
		"RESIZE_RESULT_UNSPECIFIED": 0,
		"RESIZE_RESULT_APPLIED":     1,
		"RESIZE_RESULT_IGNORED":     2,
		"RESIZE_RESULT_FAILED":      3,
	}
)

func (x ResizeResult) Enum() *ResizeResult {
	p := new(ResizeResult)
	*p = x
	return p
}

func (x ResizeResult) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ResizeResult) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[18].Descriptor()
}

func (ResizeResult) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[18]
}

func (x ResizeResult) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ResizeResult.Descriptor instead.
func (ResizeResult) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{18}
}

// MarkKind is a kind of shell-integration mark.
type MarkKind int32

const (
	// Unknown. Never produced by the SDK.
	MarkKind_MARK_KIND_UNSPECIFIED MarkKind = 0
	// OSC 133 A: the prompt starts.
	MarkKind_MARK_KIND_PROMPT_START MarkKind = 1
	// OSC 133 B: the user's input starts.
	MarkKind_MARK_KIND_INPUT_START MarkKind = 2
	// OSC 133 C: the command starts to run.
	MarkKind_MARK_KIND_COMMAND_START MarkKind = 3
	// OSC 133 D: the command ended; Mark.exit_code may carry its status.
	MarkKind_MARK_KIND_COMMAND_END MarkKind = 4
	// OSC 7 or OSC 633 P;Cwd: the current directory.
	MarkKind_MARK_KIND_CWD MarkKind = 5
)

// Enum value maps for MarkKind.
var (
	MarkKind_name = map[int32]string{
		0: "MARK_KIND_UNSPECIFIED",
		1: "MARK_KIND_PROMPT_START",
		2: "MARK_KIND_INPUT_START",
		3: "MARK_KIND_COMMAND_START",
		4: "MARK_KIND_COMMAND_END",
		5: "MARK_KIND_CWD",
	}
	MarkKind_value = map[string]int32{
		"MARK_KIND_UNSPECIFIED":   0,
		"MARK_KIND_PROMPT_START":  1,
		"MARK_KIND_INPUT_START":   2,
		"MARK_KIND_COMMAND_START": 3,
		"MARK_KIND_COMMAND_END":   4,
		"MARK_KIND_CWD":           5,
	}
)

func (x MarkKind) Enum() *MarkKind {
	p := new(MarkKind)
	*p = x
	return p
}

func (x MarkKind) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (MarkKind) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[19].Descriptor()
}

func (MarkKind) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[19]
}

func (x MarkKind) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use MarkKind.Descriptor instead.
func (MarkKind) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{19}
}

// MarkTrust says how far a mark can be trusted.
type MarkTrust int32

const (
	// The producer did not say. Read as UNVERIFIED.
	MarkTrust_MARK_TRUST_UNSPECIFIED MarkTrust = 0
	// Parsed from the byte stream with no proof of origin: any program that prints can forge it. A consumer treats
	// its command_line, cwd and exit_code as hints, never as authority.
	MarkTrust_MARK_TRUST_UNVERIFIED MarkTrust = 1
	// Carried the session's nonce: the nonce matched the one the host issued, and nothing more. A later mark that
	// repeats the same nonce is indistinguishable from the first, so a match is not proof that the integration script
	// produced the mark and does not authenticate where it came from. A consumer keeps command_line, cwd and exit_code
	// as untrusted for any decision that needs authenticated provenance. A value that a reader does not know is read
	// as UNVERIFIED, never as NONCE.
	MarkTrust_MARK_TRUST_NONCE MarkTrust = 2
)

// Enum value maps for MarkTrust.
var (
	MarkTrust_name = map[int32]string{
		0: "MARK_TRUST_UNSPECIFIED",
		1: "MARK_TRUST_UNVERIFIED",
		2: "MARK_TRUST_NONCE",
	}
	MarkTrust_value = map[string]int32{
		"MARK_TRUST_UNSPECIFIED": 0,
		"MARK_TRUST_UNVERIFIED":  1,
		"MARK_TRUST_NONCE":       2,
	}
)

func (x MarkTrust) Enum() *MarkTrust {
	p := new(MarkTrust)
	*p = x
	return p
}

func (x MarkTrust) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (MarkTrust) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[20].Descriptor()
}

func (MarkTrust) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[20]
}

func (x MarkTrust) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use MarkTrust.Descriptor instead.
func (MarkTrust) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{20}
}

// ForegroundState is whether a command is running in the foreground of a session.
type ForegroundState int32

const (
	// Unknown (see Foreground.reason). A consumer that must decide (a close prompt) asks the user.
	ForegroundState_FOREGROUND_STATE_UNSPECIFIED ForegroundState = 0
	// Only the shell is in the foreground.
	ForegroundState_FOREGROUND_STATE_IDLE ForegroundState = 1
	// A command is running in the foreground.
	ForegroundState_FOREGROUND_STATE_BUSY ForegroundState = 2
)

// Enum value maps for ForegroundState.
var (
	ForegroundState_name = map[int32]string{
		0: "FOREGROUND_STATE_UNSPECIFIED",
		1: "FOREGROUND_STATE_IDLE",
		2: "FOREGROUND_STATE_BUSY",
	}
	ForegroundState_value = map[string]int32{
		"FOREGROUND_STATE_UNSPECIFIED": 0,
		"FOREGROUND_STATE_IDLE":        1,
		"FOREGROUND_STATE_BUSY":        2,
	}
)

func (x ForegroundState) Enum() *ForegroundState {
	p := new(ForegroundState)
	*p = x
	return p
}

func (x ForegroundState) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ForegroundState) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[21].Descriptor()
}

func (ForegroundState) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[21]
}

func (x ForegroundState) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ForegroundState.Descriptor instead.
func (ForegroundState) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{21}
}

// UnknownReason says why a fact is unknown.
type UnknownReason int32

const (
	// Not said.
	UnknownReason_UNKNOWN_REASON_UNSPECIFIED UnknownReason = 0
	// The target cannot provide the fact.
	UnknownReason_UNKNOWN_REASON_UNSUPPORTED UnknownReason = 1
	// The target tried and failed.
	UnknownReason_UNKNOWN_REASON_QUERY_FAILED UnknownReason = 2
	// The fact lives on the far side of a remote and is not observable.
	UnknownReason_UNKNOWN_REASON_REMOTE UnknownReason = 3
)

// Enum value maps for UnknownReason.
var (
	UnknownReason_name = map[int32]string{
		0: "UNKNOWN_REASON_UNSPECIFIED",
		1: "UNKNOWN_REASON_UNSUPPORTED",
		2: "UNKNOWN_REASON_QUERY_FAILED",
		3: "UNKNOWN_REASON_REMOTE",
	}
	UnknownReason_value = map[string]int32{
		"UNKNOWN_REASON_UNSPECIFIED":  0,
		"UNKNOWN_REASON_UNSUPPORTED":  1,
		"UNKNOWN_REASON_QUERY_FAILED": 2,
		"UNKNOWN_REASON_REMOTE":       3,
	}
)

func (x UnknownReason) Enum() *UnknownReason {
	p := new(UnknownReason)
	*p = x
	return p
}

func (x UnknownReason) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (UnknownReason) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[22].Descriptor()
}

func (UnknownReason) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[22]
}

func (x UnknownReason) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use UnknownReason.Descriptor instead.
func (UnknownReason) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{22}
}

// DropReason says why output was dropped.
type DropReason int32

const (
	// Not said.
	DropReason_DROP_REASON_UNSPECIFIED DropReason = 0
	// The replay ring was full and overwrote its oldest bytes.
	DropReason_DROP_REASON_REPLAY_FULL DropReason = 1
	// The session was detached and its output outgrew the ring.
	DropReason_DROP_REASON_DETACHED_OVERFLOW DropReason = 2
	// The target is drained eagerly (STALL_BEHAVIOR_LOSSY) and the host fell behind by more than the ring.
	DropReason_DROP_REASON_EAGER_DRAIN_OVERFLOW DropReason = 3
)

// Enum value maps for DropReason.
var (
	DropReason_name = map[int32]string{
		0: "DROP_REASON_UNSPECIFIED",
		1: "DROP_REASON_REPLAY_FULL",
		2: "DROP_REASON_DETACHED_OVERFLOW",
		3: "DROP_REASON_EAGER_DRAIN_OVERFLOW",
	}
	DropReason_value = map[string]int32{
		"DROP_REASON_UNSPECIFIED":          0,
		"DROP_REASON_REPLAY_FULL":          1,
		"DROP_REASON_DETACHED_OVERFLOW":    2,
		"DROP_REASON_EAGER_DRAIN_OVERFLOW": 3,
	}
)

func (x DropReason) Enum() *DropReason {
	p := new(DropReason)
	*p = x
	return p
}

func (x DropReason) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (DropReason) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[23].Descriptor()
}

func (DropReason) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[23]
}

func (x DropReason) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use DropReason.Descriptor instead.
func (DropReason) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{23}
}

// NoticeKind is something the SDK did not do as asked.
type NoticeKind int32

const (
	// Not said.
	NoticeKind_NOTICE_KIND_UNSPECIFIED NoticeKind = 0
	// Start.env was accepted by the target but not applied.
	NoticeKind_NOTICE_KIND_ENV_IGNORED NoticeKind = 1
	// Start.cwd was accepted by the target but not applied.
	NoticeKind_NOTICE_KIND_CWD_IGNORED NoticeKind = 2
	// A feature in Start.features was accepted but not applied.
	NoticeKind_NOTICE_KIND_FEATURE_IGNORED NoticeKind = 3
	// The size was zero (or not applicable) and the SDK picked 80 x 24 for a tty.
	NoticeKind_NOTICE_KIND_SIZE_DEFAULTED NoticeKind = 4
	// Start.integration was accepted but the target fell back to the plain program (no sh in the image).
	NoticeKind_NOTICE_KIND_INTEGRATION_IGNORED NoticeKind = 5
)

// Enum value maps for NoticeKind.
var (
	NoticeKind_name = map[int32]string{
		0: "NOTICE_KIND_UNSPECIFIED",
		1: "NOTICE_KIND_ENV_IGNORED",
		2: "NOTICE_KIND_CWD_IGNORED",
		3: "NOTICE_KIND_FEATURE_IGNORED",
		4: "NOTICE_KIND_SIZE_DEFAULTED",
		5: "NOTICE_KIND_INTEGRATION_IGNORED",
	}
	NoticeKind_value = map[string]int32{
		"NOTICE_KIND_UNSPECIFIED":         0,
		"NOTICE_KIND_ENV_IGNORED":         1,
		"NOTICE_KIND_CWD_IGNORED":         2,
		"NOTICE_KIND_FEATURE_IGNORED":     3,
		"NOTICE_KIND_SIZE_DEFAULTED":      4,
		"NOTICE_KIND_INTEGRATION_IGNORED": 5,
	}
)

func (x NoticeKind) Enum() *NoticeKind {
	p := new(NoticeKind)
	*p = x
	return p
}

func (x NoticeKind) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (NoticeKind) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[24].Descriptor()
}

func (NoticeKind) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[24]
}

func (x NoticeKind) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use NoticeKind.Descriptor instead.
func (NoticeKind) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{24}
}

// ClosedBy says who ended a session that was not ended by the command. Every value ends the session. A consumer reads
// a value it does not know as "closed", never as an error. (Another stream taking the session over does not end it:
// see DETACH_REASON_SUPERSEDED.)
type ClosedBy int32

const (
	// Not said.
	ClosedBy_CLOSED_BY_UNSPECIFIED ClosedBy = 0
	// The host asked (Close, EndSession).
	ClosedBy_CLOSED_BY_REQUEST ClosedBy = 1
	// The plugin is shutting down.
	ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN ClosedBy = 2
	// The lease of a detached session ran out.
	ClosedBy_CLOSED_BY_DETACH_TIMEOUT ClosedBy = 3
	// The session was idle for longer than its bound.
	ClosedBy_CLOSED_BY_IDLE_TIMEOUT ClosedBy = 4
	// The connection the session belongs to was retired.
	ClosedBy_CLOSED_BY_CONNECTION_RETIRED ClosedBy = 5
	// A bounded run reached its timeout.
	ClosedBy_CLOSED_BY_RUN_TIMEOUT ClosedBy = 6
	// A bounded run's call was cancelled.
	ClosedBy_CLOSED_BY_CANCELLED ClosedBy = 7
)

// Enum value maps for ClosedBy.
var (
	ClosedBy_name = map[int32]string{
		0: "CLOSED_BY_UNSPECIFIED",
		1: "CLOSED_BY_REQUEST",
		2: "CLOSED_BY_PLUGIN_SHUTDOWN",
		3: "CLOSED_BY_DETACH_TIMEOUT",
		4: "CLOSED_BY_IDLE_TIMEOUT",
		5: "CLOSED_BY_CONNECTION_RETIRED",
		6: "CLOSED_BY_RUN_TIMEOUT",
		7: "CLOSED_BY_CANCELLED",
	}
	ClosedBy_value = map[string]int32{
		"CLOSED_BY_UNSPECIFIED":        0,
		"CLOSED_BY_REQUEST":            1,
		"CLOSED_BY_PLUGIN_SHUTDOWN":    2,
		"CLOSED_BY_DETACH_TIMEOUT":     3,
		"CLOSED_BY_IDLE_TIMEOUT":       4,
		"CLOSED_BY_CONNECTION_RETIRED": 5,
		"CLOSED_BY_RUN_TIMEOUT":        6,
		"CLOSED_BY_CANCELLED":          7,
	}
)

func (x ClosedBy) Enum() *ClosedBy {
	p := new(ClosedBy)
	*p = x
	return p
}

func (x ClosedBy) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ClosedBy) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[25].Descriptor()
}

func (ClosedBy) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[25]
}

func (x ClosedBy) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ClosedBy.Descriptor instead.
func (ClosedBy) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{25}
}

// FailureStage says how far a command got before it failed, for a session (Failed in Ended) and for a run
// (RunResult.failed).
type FailureStage int32

const (
	// Not said.
	FailureStage_FAILURE_STAGE_UNSPECIFIED FailureStage = 0
	// The command's start was not observed: the failure came before the SDK saw it start. The command may still have
	// started (see start_observed and remote_may_survive).
	FailureStage_FAILURE_STAGE_START FailureStage = 1
	// After the start was observed, while the command ran.
	FailureStage_FAILURE_STAGE_RUN FailureStage = 2
	// Between the plugin and the target, after the start was observed: the stream failed. (A stream that merely ended
	// with no status is Lost, not this.)
	FailureStage_FAILURE_STAGE_STREAM FailureStage = 3
)

// Enum value maps for FailureStage.
var (
	FailureStage_name = map[int32]string{
		0: "FAILURE_STAGE_UNSPECIFIED",
		1: "FAILURE_STAGE_START",
		2: "FAILURE_STAGE_RUN",
		3: "FAILURE_STAGE_STREAM",
	}
	FailureStage_value = map[string]int32{
		"FAILURE_STAGE_UNSPECIFIED": 0,
		"FAILURE_STAGE_START":       1,
		"FAILURE_STAGE_RUN":         2,
		"FAILURE_STAGE_STREAM":      3,
	}
)

func (x FailureStage) Enum() *FailureStage {
	p := new(FailureStage)
	*p = x
	return p
}

func (x FailureStage) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (FailureStage) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[26].Descriptor()
}

func (FailureStage) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[26]
}

func (x FailureStage) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use FailureStage.Descriptor instead.
func (FailureStage) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{26}
}

// ExitSource says whose status an exit code or signal is.
type ExitSource int32

const (
	// Not said. A consumer never reads it as PROCESS.
	ExitSource_EXIT_SOURCE_UNSPECIFIED ExitSource = 0
	// The target relayed the process's own status (a Kubernetes error-channel Status, an ssh exit-status, Docker
	// ExecInspect, a local wait status).
	ExitSource_EXIT_SOURCE_PROCESS ExitSource = 1
	// A death by signal the target folded into 128+n: the code is 128+n and the SDK claims no signal.
	ExitSource_EXIT_SOURCE_SIGNAL_FOLDED ExitSource = 2
	// The transport said the stream ended and the process's own status was not relayed. Kubernetes: the API server's Success status (the
	// exec stream ended normally) means "ended normally" and nothing more, because the API server sends the same Success for a command
	// that did not exit 0 (after a kubelet or containerd restart): a code from this source, 0 included, is never the command's, and a
	// consumer shows "ended normally", never "exit 0". remote_may_survive stays absent.
	ExitSource_EXIT_SOURCE_TRANSPORT ExitSource = 3
	// A bounded run that the SDK ended (timeout, cancel). A code, if any, is the aftermath of that kill.
	ExitSource_EXIT_SOURCE_HOST ExitSource = 4
)

// Enum value maps for ExitSource.
var (
	ExitSource_name = map[int32]string{
		0: "EXIT_SOURCE_UNSPECIFIED",
		1: "EXIT_SOURCE_PROCESS",
		2: "EXIT_SOURCE_SIGNAL_FOLDED",
		3: "EXIT_SOURCE_TRANSPORT",
		4: "EXIT_SOURCE_HOST",
	}
	ExitSource_value = map[string]int32{
		"EXIT_SOURCE_UNSPECIFIED":   0,
		"EXIT_SOURCE_PROCESS":       1,
		"EXIT_SOURCE_SIGNAL_FOLDED": 2,
		"EXIT_SOURCE_TRANSPORT":     3,
		"EXIT_SOURCE_HOST":          4,
	}
)

func (x ExitSource) Enum() *ExitSource {
	p := new(ExitSource)
	*p = x
	return p
}

func (x ExitSource) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ExitSource) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[27].Descriptor()
}

func (ExitSource) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[27]
}

func (x ExitSource) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ExitSource.Descriptor instead.
func (ExitSource) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{27}
}

// LostCause says why the SDK has no status for a command.
type LostCause int32

const (
	// Not said.
	LostCause_LOST_CAUSE_UNSPECIFIED LostCause = 0
	// The stream to the target closed with nothing on its status channel.
	LostCause_LOST_CAUSE_NO_STATUS LostCause = 1
	// The transport's keepalive gave up.
	LostCause_LOST_CAUSE_LINK_DEAD LostCause = 2
)

// Enum value maps for LostCause.
var (
	LostCause_name = map[int32]string{
		0: "LOST_CAUSE_UNSPECIFIED",
		1: "LOST_CAUSE_NO_STATUS",
		2: "LOST_CAUSE_LINK_DEAD",
	}
	LostCause_value = map[string]int32{
		"LOST_CAUSE_UNSPECIFIED": 0,
		"LOST_CAUSE_NO_STATUS":   1,
		"LOST_CAUSE_LINK_DEAD":   2,
	}
)

func (x LostCause) Enum() *LostCause {
	p := new(LostCause)
	*p = x
	return p
}

func (x LostCause) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (LostCause) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[28].Descriptor()
}

func (LostCause) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[28]
}

func (x LostCause) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use LostCause.Descriptor instead.
func (LostCause) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{28}
}

// OutputCompleteness says whether every byte the command wrote was delivered. The values are mutually exclusive:
// TRUNCATED when any byte the SDK read was dropped (even if the drain also hit its bound), COMPLETE when nothing was
// dropped and the post-exit drain finished, UNSPECIFIED otherwise.
type OutputCompleteness int32

const (
	// The SDK cannot prove it: nothing was dropped, but the stream ended abnormally, the post-exit drain ended by its
	// limit or the target cannot tell. Never read as complete.
	OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED OutputCompleteness = 0
	// No byte was dropped, every byte the command wrote was read and delivered, and the post-exit drain finished.
	OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE OutputCompleteness = 1
	// The ring or the policy dropped at least one byte, and counted it. Takes precedence over UNSPECIFIED.
	OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED OutputCompleteness = 2
)

// Enum value maps for OutputCompleteness.
var (
	OutputCompleteness_name = map[int32]string{
		0: "OUTPUT_COMPLETENESS_UNSPECIFIED",
		1: "OUTPUT_COMPLETENESS_COMPLETE",
		2: "OUTPUT_COMPLETENESS_TRUNCATED",
	}
	OutputCompleteness_value = map[string]int32{
		"OUTPUT_COMPLETENESS_UNSPECIFIED": 0,
		"OUTPUT_COMPLETENESS_COMPLETE":    1,
		"OUTPUT_COMPLETENESS_TRUNCATED":   2,
	}
)

func (x OutputCompleteness) Enum() *OutputCompleteness {
	p := new(OutputCompleteness)
	*p = x
	return p
}

func (x OutputCompleteness) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (OutputCompleteness) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[29].Descriptor()
}

func (OutputCompleteness) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[29]
}

func (x OutputCompleteness) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use OutputCompleteness.Descriptor instead.
func (OutputCompleteness) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{29}
}

// LinkState is the health of the transport between the plugin and the target.
type LinkState int32

const (
	// Not said.
	LinkState_LINK_STATE_UNSPECIFIED LinkState = 0
	LinkState_LINK_STATE_GOOD        LinkState = 1
	// Slow or lossy, still moving.
	LinkState_LINK_STATE_DEGRADED LinkState = 2
	// Nothing has moved for longer than the author's threshold.
	LinkState_LINK_STATE_STALLED LinkState = 3
)

// Enum value maps for LinkState.
var (
	LinkState_name = map[int32]string{
		0: "LINK_STATE_UNSPECIFIED",
		1: "LINK_STATE_GOOD",
		2: "LINK_STATE_DEGRADED",
		3: "LINK_STATE_STALLED",
	}
	LinkState_value = map[string]int32{
		"LINK_STATE_UNSPECIFIED": 0,
		"LINK_STATE_GOOD":        1,
		"LINK_STATE_DEGRADED":    2,
		"LINK_STATE_STALLED":     3,
	}
)

func (x LinkState) Enum() *LinkState {
	p := new(LinkState)
	*p = x
	return p
}

func (x LinkState) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (LinkState) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[30].Descriptor()
}

func (LinkState) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[30]
}

func (x LinkState) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use LinkState.Descriptor instead.
func (LinkState) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{30}
}

// TruncatePolicy says which part of a run's output is kept when it exceeds its cap.
type TruncatePolicy int32

const (
	// Not set: the target's default, which is HEAD_TAIL.
	TruncatePolicy_TRUNCATE_POLICY_UNSPECIFIED TruncatePolicy = 0
	// Keep the beginning and the end (an error is usually at the end); the middle is counted, never stored.
	TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL TruncatePolicy = 1
	// Keep the beginning.
	TruncatePolicy_TRUNCATE_POLICY_HEAD TruncatePolicy = 2
	// Keep the end.
	TruncatePolicy_TRUNCATE_POLICY_TAIL TruncatePolicy = 3
)

// Enum value maps for TruncatePolicy.
var (
	TruncatePolicy_name = map[int32]string{
		0: "TRUNCATE_POLICY_UNSPECIFIED",
		1: "TRUNCATE_POLICY_HEAD_TAIL",
		2: "TRUNCATE_POLICY_HEAD",
		3: "TRUNCATE_POLICY_TAIL",
	}
	TruncatePolicy_value = map[string]int32{
		"TRUNCATE_POLICY_UNSPECIFIED": 0,
		"TRUNCATE_POLICY_HEAD_TAIL":   1,
		"TRUNCATE_POLICY_HEAD":        2,
		"TRUNCATE_POLICY_TAIL":        3,
	}
)

func (x TruncatePolicy) Enum() *TruncatePolicy {
	p := new(TruncatePolicy)
	*p = x
	return p
}

func (x TruncatePolicy) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (TruncatePolicy) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[31].Descriptor()
}

func (TruncatePolicy) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[31]
}

func (x TruncatePolicy) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use TruncatePolicy.Descriptor instead.
func (TruncatePolicy) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{31}
}

// Outcome says how a run ended. Every outcome is a result: once the command was handed to the target, a failure is the
// outcome FAILED and not a status. A refusal, before that, is a status and no outcome.
type Outcome int32

const (
	// Not said. Never read as success.
	Outcome_OUTCOME_UNSPECIFIED Outcome = 0
	// The command ended with a status (exit_code, exit_source say whose).
	Outcome_OUTCOME_EXITED Outcome = 1
	// The command ended by a signal the target reported.
	Outcome_OUTCOME_SIGNALED Outcome = 2
	// The SDK ended the run at its timeout.
	Outcome_OUTCOME_TIMED_OUT Outcome = 3
	// The call was cancelled.
	Outcome_OUTCOME_CANCELLED Outcome = 4
	// The stream ended with no terminal status: the command may have run, whether or not its start was observed.
	// Never success, never a failure, never retried by a host on its own.
	Outcome_OUTCOME_LOST Outcome = 5
	// The SDK, the plugin or the target failed after the command was handed to the target; RunResult.failed says how
	// and how far. The command may have run and may still be running (remote_may_survive says what is known) and the
	// output read so far is returned with its account. Never success, never exit 0, never retried by a host on its own.
	Outcome_OUTCOME_FAILED Outcome = 6
)

// Enum value maps for Outcome.
var (
	Outcome_name = map[int32]string{
		0: "OUTCOME_UNSPECIFIED",
		1: "OUTCOME_EXITED",
		2: "OUTCOME_SIGNALED",
		3: "OUTCOME_TIMED_OUT",
		4: "OUTCOME_CANCELLED",
		5: "OUTCOME_LOST",
		6: "OUTCOME_FAILED",
	}
	Outcome_value = map[string]int32{
		"OUTCOME_UNSPECIFIED": 0,
		"OUTCOME_EXITED":      1,
		"OUTCOME_SIGNALED":    2,
		"OUTCOME_TIMED_OUT":   3,
		"OUTCOME_CANCELLED":   4,
		"OUTCOME_LOST":        5,
		"OUTCOME_FAILED":      6,
	}
)

func (x Outcome) Enum() *Outcome {
	p := new(Outcome)
	*p = x
	return p
}

func (x Outcome) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (Outcome) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_exec_v2_exec_proto_enumTypes[32].Descriptor()
}

func (Outcome) Type() protoreflect.EnumType {
	return &file_proto_exec_v2_exec_proto_enumTypes[32]
}

func (x Outcome) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use Outcome.Descriptor instead.
func (Outcome) EnumDescriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{32}
}

// Size is a terminal size in character cells.
//
// Set by: the host (Start.size, Resize.size); the SDK (SessionFacts.size, Resized.size, Replay.size).
// Absent: cols and rows both zero is "no size" (none requested, none applied, or not known). A size with exactly
// one zero dimension is invalid.
type Size struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Cols          uint32                 `protobuf:"varint,1,opt,name=cols,proto3" json:"cols,omitempty"`
	Rows          uint32                 `protobuf:"varint,2,opt,name=rows,proto3" json:"rows,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Size) Reset() {
	*x = Size{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Size) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Size) ProtoMessage() {}

func (x *Size) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Size.ProtoReflect.Descriptor instead.
func (*Size) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{0}
}

func (x *Size) GetCols() uint32 {
	if x != nil {
		return x.Cols
	}
	return 0
}

func (x *Size) GetRows() uint32 {
	if x != nil {
		return x.Rows
	}
	return 0
}

// NameValue is one name and its value in a request: a selection choice, an environment variable, a label. A request
// carries a repeated NameValue and never a map: the encoding is identical to a map entry's (field 1 the name, field 2
// the value), and a repeated message can be checked for unknown fields where a map entry cannot. A name appears at
// most once in a list: a duplicate is INVALID_REQUEST, never "the last one wins".
type NameValue struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Name          string                 `protobuf:"bytes,1,opt,name=name,proto3" json:"name,omitempty"`
	Value         string                 `protobuf:"bytes,2,opt,name=value,proto3" json:"value,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *NameValue) Reset() {
	*x = NameValue{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *NameValue) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*NameValue) ProtoMessage() {}

func (x *NameValue) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use NameValue.ProtoReflect.Descriptor instead.
func (*NameValue) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{1}
}

func (x *NameValue) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *NameValue) GetValue() string {
	if x != nil {
		return x.Value
	}
	return ""
}

// TargetRef names one thing to run a command in, or to ask about: a canonical resource reference, the route of the
// connection that reaches it, and the choices already made for it.
//
// Set by: the host, on every request that addresses a resource (Options, Start, RunRequest).
// route: REQUIRED, and must be the caller's connection: the plugin refuses a route that is not the connection the
// call arrived on (INVALID_REQUEST). reference: REQUIRED, canonical, with its incarnation when the host has one; a
// reference whose incarnation differs from the live resource's is CONFLICT / TARGET_REPLACED.
// selection: the choices made from Options, one NameValue per option (name: the option's name, "container"; value: the
// choice's value, "app"), each option at most once. Empty means "the target's default for every option". Options
// itself refuses a non-empty selection (INVALID_REQUEST).
type TargetRef struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Reference     *v2.ResourceReference  `protobuf:"bytes,1,opt,name=reference,proto3" json:"reference,omitempty"`
	Route         *v2.AccessRoute        `protobuf:"bytes,2,opt,name=route,proto3" json:"route,omitempty"`
	Selection     []*NameValue           `protobuf:"bytes,3,rep,name=selection,proto3" json:"selection,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *TargetRef) Reset() {
	*x = TargetRef{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *TargetRef) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*TargetRef) ProtoMessage() {}

func (x *TargetRef) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use TargetRef.ProtoReflect.Descriptor instead.
func (*TargetRef) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{2}
}

func (x *TargetRef) GetReference() *v2.ResourceReference {
	if x != nil {
		return x.Reference
	}
	return nil
}

func (x *TargetRef) GetRoute() *v2.AccessRoute {
	if x != nil {
		return x.Route
	}
	return nil
}

func (x *TargetRef) GetSelection() []*NameValue {
	if x != nil {
		return x.Selection
	}
	return nil
}

// ErrorFacts are the bounded facts a refusal carries so that the host can word it without free text.
//
// Set by: the plugin's author (exec.ErrorFacts) or the SDK. Each string is at most 256 bytes of UTF-8 without
// control characters. Absent (empty or zero) means the fact does not apply.
type ErrorFacts struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// The program that was not found (ERROR_REASON_NO_SUCH_EXECUTABLE).
	Program string `protobuf:"bytes,1,opt,name=program,proto3" json:"program,omitempty"`
	// The name of the selection the failure concerns ("container").
	SelectionName string `protobuf:"bytes,2,opt,name=selection_name,json=selectionName,proto3" json:"selection_name,omitempty"`
	// The value of that selection ("app").
	SelectionValue string `protobuf:"bytes,3,opt,name=selection_value,json=selectionValue,proto3" json:"selection_value,omitempty"`
	// When retrying can help: how long to wait. Zero means no advice, not "retry now".
	RetryAfterMs  uint32 `protobuf:"varint,4,opt,name=retry_after_ms,json=retryAfterMs,proto3" json:"retry_after_ms,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ErrorFacts) Reset() {
	*x = ErrorFacts{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ErrorFacts) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ErrorFacts) ProtoMessage() {}

func (x *ErrorFacts) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ErrorFacts.ProtoReflect.Descriptor instead.
func (*ErrorFacts) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{3}
}

func (x *ErrorFacts) GetProgram() string {
	if x != nil {
		return x.Program
	}
	return ""
}

func (x *ErrorFacts) GetSelectionName() string {
	if x != nil {
		return x.SelectionName
	}
	return ""
}

func (x *ErrorFacts) GetSelectionValue() string {
	if x != nil {
		return x.SelectionValue
	}
	return ""
}

func (x *ErrorFacts) GetRetryAfterMs() uint32 {
	if x != nil {
		return x.RetryAfterMs
	}
	return 0
}

// SignalSet is a list of signals that can be empty: an absent SignalSet is "not known", a present empty one is "none".
type SignalSet struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Signals       []Signal               `protobuf:"varint,1,rep,packed,name=signals,proto3,enum=omniview.sdk.exec.v2.Signal" json:"signals,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SignalSet) Reset() {
	*x = SignalSet{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SignalSet) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SignalSet) ProtoMessage() {}

func (x *SignalSet) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SignalSet.ProtoReflect.Descriptor instead.
func (*SignalSet) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{4}
}

func (x *SignalSet) GetSignals() []Signal {
	if x != nil {
		return x.Signals
	}
	return nil
}

// IntegrationModeSet is a list of integration modes that can be empty: absent is "not known", present and empty is
// "none".
type IntegrationModeSet struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Modes         []IntegrationMode      `protobuf:"varint,1,rep,packed,name=modes,proto3,enum=omniview.sdk.exec.v2.IntegrationMode" json:"modes,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *IntegrationModeSet) Reset() {
	*x = IntegrationModeSet{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *IntegrationModeSet) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*IntegrationModeSet) ProtoMessage() {}

func (x *IntegrationModeSet) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use IntegrationModeSet.ProtoReflect.Descriptor instead.
func (*IntegrationModeSet) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{5}
}

func (x *IntegrationModeSet) GetModes() []IntegrationMode {
	if x != nil {
		return x.Modes
	}
	return nil
}

// Capabilities is what a target can do, at two levels.
//
// TargetInfo.capabilities is the kind's UPPER BOUND: the most any session of the kind can do, declared by the plugin's
// author at registration (exec.Capabilities) and fixed for the life of the plugin process. A consumer may use it
// before any session exists (which controls a picker offers, which request it never sends).
// SessionFacts.capabilities is what THIS session can do, EFFECTIVE. At Opened it is the kind's upper bound narrowed by
// Choice.capabilities of every choice the request's selection names: the session's MODE (a Kubernetes exec or attach),
// declared with the choice, so a host knows it before anything opens. Started narrows it further only by what the
// author learned at run time and never carries the mode; a later attach's Opened carries the latest effective set. A
// session may only NARROW, never widen: for every field below, a choice's value is at most the kind's and a Started's
// at most the one Opened carried, under the order given at the field; the SDK enforces it, replacing a value above its
// bound with the bound's and logging the author's bug. EVERY capability can be narrowed, so one target kind can serve
// modes that differ (a Kubernetes Pod: exec reports an exit code and does not survive a host restart, attach reports
// no status and does). A consumer that holds a session reads that session's facts and never Describe; a consumer that
// has only Describe reads each field as "the most it can do".
//
// Absent: a Capabilities message that is absent (TargetInfo or SessionFacts) means the producer did not declare
// capabilities: unknown, to be read as nothing promised. Inside a present message a field that is absent (a bool or a
// set) or UNSPECIFIED (an enum) is not known; false, an empty set and NONE are the producer's statement that the
// target cannot. The SDK always writes every field it knows. UNSPECIFIED (or an absent field) is below every value of
// an order: a kind that did not say promises nothing, so a session cannot claim more than that, and a session that
// reports UNSPECIFIED has not reported the field, so the value it narrows stands.
type Capabilities struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// The target can run a command with a terminal (a pty). Order: false < true.
	Tty *bool `protobuf:"varint,1,opt,name=tty,proto3,oneof" json:"tty,omitempty"`
	// Without a terminal, stderr arrives as its own stream (STREAM_STDERR). Order: false < true.
	SeparateStderr *bool `protobuf:"varint,2,opt,name=separate_stderr,json=separateStderr,proto3,oneof" json:"separate_stderr,omitempty"`
	// The target honors a resize after the command started. Order: false < true.
	Resize *bool `protobuf:"varint,3,opt,name=resize,proto3,oneof" json:"resize,omitempty"`
	// The target honors Start.size before the first output; false means the first prompt is laid out at the target's
	// own size. Order: false < true.
	InitialSize *bool `protobuf:"varint,4,opt,name=initial_size,json=initialSize,proto3,oneof" json:"initial_size,omitempty"`
	// ClientFrame.close_input half-closes the command's stdin. Order: false < true.
	StdinEof *bool `protobuf:"varint,5,opt,name=stdin_eof,json=stdinEof,proto3,oneof" json:"stdin_eof,omitempty"`
	// The signals ClientFrame.signal can deliver; a present empty set means none: the host offers only Close. Order:
	// inclusion (a session's set is a subset of the kind's).
	Signals *SignalSet `protobuf:"bytes,6,opt,name=signals,proto3" json:"signals,omitempty"`
	// What ending a session does to the process. Order: CLOSES_STREAM_ONLY < BEST_EFFORT < TERMINATES.
	Kill KillBehavior `protobuf:"varint,7,opt,name=kill,proto3,enum=omniview.sdk.exec.v2.KillBehavior" json:"kill,omitempty"`
	// Start.env and RunRequest.env are honored; false means a request that carries env is refused (UNSUPPORTED). Order:
	// false < true.
	Env *bool `protobuf:"varint,8,opt,name=env,proto3,oneof" json:"env,omitempty"`
	// Start.cwd and RunRequest.cwd are honored; false means a request that carries cwd is refused (UNSUPPORTED). Order:
	// false < true.
	Cwd *bool `protobuf:"varint,9,opt,name=cwd,proto3,oneof" json:"cwd,omitempty"`
	// How the target provides the current directory of the session. Order: NONE < PULL < PUSH.
	CwdFact FactMode `protobuf:"varint,10,opt,name=cwd_fact,json=cwdFact,proto3,enum=omniview.sdk.exec.v2.FactMode" json:"cwd_fact,omitempty"`
	// How the target provides whether a command is running in the foreground. Order: NONE < PULL < PUSH.
	ForegroundFact FactMode `protobuf:"varint,11,opt,name=foreground_fact,json=foregroundFact,proto3,enum=omniview.sdk.exec.v2.FactMode" json:"foreground_fact,omitempty"`
	// What outlives a restart of the host. Order: NONE < TARGET_PROCESS < TARGET_SESSION.
	HostRestart Survival `protobuf:"varint,12,opt,name=host_restart,json=hostRestart,proto3,enum=omniview.sdk.exec.v2.Survival" json:"host_restart,omitempty"`
	// What the target can say about how a command ended. Order: NONE < CODE < CODE_AND_SIGNAL.
	ExitReport ExitReport `protobuf:"varint,13,opt,name=exit_report,json=exitReport,proto3,enum=omniview.sdk.exec.v2.ExitReport" json:"exit_report,omitempty"`
	// What a reader that stops reading does to the target. Order: LOSSY < PROPAGATES.
	Stall StallBehavior `protobuf:"varint,14,opt,name=stall,proto3,enum=omniview.sdk.exec.v2.StallBehavior" json:"stall,omitempty"`
	// The author reports the health of the transport to the target (Link frames). False means the consumer sees only
	// the health of the host-to-plugin leg. Order: false < true.
	LinkHealth *bool `protobuf:"varint,15,opt,name=link_health,json=linkHealth,proto3,oneof" json:"link_health,omitempty"`
	// The integration modes the target accepts; never applied without the host's consent flag. A present empty set means
	// none. Order: inclusion.
	Integration   *IntegrationModeSet `protobuf:"bytes,16,opt,name=integration,proto3" json:"integration,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Capabilities) Reset() {
	*x = Capabilities{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[6]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Capabilities) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Capabilities) ProtoMessage() {}

func (x *Capabilities) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[6]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Capabilities.ProtoReflect.Descriptor instead.
func (*Capabilities) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{6}
}

func (x *Capabilities) GetTty() bool {
	if x != nil && x.Tty != nil {
		return *x.Tty
	}
	return false
}

func (x *Capabilities) GetSeparateStderr() bool {
	if x != nil && x.SeparateStderr != nil {
		return *x.SeparateStderr
	}
	return false
}

func (x *Capabilities) GetResize() bool {
	if x != nil && x.Resize != nil {
		return *x.Resize
	}
	return false
}

func (x *Capabilities) GetInitialSize() bool {
	if x != nil && x.InitialSize != nil {
		return *x.InitialSize
	}
	return false
}

func (x *Capabilities) GetStdinEof() bool {
	if x != nil && x.StdinEof != nil {
		return *x.StdinEof
	}
	return false
}

func (x *Capabilities) GetSignals() *SignalSet {
	if x != nil {
		return x.Signals
	}
	return nil
}

func (x *Capabilities) GetKill() KillBehavior {
	if x != nil {
		return x.Kill
	}
	return KillBehavior_KILL_BEHAVIOR_UNSPECIFIED
}

func (x *Capabilities) GetEnv() bool {
	if x != nil && x.Env != nil {
		return *x.Env
	}
	return false
}

func (x *Capabilities) GetCwd() bool {
	if x != nil && x.Cwd != nil {
		return *x.Cwd
	}
	return false
}

func (x *Capabilities) GetCwdFact() FactMode {
	if x != nil {
		return x.CwdFact
	}
	return FactMode_FACT_MODE_UNSPECIFIED
}

func (x *Capabilities) GetForegroundFact() FactMode {
	if x != nil {
		return x.ForegroundFact
	}
	return FactMode_FACT_MODE_UNSPECIFIED
}

func (x *Capabilities) GetHostRestart() Survival {
	if x != nil {
		return x.HostRestart
	}
	return Survival_SURVIVAL_UNSPECIFIED
}

func (x *Capabilities) GetExitReport() ExitReport {
	if x != nil {
		return x.ExitReport
	}
	return ExitReport_EXIT_REPORT_UNSPECIFIED
}

func (x *Capabilities) GetStall() StallBehavior {
	if x != nil {
		return x.Stall
	}
	return StallBehavior_STALL_BEHAVIOR_UNSPECIFIED
}

func (x *Capabilities) GetLinkHealth() bool {
	if x != nil && x.LinkHealth != nil {
		return *x.LinkHealth
	}
	return false
}

func (x *Capabilities) GetIntegration() *IntegrationModeSet {
	if x != nil {
		return x.Integration
	}
	return nil
}

// SessionLimits are the bounds an interactive session is served under.
//
// Set by: the plugin's author (exec.SessionLimits, zero fields take the SDK's defaults); the SDK reports the
// effective value in Describe and in each session's facts.
// Absent: in Describe, TargetInfo.session absent means the target serves no interactive sessions. A present section
// carries effective, non-zero bounds: the SDK never sends an all-zero section, and a consumer refuses a present
// all-zero section as a malformed description. A single zero field inside a non-zero section, from another producer,
// reads as unstated (unknown), never unlimited.
type SessionLimits struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// The most sessions the plugin holds at once for this target kind.
	MaxSessions uint32 `protobuf:"varint,1,opt,name=max_sessions,json=maxSessions,proto3" json:"max_sessions,omitempty"`
	// The largest ClientFrame.input, in bytes.
	MaxInputFrame uint32 `protobuf:"varint,2,opt,name=max_input_frame,json=maxInputFrame,proto3" json:"max_input_frame,omitempty"`
	// The replay ring a session gets when Start.replay_bytes is zero, in bytes.
	ReplayBytes uint32 `protobuf:"varint,3,opt,name=replay_bytes,json=replayBytes,proto3" json:"replay_bytes,omitempty"`
	// The largest replay ring Start.replay_bytes may ask for, in bytes.
	MaxReplayBytes uint32 `protobuf:"varint,4,opt,name=max_replay_bytes,json=maxReplayBytes,proto3" json:"max_replay_bytes,omitempty"`
	// The lease a detached session gets when Start.detach_timeout_ms or Detach.lease_ms is zero, in milliseconds.
	DetachTimeoutMs uint32 `protobuf:"varint,5,opt,name=detach_timeout_ms,json=detachTimeoutMs,proto3" json:"detach_timeout_ms,omitempty"`
	// The largest lease the host may ask for, in milliseconds.
	MaxDetachTimeoutMs uint32 `protobuf:"varint,6,opt,name=max_detach_timeout_ms,json=maxDetachTimeoutMs,proto3" json:"max_detach_timeout_ms,omitempty"`
	// The largest value of either dimension of a Size.
	MaxDimension  uint32 `protobuf:"varint,7,opt,name=max_dimension,json=maxDimension,proto3" json:"max_dimension,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SessionLimits) Reset() {
	*x = SessionLimits{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[7]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SessionLimits) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SessionLimits) ProtoMessage() {}

func (x *SessionLimits) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[7]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SessionLimits.ProtoReflect.Descriptor instead.
func (*SessionLimits) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{7}
}

func (x *SessionLimits) GetMaxSessions() uint32 {
	if x != nil {
		return x.MaxSessions
	}
	return 0
}

func (x *SessionLimits) GetMaxInputFrame() uint32 {
	if x != nil {
		return x.MaxInputFrame
	}
	return 0
}

func (x *SessionLimits) GetReplayBytes() uint32 {
	if x != nil {
		return x.ReplayBytes
	}
	return 0
}

func (x *SessionLimits) GetMaxReplayBytes() uint32 {
	if x != nil {
		return x.MaxReplayBytes
	}
	return 0
}

func (x *SessionLimits) GetDetachTimeoutMs() uint32 {
	if x != nil {
		return x.DetachTimeoutMs
	}
	return 0
}

func (x *SessionLimits) GetMaxDetachTimeoutMs() uint32 {
	if x != nil {
		return x.MaxDetachTimeoutMs
	}
	return 0
}

func (x *SessionLimits) GetMaxDimension() uint32 {
	if x != nil {
		return x.MaxDimension
	}
	return 0
}

// RunLimits are the bounds a bounded run is served under.
//
// Set by: the plugin's author (exec.RunLimits, zero fields take the SDK's defaults); the SDK reports the effective
// value in Describe. Absent: in Describe, TargetInfo.run absent means the target serves no runs. A present section
// carries effective, non-zero bounds: the SDK never sends an all-zero section, and a consumer refuses a present
// all-zero section as a malformed description. A single zero field inside a non-zero section, from another producer,
// reads as unstated (unknown), never unlimited.
type RunLimits struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// The timeout a run gets when RunRequest.timeout_ms is zero, in milliseconds.
	DefaultTimeoutMs uint32 `protobuf:"varint,1,opt,name=default_timeout_ms,json=defaultTimeoutMs,proto3" json:"default_timeout_ms,omitempty"`
	// The largest RunRequest.timeout_ms; above it the run is refused (INVALID_REQUEST), never clamped.
	MaxTimeoutMs uint32 `protobuf:"varint,2,opt,name=max_timeout_ms,json=maxTimeoutMs,proto3" json:"max_timeout_ms,omitempty"`
	// The per-stream output cap a run gets when OutputLimits is zero, in bytes.
	DefaultOutputBytes uint32 `protobuf:"varint,3,opt,name=default_output_bytes,json=defaultOutputBytes,proto3" json:"default_output_bytes,omitempty"`
	// The largest per-stream output cap; above it the run is refused, never clamped.
	MaxOutputBytes uint32 `protobuf:"varint,4,opt,name=max_output_bytes,json=maxOutputBytes,proto3" json:"max_output_bytes,omitempty"`
	// The largest RunRequest.stdin, in bytes.
	MaxStdinBytes uint32 `protobuf:"varint,5,opt,name=max_stdin_bytes,json=maxStdinBytes,proto3" json:"max_stdin_bytes,omitempty"`
	// The most runs in flight at once for this target kind.
	MaxConcurrent uint32 `protobuf:"varint,6,opt,name=max_concurrent,json=maxConcurrent,proto3" json:"max_concurrent,omitempty"`
	// The largest total size of RunRequest.argv, in bytes.
	MaxArgvBytes uint32 `protobuf:"varint,7,opt,name=max_argv_bytes,json=maxArgvBytes,proto3" json:"max_argv_bytes,omitempty"`
	// The largest total size of RunRequest.env names and values, in bytes.
	MaxEnvBytes uint32 `protobuf:"varint,8,opt,name=max_env_bytes,json=maxEnvBytes,proto3" json:"max_env_bytes,omitempty"`
	// How long after the command's exit the SDK keeps reading output, in milliseconds; when it is reached the run's
	// output completeness is UNKNOWN.
	PostExitDrainMs uint32 `protobuf:"varint,9,opt,name=post_exit_drain_ms,json=postExitDrainMs,proto3" json:"post_exit_drain_ms,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *RunLimits) Reset() {
	*x = RunLimits{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[8]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RunLimits) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RunLimits) ProtoMessage() {}

func (x *RunLimits) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[8]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RunLimits.ProtoReflect.Descriptor instead.
func (*RunLimits) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{8}
}

func (x *RunLimits) GetDefaultTimeoutMs() uint32 {
	if x != nil {
		return x.DefaultTimeoutMs
	}
	return 0
}

func (x *RunLimits) GetMaxTimeoutMs() uint32 {
	if x != nil {
		return x.MaxTimeoutMs
	}
	return 0
}

func (x *RunLimits) GetDefaultOutputBytes() uint32 {
	if x != nil {
		return x.DefaultOutputBytes
	}
	return 0
}

func (x *RunLimits) GetMaxOutputBytes() uint32 {
	if x != nil {
		return x.MaxOutputBytes
	}
	return 0
}

func (x *RunLimits) GetMaxStdinBytes() uint32 {
	if x != nil {
		return x.MaxStdinBytes
	}
	return 0
}

func (x *RunLimits) GetMaxConcurrent() uint32 {
	if x != nil {
		return x.MaxConcurrent
	}
	return 0
}

func (x *RunLimits) GetMaxArgvBytes() uint32 {
	if x != nil {
		return x.MaxArgvBytes
	}
	return 0
}

func (x *RunLimits) GetMaxEnvBytes() uint32 {
	if x != nil {
		return x.MaxEnvBytes
	}
	return 0
}

func (x *RunLimits) GetPostExitDrainMs() uint32 {
	if x != nil {
		return x.PostExitDrainMs
	}
	return 0
}

// TargetInfo describes one kind of thing a command can run in.
//
// Set by: the plugin's author (exec.Target.Info), which carries the routing key. provider and type are the resource
// address provider and type a TargetRef.reference must carry to reach this kind. display_name is presentation data
// from the author ("Pod"), bounded to 128 bytes. default_argv is the command a Start with an empty argv runs; empty
// means a Start must give an argv. capabilities: the kind's UPPER BOUND (see Capabilities). session and run: see the
// limits messages; a section is either absent (not served) or present with effective, non-zero bounds. both absent
// means a target that only offers Options.
type TargetInfo struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Provider      string                 `protobuf:"bytes,1,opt,name=provider,proto3" json:"provider,omitempty"`
	Type          string                 `protobuf:"bytes,2,opt,name=type,proto3" json:"type,omitempty"`
	DisplayName   string                 `protobuf:"bytes,3,opt,name=display_name,json=displayName,proto3" json:"display_name,omitempty"`
	DefaultArgv   []string               `protobuf:"bytes,4,rep,name=default_argv,json=defaultArgv,proto3" json:"default_argv,omitempty"`
	Capabilities  *Capabilities          `protobuf:"bytes,5,opt,name=capabilities,proto3" json:"capabilities,omitempty"`
	Session       *SessionLimits         `protobuf:"bytes,6,opt,name=session,proto3" json:"session,omitempty"`
	Run           *RunLimits             `protobuf:"bytes,7,opt,name=run,proto3" json:"run,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *TargetInfo) Reset() {
	*x = TargetInfo{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[9]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *TargetInfo) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*TargetInfo) ProtoMessage() {}

func (x *TargetInfo) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[9]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use TargetInfo.ProtoReflect.Descriptor instead.
func (*TargetInfo) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{9}
}

func (x *TargetInfo) GetProvider() string {
	if x != nil {
		return x.Provider
	}
	return ""
}

func (x *TargetInfo) GetType() string {
	if x != nil {
		return x.Type
	}
	return ""
}

func (x *TargetInfo) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *TargetInfo) GetDefaultArgv() []string {
	if x != nil {
		return x.DefaultArgv
	}
	return nil
}

func (x *TargetInfo) GetCapabilities() *Capabilities {
	if x != nil {
		return x.Capabilities
	}
	return nil
}

func (x *TargetInfo) GetSession() *SessionLimits {
	if x != nil {
		return x.Session
	}
	return nil
}

func (x *TargetInfo) GetRun() *RunLimits {
	if x != nil {
		return x.Run
	}
	return nil
}

type DescribeRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *DescribeRequest) Reset() {
	*x = DescribeRequest{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[10]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DescribeRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DescribeRequest) ProtoMessage() {}

func (x *DescribeRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[10]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DescribeRequest.ProtoReflect.Descriptor instead.
func (*DescribeRequest) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{10}
}

// DescribeResponse is what a plugin can run commands in and which parts of the contract it serves.
//
// Set by: the SDK. contract_version is the version of this file's contract that the SDK build implements (1 for the
// first release); features lists what this build serves; targets lists the registered kinds in registration order.
// Absent: an empty features list means the build serves Describe and Options and nothing else.
type DescribeResponse struct {
	state           protoimpl.MessageState `protogen:"open.v1"`
	ContractVersion uint32                 `protobuf:"varint,1,opt,name=contract_version,json=contractVersion,proto3" json:"contract_version,omitempty"`
	Features        []Feature              `protobuf:"varint,2,rep,packed,name=features,proto3,enum=omniview.sdk.exec.v2.Feature" json:"features,omitempty"`
	Targets         []*TargetInfo          `protobuf:"bytes,3,rep,name=targets,proto3" json:"targets,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *DescribeResponse) Reset() {
	*x = DescribeResponse{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[11]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DescribeResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DescribeResponse) ProtoMessage() {}

func (x *DescribeResponse) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[11]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DescribeResponse.ProtoReflect.Descriptor instead.
func (*DescribeResponse) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{11}
}

func (x *DescribeResponse) GetContractVersion() uint32 {
	if x != nil {
		return x.ContractVersion
	}
	return 0
}

func (x *DescribeResponse) GetFeatures() []Feature {
	if x != nil {
		return x.Features
	}
	return nil
}

func (x *DescribeResponse) GetTargets() []*TargetInfo {
	if x != nil {
		return x.Targets
	}
	return nil
}

// OptionsRequest asks what a user can choose for one resource. target.selection must be empty.
type OptionsRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Target        *TargetRef             `protobuf:"bytes,1,opt,name=target,proto3" json:"target,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *OptionsRequest) Reset() {
	*x = OptionsRequest{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[12]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *OptionsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*OptionsRequest) ProtoMessage() {}

func (x *OptionsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[12]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use OptionsRequest.ProtoReflect.Descriptor instead.
func (*OptionsRequest) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{12}
}

func (x *OptionsRequest) GetTarget() *TargetRef {
	if x != nil {
		return x.Target
	}
	return nil
}

// Choice is one thing a user can pick for an option (one container of a Pod).
//
// Set by: the plugin's author (exec.Choice). value is the string a TargetRef.selection carries (at most 256
// bytes); display_name is presentation data (at most 256 bytes, no control characters); detail and kind are closed
// codes matching ^[a-z0-9_.-]{1,40}$, never prose ("crash-loop", "init"); empty means none. is_default marks the
// choice a Start runs in when the selection omits the option (at most one choice of an option).
//
// capabilities is the MODE of the choice: the complete set a session that selects it is served with, declared
// before anything opens. It follows the narrowing rule of Capabilities (the author starts from the kind's upper
// bound and only narrows, each field in the order written at the field). The SDK applies it at admission, so
// Opened carries the kind's bound narrowed by every choice the selection names (the default choice of an option it
// omits), and a host can show "attach: no exit status, survives a host restart" before it opens anything. Absent
// means the choice does not narrow. A field wider than the kind's is a violation: the SDK clamps it to the bound
// and names the field. Started may narrow further what is learned only at run time; it never carries the mode.
//
// no_program says that a session which selects this choice runs no program: it joins one that already runs (a Kubernetes attach joins
// the container's own process). Opened and Started then carry no program (absent, never the target's default argv), a request that
// names an argv of its own is refused (UNSUPPORTED) and so is a Run, which is a program to run. False, the common case, is a choice
// whose session runs the program the request names. A host reads it with the options, before it opens anything.
type Choice struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Value         string                 `protobuf:"bytes,1,opt,name=value,proto3" json:"value,omitempty"`
	DisplayName   string                 `protobuf:"bytes,2,opt,name=display_name,json=displayName,proto3" json:"display_name,omitempty"`
	State         ChoiceState            `protobuf:"varint,3,opt,name=state,proto3,enum=omniview.sdk.exec.v2.ChoiceState" json:"state,omitempty"`
	Detail        string                 `protobuf:"bytes,4,opt,name=detail,proto3" json:"detail,omitempty"`
	IsDefault     bool                   `protobuf:"varint,5,opt,name=is_default,json=isDefault,proto3" json:"is_default,omitempty"`
	Kind          string                 `protobuf:"bytes,6,opt,name=kind,proto3" json:"kind,omitempty"`
	Capabilities  *Capabilities          `protobuf:"bytes,7,opt,name=capabilities,proto3" json:"capabilities,omitempty"`
	NoProgram     bool                   `protobuf:"varint,8,opt,name=no_program,json=noProgram,proto3" json:"no_program,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Choice) Reset() {
	*x = Choice{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[13]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Choice) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Choice) ProtoMessage() {}

func (x *Choice) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[13]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Choice.ProtoReflect.Descriptor instead.
func (*Choice) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{13}
}

func (x *Choice) GetValue() string {
	if x != nil {
		return x.Value
	}
	return ""
}

func (x *Choice) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *Choice) GetState() ChoiceState {
	if x != nil {
		return x.State
	}
	return ChoiceState_CHOICE_STATE_UNSPECIFIED
}

func (x *Choice) GetDetail() string {
	if x != nil {
		return x.Detail
	}
	return ""
}

func (x *Choice) GetIsDefault() bool {
	if x != nil {
		return x.IsDefault
	}
	return false
}

func (x *Choice) GetKind() string {
	if x != nil {
		return x.Kind
	}
	return ""
}

func (x *Choice) GetCapabilities() *Capabilities {
	if x != nil {
		return x.Capabilities
	}
	return nil
}

func (x *Choice) GetNoProgram() bool {
	if x != nil {
		return x.NoProgram
	}
	return false
}

// Option is one dimension a user chooses along ("container") and its choices.
//
// Set by: the plugin's author (exec.Option). name is the selection key, a closed code matching
// ^[a-z0-9_.-]{1,40}$. required means a Start for this resource must carry the option in its selection or have a
// default choice. An option with no choices is present and empty: the resource has nothing to choose now.
type Option struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Name          string                 `protobuf:"bytes,1,opt,name=name,proto3" json:"name,omitempty"`
	Choices       []*Choice              `protobuf:"bytes,2,rep,name=choices,proto3" json:"choices,omitempty"`
	Required      bool                   `protobuf:"varint,3,opt,name=required,proto3" json:"required,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Option) Reset() {
	*x = Option{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[14]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Option) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Option) ProtoMessage() {}

func (x *Option) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[14]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Option.ProtoReflect.Descriptor instead.
func (*Option) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{14}
}

func (x *Option) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *Option) GetChoices() []*Choice {
	if x != nil {
		return x.Choices
	}
	return nil
}

func (x *Option) GetRequired() bool {
	if x != nil {
		return x.Required
	}
	return false
}

// OptionsResponse lists the options of one resource.
//
// Set by: the SDK, from the author's callback. Bounds: at most 8 options and 256 choices per option. observed_at is
// the SDK's clock when the callback returned, in Unix milliseconds. An empty options list means the target has
// nothing to choose.
type OptionsResponse struct {
	state            protoimpl.MessageState `protogen:"open.v1"`
	Options          []*Option              `protobuf:"bytes,1,rep,name=options,proto3" json:"options,omitempty"`
	ObservedAtUnixMs int64                  `protobuf:"varint,2,opt,name=observed_at_unix_ms,json=observedAtUnixMs,proto3" json:"observed_at_unix_ms,omitempty"`
	unknownFields    protoimpl.UnknownFields
	sizeCache        protoimpl.SizeCache
}

func (x *OptionsResponse) Reset() {
	*x = OptionsResponse{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[15]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *OptionsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*OptionsResponse) ProtoMessage() {}

func (x *OptionsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[15]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use OptionsResponse.ProtoReflect.Descriptor instead.
func (*OptionsResponse) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{15}
}

func (x *OptionsResponse) GetOptions() []*Option {
	if x != nil {
		return x.Options
	}
	return nil
}

func (x *OptionsResponse) GetObservedAtUnixMs() int64 {
	if x != nil {
		return x.ObservedAtUnixMs
	}
	return 0
}

// IntegrationRequest asks for shell integration in a target.
//
// Set by: the host. consent is the host's assertion that the user agreed to send bootstrap code to this target; any
// mode but UNSPECIFIED or NONE without it is refused INVALID_REQUEST / CONSENT_REQUIRED. Absent, UNSPECIFIED and NONE
// all mean no integration.
type IntegrationRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Mode          IntegrationMode        `protobuf:"varint,1,opt,name=mode,proto3,enum=omniview.sdk.exec.v2.IntegrationMode" json:"mode,omitempty"`
	Consent       bool                   `protobuf:"varint,2,opt,name=consent,proto3" json:"consent,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *IntegrationRequest) Reset() {
	*x = IntegrationRequest{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[16]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *IntegrationRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*IntegrationRequest) ProtoMessage() {}

func (x *IntegrationRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[16]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use IntegrationRequest.ProtoReflect.Descriptor instead.
func (*IntegrationRequest) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{16}
}

func (x *IntegrationRequest) GetMode() IntegrationMode {
	if x != nil {
		return x.Mode
	}
	return IntegrationMode_INTEGRATION_MODE_UNSPECIFIED
}

func (x *IntegrationRequest) GetConsent() bool {
	if x != nil {
		return x.Consent
	}
	return false
}

// Start is the request to run a new command in a session.
//
// Set by: the host, in OpenRequest.start. Strict (see the file header). target: required. argv: empty means the
// target's default_argv (never a shell string; each element at most 32 KiB, no NUL; at most 1,024 elements and
// 64 KiB in all). tty: ask for a terminal; refused UNSUPPORTED / TTY_UNSUPPORTED when the target cannot. size:
// the initial size (zero means the SDK picks 80 x 24 for a tty and sends NOTICE_KIND_SIZE_DEFAULTED); at most
// SessionLimits.max_dimension. env: only if Capabilities.env; one NameValue per variable, each name once, matching
// [A-Za-z_][A-Za-z0-9_]* (at most 256 bytes), at most 256 entries, values without NUL and at most 32 KiB each, at most
// 64 KiB in all; its names, never its values, may appear in any audit. cwd: only if Capabilities.cwd, at most 4,096
// bytes, no NUL. labels: opaque to the SDK and
// never authority; at most 16 NameValue entries, names unique and at most 64 bytes, values at most 256 bytes, UTF-8
// without control characters. detach_timeout_ms: the lease; zero means SessionLimits.detach_timeout_ms, at most
// max_detach_timeout_ms. replay_bytes: the replay ring; zero means SessionLimits.replay_bytes, at most
// max_replay_bytes. features: opt-in features, only FEATURE_MARKS and FEATURE_MODES, each only if
// DescribeResponse.features lists it. integration: only if FEATURE_INTEGRATION is listed and the target lists the
// mode; absent means none.
type Start struct {
	state           protoimpl.MessageState `protogen:"open.v1"`
	Target          *TargetRef             `protobuf:"bytes,1,opt,name=target,proto3" json:"target,omitempty"`
	Argv            []string               `protobuf:"bytes,2,rep,name=argv,proto3" json:"argv,omitempty"`
	Tty             bool                   `protobuf:"varint,3,opt,name=tty,proto3" json:"tty,omitempty"`
	Size            *Size                  `protobuf:"bytes,4,opt,name=size,proto3" json:"size,omitempty"`
	Env             []*NameValue           `protobuf:"bytes,5,rep,name=env,proto3" json:"env,omitempty"`
	Cwd             string                 `protobuf:"bytes,6,opt,name=cwd,proto3" json:"cwd,omitempty"`
	Labels          []*NameValue           `protobuf:"bytes,7,rep,name=labels,proto3" json:"labels,omitempty"`
	DetachTimeoutMs uint32                 `protobuf:"varint,8,opt,name=detach_timeout_ms,json=detachTimeoutMs,proto3" json:"detach_timeout_ms,omitempty"`
	ReplayBytes     uint32                 `protobuf:"varint,9,opt,name=replay_bytes,json=replayBytes,proto3" json:"replay_bytes,omitempty"`
	Features        []Feature              `protobuf:"varint,10,rep,packed,name=features,proto3,enum=omniview.sdk.exec.v2.Feature" json:"features,omitempty"`
	Integration     *IntegrationRequest    `protobuf:"bytes,11,opt,name=integration,proto3" json:"integration,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *Start) Reset() {
	*x = Start{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[17]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Start) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Start) ProtoMessage() {}

func (x *Start) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[17]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Start.ProtoReflect.Descriptor instead.
func (*Start) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{17}
}

func (x *Start) GetTarget() *TargetRef {
	if x != nil {
		return x.Target
	}
	return nil
}

func (x *Start) GetArgv() []string {
	if x != nil {
		return x.Argv
	}
	return nil
}

func (x *Start) GetTty() bool {
	if x != nil {
		return x.Tty
	}
	return false
}

func (x *Start) GetSize() *Size {
	if x != nil {
		return x.Size
	}
	return nil
}

func (x *Start) GetEnv() []*NameValue {
	if x != nil {
		return x.Env
	}
	return nil
}

func (x *Start) GetCwd() string {
	if x != nil {
		return x.Cwd
	}
	return ""
}

func (x *Start) GetLabels() []*NameValue {
	if x != nil {
		return x.Labels
	}
	return nil
}

func (x *Start) GetDetachTimeoutMs() uint32 {
	if x != nil {
		return x.DetachTimeoutMs
	}
	return 0
}

func (x *Start) GetReplayBytes() uint32 {
	if x != nil {
		return x.ReplayBytes
	}
	return 0
}

func (x *Start) GetFeatures() []Feature {
	if x != nil {
		return x.Features
	}
	return nil
}

func (x *Start) GetIntegration() *IntegrationRequest {
	if x != nil {
		return x.Integration
	}
	return nil
}

// Attach takes over an existing session. The older stream of the session, if any, receives Detached with
// DETACH_REASON_SUPERSEDED and ends: the session continues, attached to the new stream, and is not ended.
//
// Set by: the host, in OpenRequest.attach. from_offset is the first output offset the host does not have.
// Replay.start is the first ground-state boundary (not inside an escape sequence or a UTF-8 sequence) at or after
// from_offset that the ring still holds: a cursor already on a retained boundary resumes there exactly (start =
// from_offset, nothing skipped, nothing repeated); a cursor inside a sequence, or before the oldest retained byte,
// resumes at the next boundary, and [from_offset, start) is what is not replayed (bytes dropped from the ring, or the
// tail of a sequence the host had begun: the host discards its incomplete sequence). A replay never starts before
// from_offset, so no byte is repeated. When the ring holds no ground-state boundary at or after from_offset (the
// output so far ends inside a sequence), nothing is replayed (start = end) and the live stream continues at end: its
// first bytes may complete a sequence that began before start, and the host discards that fragment the same way.
type Attach struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	FromOffset    uint64                 `protobuf:"varint,1,opt,name=from_offset,json=fromOffset,proto3" json:"from_offset,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Attach) Reset() {
	*x = Attach{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[18]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Attach) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Attach) ProtoMessage() {}

func (x *Attach) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[18]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Attach.ProtoReflect.Descriptor instead.
func (*Attach) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{18}
}

func (x *Attach) GetFromOffset() uint64 {
	if x != nil {
		return x.FromOffset
	}
	return 0
}

// OpenRequest is the first frame of Open.
//
// Set by: the host, once. session_id is chosen by the host: 1 to 128 bytes of printable ASCII (0x21-0x7e); a live
// id is CONFLICT / SESSION_EXISTS for a start, and an unknown id is NOT_FOUND / SESSION_UNKNOWN for an attach.
// Reuse of an id after its session ended is allowed only once the tombstone is gone; a host never reuses an id for
// a restart.
type OpenRequest struct {
	state     protoimpl.MessageState `protogen:"open.v1"`
	SessionId string                 `protobuf:"bytes,1,opt,name=session_id,json=sessionId,proto3" json:"session_id,omitempty"`
	// Types that are valid to be assigned to Kind:
	//
	//	*OpenRequest_Start
	//	*OpenRequest_Attach
	Kind          isOpenRequest_Kind `protobuf_oneof:"kind"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *OpenRequest) Reset() {
	*x = OpenRequest{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[19]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *OpenRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*OpenRequest) ProtoMessage() {}

func (x *OpenRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[19]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use OpenRequest.ProtoReflect.Descriptor instead.
func (*OpenRequest) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{19}
}

func (x *OpenRequest) GetSessionId() string {
	if x != nil {
		return x.SessionId
	}
	return ""
}

func (x *OpenRequest) GetKind() isOpenRequest_Kind {
	if x != nil {
		return x.Kind
	}
	return nil
}

func (x *OpenRequest) GetStart() *Start {
	if x != nil {
		if x, ok := x.Kind.(*OpenRequest_Start); ok {
			return x.Start
		}
	}
	return nil
}

func (x *OpenRequest) GetAttach() *Attach {
	if x != nil {
		if x, ok := x.Kind.(*OpenRequest_Attach); ok {
			return x.Attach
		}
	}
	return nil
}

type isOpenRequest_Kind interface {
	isOpenRequest_Kind()
}

type OpenRequest_Start struct {
	Start *Start `protobuf:"bytes,2,opt,name=start,proto3,oneof"`
}

type OpenRequest_Attach struct {
	Attach *Attach `protobuf:"bytes,3,opt,name=attach,proto3,oneof"`
}

func (*OpenRequest_Start) isOpenRequest_Kind() {}

func (*OpenRequest_Attach) isOpenRequest_Kind() {}

// ClientFrame is what the host sends on an Open stream. All frames of a session are ordered with each other (a
// resize sent before a paste stays before it). An unknown frame kind is ignored and counted.
type ClientFrame struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to Frame:
	//
	//	*ClientFrame_Open
	//	*ClientFrame_Input
	//	*ClientFrame_Resize
	//	*ClientFrame_Signal
	//	*ClientFrame_CloseInput
	//	*ClientFrame_Query
	//	*ClientFrame_Close
	//	*ClientFrame_Detach
	Frame         isClientFrame_Frame `protobuf_oneof:"frame"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ClientFrame) Reset() {
	*x = ClientFrame{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[20]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ClientFrame) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ClientFrame) ProtoMessage() {}

func (x *ClientFrame) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[20]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ClientFrame.ProtoReflect.Descriptor instead.
func (*ClientFrame) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{20}
}

func (x *ClientFrame) GetFrame() isClientFrame_Frame {
	if x != nil {
		return x.Frame
	}
	return nil
}

func (x *ClientFrame) GetOpen() *OpenRequest {
	if x != nil {
		if x, ok := x.Frame.(*ClientFrame_Open); ok {
			return x.Open
		}
	}
	return nil
}

func (x *ClientFrame) GetInput() []byte {
	if x != nil {
		if x, ok := x.Frame.(*ClientFrame_Input); ok {
			return x.Input
		}
	}
	return nil
}

func (x *ClientFrame) GetResize() *Resize {
	if x != nil {
		if x, ok := x.Frame.(*ClientFrame_Resize); ok {
			return x.Resize
		}
	}
	return nil
}

func (x *ClientFrame) GetSignal() Signal {
	if x != nil {
		if x, ok := x.Frame.(*ClientFrame_Signal); ok {
			return x.Signal
		}
	}
	return Signal_SIGNAL_UNSPECIFIED
}

func (x *ClientFrame) GetCloseInput() *CloseInput {
	if x != nil {
		if x, ok := x.Frame.(*ClientFrame_CloseInput); ok {
			return x.CloseInput
		}
	}
	return nil
}

func (x *ClientFrame) GetQuery() *Query {
	if x != nil {
		if x, ok := x.Frame.(*ClientFrame_Query); ok {
			return x.Query
		}
	}
	return nil
}

func (x *ClientFrame) GetClose() *Close {
	if x != nil {
		if x, ok := x.Frame.(*ClientFrame_Close); ok {
			return x.Close
		}
	}
	return nil
}

func (x *ClientFrame) GetDetach() *Detach {
	if x != nil {
		if x, ok := x.Frame.(*ClientFrame_Detach); ok {
			return x.Detach
		}
	}
	return nil
}

type isClientFrame_Frame interface {
	isClientFrame_Frame()
}

type ClientFrame_Open struct {
	// The first frame, once. A second open frame is a protocol violation and ends the stream.
	Open *OpenRequest `protobuf:"bytes,1,opt,name=open,proto3,oneof"`
}

type ClientFrame_Input struct {
	// Bytes for the command's stdin, at most SessionLimits.max_input_frame. The SDK stops receiving when the
	// command's stdin queue is full, which is the backpressure the host sees.
	Input []byte `protobuf:"bytes,2,opt,name=input,proto3,oneof"`
}

type ClientFrame_Resize struct {
	Resize *Resize `protobuf:"bytes,3,opt,name=resize,proto3,oneof"`
}

type ClientFrame_Signal struct {
	// Deliver a signal; only one of Capabilities.signals, otherwise refused.
	Signal Signal `protobuf:"varint,4,opt,name=signal,proto3,enum=omniview.sdk.exec.v2.Signal,oneof"`
}

type ClientFrame_CloseInput struct {
	// stdin EOF.
	CloseInput *CloseInput `protobuf:"bytes,5,opt,name=close_input,json=closeInput,proto3,oneof"`
}

type ClientFrame_Query struct {
	// Ask for a fact.
	Query *Query `protobuf:"bytes,6,opt,name=query,proto3,oneof"`
}

type ClientFrame_Close struct {
	// Destroy the session.
	Close *Close `protobuf:"bytes,7,opt,name=close,proto3,oneof"`
}

type ClientFrame_Detach struct {
	// Leave the session running and end THIS stream. A stream that just breaks is an implicit Detach with the
	// session's default lease.
	Detach *Detach `protobuf:"bytes,8,opt,name=detach,proto3,oneof"`
}

func (*ClientFrame_Open) isClientFrame_Frame() {}

func (*ClientFrame_Input) isClientFrame_Frame() {}

func (*ClientFrame_Resize) isClientFrame_Frame() {}

func (*ClientFrame_Signal) isClientFrame_Frame() {}

func (*ClientFrame_CloseInput) isClientFrame_Frame() {}

func (*ClientFrame_Query) isClientFrame_Frame() {}

func (*ClientFrame_Close) isClientFrame_Frame() {}

func (*ClientFrame_Detach) isClientFrame_Frame() {}

// Resize is a new size for the terminal. seq is chosen by the host and echoed in Resized; the SDK keeps only the
// newest pending size, so a storm of resizes costs one delivery. size must be a valid, non-zero Size.
type Resize struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Seq           uint32                 `protobuf:"varint,1,opt,name=seq,proto3" json:"seq,omitempty"`
	Size          *Size                  `protobuf:"bytes,2,opt,name=size,proto3" json:"size,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Resize) Reset() {
	*x = Resize{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[21]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Resize) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Resize) ProtoMessage() {}

func (x *Resize) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[21]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Resize.ProtoReflect.Descriptor instead.
func (*Resize) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{21}
}

func (x *Resize) GetSeq() uint32 {
	if x != nil {
		return x.Seq
	}
	return 0
}

func (x *Resize) GetSize() *Size {
	if x != nil {
		return x.Size
	}
	return nil
}

// CloseInput is stdin EOF; it carries nothing.
type CloseInput struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CloseInput) Reset() {
	*x = CloseInput{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[22]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CloseInput) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CloseInput) ProtoMessage() {}

func (x *CloseInput) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[22]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CloseInput.ProtoReflect.Descriptor instead.
func (*CloseInput) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{22}
}

// Close destroys the session. The stream then ends with Ended (closed, by request).
type Close struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Mode          CloseMode              `protobuf:"varint,1,opt,name=mode,proto3,enum=omniview.sdk.exec.v2.CloseMode" json:"mode,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Close) Reset() {
	*x = Close{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[23]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Close) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Close) ProtoMessage() {}

func (x *Close) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[23]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Close.ProtoReflect.Descriptor instead.
func (*Close) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{23}
}

func (x *Close) GetMode() CloseMode {
	if x != nil {
		return x.Mode
	}
	return CloseMode_CLOSE_MODE_UNSPECIFIED
}

// Detach leaves the session running for a lease and ends this stream. lease_ms zero means
// SessionLimits.detach_timeout_ms; above max_detach_timeout_ms it is clamped and Detached says what was applied.
type Detach struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	LeaseMs       uint32                 `protobuf:"varint,1,opt,name=lease_ms,json=leaseMs,proto3" json:"lease_ms,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Detach) Reset() {
	*x = Detach{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[24]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Detach) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Detach) ProtoMessage() {}

func (x *Detach) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[24]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Detach.ProtoReflect.Descriptor instead.
func (*Detach) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{24}
}

func (x *Detach) GetLeaseMs() uint32 {
	if x != nil {
		return x.LeaseMs
	}
	return 0
}

// Query asks for one fact. id is chosen by the host, at least 1, and echoed in the answering Fact. An unsupported
// kind is answered with a Fact whose value says unknown/unsupported, never with silence.
type Query struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Id            uint32                 `protobuf:"varint,1,opt,name=id,proto3" json:"id,omitempty"`
	Kind          FactKind               `protobuf:"varint,2,opt,name=kind,proto3,enum=omniview.sdk.exec.v2.FactKind" json:"kind,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Query) Reset() {
	*x = Query{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[25]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Query) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Query) ProtoMessage() {}

func (x *Query) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[25]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Query.ProtoReflect.Descriptor instead.
func (*Query) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{25}
}

func (x *Query) GetId() uint32 {
	if x != nil {
		return x.Id
	}
	return 0
}

func (x *Query) GetKind() FactKind {
	if x != nil {
		return x.Kind
	}
	return FactKind_FACT_KIND_UNSPECIFIED
}

// ServerFrame is what the plugin sends on an Open stream. A reader ignores, and counts, a frame kind it does not
// know. Opened is always first. EVERY STREAM THAT CAN STILL RECEIVE FRAMES ENDS WITH EXACTLY ONE OF TWO: Ended, the end
// of the SESSION, or Detached, the end of THIS STREAM while the session lives on. Nothing follows either of them on
// the stream. A stream whose connection breaks after Opened receives neither (the break is an implicit detach, and if
// the lease expires before another stream attaches the end is read from the tombstone), and an Open that is refused
// before Opened is a status with no frame at all. A session has exactly one END and AT MOST ONE Ended frame, however
// many streams it had: Ended goes to the stream attached when the session ends, never to a stream that was detached or
// superseded before, and never twice. A session that ends while no stream is attached (a lease that expires, a
// retirement, a shutdown) has no Ended frame at all: its end is the tombstone's (SessionInfo.ended_with).
type ServerFrame struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to Frame:
	//
	//	*ServerFrame_Opened
	//	*ServerFrame_Output
	//	*ServerFrame_Resized
	//	*ServerFrame_Mark
	//	*ServerFrame_Fact
	//	*ServerFrame_Dropped
	//	*ServerFrame_Notice
	//	*ServerFrame_Ended
	//	*ServerFrame_Started
	//	*ServerFrame_Link
	//	*ServerFrame_Detached
	Frame         isServerFrame_Frame `protobuf_oneof:"frame"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ServerFrame) Reset() {
	*x = ServerFrame{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[26]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ServerFrame) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ServerFrame) ProtoMessage() {}

func (x *ServerFrame) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[26]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ServerFrame.ProtoReflect.Descriptor instead.
func (*ServerFrame) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{26}
}

func (x *ServerFrame) GetFrame() isServerFrame_Frame {
	if x != nil {
		return x.Frame
	}
	return nil
}

func (x *ServerFrame) GetOpened() *Opened {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Opened); ok {
			return x.Opened
		}
	}
	return nil
}

func (x *ServerFrame) GetOutput() *Output {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Output); ok {
			return x.Output
		}
	}
	return nil
}

func (x *ServerFrame) GetResized() *Resized {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Resized); ok {
			return x.Resized
		}
	}
	return nil
}

func (x *ServerFrame) GetMark() *Mark {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Mark); ok {
			return x.Mark
		}
	}
	return nil
}

func (x *ServerFrame) GetFact() *Fact {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Fact); ok {
			return x.Fact
		}
	}
	return nil
}

func (x *ServerFrame) GetDropped() *Dropped {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Dropped); ok {
			return x.Dropped
		}
	}
	return nil
}

func (x *ServerFrame) GetNotice() *Notice {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Notice); ok {
			return x.Notice
		}
	}
	return nil
}

func (x *ServerFrame) GetEnded() *Ended {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Ended); ok {
			return x.Ended
		}
	}
	return nil
}

func (x *ServerFrame) GetStarted() *Started {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Started); ok {
			return x.Started
		}
	}
	return nil
}

func (x *ServerFrame) GetLink() *Link {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Link); ok {
			return x.Link
		}
	}
	return nil
}

func (x *ServerFrame) GetDetached() *Detached {
	if x != nil {
		if x, ok := x.Frame.(*ServerFrame_Detached); ok {
			return x.Detached
		}
	}
	return nil
}

type isServerFrame_Frame interface {
	isServerFrame_Frame()
}

type ServerFrame_Opened struct {
	Opened *Opened `protobuf:"bytes,1,opt,name=opened,proto3,oneof"`
}

type ServerFrame_Output struct {
	Output *Output `protobuf:"bytes,2,opt,name=output,proto3,oneof"`
}

type ServerFrame_Resized struct {
	Resized *Resized `protobuf:"bytes,3,opt,name=resized,proto3,oneof"`
}

type ServerFrame_Mark struct {
	Mark *Mark `protobuf:"bytes,4,opt,name=mark,proto3,oneof"`
}

type ServerFrame_Fact struct {
	Fact *Fact `protobuf:"bytes,5,opt,name=fact,proto3,oneof"`
}

type ServerFrame_Dropped struct {
	Dropped *Dropped `protobuf:"bytes,6,opt,name=dropped,proto3,oneof"`
}

type ServerFrame_Notice struct {
	Notice *Notice `protobuf:"bytes,7,opt,name=notice,proto3,oneof"`
}

type ServerFrame_Ended struct {
	// The end of the SESSION: how it ended and its accounts. Sent at most once in the life of a session, to the
	// stream attached at that moment (a session that ends while no stream is attached has none: its end is the
	// tombstone's); nothing follows it and the stream then ends with status OK.
	Ended *Ended `protobuf:"bytes,8,opt,name=ended,proto3,oneof"`
}

type ServerFrame_Started struct {
	// The target says the command is running: the session facts, refined by what the producer knows now (see Started).
	// At most one per session, always before the first Output.
	Started *Started `protobuf:"bytes,9,opt,name=started,proto3,oneof"`
}

type ServerFrame_Link struct {
	// The transport's health changed (only if Capabilities.link_health).
	Link *Link `protobuf:"bytes,10,opt,name=link,proto3,oneof"`
}

type ServerFrame_Detached struct {
	// The end of THIS STREAM while the session lives on: the answer to Detach (reason REQUESTED, with the lease) or
	// the notice to a stream that another attach took the session over from (reason SUPERSEDED). It is the last
	// frame of the stream and never the end of the session.
	Detached *Detached `protobuf:"bytes,11,opt,name=detached,proto3,oneof"`
}

func (*ServerFrame_Opened) isServerFrame_Frame() {}

func (*ServerFrame_Output) isServerFrame_Frame() {}

func (*ServerFrame_Resized) isServerFrame_Frame() {}

func (*ServerFrame_Mark) isServerFrame_Frame() {}

func (*ServerFrame_Fact) isServerFrame_Frame() {}

func (*ServerFrame_Dropped) isServerFrame_Frame() {}

func (*ServerFrame_Notice) isServerFrame_Frame() {}

func (*ServerFrame_Ended) isServerFrame_Frame() {}

func (*ServerFrame_Started) isServerFrame_Frame() {}

func (*ServerFrame_Link) isServerFrame_Frame() {}

func (*ServerFrame_Detached) isServerFrame_Frame() {}

// Opened is the first frame of an Open stream: the facts the SDK knows before the author runs and, for an attach,
// the description of the replay that follows.
//
// Set by: the SDK. facts: the target, the capabilities, the requested program and size, the limits. For a new session
// the capabilities are the kind's upper bound narrowed by the choices the selection names (Choice.capabilities: the
// session's mode); a Started may narrow them further, only by what is learned at run time. For an attach they are the
// session's latest effective set, as of its last Started, so a consumer that attaches later never offers what the
// session already reported it cannot do. For an attach the replay describes exactly the Output frames that follow,
// [start, end); the live stream continues at end with no byte lost or repeated (the SDK takes the ring snapshot and
// registers the stream under one lock). A new session has start = end = 0.
type Opened struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Facts         *SessionFacts          `protobuf:"bytes,1,opt,name=facts,proto3" json:"facts,omitempty"`
	Replay        *Replay                `protobuf:"bytes,2,opt,name=replay,proto3" json:"replay,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Opened) Reset() {
	*x = Opened{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[27]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Opened) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Opened) ProtoMessage() {}

func (x *Opened) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[27]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Opened.ProtoReflect.Descriptor instead.
func (*Opened) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{27}
}

func (x *Opened) GetFacts() *SessionFacts {
	if x != nil {
		return x.Facts
	}
	return nil
}

func (x *Opened) GetReplay() *Replay {
	if x != nil {
		return x.Replay
	}
	return nil
}

// Started is the session facts, refined by what the target says now (the resolved program, a pid, the tty and size
// really granted, and the capabilities of THIS session when it learned something narrower than Opened carried). It is
// sent at most once per session and always BEFORE the first Output: the author's report (Proc.Started) or, when the
// author wrote output before reporting, one the SDK makes from the facts it has (Opened's, so the capabilities are
// the ones the selection already narrowed); a report after the first Output, or a second one, is ignored and logged.
// A session that never writes output and never reports gets none. The mode of a session (exec or attach) is declared
// on Choice.capabilities and is in Opened; Started does not carry it.
type Started struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Facts         *SessionFacts          `protobuf:"bytes,1,opt,name=facts,proto3" json:"facts,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Started) Reset() {
	*x = Started{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[28]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Started) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Started) ProtoMessage() {}

func (x *Started) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[28]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Started.ProtoReflect.Descriptor instead.
func (*Started) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{28}
}

func (x *Started) GetFacts() *SessionFacts {
	if x != nil {
		return x.Facts
	}
	return nil
}

// Detached is the last frame of a stream whose session lives on (see ServerFrame): the answer to Detach, or the
// notice to the stream that another attach superseded. It is never the end of a session.
//
// reason: why the stream ended. lease_expires_unix_ms: when the SDK ends the session if nobody attaches, after
// clamping the requested lease to the maximum; zero when no lease runs (SUPERSEDED: the session is attached).
type Detached struct {
	state              protoimpl.MessageState `protogen:"open.v1"`
	LeaseExpiresUnixMs int64                  `protobuf:"varint,1,opt,name=lease_expires_unix_ms,json=leaseExpiresUnixMs,proto3" json:"lease_expires_unix_ms,omitempty"`
	Reason             DetachReason           `protobuf:"varint,2,opt,name=reason,proto3,enum=omniview.sdk.exec.v2.DetachReason" json:"reason,omitempty"`
	unknownFields      protoimpl.UnknownFields
	sizeCache          protoimpl.SizeCache
}

func (x *Detached) Reset() {
	*x = Detached{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[29]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Detached) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Detached) ProtoMessage() {}

func (x *Detached) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[29]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Detached.ProtoReflect.Descriptor instead.
func (*Detached) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{29}
}

func (x *Detached) GetLeaseExpiresUnixMs() int64 {
	if x != nil {
		return x.LeaseExpiresUnixMs
	}
	return 0
}

func (x *Detached) GetReason() DetachReason {
	if x != nil {
		return x.Reason
	}
	return DetachReason_DETACH_REASON_UNSPECIFIED
}

// TargetFacts identify the thing a session or run is in.
//
// Set by: the SDK, from the validated request. id is the resource address id; selection echoes the request's.
// display_name is the author's presentation name for the target kind ("Pod"), not the resource's.
type TargetFacts struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Provider      string                 `protobuf:"bytes,1,opt,name=provider,proto3" json:"provider,omitempty"`
	Type          string                 `protobuf:"bytes,2,opt,name=type,proto3" json:"type,omitempty"`
	Id            string                 `protobuf:"bytes,3,opt,name=id,proto3" json:"id,omitempty"`
	DisplayName   string                 `protobuf:"bytes,4,opt,name=display_name,json=displayName,proto3" json:"display_name,omitempty"`
	Selection     map[string]string      `protobuf:"bytes,5,rep,name=selection,proto3" json:"selection,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *TargetFacts) Reset() {
	*x = TargetFacts{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[30]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *TargetFacts) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*TargetFacts) ProtoMessage() {}

func (x *TargetFacts) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[30]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use TargetFacts.ProtoReflect.Descriptor instead.
func (*TargetFacts) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{30}
}

func (x *TargetFacts) GetProvider() string {
	if x != nil {
		return x.Provider
	}
	return ""
}

func (x *TargetFacts) GetType() string {
	if x != nil {
		return x.Type
	}
	return ""
}

func (x *TargetFacts) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *TargetFacts) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *TargetFacts) GetSelection() map[string]string {
	if x != nil {
		return x.Selection
	}
	return nil
}

// Program is what runs, as the producer knows it (the default shell resolved), not what was asked.
//
// Set by: the SDK from the request at Opened, refined by the author at Started. argv: the argv that was run
// (the requested one, or the default). name: the program's base name ("bash"), empty when unknown. path: its full
// path when the producer resolved one, empty when unknown.
type Program struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Argv          []string               `protobuf:"bytes,1,rep,name=argv,proto3" json:"argv,omitempty"`
	Name          string                 `protobuf:"bytes,2,opt,name=name,proto3" json:"name,omitempty"`
	Path          string                 `protobuf:"bytes,3,opt,name=path,proto3" json:"path,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Program) Reset() {
	*x = Program{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[31]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Program) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Program) ProtoMessage() {}

func (x *Program) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[31]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Program.ProtoReflect.Descriptor instead.
func (*Program) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{31}
}

func (x *Program) GetArgv() []string {
	if x != nil {
		return x.Argv
	}
	return nil
}

func (x *Program) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *Program) GetPath() string {
	if x != nil {
		return x.Path
	}
	return ""
}

// IntegrationFacts say what integration a session got.
//
// Set by: the SDK. mode is what was applied, never more than asked: NONE means none was applied, UNSPECIFIED that it
// is not known. nonce is the per-session value the integration script echoes inside its marks, for the host and the UI
// to verify marks they parse from bytes. SECRET: never logged, audited, put in a span, put in an error or shown to a
// model. (Generated String() of this message prints it: never log this message.) Empty means no nonce exists.
type IntegrationFacts struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Mode          IntegrationMode        `protobuf:"varint,1,opt,name=mode,proto3,enum=omniview.sdk.exec.v2.IntegrationMode" json:"mode,omitempty"`
	Nonce         string                 `protobuf:"bytes,2,opt,name=nonce,proto3" json:"nonce,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *IntegrationFacts) Reset() {
	*x = IntegrationFacts{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[32]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *IntegrationFacts) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*IntegrationFacts) ProtoMessage() {}

func (x *IntegrationFacts) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[32]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use IntegrationFacts.ProtoReflect.Descriptor instead.
func (*IntegrationFacts) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{32}
}

func (x *IntegrationFacts) GetMode() IntegrationMode {
	if x != nil {
		return x.Mode
	}
	return IntegrationMode_INTEGRATION_MODE_UNSPECIFIED
}

func (x *IntegrationFacts) GetNonce() string {
	if x != nil {
		return x.Nonce
	}
	return ""
}

// SessionFacts are the facts of one session.
//
// Set by: the SDK, at Opened and again, refined, at Started. A fact the producer does not know is absent (an optional
// field or a message) or, where the field says so, zero or empty. session_id: the host's id. target, program: see
// their messages. tty: whether the target gave the command a terminal; absent until the target says so (the host
// knows what it asked for, and a session whose author never calls Started keeps it absent). size: the terminal size
// the target applied; absent when none was applied or it is not known. capabilities: what THIS session can do,
// effective (see Capabilities): at Opened of a new session the kind's upper bound narrowed by the choices the selection
// names (of an attach, the latest effective set), at Started what the author reported or what the SDK made from
// Opened's, never wider than Opened carried. limits: as declared. created_at_unix_ms: when the SDK accepted the Open
// (zero: unknown). pid: the process id when the producer has one (a local process), else zero. transport: a closed
// code for the channel to the target ("k8s-ws-v5", "local-pty") matching ^[a-z0-9_.-]{1,40}$; empty means unknown.
// term: the TERM the target really applied (at most 64 bytes); empty means unknown.
type SessionFacts struct {
	state           protoimpl.MessageState `protogen:"open.v1"`
	SessionId       string                 `protobuf:"bytes,1,opt,name=session_id,json=sessionId,proto3" json:"session_id,omitempty"`
	Target          *TargetFacts           `protobuf:"bytes,2,opt,name=target,proto3" json:"target,omitempty"`
	Program         *Program               `protobuf:"bytes,3,opt,name=program,proto3" json:"program,omitempty"`
	Tty             *bool                  `protobuf:"varint,4,opt,name=tty,proto3,oneof" json:"tty,omitempty"`
	Size            *Size                  `protobuf:"bytes,5,opt,name=size,proto3" json:"size,omitempty"`
	Capabilities    *Capabilities          `protobuf:"bytes,6,opt,name=capabilities,proto3" json:"capabilities,omitempty"`
	Limits          *SessionLimits         `protobuf:"bytes,7,opt,name=limits,proto3" json:"limits,omitempty"`
	CreatedAtUnixMs int64                  `protobuf:"varint,8,opt,name=created_at_unix_ms,json=createdAtUnixMs,proto3" json:"created_at_unix_ms,omitempty"`
	Pid             uint32                 `protobuf:"varint,9,opt,name=pid,proto3" json:"pid,omitempty"`
	Transport       string                 `protobuf:"bytes,10,opt,name=transport,proto3" json:"transport,omitempty"`
	Term            string                 `protobuf:"bytes,11,opt,name=term,proto3" json:"term,omitempty"`
	Integration     *IntegrationFacts      `protobuf:"bytes,12,opt,name=integration,proto3" json:"integration,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *SessionFacts) Reset() {
	*x = SessionFacts{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[33]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SessionFacts) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SessionFacts) ProtoMessage() {}

func (x *SessionFacts) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[33]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SessionFacts.ProtoReflect.Descriptor instead.
func (*SessionFacts) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{33}
}

func (x *SessionFacts) GetSessionId() string {
	if x != nil {
		return x.SessionId
	}
	return ""
}

func (x *SessionFacts) GetTarget() *TargetFacts {
	if x != nil {
		return x.Target
	}
	return nil
}

func (x *SessionFacts) GetProgram() *Program {
	if x != nil {
		return x.Program
	}
	return nil
}

func (x *SessionFacts) GetTty() bool {
	if x != nil && x.Tty != nil {
		return *x.Tty
	}
	return false
}

func (x *SessionFacts) GetSize() *Size {
	if x != nil {
		return x.Size
	}
	return nil
}

func (x *SessionFacts) GetCapabilities() *Capabilities {
	if x != nil {
		return x.Capabilities
	}
	return nil
}

func (x *SessionFacts) GetLimits() *SessionLimits {
	if x != nil {
		return x.Limits
	}
	return nil
}

func (x *SessionFacts) GetCreatedAtUnixMs() int64 {
	if x != nil {
		return x.CreatedAtUnixMs
	}
	return 0
}

func (x *SessionFacts) GetPid() uint32 {
	if x != nil {
		return x.Pid
	}
	return 0
}

func (x *SessionFacts) GetTransport() string {
	if x != nil {
		return x.Transport
	}
	return ""
}

func (x *SessionFacts) GetTerm() string {
	if x != nil {
		return x.Term
	}
	return ""
}

func (x *SessionFacts) GetIntegration() *IntegrationFacts {
	if x != nil {
		return x.Integration
	}
	return nil
}

// TerminalModes are the input modes of the terminal at a point in the output, for a reload to restore.
//
// Set by: the SDK when FEATURE_MODES is tracked. A TerminalModes that is absent means the modes are not tracked
// (FEATURE_MODES not requested or not served): unknown. A present one states every mode it carries: false is "off",
// not "unknown", and an enum at UNSPECIFIED is not known. kitty_keyboard is the flag stack, bottom first, at most 16
// entries; an empty stack is empty.
type TerminalModes struct {
	state             protoimpl.MessageState `protogen:"open.v1"`
	AltScreen         bool                   `protobuf:"varint,1,opt,name=alt_screen,json=altScreen,proto3" json:"alt_screen,omitempty"`
	BracketedPaste    bool                   `protobuf:"varint,2,opt,name=bracketed_paste,json=bracketedPaste,proto3" json:"bracketed_paste,omitempty"`
	ApplicationCursor bool                   `protobuf:"varint,3,opt,name=application_cursor,json=applicationCursor,proto3" json:"application_cursor,omitempty"`
	FocusEvents       bool                   `protobuf:"varint,4,opt,name=focus_events,json=focusEvents,proto3" json:"focus_events,omitempty"`
	CursorHidden      bool                   `protobuf:"varint,5,opt,name=cursor_hidden,json=cursorHidden,proto3" json:"cursor_hidden,omitempty"`
	MouseTracking     MouseTracking          `protobuf:"varint,6,opt,name=mouse_tracking,json=mouseTracking,proto3,enum=omniview.sdk.exec.v2.MouseTracking" json:"mouse_tracking,omitempty"`
	MouseEncoding     MouseEncoding          `protobuf:"varint,7,opt,name=mouse_encoding,json=mouseEncoding,proto3,enum=omniview.sdk.exec.v2.MouseEncoding" json:"mouse_encoding,omitempty"`
	KittyKeyboard     []uint32               `protobuf:"varint,8,rep,packed,name=kitty_keyboard,json=kittyKeyboard,proto3" json:"kitty_keyboard,omitempty"`
	unknownFields     protoimpl.UnknownFields
	sizeCache         protoimpl.SizeCache
}

func (x *TerminalModes) Reset() {
	*x = TerminalModes{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[34]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *TerminalModes) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*TerminalModes) ProtoMessage() {}

func (x *TerminalModes) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[34]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use TerminalModes.ProtoReflect.Descriptor instead.
func (*TerminalModes) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{34}
}

func (x *TerminalModes) GetAltScreen() bool {
	if x != nil {
		return x.AltScreen
	}
	return false
}

func (x *TerminalModes) GetBracketedPaste() bool {
	if x != nil {
		return x.BracketedPaste
	}
	return false
}

func (x *TerminalModes) GetApplicationCursor() bool {
	if x != nil {
		return x.ApplicationCursor
	}
	return false
}

func (x *TerminalModes) GetFocusEvents() bool {
	if x != nil {
		return x.FocusEvents
	}
	return false
}

func (x *TerminalModes) GetCursorHidden() bool {
	if x != nil {
		return x.CursorHidden
	}
	return false
}

func (x *TerminalModes) GetMouseTracking() MouseTracking {
	if x != nil {
		return x.MouseTracking
	}
	return MouseTracking_MOUSE_TRACKING_UNSPECIFIED
}

func (x *TerminalModes) GetMouseEncoding() MouseEncoding {
	if x != nil {
		return x.MouseEncoding
	}
	return MouseEncoding_MOUSE_ENCODING_UNSPECIFIED
}

func (x *TerminalModes) GetKittyKeyboard() []uint32 {
	if x != nil {
		return x.KittyKeyboard
	}
	return nil
}

// Replay describes the Output frames that follow Opened.
//
// Set by: the SDK. start: the offset of the first replayed byte, a ground-state boundary (not inside an escape
// sequence or a UTF-8 sequence); start greater than Attach.from_offset means the bytes in between are not replayed
// (dropped from the ring, or the tail of a sequence the host had begun: see Attach). end:
// where the live stream continues. bytes: end - start, always. start_size: the size the bytes at start were
// produced for (the emulator replays at this size, then resizes); absent means unknown. start_modes: the modes in
// force at start; absent means not tracked. size, modes: the size and modes now (absent: not known, not tracked).
// Absent: a Replay on a new session is present with start = end = bytes = 0.
type Replay struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Start         uint64                 `protobuf:"varint,1,opt,name=start,proto3" json:"start,omitempty"`
	End           uint64                 `protobuf:"varint,2,opt,name=end,proto3" json:"end,omitempty"`
	Bytes         uint64                 `protobuf:"varint,3,opt,name=bytes,proto3" json:"bytes,omitempty"`
	StartSize     *Size                  `protobuf:"bytes,4,opt,name=start_size,json=startSize,proto3" json:"start_size,omitempty"`
	StartModes    *TerminalModes         `protobuf:"bytes,5,opt,name=start_modes,json=startModes,proto3" json:"start_modes,omitempty"`
	Size          *Size                  `protobuf:"bytes,6,opt,name=size,proto3" json:"size,omitempty"`
	Modes         *TerminalModes         `protobuf:"bytes,7,opt,name=modes,proto3" json:"modes,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Replay) Reset() {
	*x = Replay{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[35]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Replay) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Replay) ProtoMessage() {}

func (x *Replay) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[35]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Replay.ProtoReflect.Descriptor instead.
func (*Replay) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{35}
}

func (x *Replay) GetStart() uint64 {
	if x != nil {
		return x.Start
	}
	return 0
}

func (x *Replay) GetEnd() uint64 {
	if x != nil {
		return x.End
	}
	return 0
}

func (x *Replay) GetBytes() uint64 {
	if x != nil {
		return x.Bytes
	}
	return 0
}

func (x *Replay) GetStartSize() *Size {
	if x != nil {
		return x.StartSize
	}
	return nil
}

func (x *Replay) GetStartModes() *TerminalModes {
	if x != nil {
		return x.StartModes
	}
	return nil
}

func (x *Replay) GetSize() *Size {
	if x != nil {
		return x.Size
	}
	return nil
}

func (x *Replay) GetModes() *TerminalModes {
	if x != nil {
		return x.Modes
	}
	return nil
}

// Output is bytes the command wrote.
//
// Set by: the SDK. offset is the position of data[0] in the session's single output sequence (all streams, in
// arrival order); offsets are contiguous except across a Dropped range. The bytes are untouched: never decoded,
// recoded or repaired, and a frame may end inside a UTF-8 sequence or an escape sequence.
type Output struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Stream        Stream                 `protobuf:"varint,1,opt,name=stream,proto3,enum=omniview.sdk.exec.v2.Stream" json:"stream,omitempty"`
	Offset        uint64                 `protobuf:"varint,2,opt,name=offset,proto3" json:"offset,omitempty"`
	Data          []byte                 `protobuf:"bytes,3,opt,name=data,proto3" json:"data,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Output) Reset() {
	*x = Output{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[36]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Output) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Output) ProtoMessage() {}

func (x *Output) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[36]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Output.ProtoReflect.Descriptor instead.
func (*Output) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{36}
}

func (x *Output) GetStream() Stream {
	if x != nil {
		return x.Stream
	}
	return Stream_STREAM_UNSPECIFIED
}

func (x *Output) GetOffset() uint64 {
	if x != nil {
		return x.Offset
	}
	return 0
}

func (x *Output) GetData() []byte {
	if x != nil {
		return x.Data
	}
	return nil
}

// Resized answers a Resize. seq echoes Resize.seq of the newest resize that was processed; size is that size.
type Resized struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Seq           uint32                 `protobuf:"varint,1,opt,name=seq,proto3" json:"seq,omitempty"`
	Size          *Size                  `protobuf:"bytes,2,opt,name=size,proto3" json:"size,omitempty"`
	Result        ResizeResult           `protobuf:"varint,3,opt,name=result,proto3,enum=omniview.sdk.exec.v2.ResizeResult" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Resized) Reset() {
	*x = Resized{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[37]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Resized) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Resized) ProtoMessage() {}

func (x *Resized) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[37]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Resized.ProtoReflect.Descriptor instead.
func (*Resized) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{37}
}

func (x *Resized) GetSeq() uint32 {
	if x != nil {
		return x.Seq
	}
	return 0
}

func (x *Resized) GetSize() *Size {
	if x != nil {
		return x.Size
	}
	return nil
}

func (x *Resized) GetResult() ResizeResult {
	if x != nil {
		return x.Result
	}
	return ResizeResult_RESIZE_RESULT_UNSPECIFIED
}

// Mark is a typed shell-integration event parsed out of the output (FEATURE_MARKS). The bytes it came from are also
// delivered, untouched.
//
// Set by: the SDK's scanner. at_offset is the offset of the first byte of the sequence. exit_code is present only on
// COMMAND_END and only when the shell reported one (absent means unknown, not 0). cwd is set on CWD marks.
// command_id is assigned by the SDK, unique and monotonic in the session, and equal on the marks of one command.
// command_line is the unescaped command, at most 4 KiB, present only with trust NONCE (a nonce match, not proof of
// origin: see MarkTrust); it is content, and untrusted for any decision that needs authenticated provenance: never
// logged, audited or put in a model prompt without a gate. duration_ms is present on COMMAND_END when the SDK saw
// both the start and the end (absent means unknown), measured by the SDK's monotonic clock. redraw means the prompt
// was redrawn (a resize), not a new prompt.
type Mark struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Kind          MarkKind               `protobuf:"varint,1,opt,name=kind,proto3,enum=omniview.sdk.exec.v2.MarkKind" json:"kind,omitempty"`
	AtOffset      uint64                 `protobuf:"varint,2,opt,name=at_offset,json=atOffset,proto3" json:"at_offset,omitempty"`
	ExitCode      *int64                 `protobuf:"zigzag64,3,opt,name=exit_code,json=exitCode,proto3,oneof" json:"exit_code,omitempty"`
	Cwd           string                 `protobuf:"bytes,4,opt,name=cwd,proto3" json:"cwd,omitempty"`
	CommandId     string                 `protobuf:"bytes,5,opt,name=command_id,json=commandId,proto3" json:"command_id,omitempty"`
	CommandLine   string                 `protobuf:"bytes,6,opt,name=command_line,json=commandLine,proto3" json:"command_line,omitempty"`
	Trust         MarkTrust              `protobuf:"varint,7,opt,name=trust,proto3,enum=omniview.sdk.exec.v2.MarkTrust" json:"trust,omitempty"`
	DurationMs    *uint64                `protobuf:"varint,8,opt,name=duration_ms,json=durationMs,proto3,oneof" json:"duration_ms,omitempty"`
	Redraw        bool                   `protobuf:"varint,9,opt,name=redraw,proto3" json:"redraw,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Mark) Reset() {
	*x = Mark{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[38]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Mark) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Mark) ProtoMessage() {}

func (x *Mark) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[38]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Mark.ProtoReflect.Descriptor instead.
func (*Mark) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{38}
}

func (x *Mark) GetKind() MarkKind {
	if x != nil {
		return x.Kind
	}
	return MarkKind_MARK_KIND_UNSPECIFIED
}

func (x *Mark) GetAtOffset() uint64 {
	if x != nil {
		return x.AtOffset
	}
	return 0
}

func (x *Mark) GetExitCode() int64 {
	if x != nil && x.ExitCode != nil {
		return *x.ExitCode
	}
	return 0
}

func (x *Mark) GetCwd() string {
	if x != nil {
		return x.Cwd
	}
	return ""
}

func (x *Mark) GetCommandId() string {
	if x != nil {
		return x.CommandId
	}
	return ""
}

func (x *Mark) GetCommandLine() string {
	if x != nil {
		return x.CommandLine
	}
	return ""
}

func (x *Mark) GetTrust() MarkTrust {
	if x != nil {
		return x.Trust
	}
	return MarkTrust_MARK_TRUST_UNSPECIFIED
}

func (x *Mark) GetDurationMs() uint64 {
	if x != nil && x.DurationMs != nil {
		return *x.DurationMs
	}
	return 0
}

func (x *Mark) GetRedraw() bool {
	if x != nil {
		return x.Redraw
	}
	return false
}

// Foreground answers FACT_KIND_FOREGROUND. state UNSPECIFIED is unknown and reason says why. name is the foreground
// process's name and pgid its process group, only when busy and known (zero means unknown).
type Foreground struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	State         ForegroundState        `protobuf:"varint,1,opt,name=state,proto3,enum=omniview.sdk.exec.v2.ForegroundState" json:"state,omitempty"`
	Name          string                 `protobuf:"bytes,2,opt,name=name,proto3" json:"name,omitempty"`
	Pgid          uint32                 `protobuf:"varint,3,opt,name=pgid,proto3" json:"pgid,omitempty"`
	Reason        UnknownReason          `protobuf:"varint,4,opt,name=reason,proto3,enum=omniview.sdk.exec.v2.UnknownReason" json:"reason,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Foreground) Reset() {
	*x = Foreground{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[39]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Foreground) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Foreground) ProtoMessage() {}

func (x *Foreground) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[39]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Foreground.ProtoReflect.Descriptor instead.
func (*Foreground) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{39}
}

func (x *Foreground) GetState() ForegroundState {
	if x != nil {
		return x.State
	}
	return ForegroundState_FOREGROUND_STATE_UNSPECIFIED
}

func (x *Foreground) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *Foreground) GetPgid() uint32 {
	if x != nil {
		return x.Pgid
	}
	return 0
}

func (x *Foreground) GetReason() UnknownReason {
	if x != nil {
		return x.Reason
	}
	return UnknownReason_UNKNOWN_REASON_UNSPECIFIED
}

// Directory answers FACT_KIND_CWD. An empty path is unknown and reason says why.
type Directory struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Path          string                 `protobuf:"bytes,1,opt,name=path,proto3" json:"path,omitempty"`
	Reason        UnknownReason          `protobuf:"varint,2,opt,name=reason,proto3,enum=omniview.sdk.exec.v2.UnknownReason" json:"reason,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Directory) Reset() {
	*x = Directory{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[40]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Directory) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Directory) ProtoMessage() {}

func (x *Directory) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[40]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Directory.ProtoReflect.Descriptor instead.
func (*Directory) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{40}
}

func (x *Directory) GetPath() string {
	if x != nil {
		return x.Path
	}
	return ""
}

func (x *Directory) GetReason() UnknownReason {
	if x != nil {
		return x.Reason
	}
	return UnknownReason_UNKNOWN_REASON_UNSPECIFIED
}

// Fact is a fact about a running session. The arm that is set says which fact it is. query_id is the Query.id it
// answers; zero means pushed by the target. An unset arm is a fact kind this reader does not know.
type Fact struct {
	state   protoimpl.MessageState `protogen:"open.v1"`
	QueryId uint32                 `protobuf:"varint,1,opt,name=query_id,json=queryId,proto3" json:"query_id,omitempty"`
	// Types that are valid to be assigned to Value:
	//
	//	*Fact_Foreground
	//	*Fact_Cwd
	//	*Fact_Tier
	Value         isFact_Value `protobuf_oneof:"value"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Fact) Reset() {
	*x = Fact{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[41]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Fact) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Fact) ProtoMessage() {}

func (x *Fact) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[41]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Fact.ProtoReflect.Descriptor instead.
func (*Fact) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{41}
}

func (x *Fact) GetQueryId() uint32 {
	if x != nil {
		return x.QueryId
	}
	return 0
}

func (x *Fact) GetValue() isFact_Value {
	if x != nil {
		return x.Value
	}
	return nil
}

func (x *Fact) GetForeground() *Foreground {
	if x != nil {
		if x, ok := x.Value.(*Fact_Foreground); ok {
			return x.Foreground
		}
	}
	return nil
}

func (x *Fact) GetCwd() *Directory {
	if x != nil {
		if x, ok := x.Value.(*Fact_Cwd); ok {
			return x.Cwd
		}
	}
	return nil
}

func (x *Fact) GetTier() IntegrationTier {
	if x != nil {
		if x, ok := x.Value.(*Fact_Tier); ok {
			return x.Tier
		}
	}
	return IntegrationTier_INTEGRATION_TIER_UNSPECIFIED
}

type isFact_Value interface {
	isFact_Value()
}

type Fact_Foreground struct {
	Foreground *Foreground `protobuf:"bytes,2,opt,name=foreground,proto3,oneof"`
}

type Fact_Cwd struct {
	Cwd *Directory `protobuf:"bytes,3,opt,name=cwd,proto3,oneof"`
}

type Fact_Tier struct {
	Tier IntegrationTier `protobuf:"varint,4,opt,name=tier,proto3,enum=omniview.sdk.exec.v2.IntegrationTier,oneof"`
}

func (*Fact_Foreground) isFact_Value() {}

func (*Fact_Cwd) isFact_Value() {}

func (*Fact_Tier) isFact_Value() {}

// Dropped says a range of output was lost and will not be delivered.
//
// Set by: the SDK, before any later output. The range is [from_offset, from_offset + bytes). mode_reset is bytes for
// the UI's emulator, never for the target: a dropped range may hold a mode change, and this restores the input modes
// (see Ended.mode_reset); absent means not known, present and empty means none is needed.
type Dropped struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Stream        Stream                 `protobuf:"varint,1,opt,name=stream,proto3,enum=omniview.sdk.exec.v2.Stream" json:"stream,omitempty"`
	FromOffset    uint64                 `protobuf:"varint,2,opt,name=from_offset,json=fromOffset,proto3" json:"from_offset,omitempty"`
	Bytes         uint64                 `protobuf:"varint,3,opt,name=bytes,proto3" json:"bytes,omitempty"`
	Reason        DropReason             `protobuf:"varint,4,opt,name=reason,proto3,enum=omniview.sdk.exec.v2.DropReason" json:"reason,omitempty"`
	ModeReset     []byte                 `protobuf:"bytes,5,opt,name=mode_reset,json=modeReset,proto3,oneof" json:"mode_reset,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Dropped) Reset() {
	*x = Dropped{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[42]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Dropped) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Dropped) ProtoMessage() {}

func (x *Dropped) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[42]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Dropped.ProtoReflect.Descriptor instead.
func (*Dropped) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{42}
}

func (x *Dropped) GetStream() Stream {
	if x != nil {
		return x.Stream
	}
	return Stream_STREAM_UNSPECIFIED
}

func (x *Dropped) GetFromOffset() uint64 {
	if x != nil {
		return x.FromOffset
	}
	return 0
}

func (x *Dropped) GetBytes() uint64 {
	if x != nil {
		return x.Bytes
	}
	return 0
}

func (x *Dropped) GetReason() DropReason {
	if x != nil {
		return x.Reason
	}
	return DropReason_DROP_REASON_UNSPECIFIED
}

func (x *Dropped) GetModeReset() []byte {
	if x != nil {
		return x.ModeReset
	}
	return nil
}

// Notice tells the host that something it asked for was accepted and then not done; a refusal would have been a
// status. Set by: the SDK or the author, at most once per kind per session, before the first output.
type Notice struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Kind          NoticeKind             `protobuf:"varint,1,opt,name=kind,proto3,enum=omniview.sdk.exec.v2.NoticeKind" json:"kind,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Notice) Reset() {
	*x = Notice{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[43]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Notice) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Notice) ProtoMessage() {}

func (x *Notice) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[43]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Notice.ProtoReflect.Descriptor instead.
func (*Notice) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{43}
}

func (x *Notice) GetKind() NoticeKind {
	if x != nil {
		return x.Kind
	}
	return NoticeKind_NOTICE_KIND_UNSPECIFIED
}

// Exited is an end with an exit code. exit_code is the status exactly as the target reported it; source says whose
// it is.
//
// The code is a signed 64-bit integer so that it carries every platform's status exactly: the SDK never narrows,
// folds or saturates it, and never converts between two readings of the same status. A POSIX status is 0 to 255. A
// Windows process status is a DWORD, 0 to 4294967295 (0xC0000005 is 3221225477), and some reporters give the same
// DWORD as a negative 32-bit integer (-1073741819, as kubelet's container statuses do). An ssh exit-status is a
// uint32. Docker's ExecInspect and kubectl's error-channel Status relay an int. A signal is never a code: it is
// Signaled, or an Exited with EXIT_SOURCE_SIGNAL_FOLDED when the target folded it into 128+n.
//
// The arm exists only with a status that a target or a transport relayed. From the process (PROCESS, SIGNAL_FOLDED) the code is the
// command's own; from the transport (TRANSPORT) it is "the stream ended normally" (Kubernetes' Success status, code 0) and says nothing
// about what the command did. A stream that ended with nothing relayed (an empty status channel, a break) is Lost, never Exited with
// code 0. exit_code is not optional because the arm itself says it is known.
type Exited struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ExitCode      int64                  `protobuf:"zigzag64,1,opt,name=exit_code,json=exitCode,proto3" json:"exit_code,omitempty"`
	Source        ExitSource             `protobuf:"varint,2,opt,name=source,proto3,enum=omniview.sdk.exec.v2.ExitSource" json:"source,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Exited) Reset() {
	*x = Exited{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[44]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Exited) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Exited) ProtoMessage() {}

func (x *Exited) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[44]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Exited.ProtoReflect.Descriptor instead.
func (*Exited) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{44}
}

func (x *Exited) GetExitCode() int64 {
	if x != nil {
		return x.ExitCode
	}
	return 0
}

func (x *Exited) GetSource() ExitSource {
	if x != nil {
		return x.Source
	}
	return ExitSource_EXIT_SOURCE_UNSPECIFIED
}

// Signaled is an end by a signal the target reported. signal is its name ("SIGKILL"): valid UTF-8 without control
// characters, at most 256 bytes, empty when not reported. signal_number is its number, 1 to 64, zero when not reported.
// The SDK drops a name or a number that breaks these bounds and keeps the other; with neither usable the end is a failure
// of the target. core_dumped is whether the target said a core was dumped (absent means not reported, never "no").
type Signaled struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Signal        string                 `protobuf:"bytes,1,opt,name=signal,proto3" json:"signal,omitempty"`
	SignalNumber  int32                  `protobuf:"varint,2,opt,name=signal_number,json=signalNumber,proto3" json:"signal_number,omitempty"`
	CoreDumped    *bool                  `protobuf:"varint,3,opt,name=core_dumped,json=coreDumped,proto3,oneof" json:"core_dumped,omitempty"`
	Source        ExitSource             `protobuf:"varint,4,opt,name=source,proto3,enum=omniview.sdk.exec.v2.ExitSource" json:"source,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Signaled) Reset() {
	*x = Signaled{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[45]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Signaled) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Signaled) ProtoMessage() {}

func (x *Signaled) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[45]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Signaled.ProtoReflect.Descriptor instead.
func (*Signaled) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{45}
}

func (x *Signaled) GetSignal() string {
	if x != nil {
		return x.Signal
	}
	return ""
}

func (x *Signaled) GetSignalNumber() int32 {
	if x != nil {
		return x.SignalNumber
	}
	return 0
}

func (x *Signaled) GetCoreDumped() bool {
	if x != nil && x.CoreDumped != nil {
		return *x.CoreDumped
	}
	return false
}

func (x *Signaled) GetSource() ExitSource {
	if x != nil {
		return x.Source
	}
	return ExitSource_EXIT_SOURCE_UNSPECIFIED
}

// Closed is an end that was not the command's: someone ended the session (see ClosedBy). The session is over.
type Closed struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	By            ClosedBy               `protobuf:"varint,1,opt,name=by,proto3,enum=omniview.sdk.exec.v2.ClosedBy" json:"by,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Closed) Reset() {
	*x = Closed{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[46]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Closed) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Closed) ProtoMessage() {}

func (x *Closed) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[46]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Closed.ProtoReflect.Descriptor instead.
func (*Closed) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{46}
}

func (x *Closed) GetBy() ClosedBy {
	if x != nil {
		return x.By
	}
	return ClosedBy_CLOSED_BY_UNSPECIFIED
}

// Failed is an end by a failure of the SDK, the plugin or the target, after the command was handed to the target
// (before that it is a refusal, a status): a session's Ended, or a run's RunResult with OUTCOME_FAILED. stage says how
// far it got (see FailureStage). public_message is optional, at most 512 bytes of UTF-8, reviewed classes only, and
// never authority.
type Failed struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Class         ErrorClass             `protobuf:"varint,1,opt,name=class,proto3,enum=omniview.sdk.exec.v2.ErrorClass" json:"class,omitempty"`
	Reason        ErrorReason            `protobuf:"varint,2,opt,name=reason,proto3,enum=omniview.sdk.exec.v2.ErrorReason" json:"reason,omitempty"`
	Facts         *ErrorFacts            `protobuf:"bytes,3,opt,name=facts,proto3" json:"facts,omitempty"`
	Stage         FailureStage           `protobuf:"varint,4,opt,name=stage,proto3,enum=omniview.sdk.exec.v2.FailureStage" json:"stage,omitempty"`
	PublicMessage string                 `protobuf:"bytes,5,opt,name=public_message,json=publicMessage,proto3" json:"public_message,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Failed) Reset() {
	*x = Failed{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[47]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Failed) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Failed) ProtoMessage() {}

func (x *Failed) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[47]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Failed.ProtoReflect.Descriptor instead.
func (*Failed) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{47}
}

func (x *Failed) GetClass() ErrorClass {
	if x != nil {
		return x.Class
	}
	return ErrorClass_ERROR_CLASS_UNSPECIFIED
}

func (x *Failed) GetReason() ErrorReason {
	if x != nil {
		return x.Reason
	}
	return ErrorReason_ERROR_REASON_UNSPECIFIED
}

func (x *Failed) GetFacts() *ErrorFacts {
	if x != nil {
		return x.Facts
	}
	return nil
}

func (x *Failed) GetStage() FailureStage {
	if x != nil {
		return x.Stage
	}
	return FailureStage_FAILURE_STAGE_UNSPECIFIED
}

func (x *Failed) GetPublicMessage() string {
	if x != nil {
		return x.PublicMessage
	}
	return ""
}

// Lost is an end that is not a status: the connection to the target ended with no terminal status, so the process
// may be running, may have exited with any code or may have been killed. Never exit 0, never a failure of the
// command, never a reason to retry the command.
type Lost struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Cause         LostCause              `protobuf:"varint,1,opt,name=cause,proto3,enum=omniview.sdk.exec.v2.LostCause" json:"cause,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Lost) Reset() {
	*x = Lost{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[48]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Lost) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Lost) ProtoMessage() {}

func (x *Lost) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[48]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Lost.ProtoReflect.Descriptor instead.
func (*Lost) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{48}
}

func (x *Lost) GetCause() LostCause {
	if x != nil {
		return x.Cause
	}
	return LostCause_LOST_CAUSE_UNSPECIFIED
}

// End is how a session ended: exactly one arm is set. An unset arm is an end kind this reader does not know: read
// it as "ended, how unknown", never as an exit.
//
// remote_may_survive: whether the process the session ran may still be running after this end. Absent means it is
// not known, which a consumer reads as "may" for every decision about safety and never as "no". True: it may (always
// for LOST). False: the producer knows it is gone: the target reported its status (EXITED or SIGNALED from the PROCESS
// or SIGNAL_FOLDED source) or ending the session terminates the process (CLOSED when the session's effective kill
// behavior, see Capabilities, is TERMINATES). An exit from the TRANSPORT source and a FAILED end leave it absent unless
// the producer knows.
type End struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to Kind:
	//
	//	*End_Exited
	//	*End_Signaled
	//	*End_Closed
	//	*End_Failed
	//	*End_Lost
	Kind             isEnd_Kind `protobuf_oneof:"kind"`
	RemoteMaySurvive *bool      `protobuf:"varint,6,opt,name=remote_may_survive,json=remoteMaySurvive,proto3,oneof" json:"remote_may_survive,omitempty"`
	unknownFields    protoimpl.UnknownFields
	sizeCache        protoimpl.SizeCache
}

func (x *End) Reset() {
	*x = End{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[49]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *End) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*End) ProtoMessage() {}

func (x *End) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[49]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use End.ProtoReflect.Descriptor instead.
func (*End) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{49}
}

func (x *End) GetKind() isEnd_Kind {
	if x != nil {
		return x.Kind
	}
	return nil
}

func (x *End) GetExited() *Exited {
	if x != nil {
		if x, ok := x.Kind.(*End_Exited); ok {
			return x.Exited
		}
	}
	return nil
}

func (x *End) GetSignaled() *Signaled {
	if x != nil {
		if x, ok := x.Kind.(*End_Signaled); ok {
			return x.Signaled
		}
	}
	return nil
}

func (x *End) GetClosed() *Closed {
	if x != nil {
		if x, ok := x.Kind.(*End_Closed); ok {
			return x.Closed
		}
	}
	return nil
}

func (x *End) GetFailed() *Failed {
	if x != nil {
		if x, ok := x.Kind.(*End_Failed); ok {
			return x.Failed
		}
	}
	return nil
}

func (x *End) GetLost() *Lost {
	if x != nil {
		if x, ok := x.Kind.(*End_Lost); ok {
			return x.Lost
		}
	}
	return nil
}

func (x *End) GetRemoteMaySurvive() bool {
	if x != nil && x.RemoteMaySurvive != nil {
		return *x.RemoteMaySurvive
	}
	return false
}

type isEnd_Kind interface {
	isEnd_Kind()
}

type End_Exited struct {
	Exited *Exited `protobuf:"bytes,1,opt,name=exited,proto3,oneof"`
}

type End_Signaled struct {
	Signaled *Signaled `protobuf:"bytes,2,opt,name=signaled,proto3,oneof"`
}

type End_Closed struct {
	Closed *Closed `protobuf:"bytes,3,opt,name=closed,proto3,oneof"`
}

type End_Failed struct {
	Failed *Failed `protobuf:"bytes,4,opt,name=failed,proto3,oneof"`
}

type End_Lost struct {
	Lost *Lost `protobuf:"bytes,5,opt,name=lost,proto3,oneof"`
}

func (*End_Exited) isEnd_Kind() {}

func (*End_Signaled) isEnd_Kind() {}

func (*End_Closed) isEnd_Kind() {}

func (*End_Failed) isEnd_Kind() {}

func (*End_Lost) isEnd_Kind() {}

// Ended is the last frame of a SESSION that a stream sees: its end and its accounts. A session has exactly one END and
// this frame carries it to the stream that is attached when the session ends: AT MOST ONE Ended per session, however
// many streams it had; nothing follows it on the stream, and the stream then ends with status OK. A stream that left
// earlier (Detached) never receives it, and a session that ends while no stream is attached (a lease that expires, a
// retirement, a shutdown) has no Ended frame at all: its end is read from ListSessions (the tombstone:
// SessionInfo.ended_with).
//
// Set by: the SDK, at most once per session. start_observed is true once the author called Started, the first output
// arrived or the command exited with a status; false means the start was not observed, not that the command never ran
// (a silent command can start and lose its link before the SDK sees Started, output or a status, which is a Lost end).
// duration_ms runs from the accepted Open to the end. bytes_out is the number of bytes the command wrote that the SDK
// read; bytes_in the number the host sent. dropped_bytes is the number of output bytes in Dropped ranges. output:
// TRUNCATED when dropped_bytes is not zero (it takes precedence over UNSPECIFIED); COMPLETE when dropped_bytes is zero
// and every byte the command wrote was read and delivered, with the post-exit drain finished; UNSPECIFIED when
// nothing was dropped but the SDK cannot prove completeness (a lost end, a drain that hit its bound). last_offset is
// one past the last output byte (equal to bytes_out).
// mode_reset is bytes for the UI's emulator, never for the target: restores the input modes (alt screen, mouse,
// bracketed paste, application cursor, kitty stack); exact with FEATURE_MODES, the fixed full reset otherwise. Absent
// means not known (the host applies its own full reset); present and empty means none is needed (a session without a
// terminal).
type Ended struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	End           *End                   `protobuf:"bytes,1,opt,name=end,proto3" json:"end,omitempty"`
	StartObserved bool                   `protobuf:"varint,2,opt,name=start_observed,json=startObserved,proto3" json:"start_observed,omitempty"`
	DurationMs    uint64                 `protobuf:"varint,3,opt,name=duration_ms,json=durationMs,proto3" json:"duration_ms,omitempty"`
	BytesOut      uint64                 `protobuf:"varint,4,opt,name=bytes_out,json=bytesOut,proto3" json:"bytes_out,omitempty"`
	BytesIn       uint64                 `protobuf:"varint,5,opt,name=bytes_in,json=bytesIn,proto3" json:"bytes_in,omitempty"`
	DroppedBytes  uint64                 `protobuf:"varint,6,opt,name=dropped_bytes,json=droppedBytes,proto3" json:"dropped_bytes,omitempty"`
	Output        OutputCompleteness     `protobuf:"varint,7,opt,name=output,proto3,enum=omniview.sdk.exec.v2.OutputCompleteness" json:"output,omitempty"`
	LastOffset    uint64                 `protobuf:"varint,8,opt,name=last_offset,json=lastOffset,proto3" json:"last_offset,omitempty"`
	ModeReset     []byte                 `protobuf:"bytes,9,opt,name=mode_reset,json=modeReset,proto3,oneof" json:"mode_reset,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Ended) Reset() {
	*x = Ended{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[50]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Ended) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Ended) ProtoMessage() {}

func (x *Ended) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[50]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Ended.ProtoReflect.Descriptor instead.
func (*Ended) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{50}
}

func (x *Ended) GetEnd() *End {
	if x != nil {
		return x.End
	}
	return nil
}

func (x *Ended) GetStartObserved() bool {
	if x != nil {
		return x.StartObserved
	}
	return false
}

func (x *Ended) GetDurationMs() uint64 {
	if x != nil {
		return x.DurationMs
	}
	return 0
}

func (x *Ended) GetBytesOut() uint64 {
	if x != nil {
		return x.BytesOut
	}
	return 0
}

func (x *Ended) GetBytesIn() uint64 {
	if x != nil {
		return x.BytesIn
	}
	return 0
}

func (x *Ended) GetDroppedBytes() uint64 {
	if x != nil {
		return x.DroppedBytes
	}
	return 0
}

func (x *Ended) GetOutput() OutputCompleteness {
	if x != nil {
		return x.Output
	}
	return OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
}

func (x *Ended) GetLastOffset() uint64 {
	if x != nil {
		return x.LastOffset
	}
	return 0
}

func (x *Ended) GetModeReset() []byte {
	if x != nil {
		return x.ModeReset
	}
	return nil
}

// Link reports a change of the transport's health (only if Capabilities.link_health); never on a timer. silent_ms
// is how long nothing has moved when the state is DEGRADED or STALLED, zero otherwise.
type Link struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	State         LinkState              `protobuf:"varint,1,opt,name=state,proto3,enum=omniview.sdk.exec.v2.LinkState" json:"state,omitempty"`
	SilentMs      uint32                 `protobuf:"varint,2,opt,name=silent_ms,json=silentMs,proto3" json:"silent_ms,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Link) Reset() {
	*x = Link{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[51]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Link) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Link) ProtoMessage() {}

func (x *Link) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[51]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Link.ProtoReflect.Descriptor instead.
func (*Link) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{51}
}

func (x *Link) GetState() LinkState {
	if x != nil {
		return x.State
	}
	return LinkState_LINK_STATE_UNSPECIFIED
}

func (x *Link) GetSilentMs() uint32 {
	if x != nil {
		return x.SilentMs
	}
	return 0
}

// SessionInfo is one session as ListSessions reports it, a live one or a tombstone.
//
// Set by: the SDK. facts: as of the last Started. attached: a stream is attached now. ended_with: present exactly
// when the session ended (a tombstone), absent while it runs. next_offset: the offset the next output byte would
// have (for an ended session, ended_with.last_offset). replay_start: the oldest offset the ring still holds.
// last_activity_unix_ms: zero means no activity yet. link: the last reported link state (UNSPECIFIED when
// Capabilities.link_health is false). lease_expires_unix_ms: when a detached session is ended, zero while attached.
type SessionInfo struct {
	state              protoimpl.MessageState `protogen:"open.v1"`
	Facts              *SessionFacts          `protobuf:"bytes,1,opt,name=facts,proto3" json:"facts,omitempty"`
	Attached           bool                   `protobuf:"varint,2,opt,name=attached,proto3" json:"attached,omitempty"`
	EndedWith          *Ended                 `protobuf:"bytes,3,opt,name=ended_with,json=endedWith,proto3" json:"ended_with,omitempty"`
	NextOffset         uint64                 `protobuf:"varint,4,opt,name=next_offset,json=nextOffset,proto3" json:"next_offset,omitempty"`
	ReplayStart        uint64                 `protobuf:"varint,5,opt,name=replay_start,json=replayStart,proto3" json:"replay_start,omitempty"`
	LastActivityUnixMs int64                  `protobuf:"varint,6,opt,name=last_activity_unix_ms,json=lastActivityUnixMs,proto3" json:"last_activity_unix_ms,omitempty"`
	Link               LinkState              `protobuf:"varint,7,opt,name=link,proto3,enum=omniview.sdk.exec.v2.LinkState" json:"link,omitempty"`
	LeaseExpiresUnixMs int64                  `protobuf:"varint,8,opt,name=lease_expires_unix_ms,json=leaseExpiresUnixMs,proto3" json:"lease_expires_unix_ms,omitempty"`
	unknownFields      protoimpl.UnknownFields
	sizeCache          protoimpl.SizeCache
}

func (x *SessionInfo) Reset() {
	*x = SessionInfo{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[52]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SessionInfo) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SessionInfo) ProtoMessage() {}

func (x *SessionInfo) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[52]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SessionInfo.ProtoReflect.Descriptor instead.
func (*SessionInfo) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{52}
}

func (x *SessionInfo) GetFacts() *SessionFacts {
	if x != nil {
		return x.Facts
	}
	return nil
}

func (x *SessionInfo) GetAttached() bool {
	if x != nil {
		return x.Attached
	}
	return false
}

func (x *SessionInfo) GetEndedWith() *Ended {
	if x != nil {
		return x.EndedWith
	}
	return nil
}

func (x *SessionInfo) GetNextOffset() uint64 {
	if x != nil {
		return x.NextOffset
	}
	return 0
}

func (x *SessionInfo) GetReplayStart() uint64 {
	if x != nil {
		return x.ReplayStart
	}
	return 0
}

func (x *SessionInfo) GetLastActivityUnixMs() int64 {
	if x != nil {
		return x.LastActivityUnixMs
	}
	return 0
}

func (x *SessionInfo) GetLink() LinkState {
	if x != nil {
		return x.Link
	}
	return LinkState_LINK_STATE_UNSPECIFIED
}

func (x *SessionInfo) GetLeaseExpiresUnixMs() int64 {
	if x != nil {
		return x.LeaseExpiresUnixMs
	}
	return 0
}

type ListSessionsRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSessionsRequest) Reset() {
	*x = ListSessionsRequest{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[53]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSessionsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSessionsRequest) ProtoMessage() {}

func (x *ListSessionsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[53]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSessionsRequest.ProtoReflect.Descriptor instead.
func (*ListSessionsRequest) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{53}
}

type ListSessionsResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Sessions      []*SessionInfo         `protobuf:"bytes,1,rep,name=sessions,proto3" json:"sessions,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSessionsResponse) Reset() {
	*x = ListSessionsResponse{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[54]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSessionsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSessionsResponse) ProtoMessage() {}

func (x *ListSessionsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[54]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSessionsResponse.ProtoReflect.Descriptor instead.
func (*ListSessionsResponse) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{54}
}

func (x *ListSessionsResponse) GetSessions() []*SessionInfo {
	if x != nil {
		return x.Sessions
	}
	return nil
}

// ReadOutputRequest reads the replay ring of one session from a cursor. max_bytes zero means the SDK's default
// (64 KiB); it is clamped to the session's replay size.
type ReadOutputRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	SessionId     string                 `protobuf:"bytes,1,opt,name=session_id,json=sessionId,proto3" json:"session_id,omitempty"`
	FromOffset    uint64                 `protobuf:"varint,2,opt,name=from_offset,json=fromOffset,proto3" json:"from_offset,omitempty"`
	MaxBytes      uint32                 `protobuf:"varint,3,opt,name=max_bytes,json=maxBytes,proto3" json:"max_bytes,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ReadOutputRequest) Reset() {
	*x = ReadOutputRequest{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[55]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ReadOutputRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ReadOutputRequest) ProtoMessage() {}

func (x *ReadOutputRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[55]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ReadOutputRequest.ProtoReflect.Descriptor instead.
func (*ReadOutputRequest) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{55}
}

func (x *ReadOutputRequest) GetSessionId() string {
	if x != nil {
		return x.SessionId
	}
	return ""
}

func (x *ReadOutputRequest) GetFromOffset() uint64 {
	if x != nil {
		return x.FromOffset
	}
	return 0
}

func (x *ReadOutputRequest) GetMaxBytes() uint32 {
	if x != nil {
		return x.MaxBytes
	}
	return 0
}

// ReadOutputResponse is a cursor read. chunks are contiguous from max(from_offset, replay_start); replay_start > from_offset means
// bytes were dropped in between; next_offset is where the next read starts; ended is true when the session has
// ended and next_offset is its last offset (nothing more will come).
type ReadOutputResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Chunks        []*Output              `protobuf:"bytes,1,rep,name=chunks,proto3" json:"chunks,omitempty"`
	ReplayStart   uint64                 `protobuf:"varint,2,opt,name=replay_start,json=replayStart,proto3" json:"replay_start,omitempty"`
	NextOffset    uint64                 `protobuf:"varint,3,opt,name=next_offset,json=nextOffset,proto3" json:"next_offset,omitempty"`
	Ended         bool                   `protobuf:"varint,4,opt,name=ended,proto3" json:"ended,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ReadOutputResponse) Reset() {
	*x = ReadOutputResponse{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[56]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ReadOutputResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ReadOutputResponse) ProtoMessage() {}

func (x *ReadOutputResponse) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[56]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ReadOutputResponse.ProtoReflect.Descriptor instead.
func (*ReadOutputResponse) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{56}
}

func (x *ReadOutputResponse) GetChunks() []*Output {
	if x != nil {
		return x.Chunks
	}
	return nil
}

func (x *ReadOutputResponse) GetReplayStart() uint64 {
	if x != nil {
		return x.ReplayStart
	}
	return 0
}

func (x *ReadOutputResponse) GetNextOffset() uint64 {
	if x != nil {
		return x.NextOffset
	}
	return 0
}

func (x *ReadOutputResponse) GetEnded() bool {
	if x != nil {
		return x.Ended
	}
	return false
}

// EndSessionRequest destroys a session that has no attached stream. mode UNSPECIFIED is GRACEFUL.
type EndSessionRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	SessionId     string                 `protobuf:"bytes,1,opt,name=session_id,json=sessionId,proto3" json:"session_id,omitempty"`
	Mode          CloseMode              `protobuf:"varint,2,opt,name=mode,proto3,enum=omniview.sdk.exec.v2.CloseMode" json:"mode,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndSessionRequest) Reset() {
	*x = EndSessionRequest{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[57]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndSessionRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndSessionRequest) ProtoMessage() {}

func (x *EndSessionRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[57]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndSessionRequest.ProtoReflect.Descriptor instead.
func (*EndSessionRequest) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{57}
}

func (x *EndSessionRequest) GetSessionId() string {
	if x != nil {
		return x.SessionId
	}
	return ""
}

func (x *EndSessionRequest) GetMode() CloseMode {
	if x != nil {
		return x.Mode
	}
	return CloseMode_CLOSE_MODE_UNSPECIFIED
}

type EndSessionResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndSessionResponse) Reset() {
	*x = EndSessionResponse{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[58]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndSessionResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndSessionResponse) ProtoMessage() {}

func (x *EndSessionResponse) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[58]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndSessionResponse.ProtoReflect.Descriptor instead.
func (*EndSessionResponse) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{58}
}

// OutputLimits are the caps of one run. Zero means the target's default; above the maximum is INVALID_REQUEST, never
// silently clamped.
type OutputLimits struct {
	state          protoimpl.MessageState `protogen:"open.v1"`
	MaxStdoutBytes uint32                 `protobuf:"varint,1,opt,name=max_stdout_bytes,json=maxStdoutBytes,proto3" json:"max_stdout_bytes,omitempty"`
	MaxStderrBytes uint32                 `protobuf:"varint,2,opt,name=max_stderr_bytes,json=maxStderrBytes,proto3" json:"max_stderr_bytes,omitempty"`
	Policy         TruncatePolicy         `protobuf:"varint,3,opt,name=policy,proto3,enum=omniview.sdk.exec.v2.TruncatePolicy" json:"policy,omitempty"`
	unknownFields  protoimpl.UnknownFields
	sizeCache      protoimpl.SizeCache
}

func (x *OutputLimits) Reset() {
	*x = OutputLimits{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[59]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *OutputLimits) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*OutputLimits) ProtoMessage() {}

func (x *OutputLimits) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[59]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use OutputLimits.ProtoReflect.Descriptor instead.
func (*OutputLimits) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{59}
}

func (x *OutputLimits) GetMaxStdoutBytes() uint32 {
	if x != nil {
		return x.MaxStdoutBytes
	}
	return 0
}

func (x *OutputLimits) GetMaxStderrBytes() uint32 {
	if x != nil {
		return x.MaxStderrBytes
	}
	return 0
}

func (x *OutputLimits) GetPolicy() TruncatePolicy {
	if x != nil {
		return x.Policy
	}
	return TruncatePolicy_TRUNCATE_POLICY_UNSPECIFIED
}

// RunRequest is the request to run one bounded command. Strict.
//
// Set by: the host. request_id: correlation for the host's audit, echoed in the result; at most 128 bytes of
// printable ASCII, may be empty. target: required. argv: REQUIRED, argv[0] non-empty, no NUL, there is no command
// string (a shell is ["sh", "-c", script], visible as such to consent and audit). stdin: at most
// RunLimits.max_stdin_bytes; empty means no stdin. env, cwd: as for Start; refused UNSUPPORTED when the target does
// not support them, never ignored. timeout_ms: zero means RunLimits.default_timeout_ms; above max_timeout_ms is
// INVALID_REQUEST. labels: as for Start.
type RunRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	RequestId     string                 `protobuf:"bytes,1,opt,name=request_id,json=requestId,proto3" json:"request_id,omitempty"`
	Target        *TargetRef             `protobuf:"bytes,2,opt,name=target,proto3" json:"target,omitempty"`
	Argv          []string               `protobuf:"bytes,3,rep,name=argv,proto3" json:"argv,omitempty"`
	Stdin         []byte                 `protobuf:"bytes,4,opt,name=stdin,proto3" json:"stdin,omitempty"`
	Env           []*NameValue           `protobuf:"bytes,5,rep,name=env,proto3" json:"env,omitempty"`
	Cwd           string                 `protobuf:"bytes,6,opt,name=cwd,proto3" json:"cwd,omitempty"`
	TimeoutMs     uint32                 `protobuf:"varint,7,opt,name=timeout_ms,json=timeoutMs,proto3" json:"timeout_ms,omitempty"`
	Output        *OutputLimits          `protobuf:"bytes,8,opt,name=output,proto3" json:"output,omitempty"`
	Labels        []*NameValue           `protobuf:"bytes,9,rep,name=labels,proto3" json:"labels,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RunRequest) Reset() {
	*x = RunRequest{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[60]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RunRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RunRequest) ProtoMessage() {}

func (x *RunRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[60]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RunRequest.ProtoReflect.Descriptor instead.
func (*RunRequest) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{60}
}

func (x *RunRequest) GetRequestId() string {
	if x != nil {
		return x.RequestId
	}
	return ""
}

func (x *RunRequest) GetTarget() *TargetRef {
	if x != nil {
		return x.Target
	}
	return nil
}

func (x *RunRequest) GetArgv() []string {
	if x != nil {
		return x.Argv
	}
	return nil
}

func (x *RunRequest) GetStdin() []byte {
	if x != nil {
		return x.Stdin
	}
	return nil
}

func (x *RunRequest) GetEnv() []*NameValue {
	if x != nil {
		return x.Env
	}
	return nil
}

func (x *RunRequest) GetCwd() string {
	if x != nil {
		return x.Cwd
	}
	return ""
}

func (x *RunRequest) GetTimeoutMs() uint32 {
	if x != nil {
		return x.TimeoutMs
	}
	return 0
}

func (x *RunRequest) GetOutput() *OutputLimits {
	if x != nil {
		return x.Output
	}
	return nil
}

func (x *RunRequest) GetLabels() []*NameValue {
	if x != nil {
		return x.Labels
	}
	return nil
}

// OutputAccount is the exact account of one stream of a run.
//
// Set by: the SDK. total_bytes: bytes the command wrote that arrived; kept_bytes: bytes returned; dropped_bytes: bytes
// the cap elided, always total - kept; truncated: dropped_bytes is not zero; head_bytes and tail_bytes: how the kept
// bytes split (under HEAD_TAIL, the head is the first head_bytes of the returned bytes and the tail the last
// tail_bytes); valid_utf8: the returned bytes, as returned, are valid UTF-8 (the host's tool boundary decides text or
// base64 from it). completeness is exactly one of: TRUNCATED when dropped_bytes is not zero (any byte the SDK read
// was dropped), whatever else happened, so it takes precedence over UNSPECIFIED; COMPLETE when dropped_bytes is zero,
// the stream was read to its end, the post-exit drain finished and nothing was lost in transit; UNSPECIFIED when
// nothing was dropped but the stream ended abnormally, the post-exit drain ended by its bound or the target cannot
// tell (total_bytes is then what arrived).
type OutputAccount struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	TotalBytes    uint64                 `protobuf:"varint,1,opt,name=total_bytes,json=totalBytes,proto3" json:"total_bytes,omitempty"`
	KeptBytes     uint64                 `protobuf:"varint,2,opt,name=kept_bytes,json=keptBytes,proto3" json:"kept_bytes,omitempty"`
	DroppedBytes  uint64                 `protobuf:"varint,3,opt,name=dropped_bytes,json=droppedBytes,proto3" json:"dropped_bytes,omitempty"`
	Truncated     bool                   `protobuf:"varint,4,opt,name=truncated,proto3" json:"truncated,omitempty"`
	HeadBytes     uint32                 `protobuf:"varint,5,opt,name=head_bytes,json=headBytes,proto3" json:"head_bytes,omitempty"`
	TailBytes     uint32                 `protobuf:"varint,6,opt,name=tail_bytes,json=tailBytes,proto3" json:"tail_bytes,omitempty"`
	ValidUtf8     bool                   `protobuf:"varint,7,opt,name=valid_utf8,json=validUtf8,proto3" json:"valid_utf8,omitempty"`
	Completeness  OutputCompleteness     `protobuf:"varint,8,opt,name=completeness,proto3,enum=omniview.sdk.exec.v2.OutputCompleteness" json:"completeness,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *OutputAccount) Reset() {
	*x = OutputAccount{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[61]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *OutputAccount) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*OutputAccount) ProtoMessage() {}

func (x *OutputAccount) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[61]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use OutputAccount.ProtoReflect.Descriptor instead.
func (*OutputAccount) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{61}
}

func (x *OutputAccount) GetTotalBytes() uint64 {
	if x != nil {
		return x.TotalBytes
	}
	return 0
}

func (x *OutputAccount) GetKeptBytes() uint64 {
	if x != nil {
		return x.KeptBytes
	}
	return 0
}

func (x *OutputAccount) GetDroppedBytes() uint64 {
	if x != nil {
		return x.DroppedBytes
	}
	return 0
}

func (x *OutputAccount) GetTruncated() bool {
	if x != nil {
		return x.Truncated
	}
	return false
}

func (x *OutputAccount) GetHeadBytes() uint32 {
	if x != nil {
		return x.HeadBytes
	}
	return 0
}

func (x *OutputAccount) GetTailBytes() uint32 {
	if x != nil {
		return x.TailBytes
	}
	return 0
}

func (x *OutputAccount) GetValidUtf8() bool {
	if x != nil {
		return x.ValidUtf8
	}
	return false
}

func (x *OutputAccount) GetCompleteness() OutputCompleteness {
	if x != nil {
		return x.Completeness
	}
	return OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
}

// RunResult is the answer to a run.
//
// Set by: the SDK. request_id echoes the request's. outcome: how it ended. exit_code: present when the command
// ended with a status or the SDK's kill left one (absent means none, not 0); exit_source says whose it is, HOST for
// TIMED_OUT and CANCELLED; the code is a signed 64-bit integer, exactly as reported (see Exited). signal and
// signal_number: set for OUTCOME_SIGNALED (empty and zero otherwise). stdout and stderr: the kept bytes, raw and
// untouched; under HEAD_TAIL the middle is elided and counted in the account; for TIMED_OUT, CANCELLED, LOST and
// FAILED they are the output read before the end, which is partial, and the account's completeness says what can be
// claimed. duration_ms: from accepted request to end. start_observed: the SDK observed the command start (the target
// reported it, the first output arrived or a status came); false means the start was not observed, not that the
// command never ran: a silent command can start and lose its link before the SDK sees anything, and a host never
// retries on the strength of start_observed=false alone. remote_may_survive: whether the process may still be running
// (see End.remote_may_survive: absent means not known and reads as "may"; always true for OUTCOME_LOST; true after a
// timeout or cancel on a target that cannot promise it stopped). target, program: as for a session.
//
// failed: present exactly when outcome is OUTCOME_FAILED. It says how the run failed and how far it got: class and
// reason as for a refusal, facts, and stage (FAILURE_STAGE_START: the start was not observed; RUN: after the start,
// while the command ran; STREAM: the stream between the plugin and the target failed after the start). A RunResult
// with OUTCOME_FAILED answers a run that was handed to the target: unlike a refusal it may have run, may still be
// running and is never retried by a host on its own. A refusal, before the command was handed to the target, is a
// gRPC status (see the file header).
type RunResult struct {
	state            protoimpl.MessageState `protogen:"open.v1"`
	RequestId        string                 `protobuf:"bytes,1,opt,name=request_id,json=requestId,proto3" json:"request_id,omitempty"`
	Outcome          Outcome                `protobuf:"varint,2,opt,name=outcome,proto3,enum=omniview.sdk.exec.v2.Outcome" json:"outcome,omitempty"`
	ExitCode         *int64                 `protobuf:"zigzag64,3,opt,name=exit_code,json=exitCode,proto3,oneof" json:"exit_code,omitempty"`
	Signal           string                 `protobuf:"bytes,4,opt,name=signal,proto3" json:"signal,omitempty"`
	SignalNumber     int32                  `protobuf:"varint,5,opt,name=signal_number,json=signalNumber,proto3" json:"signal_number,omitempty"`
	Stdout           []byte                 `protobuf:"bytes,6,opt,name=stdout,proto3" json:"stdout,omitempty"`
	Stderr           []byte                 `protobuf:"bytes,7,opt,name=stderr,proto3" json:"stderr,omitempty"`
	StdoutAccount    *OutputAccount         `protobuf:"bytes,8,opt,name=stdout_account,json=stdoutAccount,proto3" json:"stdout_account,omitempty"`
	StderrAccount    *OutputAccount         `protobuf:"bytes,9,opt,name=stderr_account,json=stderrAccount,proto3" json:"stderr_account,omitempty"`
	DurationMs       uint64                 `protobuf:"varint,10,opt,name=duration_ms,json=durationMs,proto3" json:"duration_ms,omitempty"`
	StartObserved    bool                   `protobuf:"varint,11,opt,name=start_observed,json=startObserved,proto3" json:"start_observed,omitempty"`
	RemoteMaySurvive *bool                  `protobuf:"varint,12,opt,name=remote_may_survive,json=remoteMaySurvive,proto3,oneof" json:"remote_may_survive,omitempty"`
	Target           *TargetFacts           `protobuf:"bytes,13,opt,name=target,proto3" json:"target,omitempty"`
	Program          *Program               `protobuf:"bytes,14,opt,name=program,proto3" json:"program,omitempty"`
	ExitSource       ExitSource             `protobuf:"varint,15,opt,name=exit_source,json=exitSource,proto3,enum=omniview.sdk.exec.v2.ExitSource" json:"exit_source,omitempty"`
	Failed           *Failed                `protobuf:"bytes,16,opt,name=failed,proto3" json:"failed,omitempty"`
	unknownFields    protoimpl.UnknownFields
	sizeCache        protoimpl.SizeCache
}

func (x *RunResult) Reset() {
	*x = RunResult{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[62]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RunResult) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RunResult) ProtoMessage() {}

func (x *RunResult) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[62]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RunResult.ProtoReflect.Descriptor instead.
func (*RunResult) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{62}
}

func (x *RunResult) GetRequestId() string {
	if x != nil {
		return x.RequestId
	}
	return ""
}

func (x *RunResult) GetOutcome() Outcome {
	if x != nil {
		return x.Outcome
	}
	return Outcome_OUTCOME_UNSPECIFIED
}

func (x *RunResult) GetExitCode() int64 {
	if x != nil && x.ExitCode != nil {
		return *x.ExitCode
	}
	return 0
}

func (x *RunResult) GetSignal() string {
	if x != nil {
		return x.Signal
	}
	return ""
}

func (x *RunResult) GetSignalNumber() int32 {
	if x != nil {
		return x.SignalNumber
	}
	return 0
}

func (x *RunResult) GetStdout() []byte {
	if x != nil {
		return x.Stdout
	}
	return nil
}

func (x *RunResult) GetStderr() []byte {
	if x != nil {
		return x.Stderr
	}
	return nil
}

func (x *RunResult) GetStdoutAccount() *OutputAccount {
	if x != nil {
		return x.StdoutAccount
	}
	return nil
}

func (x *RunResult) GetStderrAccount() *OutputAccount {
	if x != nil {
		return x.StderrAccount
	}
	return nil
}

func (x *RunResult) GetDurationMs() uint64 {
	if x != nil {
		return x.DurationMs
	}
	return 0
}

func (x *RunResult) GetStartObserved() bool {
	if x != nil {
		return x.StartObserved
	}
	return false
}

func (x *RunResult) GetRemoteMaySurvive() bool {
	if x != nil && x.RemoteMaySurvive != nil {
		return *x.RemoteMaySurvive
	}
	return false
}

func (x *RunResult) GetTarget() *TargetFacts {
	if x != nil {
		return x.Target
	}
	return nil
}

func (x *RunResult) GetProgram() *Program {
	if x != nil {
		return x.Program
	}
	return nil
}

func (x *RunResult) GetExitSource() ExitSource {
	if x != nil {
		return x.ExitSource
	}
	return ExitSource_EXIT_SOURCE_UNSPECIFIED
}

func (x *RunResult) GetFailed() *Failed {
	if x != nil {
		return x.Failed
	}
	return nil
}

// RunEvent is what RunStream sends: RunStarted first when the SDK observed the command start, then output and
// progress, then RunEnded, once. A run that failed or was lost before its start was observed (FAILURE_STAGE_START, or
// LOST) sends RunEnded alone, with no RunStarted: a start is never fabricated. Nothing follows RunEnded.
type RunEvent struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to Event:
	//
	//	*RunEvent_Started
	//	*RunEvent_Output
	//	*RunEvent_Progress
	//	*RunEvent_Ended
	Event         isRunEvent_Event `protobuf_oneof:"event"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RunEvent) Reset() {
	*x = RunEvent{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[63]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RunEvent) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RunEvent) ProtoMessage() {}

func (x *RunEvent) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[63]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RunEvent.ProtoReflect.Descriptor instead.
func (*RunEvent) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{63}
}

func (x *RunEvent) GetEvent() isRunEvent_Event {
	if x != nil {
		return x.Event
	}
	return nil
}

func (x *RunEvent) GetStarted() *RunStarted {
	if x != nil {
		if x, ok := x.Event.(*RunEvent_Started); ok {
			return x.Started
		}
	}
	return nil
}

func (x *RunEvent) GetOutput() *Output {
	if x != nil {
		if x, ok := x.Event.(*RunEvent_Output); ok {
			return x.Output
		}
	}
	return nil
}

func (x *RunEvent) GetProgress() *RunProgress {
	if x != nil {
		if x, ok := x.Event.(*RunEvent_Progress); ok {
			return x.Progress
		}
	}
	return nil
}

func (x *RunEvent) GetEnded() *RunEnded {
	if x != nil {
		if x, ok := x.Event.(*RunEvent_Ended); ok {
			return x.Ended
		}
	}
	return nil
}

type isRunEvent_Event interface {
	isRunEvent_Event()
}

type RunEvent_Started struct {
	Started *RunStarted `protobuf:"bytes,1,opt,name=started,proto3,oneof"`
}

type RunEvent_Output struct {
	Output *Output `protobuf:"bytes,2,opt,name=output,proto3,oneof"`
}

type RunEvent_Progress struct {
	Progress *RunProgress `protobuf:"bytes,3,opt,name=progress,proto3,oneof"`
}

type RunEvent_Ended struct {
	Ended *RunEnded `protobuf:"bytes,4,opt,name=ended,proto3,oneof"`
}

func (*RunEvent_Started) isRunEvent_Event() {}

func (*RunEvent_Output) isRunEvent_Event() {}

func (*RunEvent_Progress) isRunEvent_Event() {}

func (*RunEvent_Ended) isRunEvent_Event() {}

// RunStarted is the first event of a RunStream: the command is running.
type RunStarted struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Target        *TargetFacts           `protobuf:"bytes,1,opt,name=target,proto3" json:"target,omitempty"`
	Program       *Program               `protobuf:"bytes,2,opt,name=program,proto3" json:"program,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RunStarted) Reset() {
	*x = RunStarted{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[64]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RunStarted) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RunStarted) ProtoMessage() {}

func (x *RunStarted) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[64]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RunStarted.ProtoReflect.Descriptor instead.
func (*RunStarted) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{64}
}

func (x *RunStarted) GetTarget() *TargetFacts {
	if x != nil {
		return x.Target
	}
	return nil
}

func (x *RunStarted) GetProgram() *Program {
	if x != nil {
		return x.Program
	}
	return nil
}

// RunProgress reports, at the SDK's pace (not on a timer the host can rely on), how long the run has taken and how many
// bytes it has written.
type RunProgress struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ElapsedMs     uint64                 `protobuf:"varint,1,opt,name=elapsed_ms,json=elapsedMs,proto3" json:"elapsed_ms,omitempty"`
	BytesOut      uint64                 `protobuf:"varint,2,opt,name=bytes_out,json=bytesOut,proto3" json:"bytes_out,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RunProgress) Reset() {
	*x = RunProgress{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[65]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RunProgress) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RunProgress) ProtoMessage() {}

func (x *RunProgress) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[65]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RunProgress.ProtoReflect.Descriptor instead.
func (*RunProgress) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{65}
}

func (x *RunProgress) GetElapsedMs() uint64 {
	if x != nil {
		return x.ElapsedMs
	}
	return 0
}

func (x *RunProgress) GetBytesOut() uint64 {
	if x != nil {
		return x.BytesOut
	}
	return 0
}

// RunEnded is the last event of a RunStream: the result without the payloads (stdout and stderr are empty, they were
// streamed); the outcome, the exit, the account, the failure and the flags only. Like RunResult it is what the SDK
// sends for every end after the command was handed to the target, a failure included (OUTCOME_FAILED, with
// RunResult.failed), whenever it can still deliver it. A refusal, when nothing ran, ends the stream with a status; a
// cancelled call, an expired deadline or a broken connection can prevent RunEnded and end the stream with a status of
// the transport's own, which says nothing about whether the command ran and never authorizes a retry.
type RunEnded struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Result        *RunResult             `protobuf:"bytes,1,opt,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RunEnded) Reset() {
	*x = RunEnded{}
	mi := &file_proto_exec_v2_exec_proto_msgTypes[66]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RunEnded) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RunEnded) ProtoMessage() {}

func (x *RunEnded) ProtoReflect() protoreflect.Message {
	mi := &file_proto_exec_v2_exec_proto_msgTypes[66]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RunEnded.ProtoReflect.Descriptor instead.
func (*RunEnded) Descriptor() ([]byte, []int) {
	return file_proto_exec_v2_exec_proto_rawDescGZIP(), []int{66}
}

func (x *RunEnded) GetResult() *RunResult {
	if x != nil {
		return x.Result
	}
	return nil
}

var File_proto_exec_v2_exec_proto protoreflect.FileDescriptor

const file_proto_exec_v2_exec_proto_rawDesc = "" +
	"\n" +
	"\x18proto/exec/v2/exec.proto\x12\x14omniview.sdk.exec.v2\x1a\x1eproto/resource/v2/domain.proto\".\n" +
	"\x04Size\x12\x12\n" +
	"\x04cols\x18\x01 \x01(\rR\x04cols\x12\x12\n" +
	"\x04rows\x18\x02 \x01(\rR\x04rows\"5\n" +
	"\tNameValue\x12\x12\n" +
	"\x04name\x18\x01 \x01(\tR\x04name\x12\x14\n" +
	"\x05value\x18\x02 \x01(\tR\x05value\"\xca\x01\n" +
	"\tTargetRef\x12E\n" +
	"\treference\x18\x01 \x01(\v2'.omniview.resource.v2.ResourceReferenceR\treference\x127\n" +
	"\x05route\x18\x02 \x01(\v2!.omniview.resource.v2.AccessRouteR\x05route\x12=\n" +
	"\tselection\x18\x03 \x03(\v2\x1f.omniview.sdk.exec.v2.NameValueR\tselection\"\x9c\x01\n" +
	"\n" +
	"ErrorFacts\x12\x18\n" +
	"\aprogram\x18\x01 \x01(\tR\aprogram\x12%\n" +
	"\x0eselection_name\x18\x02 \x01(\tR\rselectionName\x12'\n" +
	"\x0fselection_value\x18\x03 \x01(\tR\x0eselectionValue\x12$\n" +
	"\x0eretry_after_ms\x18\x04 \x01(\rR\fretryAfterMs\"C\n" +
	"\tSignalSet\x126\n" +
	"\asignals\x18\x01 \x03(\x0e2\x1c.omniview.sdk.exec.v2.SignalR\asignals\"Q\n" +
	"\x12IntegrationModeSet\x12;\n" +
	"\x05modes\x18\x01 \x03(\x0e2%.omniview.sdk.exec.v2.IntegrationModeR\x05modes\"\xf8\x06\n" +
	"\fCapabilities\x12\x15\n" +
	"\x03tty\x18\x01 \x01(\bH\x00R\x03tty\x88\x01\x01\x12,\n" +
	"\x0fseparate_stderr\x18\x02 \x01(\bH\x01R\x0eseparateStderr\x88\x01\x01\x12\x1b\n" +
	"\x06resize\x18\x03 \x01(\bH\x02R\x06resize\x88\x01\x01\x12&\n" +
	"\finitial_size\x18\x04 \x01(\bH\x03R\vinitialSize\x88\x01\x01\x12 \n" +
	"\tstdin_eof\x18\x05 \x01(\bH\x04R\bstdinEof\x88\x01\x01\x129\n" +
	"\asignals\x18\x06 \x01(\v2\x1f.omniview.sdk.exec.v2.SignalSetR\asignals\x126\n" +
	"\x04kill\x18\a \x01(\x0e2\".omniview.sdk.exec.v2.KillBehaviorR\x04kill\x12\x15\n" +
	"\x03env\x18\b \x01(\bH\x05R\x03env\x88\x01\x01\x12\x15\n" +
	"\x03cwd\x18\t \x01(\bH\x06R\x03cwd\x88\x01\x01\x129\n" +
	"\bcwd_fact\x18\n" +
	" \x01(\x0e2\x1e.omniview.sdk.exec.v2.FactModeR\acwdFact\x12G\n" +
	"\x0fforeground_fact\x18\v \x01(\x0e2\x1e.omniview.sdk.exec.v2.FactModeR\x0eforegroundFact\x12A\n" +
	"\fhost_restart\x18\f \x01(\x0e2\x1e.omniview.sdk.exec.v2.SurvivalR\vhostRestart\x12A\n" +
	"\vexit_report\x18\r \x01(\x0e2 .omniview.sdk.exec.v2.ExitReportR\n" +
	"exitReport\x129\n" +
	"\x05stall\x18\x0e \x01(\x0e2#.omniview.sdk.exec.v2.StallBehaviorR\x05stall\x12$\n" +
	"\vlink_health\x18\x0f \x01(\bH\aR\n" +
	"linkHealth\x88\x01\x01\x12J\n" +
	"\vintegration\x18\x10 \x01(\v2(.omniview.sdk.exec.v2.IntegrationModeSetR\vintegrationB\x06\n" +
	"\x04_ttyB\x12\n" +
	"\x10_separate_stderrB\t\n" +
	"\a_resizeB\x0f\n" +
	"\r_initial_sizeB\f\n" +
	"\n" +
	"_stdin_eofB\x06\n" +
	"\x04_envB\x06\n" +
	"\x04_cwdB\x0e\n" +
	"\f_link_health\"\xab\x02\n" +
	"\rSessionLimits\x12!\n" +
	"\fmax_sessions\x18\x01 \x01(\rR\vmaxSessions\x12&\n" +
	"\x0fmax_input_frame\x18\x02 \x01(\rR\rmaxInputFrame\x12!\n" +
	"\freplay_bytes\x18\x03 \x01(\rR\vreplayBytes\x12(\n" +
	"\x10max_replay_bytes\x18\x04 \x01(\rR\x0emaxReplayBytes\x12*\n" +
	"\x11detach_timeout_ms\x18\x05 \x01(\rR\x0fdetachTimeoutMs\x121\n" +
	"\x15max_detach_timeout_ms\x18\x06 \x01(\rR\x12maxDetachTimeoutMs\x12#\n" +
	"\rmax_dimension\x18\a \x01(\rR\fmaxDimension\"\x81\x03\n" +
	"\tRunLimits\x12,\n" +
	"\x12default_timeout_ms\x18\x01 \x01(\rR\x10defaultTimeoutMs\x12$\n" +
	"\x0emax_timeout_ms\x18\x02 \x01(\rR\fmaxTimeoutMs\x120\n" +
	"\x14default_output_bytes\x18\x03 \x01(\rR\x12defaultOutputBytes\x12(\n" +
	"\x10max_output_bytes\x18\x04 \x01(\rR\x0emaxOutputBytes\x12&\n" +
	"\x0fmax_stdin_bytes\x18\x05 \x01(\rR\rmaxStdinBytes\x12%\n" +
	"\x0emax_concurrent\x18\x06 \x01(\rR\rmaxConcurrent\x12$\n" +
	"\x0emax_argv_bytes\x18\a \x01(\rR\fmaxArgvBytes\x12\"\n" +
	"\rmax_env_bytes\x18\b \x01(\rR\vmaxEnvBytes\x12+\n" +
	"\x12post_exit_drain_ms\x18\t \x01(\rR\x0fpostExitDrainMs\"\xbc\x02\n" +
	"\n" +
	"TargetInfo\x12\x1a\n" +
	"\bprovider\x18\x01 \x01(\tR\bprovider\x12\x12\n" +
	"\x04type\x18\x02 \x01(\tR\x04type\x12!\n" +
	"\fdisplay_name\x18\x03 \x01(\tR\vdisplayName\x12!\n" +
	"\fdefault_argv\x18\x04 \x03(\tR\vdefaultArgv\x12F\n" +
	"\fcapabilities\x18\x05 \x01(\v2\".omniview.sdk.exec.v2.CapabilitiesR\fcapabilities\x12=\n" +
	"\asession\x18\x06 \x01(\v2#.omniview.sdk.exec.v2.SessionLimitsR\asession\x121\n" +
	"\x03run\x18\a \x01(\v2\x1f.omniview.sdk.exec.v2.RunLimitsR\x03run\"\x11\n" +
	"\x0fDescribeRequest\"\xb4\x01\n" +
	"\x10DescribeResponse\x12)\n" +
	"\x10contract_version\x18\x01 \x01(\rR\x0fcontractVersion\x129\n" +
	"\bfeatures\x18\x02 \x03(\x0e2\x1d.omniview.sdk.exec.v2.FeatureR\bfeatures\x12:\n" +
	"\atargets\x18\x03 \x03(\v2 .omniview.sdk.exec.v2.TargetInfoR\atargets\"I\n" +
	"\x0eOptionsRequest\x127\n" +
	"\x06target\x18\x01 \x01(\v2\x1f.omniview.sdk.exec.v2.TargetRefR\x06target\"\xac\x02\n" +
	"\x06Choice\x12\x14\n" +
	"\x05value\x18\x01 \x01(\tR\x05value\x12!\n" +
	"\fdisplay_name\x18\x02 \x01(\tR\vdisplayName\x127\n" +
	"\x05state\x18\x03 \x01(\x0e2!.omniview.sdk.exec.v2.ChoiceStateR\x05state\x12\x16\n" +
	"\x06detail\x18\x04 \x01(\tR\x06detail\x12\x1d\n" +
	"\n" +
	"is_default\x18\x05 \x01(\bR\tisDefault\x12\x12\n" +
	"\x04kind\x18\x06 \x01(\tR\x04kind\x12F\n" +
	"\fcapabilities\x18\a \x01(\v2\".omniview.sdk.exec.v2.CapabilitiesR\fcapabilities\x12\x1d\n" +
	"\n" +
	"no_program\x18\b \x01(\bR\tnoProgram\"p\n" +
	"\x06Option\x12\x12\n" +
	"\x04name\x18\x01 \x01(\tR\x04name\x126\n" +
	"\achoices\x18\x02 \x03(\v2\x1c.omniview.sdk.exec.v2.ChoiceR\achoices\x12\x1a\n" +
	"\brequired\x18\x03 \x01(\bR\brequired\"x\n" +
	"\x0fOptionsResponse\x126\n" +
	"\aoptions\x18\x01 \x03(\v2\x1c.omniview.sdk.exec.v2.OptionR\aoptions\x12-\n" +
	"\x13observed_at_unix_ms\x18\x02 \x01(\x03R\x10observedAtUnixMs\"i\n" +
	"\x12IntegrationRequest\x129\n" +
	"\x04mode\x18\x01 \x01(\x0e2%.omniview.sdk.exec.v2.IntegrationModeR\x04mode\x12\x18\n" +
	"\aconsent\x18\x02 \x01(\bR\aconsent\"\xea\x03\n" +
	"\x05Start\x127\n" +
	"\x06target\x18\x01 \x01(\v2\x1f.omniview.sdk.exec.v2.TargetRefR\x06target\x12\x12\n" +
	"\x04argv\x18\x02 \x03(\tR\x04argv\x12\x10\n" +
	"\x03tty\x18\x03 \x01(\bR\x03tty\x12.\n" +
	"\x04size\x18\x04 \x01(\v2\x1a.omniview.sdk.exec.v2.SizeR\x04size\x121\n" +
	"\x03env\x18\x05 \x03(\v2\x1f.omniview.sdk.exec.v2.NameValueR\x03env\x12\x10\n" +
	"\x03cwd\x18\x06 \x01(\tR\x03cwd\x127\n" +
	"\x06labels\x18\a \x03(\v2\x1f.omniview.sdk.exec.v2.NameValueR\x06labels\x12*\n" +
	"\x11detach_timeout_ms\x18\b \x01(\rR\x0fdetachTimeoutMs\x12!\n" +
	"\freplay_bytes\x18\t \x01(\rR\vreplayBytes\x129\n" +
	"\bfeatures\x18\n" +
	" \x03(\x0e2\x1d.omniview.sdk.exec.v2.FeatureR\bfeatures\x12J\n" +
	"\vintegration\x18\v \x01(\v2(.omniview.sdk.exec.v2.IntegrationRequestR\vintegration\")\n" +
	"\x06Attach\x12\x1f\n" +
	"\vfrom_offset\x18\x01 \x01(\x04R\n" +
	"fromOffset\"\xa1\x01\n" +
	"\vOpenRequest\x12\x1d\n" +
	"\n" +
	"session_id\x18\x01 \x01(\tR\tsessionId\x123\n" +
	"\x05start\x18\x02 \x01(\v2\x1b.omniview.sdk.exec.v2.StartH\x00R\x05start\x126\n" +
	"\x06attach\x18\x03 \x01(\v2\x1c.omniview.sdk.exec.v2.AttachH\x00R\x06attachB\x06\n" +
	"\x04kind\"\xbe\x03\n" +
	"\vClientFrame\x127\n" +
	"\x04open\x18\x01 \x01(\v2!.omniview.sdk.exec.v2.OpenRequestH\x00R\x04open\x12\x16\n" +
	"\x05input\x18\x02 \x01(\fH\x00R\x05input\x126\n" +
	"\x06resize\x18\x03 \x01(\v2\x1c.omniview.sdk.exec.v2.ResizeH\x00R\x06resize\x126\n" +
	"\x06signal\x18\x04 \x01(\x0e2\x1c.omniview.sdk.exec.v2.SignalH\x00R\x06signal\x12C\n" +
	"\vclose_input\x18\x05 \x01(\v2 .omniview.sdk.exec.v2.CloseInputH\x00R\n" +
	"closeInput\x123\n" +
	"\x05query\x18\x06 \x01(\v2\x1b.omniview.sdk.exec.v2.QueryH\x00R\x05query\x123\n" +
	"\x05close\x18\a \x01(\v2\x1b.omniview.sdk.exec.v2.CloseH\x00R\x05close\x126\n" +
	"\x06detach\x18\b \x01(\v2\x1c.omniview.sdk.exec.v2.DetachH\x00R\x06detachB\a\n" +
	"\x05frame\"J\n" +
	"\x06Resize\x12\x10\n" +
	"\x03seq\x18\x01 \x01(\rR\x03seq\x12.\n" +
	"\x04size\x18\x02 \x01(\v2\x1a.omniview.sdk.exec.v2.SizeR\x04size\"\f\n" +
	"\n" +
	"CloseInput\"<\n" +
	"\x05Close\x123\n" +
	"\x04mode\x18\x01 \x01(\x0e2\x1f.omniview.sdk.exec.v2.CloseModeR\x04mode\"#\n" +
	"\x06Detach\x12\x19\n" +
	"\blease_ms\x18\x01 \x01(\rR\aleaseMs\"K\n" +
	"\x05Query\x12\x0e\n" +
	"\x02id\x18\x01 \x01(\rR\x02id\x122\n" +
	"\x04kind\x18\x02 \x01(\x0e2\x1e.omniview.sdk.exec.v2.FactKindR\x04kind\"\xf8\x04\n" +
	"\vServerFrame\x126\n" +
	"\x06opened\x18\x01 \x01(\v2\x1c.omniview.sdk.exec.v2.OpenedH\x00R\x06opened\x126\n" +
	"\x06output\x18\x02 \x01(\v2\x1c.omniview.sdk.exec.v2.OutputH\x00R\x06output\x129\n" +
	"\aresized\x18\x03 \x01(\v2\x1d.omniview.sdk.exec.v2.ResizedH\x00R\aresized\x120\n" +
	"\x04mark\x18\x04 \x01(\v2\x1a.omniview.sdk.exec.v2.MarkH\x00R\x04mark\x120\n" +
	"\x04fact\x18\x05 \x01(\v2\x1a.omniview.sdk.exec.v2.FactH\x00R\x04fact\x129\n" +
	"\adropped\x18\x06 \x01(\v2\x1d.omniview.sdk.exec.v2.DroppedH\x00R\adropped\x126\n" +
	"\x06notice\x18\a \x01(\v2\x1c.omniview.sdk.exec.v2.NoticeH\x00R\x06notice\x123\n" +
	"\x05ended\x18\b \x01(\v2\x1b.omniview.sdk.exec.v2.EndedH\x00R\x05ended\x129\n" +
	"\astarted\x18\t \x01(\v2\x1d.omniview.sdk.exec.v2.StartedH\x00R\astarted\x120\n" +
	"\x04link\x18\n" +
	" \x01(\v2\x1a.omniview.sdk.exec.v2.LinkH\x00R\x04link\x12<\n" +
	"\bdetached\x18\v \x01(\v2\x1e.omniview.sdk.exec.v2.DetachedH\x00R\bdetachedB\a\n" +
	"\x05frame\"x\n" +
	"\x06Opened\x128\n" +
	"\x05facts\x18\x01 \x01(\v2\".omniview.sdk.exec.v2.SessionFactsR\x05facts\x124\n" +
	"\x06replay\x18\x02 \x01(\v2\x1c.omniview.sdk.exec.v2.ReplayR\x06replay\"C\n" +
	"\aStarted\x128\n" +
	"\x05facts\x18\x01 \x01(\v2\".omniview.sdk.exec.v2.SessionFactsR\x05facts\"y\n" +
	"\bDetached\x121\n" +
	"\x15lease_expires_unix_ms\x18\x01 \x01(\x03R\x12leaseExpiresUnixMs\x12:\n" +
	"\x06reason\x18\x02 \x01(\x0e2\".omniview.sdk.exec.v2.DetachReasonR\x06reason\"\xfe\x01\n" +
	"\vTargetFacts\x12\x1a\n" +
	"\bprovider\x18\x01 \x01(\tR\bprovider\x12\x12\n" +
	"\x04type\x18\x02 \x01(\tR\x04type\x12\x0e\n" +
	"\x02id\x18\x03 \x01(\tR\x02id\x12!\n" +
	"\fdisplay_name\x18\x04 \x01(\tR\vdisplayName\x12N\n" +
	"\tselection\x18\x05 \x03(\v20.omniview.sdk.exec.v2.TargetFacts.SelectionEntryR\tselection\x1a<\n" +
	"\x0eSelectionEntry\x12\x10\n" +
	"\x03key\x18\x01 \x01(\tR\x03key\x12\x14\n" +
	"\x05value\x18\x02 \x01(\tR\x05value:\x028\x01\"E\n" +
	"\aProgram\x12\x12\n" +
	"\x04argv\x18\x01 \x03(\tR\x04argv\x12\x12\n" +
	"\x04name\x18\x02 \x01(\tR\x04name\x12\x12\n" +
	"\x04path\x18\x03 \x01(\tR\x04path\"c\n" +
	"\x10IntegrationFacts\x129\n" +
	"\x04mode\x18\x01 \x01(\x0e2%.omniview.sdk.exec.v2.IntegrationModeR\x04mode\x12\x14\n" +
	"\x05nonce\x18\x02 \x01(\tR\x05nonce\"\xb0\x04\n" +
	"\fSessionFacts\x12\x1d\n" +
	"\n" +
	"session_id\x18\x01 \x01(\tR\tsessionId\x129\n" +
	"\x06target\x18\x02 \x01(\v2!.omniview.sdk.exec.v2.TargetFactsR\x06target\x127\n" +
	"\aprogram\x18\x03 \x01(\v2\x1d.omniview.sdk.exec.v2.ProgramR\aprogram\x12\x15\n" +
	"\x03tty\x18\x04 \x01(\bH\x00R\x03tty\x88\x01\x01\x12.\n" +
	"\x04size\x18\x05 \x01(\v2\x1a.omniview.sdk.exec.v2.SizeR\x04size\x12F\n" +
	"\fcapabilities\x18\x06 \x01(\v2\".omniview.sdk.exec.v2.CapabilitiesR\fcapabilities\x12;\n" +
	"\x06limits\x18\a \x01(\v2#.omniview.sdk.exec.v2.SessionLimitsR\x06limits\x12+\n" +
	"\x12created_at_unix_ms\x18\b \x01(\x03R\x0fcreatedAtUnixMs\x12\x10\n" +
	"\x03pid\x18\t \x01(\rR\x03pid\x12\x1c\n" +
	"\ttransport\x18\n" +
	" \x01(\tR\ttransport\x12\x12\n" +
	"\x04term\x18\v \x01(\tR\x04term\x12H\n" +
	"\vintegration\x18\f \x01(\v2&.omniview.sdk.exec.v2.IntegrationFactsR\vintegrationB\x06\n" +
	"\x04_tty\"\x8d\x03\n" +
	"\rTerminalModes\x12\x1d\n" +
	"\n" +
	"alt_screen\x18\x01 \x01(\bR\taltScreen\x12'\n" +
	"\x0fbracketed_paste\x18\x02 \x01(\bR\x0ebracketedPaste\x12-\n" +
	"\x12application_cursor\x18\x03 \x01(\bR\x11applicationCursor\x12!\n" +
	"\ffocus_events\x18\x04 \x01(\bR\vfocusEvents\x12#\n" +
	"\rcursor_hidden\x18\x05 \x01(\bR\fcursorHidden\x12J\n" +
	"\x0emouse_tracking\x18\x06 \x01(\x0e2#.omniview.sdk.exec.v2.MouseTrackingR\rmouseTracking\x12J\n" +
	"\x0emouse_encoding\x18\a \x01(\x0e2#.omniview.sdk.exec.v2.MouseEncodingR\rmouseEncoding\x12%\n" +
	"\x0ekitty_keyboard\x18\b \x03(\rR\rkittyKeyboard\"\xb2\x02\n" +
	"\x06Replay\x12\x14\n" +
	"\x05start\x18\x01 \x01(\x04R\x05start\x12\x10\n" +
	"\x03end\x18\x02 \x01(\x04R\x03end\x12\x14\n" +
	"\x05bytes\x18\x03 \x01(\x04R\x05bytes\x129\n" +
	"\n" +
	"start_size\x18\x04 \x01(\v2\x1a.omniview.sdk.exec.v2.SizeR\tstartSize\x12D\n" +
	"\vstart_modes\x18\x05 \x01(\v2#.omniview.sdk.exec.v2.TerminalModesR\n" +
	"startModes\x12.\n" +
	"\x04size\x18\x06 \x01(\v2\x1a.omniview.sdk.exec.v2.SizeR\x04size\x129\n" +
	"\x05modes\x18\a \x01(\v2#.omniview.sdk.exec.v2.TerminalModesR\x05modes\"j\n" +
	"\x06Output\x124\n" +
	"\x06stream\x18\x01 \x01(\x0e2\x1c.omniview.sdk.exec.v2.StreamR\x06stream\x12\x16\n" +
	"\x06offset\x18\x02 \x01(\x04R\x06offset\x12\x12\n" +
	"\x04data\x18\x03 \x01(\fR\x04data\"\x87\x01\n" +
	"\aResized\x12\x10\n" +
	"\x03seq\x18\x01 \x01(\rR\x03seq\x12.\n" +
	"\x04size\x18\x02 \x01(\v2\x1a.omniview.sdk.exec.v2.SizeR\x04size\x12:\n" +
	"\x06result\x18\x03 \x01(\x0e2\".omniview.sdk.exec.v2.ResizeResultR\x06result\"\xe0\x02\n" +
	"\x04Mark\x122\n" +
	"\x04kind\x18\x01 \x01(\x0e2\x1e.omniview.sdk.exec.v2.MarkKindR\x04kind\x12\x1b\n" +
	"\tat_offset\x18\x02 \x01(\x04R\batOffset\x12 \n" +
	"\texit_code\x18\x03 \x01(\x12H\x00R\bexitCode\x88\x01\x01\x12\x10\n" +
	"\x03cwd\x18\x04 \x01(\tR\x03cwd\x12\x1d\n" +
	"\n" +
	"command_id\x18\x05 \x01(\tR\tcommandId\x12!\n" +
	"\fcommand_line\x18\x06 \x01(\tR\vcommandLine\x125\n" +
	"\x05trust\x18\a \x01(\x0e2\x1f.omniview.sdk.exec.v2.MarkTrustR\x05trust\x12$\n" +
	"\vduration_ms\x18\b \x01(\x04H\x01R\n" +
	"durationMs\x88\x01\x01\x12\x16\n" +
	"\x06redraw\x18\t \x01(\bR\x06redrawB\f\n" +
	"\n" +
	"_exit_codeB\x0e\n" +
	"\f_duration_ms\"\xae\x01\n" +
	"\n" +
	"Foreground\x12;\n" +
	"\x05state\x18\x01 \x01(\x0e2%.omniview.sdk.exec.v2.ForegroundStateR\x05state\x12\x12\n" +
	"\x04name\x18\x02 \x01(\tR\x04name\x12\x12\n" +
	"\x04pgid\x18\x03 \x01(\rR\x04pgid\x12;\n" +
	"\x06reason\x18\x04 \x01(\x0e2#.omniview.sdk.exec.v2.UnknownReasonR\x06reason\"\\\n" +
	"\tDirectory\x12\x12\n" +
	"\x04path\x18\x01 \x01(\tR\x04path\x12;\n" +
	"\x06reason\x18\x02 \x01(\x0e2#.omniview.sdk.exec.v2.UnknownReasonR\x06reason\"\xe0\x01\n" +
	"\x04Fact\x12\x19\n" +
	"\bquery_id\x18\x01 \x01(\rR\aqueryId\x12B\n" +
	"\n" +
	"foreground\x18\x02 \x01(\v2 .omniview.sdk.exec.v2.ForegroundH\x00R\n" +
	"foreground\x123\n" +
	"\x03cwd\x18\x03 \x01(\v2\x1f.omniview.sdk.exec.v2.DirectoryH\x00R\x03cwd\x12;\n" +
	"\x04tier\x18\x04 \x01(\x0e2%.omniview.sdk.exec.v2.IntegrationTierH\x00R\x04tierB\a\n" +
	"\x05value\"\xe3\x01\n" +
	"\aDropped\x124\n" +
	"\x06stream\x18\x01 \x01(\x0e2\x1c.omniview.sdk.exec.v2.StreamR\x06stream\x12\x1f\n" +
	"\vfrom_offset\x18\x02 \x01(\x04R\n" +
	"fromOffset\x12\x14\n" +
	"\x05bytes\x18\x03 \x01(\x04R\x05bytes\x128\n" +
	"\x06reason\x18\x04 \x01(\x0e2 .omniview.sdk.exec.v2.DropReasonR\x06reason\x12\"\n" +
	"\n" +
	"mode_reset\x18\x05 \x01(\fH\x00R\tmodeReset\x88\x01\x01B\r\n" +
	"\v_mode_reset\">\n" +
	"\x06Notice\x124\n" +
	"\x04kind\x18\x01 \x01(\x0e2 .omniview.sdk.exec.v2.NoticeKindR\x04kind\"_\n" +
	"\x06Exited\x12\x1b\n" +
	"\texit_code\x18\x01 \x01(\x12R\bexitCode\x128\n" +
	"\x06source\x18\x02 \x01(\x0e2 .omniview.sdk.exec.v2.ExitSourceR\x06source\"\xb7\x01\n" +
	"\bSignaled\x12\x16\n" +
	"\x06signal\x18\x01 \x01(\tR\x06signal\x12#\n" +
	"\rsignal_number\x18\x02 \x01(\x05R\fsignalNumber\x12$\n" +
	"\vcore_dumped\x18\x03 \x01(\bH\x00R\n" +
	"coreDumped\x88\x01\x01\x128\n" +
	"\x06source\x18\x04 \x01(\x0e2 .omniview.sdk.exec.v2.ExitSourceR\x06sourceB\x0e\n" +
	"\f_core_dumped\"8\n" +
	"\x06Closed\x12.\n" +
	"\x02by\x18\x01 \x01(\x0e2\x1e.omniview.sdk.exec.v2.ClosedByR\x02by\"\x94\x02\n" +
	"\x06Failed\x126\n" +
	"\x05class\x18\x01 \x01(\x0e2 .omniview.sdk.exec.v2.ErrorClassR\x05class\x129\n" +
	"\x06reason\x18\x02 \x01(\x0e2!.omniview.sdk.exec.v2.ErrorReasonR\x06reason\x126\n" +
	"\x05facts\x18\x03 \x01(\v2 .omniview.sdk.exec.v2.ErrorFactsR\x05facts\x128\n" +
	"\x05stage\x18\x04 \x01(\x0e2\".omniview.sdk.exec.v2.FailureStageR\x05stage\x12%\n" +
	"\x0epublic_message\x18\x05 \x01(\tR\rpublicMessage\"=\n" +
	"\x04Lost\x125\n" +
	"\x05cause\x18\x01 \x01(\x0e2\x1f.omniview.sdk.exec.v2.LostCauseR\x05cause\"\xef\x02\n" +
	"\x03End\x126\n" +
	"\x06exited\x18\x01 \x01(\v2\x1c.omniview.sdk.exec.v2.ExitedH\x00R\x06exited\x12<\n" +
	"\bsignaled\x18\x02 \x01(\v2\x1e.omniview.sdk.exec.v2.SignaledH\x00R\bsignaled\x126\n" +
	"\x06closed\x18\x03 \x01(\v2\x1c.omniview.sdk.exec.v2.ClosedH\x00R\x06closed\x126\n" +
	"\x06failed\x18\x04 \x01(\v2\x1c.omniview.sdk.exec.v2.FailedH\x00R\x06failed\x120\n" +
	"\x04lost\x18\x05 \x01(\v2\x1a.omniview.sdk.exec.v2.LostH\x00R\x04lost\x121\n" +
	"\x12remote_may_survive\x18\x06 \x01(\bH\x01R\x10remoteMaySurvive\x88\x01\x01B\x06\n" +
	"\x04kindB\x15\n" +
	"\x13_remote_may_survive\"\xef\x02\n" +
	"\x05Ended\x12+\n" +
	"\x03end\x18\x01 \x01(\v2\x19.omniview.sdk.exec.v2.EndR\x03end\x12%\n" +
	"\x0estart_observed\x18\x02 \x01(\bR\rstartObserved\x12\x1f\n" +
	"\vduration_ms\x18\x03 \x01(\x04R\n" +
	"durationMs\x12\x1b\n" +
	"\tbytes_out\x18\x04 \x01(\x04R\bbytesOut\x12\x19\n" +
	"\bbytes_in\x18\x05 \x01(\x04R\abytesIn\x12#\n" +
	"\rdropped_bytes\x18\x06 \x01(\x04R\fdroppedBytes\x12@\n" +
	"\x06output\x18\a \x01(\x0e2(.omniview.sdk.exec.v2.OutputCompletenessR\x06output\x12\x1f\n" +
	"\vlast_offset\x18\b \x01(\x04R\n" +
	"lastOffset\x12\"\n" +
	"\n" +
	"mode_reset\x18\t \x01(\fH\x00R\tmodeReset\x88\x01\x01B\r\n" +
	"\v_mode_reset\"Z\n" +
	"\x04Link\x125\n" +
	"\x05state\x18\x01 \x01(\x0e2\x1f.omniview.sdk.exec.v2.LinkStateR\x05state\x12\x1b\n" +
	"\tsilent_ms\x18\x02 \x01(\rR\bsilentMs\"\xfe\x02\n" +
	"\vSessionInfo\x128\n" +
	"\x05facts\x18\x01 \x01(\v2\".omniview.sdk.exec.v2.SessionFactsR\x05facts\x12\x1a\n" +
	"\battached\x18\x02 \x01(\bR\battached\x12:\n" +
	"\n" +
	"ended_with\x18\x03 \x01(\v2\x1b.omniview.sdk.exec.v2.EndedR\tendedWith\x12\x1f\n" +
	"\vnext_offset\x18\x04 \x01(\x04R\n" +
	"nextOffset\x12!\n" +
	"\freplay_start\x18\x05 \x01(\x04R\vreplayStart\x121\n" +
	"\x15last_activity_unix_ms\x18\x06 \x01(\x03R\x12lastActivityUnixMs\x123\n" +
	"\x04link\x18\a \x01(\x0e2\x1f.omniview.sdk.exec.v2.LinkStateR\x04link\x121\n" +
	"\x15lease_expires_unix_ms\x18\b \x01(\x03R\x12leaseExpiresUnixMs\"\x15\n" +
	"\x13ListSessionsRequest\"U\n" +
	"\x14ListSessionsResponse\x12=\n" +
	"\bsessions\x18\x01 \x03(\v2!.omniview.sdk.exec.v2.SessionInfoR\bsessions\"p\n" +
	"\x11ReadOutputRequest\x12\x1d\n" +
	"\n" +
	"session_id\x18\x01 \x01(\tR\tsessionId\x12\x1f\n" +
	"\vfrom_offset\x18\x02 \x01(\x04R\n" +
	"fromOffset\x12\x1b\n" +
	"\tmax_bytes\x18\x03 \x01(\rR\bmaxBytes\"\xa4\x01\n" +
	"\x12ReadOutputResponse\x124\n" +
	"\x06chunks\x18\x01 \x03(\v2\x1c.omniview.sdk.exec.v2.OutputR\x06chunks\x12!\n" +
	"\freplay_start\x18\x02 \x01(\x04R\vreplayStart\x12\x1f\n" +
	"\vnext_offset\x18\x03 \x01(\x04R\n" +
	"nextOffset\x12\x14\n" +
	"\x05ended\x18\x04 \x01(\bR\x05ended\"g\n" +
	"\x11EndSessionRequest\x12\x1d\n" +
	"\n" +
	"session_id\x18\x01 \x01(\tR\tsessionId\x123\n" +
	"\x04mode\x18\x02 \x01(\x0e2\x1f.omniview.sdk.exec.v2.CloseModeR\x04mode\"\x14\n" +
	"\x12EndSessionResponse\"\xa0\x01\n" +
	"\fOutputLimits\x12(\n" +
	"\x10max_stdout_bytes\x18\x01 \x01(\rR\x0emaxStdoutBytes\x12(\n" +
	"\x10max_stderr_bytes\x18\x02 \x01(\rR\x0emaxStderrBytes\x12<\n" +
	"\x06policy\x18\x03 \x01(\x0e2$.omniview.sdk.exec.v2.TruncatePolicyR\x06policy\"\xe7\x02\n" +
	"\n" +
	"RunRequest\x12\x1d\n" +
	"\n" +
	"request_id\x18\x01 \x01(\tR\trequestId\x127\n" +
	"\x06target\x18\x02 \x01(\v2\x1f.omniview.sdk.exec.v2.TargetRefR\x06target\x12\x12\n" +
	"\x04argv\x18\x03 \x03(\tR\x04argv\x12\x14\n" +
	"\x05stdin\x18\x04 \x01(\fR\x05stdin\x121\n" +
	"\x03env\x18\x05 \x03(\v2\x1f.omniview.sdk.exec.v2.NameValueR\x03env\x12\x10\n" +
	"\x03cwd\x18\x06 \x01(\tR\x03cwd\x12\x1d\n" +
	"\n" +
	"timeout_ms\x18\a \x01(\rR\ttimeoutMs\x12:\n" +
	"\x06output\x18\b \x01(\v2\".omniview.sdk.exec.v2.OutputLimitsR\x06output\x127\n" +
	"\x06labels\x18\t \x03(\v2\x1f.omniview.sdk.exec.v2.NameValueR\x06labels\"\xbd\x02\n" +
	"\rOutputAccount\x12\x1f\n" +
	"\vtotal_bytes\x18\x01 \x01(\x04R\n" +
	"totalBytes\x12\x1d\n" +
	"\n" +
	"kept_bytes\x18\x02 \x01(\x04R\tkeptBytes\x12#\n" +
	"\rdropped_bytes\x18\x03 \x01(\x04R\fdroppedBytes\x12\x1c\n" +
	"\ttruncated\x18\x04 \x01(\bR\ttruncated\x12\x1d\n" +
	"\n" +
	"head_bytes\x18\x05 \x01(\rR\theadBytes\x12\x1d\n" +
	"\n" +
	"tail_bytes\x18\x06 \x01(\rR\ttailBytes\x12\x1d\n" +
	"\n" +
	"valid_utf8\x18\a \x01(\bR\tvalidUtf8\x12L\n" +
	"\fcompleteness\x18\b \x01(\x0e2(.omniview.sdk.exec.v2.OutputCompletenessR\fcompleteness\"\x97\x06\n" +
	"\tRunResult\x12\x1d\n" +
	"\n" +
	"request_id\x18\x01 \x01(\tR\trequestId\x127\n" +
	"\aoutcome\x18\x02 \x01(\x0e2\x1d.omniview.sdk.exec.v2.OutcomeR\aoutcome\x12 \n" +
	"\texit_code\x18\x03 \x01(\x12H\x00R\bexitCode\x88\x01\x01\x12\x16\n" +
	"\x06signal\x18\x04 \x01(\tR\x06signal\x12#\n" +
	"\rsignal_number\x18\x05 \x01(\x05R\fsignalNumber\x12\x16\n" +
	"\x06stdout\x18\x06 \x01(\fR\x06stdout\x12\x16\n" +
	"\x06stderr\x18\a \x01(\fR\x06stderr\x12J\n" +
	"\x0estdout_account\x18\b \x01(\v2#.omniview.sdk.exec.v2.OutputAccountR\rstdoutAccount\x12J\n" +
	"\x0estderr_account\x18\t \x01(\v2#.omniview.sdk.exec.v2.OutputAccountR\rstderrAccount\x12\x1f\n" +
	"\vduration_ms\x18\n" +
	" \x01(\x04R\n" +
	"durationMs\x12%\n" +
	"\x0estart_observed\x18\v \x01(\bR\rstartObserved\x121\n" +
	"\x12remote_may_survive\x18\f \x01(\bH\x01R\x10remoteMaySurvive\x88\x01\x01\x129\n" +
	"\x06target\x18\r \x01(\v2!.omniview.sdk.exec.v2.TargetFactsR\x06target\x127\n" +
	"\aprogram\x18\x0e \x01(\v2\x1d.omniview.sdk.exec.v2.ProgramR\aprogram\x12A\n" +
	"\vexit_source\x18\x0f \x01(\x0e2 .omniview.sdk.exec.v2.ExitSourceR\n" +
	"exitSource\x124\n" +
	"\x06failed\x18\x10 \x01(\v2\x1c.omniview.sdk.exec.v2.FailedR\x06failedB\f\n" +
	"\n" +
	"_exit_codeB\x15\n" +
	"\x13_remote_may_survive\"\x82\x02\n" +
	"\bRunEvent\x12<\n" +
	"\astarted\x18\x01 \x01(\v2 .omniview.sdk.exec.v2.RunStartedH\x00R\astarted\x126\n" +
	"\x06output\x18\x02 \x01(\v2\x1c.omniview.sdk.exec.v2.OutputH\x00R\x06output\x12?\n" +
	"\bprogress\x18\x03 \x01(\v2!.omniview.sdk.exec.v2.RunProgressH\x00R\bprogress\x126\n" +
	"\x05ended\x18\x04 \x01(\v2\x1e.omniview.sdk.exec.v2.RunEndedH\x00R\x05endedB\a\n" +
	"\x05event\"\x80\x01\n" +
	"\n" +
	"RunStarted\x129\n" +
	"\x06target\x18\x01 \x01(\v2!.omniview.sdk.exec.v2.TargetFactsR\x06target\x127\n" +
	"\aprogram\x18\x02 \x01(\v2\x1d.omniview.sdk.exec.v2.ProgramR\aprogram\"I\n" +
	"\vRunProgress\x12\x1d\n" +
	"\n" +
	"elapsed_ms\x18\x01 \x01(\x04R\telapsedMs\x12\x1b\n" +
	"\tbytes_out\x18\x02 \x01(\x04R\bbytesOut\"C\n" +
	"\bRunEnded\x127\n" +
	"\x06result\x18\x01 \x01(\v2\x1f.omniview.sdk.exec.v2.RunResultR\x06result*V\n" +
	"\x06Stream\x12\x16\n" +
	"\x12STREAM_UNSPECIFIED\x10\x00\x12\x0e\n" +
	"\n" +
	"STREAM_TTY\x10\x01\x12\x11\n" +
	"\rSTREAM_STDOUT\x10\x02\x12\x11\n" +
	"\rSTREAM_STDERR\x10\x03*\x95\x01\n" +
	"\x06Signal\x12\x16\n" +
	"\x12SIGNAL_UNSPECIFIED\x10\x00\x12\x0e\n" +
	"\n" +
	"SIGNAL_INT\x10\x01\x12\x0f\n" +
	"\vSIGNAL_QUIT\x10\x02\x12\x0f\n" +
	"\vSIGNAL_TERM\x10\x03\x12\x0f\n" +
	"\vSIGNAL_KILL\x10\x04\x12\x0e\n" +
	"\n" +
	"SIGNAL_HUP\x10\x05\x12\x0f\n" +
	"\vSIGNAL_USR1\x10\x06\x12\x0f\n" +
	"\vSIGNAL_USR2\x10\a*a\n" +
	"\bFactMode\x12\x19\n" +
	"\x15FACT_MODE_UNSPECIFIED\x10\x00\x12\x12\n" +
	"\x0eFACT_MODE_NONE\x10\x01\x12\x12\n" +
	"\x0eFACT_MODE_PULL\x10\x02\x12\x12\n" +
	"\x0eFACT_MODE_PUSH\x10\x03*\x90\x01\n" +
	"\fKillBehavior\x12\x1d\n" +
	"\x19KILL_BEHAVIOR_UNSPECIFIED\x10\x00\x12\x1c\n" +
	"\x18KILL_BEHAVIOR_TERMINATES\x10\x01\x12\x1d\n" +
	"\x19KILL_BEHAVIOR_BEST_EFFORT\x10\x02\x12$\n" +
	" KILL_BEHAVIOR_CLOSES_STREAM_ONLY\x10\x03*q\n" +
	"\bSurvival\x12\x18\n" +
	"\x14SURVIVAL_UNSPECIFIED\x10\x00\x12\x11\n" +
	"\rSURVIVAL_NONE\x10\x01\x12\x1b\n" +
	"\x17SURVIVAL_TARGET_PROCESS\x10\x02\x12\x1b\n" +
	"\x17SURVIVAL_TARGET_SESSION\x10\x03*v\n" +
	"\n" +
	"ExitReport\x12\x1b\n" +
	"\x17EXIT_REPORT_UNSPECIFIED\x10\x00\x12\x14\n" +
	"\x10EXIT_REPORT_NONE\x10\x01\x12\x14\n" +
	"\x10EXIT_REPORT_CODE\x10\x02\x12\x1f\n" +
	"\x1bEXIT_REPORT_CODE_AND_SIGNAL\x10\x03*h\n" +
	"\rStallBehavior\x12\x1e\n" +
	"\x1aSTALL_BEHAVIOR_UNSPECIFIED\x10\x00\x12\x1d\n" +
	"\x19STALL_BEHAVIOR_PROPAGATES\x10\x01\x12\x18\n" +
	"\x14STALL_BEHAVIOR_LOSSY\x10\x02*q\n" +
	"\x0fIntegrationMode\x12 \n" +
	"\x1cINTEGRATION_MODE_UNSPECIFIED\x10\x00\x12\x19\n" +
	"\x15INTEGRATION_MODE_NONE\x10\x01\x12!\n" +
	"\x1dINTEGRATION_MODE_ARGV_WRAPPER\x10\x02*\xf8\x01\n" +
	"\aFeature\x12\x17\n" +
	"\x13FEATURE_UNSPECIFIED\x10\x00\x12\x11\n" +
	"\rFEATURE_MARKS\x10\x01\x12\x1e\n" +
	"\x1aFEATURE_SESSION_MANAGEMENT\x10\x02\x12\x16\n" +
	"\x12FEATURE_RUN_STREAM\x10\x03\x12\x0f\n" +
	"\vFEATURE_ENV\x10\x04\x12\x0f\n" +
	"\vFEATURE_CWD\x10\x05\x12\x15\n" +
	"\x11FEATURE_STDIN_EOF\x10\x06\x12\x11\n" +
	"\rFEATURE_MODES\x10\a\x12\x17\n" +
	"\x13FEATURE_INTEGRATION\x10\b\x12\x13\n" +
	"\x0fFEATURE_SESSION\x10\t\x12\x0f\n" +
	"\vFEATURE_RUN\x10\n" +
	"*\xf3\x02\n" +
	"\n" +
	"ErrorClass\x12\x1b\n" +
	"\x17ERROR_CLASS_UNSPECIFIED\x10\x00\x12\x19\n" +
	"\x15ERROR_CLASS_FORBIDDEN\x10\x01\x12\x1c\n" +
	"\x18ERROR_CLASS_UNAUTHORIZED\x10\x02\x12\x19\n" +
	"\x15ERROR_CLASS_NOT_FOUND\x10\x03\x12\x18\n" +
	"\x14ERROR_CLASS_CONFLICT\x10\x04\x12\x1b\n" +
	"\x17ERROR_CLASS_UNAVAILABLE\x10\x05\x12\x1a\n" +
	"\x16ERROR_CLASS_CONNECTION\x10\x06\x12\x17\n" +
	"\x13ERROR_CLASS_TIMEOUT\x10\a\x12\x1f\n" +
	"\x1bERROR_CLASS_INVALID_REQUEST\x10\b\x12\x1b\n" +
	"\x17ERROR_CLASS_UNSUPPORTED\x10\t\x12\x15\n" +
	"\x11ERROR_CLASS_LIMIT\x10\n" +
	"\x12\x19\n" +
	"\x15ERROR_CLASS_CANCELLED\x10\v\x12\x18\n" +
	"\x14ERROR_CLASS_INTERNAL\x10\f*\xa1\x04\n" +
	"\vErrorReason\x12\x1c\n" +
	"\x18ERROR_REASON_UNSPECIFIED\x10\x00\x12\x1e\n" +
	"\x1aERROR_REASON_TARGET_DENIED\x10\x01\x12\x1c\n" +
	"\x18ERROR_REASON_TARGET_GONE\x10\x02\x12 \n" +
	"\x1cERROR_REASON_TARGET_REPLACED\x10\x03\x12\x1c\n" +
	"\x18ERROR_REASON_NOT_RUNNING\x10\x04\x12#\n" +
	"\x1fERROR_REASON_NO_SUCH_EXECUTABLE\x10\x05\x12 \n" +
	"\x1cERROR_REASON_TTY_UNSUPPORTED\x10\x06\x12\"\n" +
	"\x1eERROR_REASON_TOO_MANY_SESSIONS\x10\a\x12\x1e\n" +
	"\x1aERROR_REASON_TOO_MANY_RUNS\x10\b\x12 \n" +
	"\x1cERROR_REASON_INPUT_TOO_LARGE\x10\t\x12#\n" +
	"\x1fERROR_REASON_CONNECTION_RETIRED\x10\n" +
	"\x12\x1f\n" +
	"\x1bERROR_REASON_SESSION_EXISTS\x10\v\x12 \n" +
	"\x1cERROR_REASON_SESSION_UNKNOWN\x10\f\x12!\n" +
	"\x1dERROR_REASON_CONSENT_REQUIRED\x10\r\x12\x1e\n" +
	"\x1aERROR_REASON_INPUT_BACKLOG\x10\x0e\x12\x1e\n" +
	"\x1aERROR_REASON_TARGET_WEDGED\x10\x0f*x\n" +
	"\vChoiceState\x12\x1c\n" +
	"\x18CHOICE_STATE_UNSPECIFIED\x10\x00\x12\x16\n" +
	"\x12CHOICE_STATE_READY\x10\x01\x12\x19\n" +
	"\x15CHOICE_STATE_STARTING\x10\x02\x12\x18\n" +
	"\x14CHOICE_STATE_STOPPED\x10\x03*U\n" +
	"\tCloseMode\x12\x1a\n" +
	"\x16CLOSE_MODE_UNSPECIFIED\x10\x00\x12\x17\n" +
	"\x13CLOSE_MODE_GRACEFUL\x10\x01\x12\x13\n" +
	"\x0fCLOSE_MODE_KILL\x10\x02*r\n" +
	"\bFactKind\x12\x19\n" +
	"\x15FACT_KIND_UNSPECIFIED\x10\x00\x12\x18\n" +
	"\x14FACT_KIND_FOREGROUND\x10\x01\x12\x11\n" +
	"\rFACT_KIND_CWD\x10\x02\x12\x1e\n" +
	"\x1aFACT_KIND_INTEGRATION_TIER\x10\x03*h\n" +
	"\fDetachReason\x12\x1d\n" +
	"\x19DETACH_REASON_UNSPECIFIED\x10\x00\x12\x1b\n" +
	"\x17DETACH_REASON_REQUESTED\x10\x01\x12\x1c\n" +
	"\x18DETACH_REASON_SUPERSEDED\x10\x02*\x85\x01\n" +
	"\x0fIntegrationTier\x12 \n" +
	"\x1cINTEGRATION_TIER_UNSPECIFIED\x10\x00\x12\x19\n" +
	"\x15INTEGRATION_TIER_NONE\x10\x01\x12\x1a\n" +
	"\x16INTEGRATION_TIER_BASIC\x10\x02\x12\x19\n" +
	"\x15INTEGRATION_TIER_RICH\x10\x03*\xae\x01\n" +
	"\rMouseTracking\x12\x1e\n" +
	"\x1aMOUSE_TRACKING_UNSPECIFIED\x10\x00\x12\x17\n" +
	"\x13MOUSE_TRACKING_NONE\x10\x01\x12\x16\n" +
	"\x12MOUSE_TRACKING_X10\x10\x02\x12\x19\n" +
	"\x15MOUSE_TRACKING_NORMAL\x10\x03\x12\x19\n" +
	"\x15MOUSE_TRACKING_BUTTON\x10\x04\x12\x16\n" +
	"\x12MOUSE_TRACKING_ANY\x10\x05*\x92\x01\n" +
	"\rMouseEncoding\x12\x1e\n" +
	"\x1aMOUSE_ENCODING_UNSPECIFIED\x10\x00\x12\x16\n" +
	"\x12MOUSE_ENCODING_X10\x10\x01\x12\x17\n" +
	"\x13MOUSE_ENCODING_UTF8\x10\x02\x12\x16\n" +
	"\x12MOUSE_ENCODING_SGR\x10\x03\x12\x18\n" +
	"\x14MOUSE_ENCODING_URXVT\x10\x04*}\n" +
	"\fResizeResult\x12\x1d\n" +
	"\x19RESIZE_RESULT_UNSPECIFIED\x10\x00\x12\x19\n" +
	"\x15RESIZE_RESULT_APPLIED\x10\x01\x12\x19\n" +
	"\x15RESIZE_RESULT_IGNORED\x10\x02\x12\x18\n" +
	"\x14RESIZE_RESULT_FAILED\x10\x03*\xa7\x01\n" +
	"\bMarkKind\x12\x19\n" +
	"\x15MARK_KIND_UNSPECIFIED\x10\x00\x12\x1a\n" +
	"\x16MARK_KIND_PROMPT_START\x10\x01\x12\x19\n" +
	"\x15MARK_KIND_INPUT_START\x10\x02\x12\x1b\n" +
	"\x17MARK_KIND_COMMAND_START\x10\x03\x12\x19\n" +
	"\x15MARK_KIND_COMMAND_END\x10\x04\x12\x11\n" +
	"\rMARK_KIND_CWD\x10\x05*X\n" +
	"\tMarkTrust\x12\x1a\n" +
	"\x16MARK_TRUST_UNSPECIFIED\x10\x00\x12\x19\n" +
	"\x15MARK_TRUST_UNVERIFIED\x10\x01\x12\x14\n" +
	"\x10MARK_TRUST_NONCE\x10\x02*i\n" +
	"\x0fForegroundState\x12 \n" +
	"\x1cFOREGROUND_STATE_UNSPECIFIED\x10\x00\x12\x19\n" +
	"\x15FOREGROUND_STATE_IDLE\x10\x01\x12\x19\n" +
	"\x15FOREGROUND_STATE_BUSY\x10\x02*\x8b\x01\n" +
	"\rUnknownReason\x12\x1e\n" +
	"\x1aUNKNOWN_REASON_UNSPECIFIED\x10\x00\x12\x1e\n" +
	"\x1aUNKNOWN_REASON_UNSUPPORTED\x10\x01\x12\x1f\n" +
	"\x1bUNKNOWN_REASON_QUERY_FAILED\x10\x02\x12\x19\n" +
	"\x15UNKNOWN_REASON_REMOTE\x10\x03*\x8f\x01\n" +
	"\n" +
	"DropReason\x12\x1b\n" +
	"\x17DROP_REASON_UNSPECIFIED\x10\x00\x12\x1b\n" +
	"\x17DROP_REASON_REPLAY_FULL\x10\x01\x12!\n" +
	"\x1dDROP_REASON_DETACHED_OVERFLOW\x10\x02\x12$\n" +
	" DROP_REASON_EAGER_DRAIN_OVERFLOW\x10\x03*\xc9\x01\n" +
	"\n" +
	"NoticeKind\x12\x1b\n" +
	"\x17NOTICE_KIND_UNSPECIFIED\x10\x00\x12\x1b\n" +
	"\x17NOTICE_KIND_ENV_IGNORED\x10\x01\x12\x1b\n" +
	"\x17NOTICE_KIND_CWD_IGNORED\x10\x02\x12\x1f\n" +
	"\x1bNOTICE_KIND_FEATURE_IGNORED\x10\x03\x12\x1e\n" +
	"\x1aNOTICE_KIND_SIZE_DEFAULTED\x10\x04\x12#\n" +
	"\x1fNOTICE_KIND_INTEGRATION_IGNORED\x10\x05*\xeb\x01\n" +
	"\bClosedBy\x12\x19\n" +
	"\x15CLOSED_BY_UNSPECIFIED\x10\x00\x12\x15\n" +
	"\x11CLOSED_BY_REQUEST\x10\x01\x12\x1d\n" +
	"\x19CLOSED_BY_PLUGIN_SHUTDOWN\x10\x02\x12\x1c\n" +
	"\x18CLOSED_BY_DETACH_TIMEOUT\x10\x03\x12\x1a\n" +
	"\x16CLOSED_BY_IDLE_TIMEOUT\x10\x04\x12 \n" +
	"\x1cCLOSED_BY_CONNECTION_RETIRED\x10\x05\x12\x19\n" +
	"\x15CLOSED_BY_RUN_TIMEOUT\x10\x06\x12\x17\n" +
	"\x13CLOSED_BY_CANCELLED\x10\a*w\n" +
	"\fFailureStage\x12\x1d\n" +
	"\x19FAILURE_STAGE_UNSPECIFIED\x10\x00\x12\x17\n" +
	"\x13FAILURE_STAGE_START\x10\x01\x12\x15\n" +
	"\x11FAILURE_STAGE_RUN\x10\x02\x12\x18\n" +
	"\x14FAILURE_STAGE_STREAM\x10\x03*\x92\x01\n" +
	"\n" +
	"ExitSource\x12\x1b\n" +
	"\x17EXIT_SOURCE_UNSPECIFIED\x10\x00\x12\x17\n" +
	"\x13EXIT_SOURCE_PROCESS\x10\x01\x12\x1d\n" +
	"\x19EXIT_SOURCE_SIGNAL_FOLDED\x10\x02\x12\x19\n" +
	"\x15EXIT_SOURCE_TRANSPORT\x10\x03\x12\x14\n" +
	"\x10EXIT_SOURCE_HOST\x10\x04*[\n" +
	"\tLostCause\x12\x1a\n" +
	"\x16LOST_CAUSE_UNSPECIFIED\x10\x00\x12\x18\n" +
	"\x14LOST_CAUSE_NO_STATUS\x10\x01\x12\x18\n" +
	"\x14LOST_CAUSE_LINK_DEAD\x10\x02*~\n" +
	"\x12OutputCompleteness\x12#\n" +
	"\x1fOUTPUT_COMPLETENESS_UNSPECIFIED\x10\x00\x12 \n" +
	"\x1cOUTPUT_COMPLETENESS_COMPLETE\x10\x01\x12!\n" +
	"\x1dOUTPUT_COMPLETENESS_TRUNCATED\x10\x02*m\n" +
	"\tLinkState\x12\x1a\n" +
	"\x16LINK_STATE_UNSPECIFIED\x10\x00\x12\x13\n" +
	"\x0fLINK_STATE_GOOD\x10\x01\x12\x17\n" +
	"\x13LINK_STATE_DEGRADED\x10\x02\x12\x16\n" +
	"\x12LINK_STATE_STALLED\x10\x03*\x84\x01\n" +
	"\x0eTruncatePolicy\x12\x1f\n" +
	"\x1bTRUNCATE_POLICY_UNSPECIFIED\x10\x00\x12\x1d\n" +
	"\x19TRUNCATE_POLICY_HEAD_TAIL\x10\x01\x12\x18\n" +
	"\x14TRUNCATE_POLICY_HEAD\x10\x02\x12\x18\n" +
	"\x14TRUNCATE_POLICY_TAIL\x10\x03*\xa0\x01\n" +
	"\aOutcome\x12\x17\n" +
	"\x13OUTCOME_UNSPECIFIED\x10\x00\x12\x12\n" +
	"\x0eOUTCOME_EXITED\x10\x01\x12\x14\n" +
	"\x10OUTCOME_SIGNALED\x10\x02\x12\x15\n" +
	"\x11OUTCOME_TIMED_OUT\x10\x03\x12\x15\n" +
	"\x11OUTCOME_CANCELLED\x10\x04\x12\x10\n" +
	"\fOUTCOME_LOST\x10\x05\x12\x12\n" +
	"\x0eOUTCOME_FAILED\x10\x062\xcf\x05\n" +
	"\x04Exec\x12Y\n" +
	"\bDescribe\x12%.omniview.sdk.exec.v2.DescribeRequest\x1a&.omniview.sdk.exec.v2.DescribeResponse\x12V\n" +
	"\aOptions\x12$.omniview.sdk.exec.v2.OptionsRequest\x1a%.omniview.sdk.exec.v2.OptionsResponse\x12P\n" +
	"\x04Open\x12!.omniview.sdk.exec.v2.ClientFrame\x1a!.omniview.sdk.exec.v2.ServerFrame(\x010\x01\x12H\n" +
	"\x03Run\x12 .omniview.sdk.exec.v2.RunRequest\x1a\x1f.omniview.sdk.exec.v2.RunResult\x12O\n" +
	"\tRunStream\x12 .omniview.sdk.exec.v2.RunRequest\x1a\x1e.omniview.sdk.exec.v2.RunEvent0\x01\x12e\n" +
	"\fListSessions\x12).omniview.sdk.exec.v2.ListSessionsRequest\x1a*.omniview.sdk.exec.v2.ListSessionsResponse\x12_\n" +
	"\n" +
	"ReadOutput\x12'.omniview.sdk.exec.v2.ReadOutputRequest\x1a(.omniview.sdk.exec.v2.ReadOutputResponse\x12_\n" +
	"\n" +
	"EndSession\x12'.omniview.sdk.exec.v2.EndSessionRequest\x1a(.omniview.sdk.exec.v2.EndSessionResponseB8Z6github.com/omniviewdev/plugin-sdk/proto/exec/v2;execv2b\x06proto3"

var (
	file_proto_exec_v2_exec_proto_rawDescOnce sync.Once
	file_proto_exec_v2_exec_proto_rawDescData []byte
)

func file_proto_exec_v2_exec_proto_rawDescGZIP() []byte {
	file_proto_exec_v2_exec_proto_rawDescOnce.Do(func() {
		file_proto_exec_v2_exec_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_proto_exec_v2_exec_proto_rawDesc), len(file_proto_exec_v2_exec_proto_rawDesc)))
	})
	return file_proto_exec_v2_exec_proto_rawDescData
}

var file_proto_exec_v2_exec_proto_enumTypes = make([]protoimpl.EnumInfo, 33)
var file_proto_exec_v2_exec_proto_msgTypes = make([]protoimpl.MessageInfo, 68)
var file_proto_exec_v2_exec_proto_goTypes = []any{
	(Stream)(0),                  // 0: omniview.sdk.exec.v2.Stream
	(Signal)(0),                  // 1: omniview.sdk.exec.v2.Signal
	(FactMode)(0),                // 2: omniview.sdk.exec.v2.FactMode
	(KillBehavior)(0),            // 3: omniview.sdk.exec.v2.KillBehavior
	(Survival)(0),                // 4: omniview.sdk.exec.v2.Survival
	(ExitReport)(0),              // 5: omniview.sdk.exec.v2.ExitReport
	(StallBehavior)(0),           // 6: omniview.sdk.exec.v2.StallBehavior
	(IntegrationMode)(0),         // 7: omniview.sdk.exec.v2.IntegrationMode
	(Feature)(0),                 // 8: omniview.sdk.exec.v2.Feature
	(ErrorClass)(0),              // 9: omniview.sdk.exec.v2.ErrorClass
	(ErrorReason)(0),             // 10: omniview.sdk.exec.v2.ErrorReason
	(ChoiceState)(0),             // 11: omniview.sdk.exec.v2.ChoiceState
	(CloseMode)(0),               // 12: omniview.sdk.exec.v2.CloseMode
	(FactKind)(0),                // 13: omniview.sdk.exec.v2.FactKind
	(DetachReason)(0),            // 14: omniview.sdk.exec.v2.DetachReason
	(IntegrationTier)(0),         // 15: omniview.sdk.exec.v2.IntegrationTier
	(MouseTracking)(0),           // 16: omniview.sdk.exec.v2.MouseTracking
	(MouseEncoding)(0),           // 17: omniview.sdk.exec.v2.MouseEncoding
	(ResizeResult)(0),            // 18: omniview.sdk.exec.v2.ResizeResult
	(MarkKind)(0),                // 19: omniview.sdk.exec.v2.MarkKind
	(MarkTrust)(0),               // 20: omniview.sdk.exec.v2.MarkTrust
	(ForegroundState)(0),         // 21: omniview.sdk.exec.v2.ForegroundState
	(UnknownReason)(0),           // 22: omniview.sdk.exec.v2.UnknownReason
	(DropReason)(0),              // 23: omniview.sdk.exec.v2.DropReason
	(NoticeKind)(0),              // 24: omniview.sdk.exec.v2.NoticeKind
	(ClosedBy)(0),                // 25: omniview.sdk.exec.v2.ClosedBy
	(FailureStage)(0),            // 26: omniview.sdk.exec.v2.FailureStage
	(ExitSource)(0),              // 27: omniview.sdk.exec.v2.ExitSource
	(LostCause)(0),               // 28: omniview.sdk.exec.v2.LostCause
	(OutputCompleteness)(0),      // 29: omniview.sdk.exec.v2.OutputCompleteness
	(LinkState)(0),               // 30: omniview.sdk.exec.v2.LinkState
	(TruncatePolicy)(0),          // 31: omniview.sdk.exec.v2.TruncatePolicy
	(Outcome)(0),                 // 32: omniview.sdk.exec.v2.Outcome
	(*Size)(nil),                 // 33: omniview.sdk.exec.v2.Size
	(*NameValue)(nil),            // 34: omniview.sdk.exec.v2.NameValue
	(*TargetRef)(nil),            // 35: omniview.sdk.exec.v2.TargetRef
	(*ErrorFacts)(nil),           // 36: omniview.sdk.exec.v2.ErrorFacts
	(*SignalSet)(nil),            // 37: omniview.sdk.exec.v2.SignalSet
	(*IntegrationModeSet)(nil),   // 38: omniview.sdk.exec.v2.IntegrationModeSet
	(*Capabilities)(nil),         // 39: omniview.sdk.exec.v2.Capabilities
	(*SessionLimits)(nil),        // 40: omniview.sdk.exec.v2.SessionLimits
	(*RunLimits)(nil),            // 41: omniview.sdk.exec.v2.RunLimits
	(*TargetInfo)(nil),           // 42: omniview.sdk.exec.v2.TargetInfo
	(*DescribeRequest)(nil),      // 43: omniview.sdk.exec.v2.DescribeRequest
	(*DescribeResponse)(nil),     // 44: omniview.sdk.exec.v2.DescribeResponse
	(*OptionsRequest)(nil),       // 45: omniview.sdk.exec.v2.OptionsRequest
	(*Choice)(nil),               // 46: omniview.sdk.exec.v2.Choice
	(*Option)(nil),               // 47: omniview.sdk.exec.v2.Option
	(*OptionsResponse)(nil),      // 48: omniview.sdk.exec.v2.OptionsResponse
	(*IntegrationRequest)(nil),   // 49: omniview.sdk.exec.v2.IntegrationRequest
	(*Start)(nil),                // 50: omniview.sdk.exec.v2.Start
	(*Attach)(nil),               // 51: omniview.sdk.exec.v2.Attach
	(*OpenRequest)(nil),          // 52: omniview.sdk.exec.v2.OpenRequest
	(*ClientFrame)(nil),          // 53: omniview.sdk.exec.v2.ClientFrame
	(*Resize)(nil),               // 54: omniview.sdk.exec.v2.Resize
	(*CloseInput)(nil),           // 55: omniview.sdk.exec.v2.CloseInput
	(*Close)(nil),                // 56: omniview.sdk.exec.v2.Close
	(*Detach)(nil),               // 57: omniview.sdk.exec.v2.Detach
	(*Query)(nil),                // 58: omniview.sdk.exec.v2.Query
	(*ServerFrame)(nil),          // 59: omniview.sdk.exec.v2.ServerFrame
	(*Opened)(nil),               // 60: omniview.sdk.exec.v2.Opened
	(*Started)(nil),              // 61: omniview.sdk.exec.v2.Started
	(*Detached)(nil),             // 62: omniview.sdk.exec.v2.Detached
	(*TargetFacts)(nil),          // 63: omniview.sdk.exec.v2.TargetFacts
	(*Program)(nil),              // 64: omniview.sdk.exec.v2.Program
	(*IntegrationFacts)(nil),     // 65: omniview.sdk.exec.v2.IntegrationFacts
	(*SessionFacts)(nil),         // 66: omniview.sdk.exec.v2.SessionFacts
	(*TerminalModes)(nil),        // 67: omniview.sdk.exec.v2.TerminalModes
	(*Replay)(nil),               // 68: omniview.sdk.exec.v2.Replay
	(*Output)(nil),               // 69: omniview.sdk.exec.v2.Output
	(*Resized)(nil),              // 70: omniview.sdk.exec.v2.Resized
	(*Mark)(nil),                 // 71: omniview.sdk.exec.v2.Mark
	(*Foreground)(nil),           // 72: omniview.sdk.exec.v2.Foreground
	(*Directory)(nil),            // 73: omniview.sdk.exec.v2.Directory
	(*Fact)(nil),                 // 74: omniview.sdk.exec.v2.Fact
	(*Dropped)(nil),              // 75: omniview.sdk.exec.v2.Dropped
	(*Notice)(nil),               // 76: omniview.sdk.exec.v2.Notice
	(*Exited)(nil),               // 77: omniview.sdk.exec.v2.Exited
	(*Signaled)(nil),             // 78: omniview.sdk.exec.v2.Signaled
	(*Closed)(nil),               // 79: omniview.sdk.exec.v2.Closed
	(*Failed)(nil),               // 80: omniview.sdk.exec.v2.Failed
	(*Lost)(nil),                 // 81: omniview.sdk.exec.v2.Lost
	(*End)(nil),                  // 82: omniview.sdk.exec.v2.End
	(*Ended)(nil),                // 83: omniview.sdk.exec.v2.Ended
	(*Link)(nil),                 // 84: omniview.sdk.exec.v2.Link
	(*SessionInfo)(nil),          // 85: omniview.sdk.exec.v2.SessionInfo
	(*ListSessionsRequest)(nil),  // 86: omniview.sdk.exec.v2.ListSessionsRequest
	(*ListSessionsResponse)(nil), // 87: omniview.sdk.exec.v2.ListSessionsResponse
	(*ReadOutputRequest)(nil),    // 88: omniview.sdk.exec.v2.ReadOutputRequest
	(*ReadOutputResponse)(nil),   // 89: omniview.sdk.exec.v2.ReadOutputResponse
	(*EndSessionRequest)(nil),    // 90: omniview.sdk.exec.v2.EndSessionRequest
	(*EndSessionResponse)(nil),   // 91: omniview.sdk.exec.v2.EndSessionResponse
	(*OutputLimits)(nil),         // 92: omniview.sdk.exec.v2.OutputLimits
	(*RunRequest)(nil),           // 93: omniview.sdk.exec.v2.RunRequest
	(*OutputAccount)(nil),        // 94: omniview.sdk.exec.v2.OutputAccount
	(*RunResult)(nil),            // 95: omniview.sdk.exec.v2.RunResult
	(*RunEvent)(nil),             // 96: omniview.sdk.exec.v2.RunEvent
	(*RunStarted)(nil),           // 97: omniview.sdk.exec.v2.RunStarted
	(*RunProgress)(nil),          // 98: omniview.sdk.exec.v2.RunProgress
	(*RunEnded)(nil),             // 99: omniview.sdk.exec.v2.RunEnded
	nil,                          // 100: omniview.sdk.exec.v2.TargetFacts.SelectionEntry
	(*v2.ResourceReference)(nil), // 101: omniview.resource.v2.ResourceReference
	(*v2.AccessRoute)(nil),       // 102: omniview.resource.v2.AccessRoute
}
var file_proto_exec_v2_exec_proto_depIdxs = []int32{
	101, // 0: omniview.sdk.exec.v2.TargetRef.reference:type_name -> omniview.resource.v2.ResourceReference
	102, // 1: omniview.sdk.exec.v2.TargetRef.route:type_name -> omniview.resource.v2.AccessRoute
	34,  // 2: omniview.sdk.exec.v2.TargetRef.selection:type_name -> omniview.sdk.exec.v2.NameValue
	1,   // 3: omniview.sdk.exec.v2.SignalSet.signals:type_name -> omniview.sdk.exec.v2.Signal
	7,   // 4: omniview.sdk.exec.v2.IntegrationModeSet.modes:type_name -> omniview.sdk.exec.v2.IntegrationMode
	37,  // 5: omniview.sdk.exec.v2.Capabilities.signals:type_name -> omniview.sdk.exec.v2.SignalSet
	3,   // 6: omniview.sdk.exec.v2.Capabilities.kill:type_name -> omniview.sdk.exec.v2.KillBehavior
	2,   // 7: omniview.sdk.exec.v2.Capabilities.cwd_fact:type_name -> omniview.sdk.exec.v2.FactMode
	2,   // 8: omniview.sdk.exec.v2.Capabilities.foreground_fact:type_name -> omniview.sdk.exec.v2.FactMode
	4,   // 9: omniview.sdk.exec.v2.Capabilities.host_restart:type_name -> omniview.sdk.exec.v2.Survival
	5,   // 10: omniview.sdk.exec.v2.Capabilities.exit_report:type_name -> omniview.sdk.exec.v2.ExitReport
	6,   // 11: omniview.sdk.exec.v2.Capabilities.stall:type_name -> omniview.sdk.exec.v2.StallBehavior
	38,  // 12: omniview.sdk.exec.v2.Capabilities.integration:type_name -> omniview.sdk.exec.v2.IntegrationModeSet
	39,  // 13: omniview.sdk.exec.v2.TargetInfo.capabilities:type_name -> omniview.sdk.exec.v2.Capabilities
	40,  // 14: omniview.sdk.exec.v2.TargetInfo.session:type_name -> omniview.sdk.exec.v2.SessionLimits
	41,  // 15: omniview.sdk.exec.v2.TargetInfo.run:type_name -> omniview.sdk.exec.v2.RunLimits
	8,   // 16: omniview.sdk.exec.v2.DescribeResponse.features:type_name -> omniview.sdk.exec.v2.Feature
	42,  // 17: omniview.sdk.exec.v2.DescribeResponse.targets:type_name -> omniview.sdk.exec.v2.TargetInfo
	35,  // 18: omniview.sdk.exec.v2.OptionsRequest.target:type_name -> omniview.sdk.exec.v2.TargetRef
	11,  // 19: omniview.sdk.exec.v2.Choice.state:type_name -> omniview.sdk.exec.v2.ChoiceState
	39,  // 20: omniview.sdk.exec.v2.Choice.capabilities:type_name -> omniview.sdk.exec.v2.Capabilities
	46,  // 21: omniview.sdk.exec.v2.Option.choices:type_name -> omniview.sdk.exec.v2.Choice
	47,  // 22: omniview.sdk.exec.v2.OptionsResponse.options:type_name -> omniview.sdk.exec.v2.Option
	7,   // 23: omniview.sdk.exec.v2.IntegrationRequest.mode:type_name -> omniview.sdk.exec.v2.IntegrationMode
	35,  // 24: omniview.sdk.exec.v2.Start.target:type_name -> omniview.sdk.exec.v2.TargetRef
	33,  // 25: omniview.sdk.exec.v2.Start.size:type_name -> omniview.sdk.exec.v2.Size
	34,  // 26: omniview.sdk.exec.v2.Start.env:type_name -> omniview.sdk.exec.v2.NameValue
	34,  // 27: omniview.sdk.exec.v2.Start.labels:type_name -> omniview.sdk.exec.v2.NameValue
	8,   // 28: omniview.sdk.exec.v2.Start.features:type_name -> omniview.sdk.exec.v2.Feature
	49,  // 29: omniview.sdk.exec.v2.Start.integration:type_name -> omniview.sdk.exec.v2.IntegrationRequest
	50,  // 30: omniview.sdk.exec.v2.OpenRequest.start:type_name -> omniview.sdk.exec.v2.Start
	51,  // 31: omniview.sdk.exec.v2.OpenRequest.attach:type_name -> omniview.sdk.exec.v2.Attach
	52,  // 32: omniview.sdk.exec.v2.ClientFrame.open:type_name -> omniview.sdk.exec.v2.OpenRequest
	54,  // 33: omniview.sdk.exec.v2.ClientFrame.resize:type_name -> omniview.sdk.exec.v2.Resize
	1,   // 34: omniview.sdk.exec.v2.ClientFrame.signal:type_name -> omniview.sdk.exec.v2.Signal
	55,  // 35: omniview.sdk.exec.v2.ClientFrame.close_input:type_name -> omniview.sdk.exec.v2.CloseInput
	58,  // 36: omniview.sdk.exec.v2.ClientFrame.query:type_name -> omniview.sdk.exec.v2.Query
	56,  // 37: omniview.sdk.exec.v2.ClientFrame.close:type_name -> omniview.sdk.exec.v2.Close
	57,  // 38: omniview.sdk.exec.v2.ClientFrame.detach:type_name -> omniview.sdk.exec.v2.Detach
	33,  // 39: omniview.sdk.exec.v2.Resize.size:type_name -> omniview.sdk.exec.v2.Size
	12,  // 40: omniview.sdk.exec.v2.Close.mode:type_name -> omniview.sdk.exec.v2.CloseMode
	13,  // 41: omniview.sdk.exec.v2.Query.kind:type_name -> omniview.sdk.exec.v2.FactKind
	60,  // 42: omniview.sdk.exec.v2.ServerFrame.opened:type_name -> omniview.sdk.exec.v2.Opened
	69,  // 43: omniview.sdk.exec.v2.ServerFrame.output:type_name -> omniview.sdk.exec.v2.Output
	70,  // 44: omniview.sdk.exec.v2.ServerFrame.resized:type_name -> omniview.sdk.exec.v2.Resized
	71,  // 45: omniview.sdk.exec.v2.ServerFrame.mark:type_name -> omniview.sdk.exec.v2.Mark
	74,  // 46: omniview.sdk.exec.v2.ServerFrame.fact:type_name -> omniview.sdk.exec.v2.Fact
	75,  // 47: omniview.sdk.exec.v2.ServerFrame.dropped:type_name -> omniview.sdk.exec.v2.Dropped
	76,  // 48: omniview.sdk.exec.v2.ServerFrame.notice:type_name -> omniview.sdk.exec.v2.Notice
	83,  // 49: omniview.sdk.exec.v2.ServerFrame.ended:type_name -> omniview.sdk.exec.v2.Ended
	61,  // 50: omniview.sdk.exec.v2.ServerFrame.started:type_name -> omniview.sdk.exec.v2.Started
	84,  // 51: omniview.sdk.exec.v2.ServerFrame.link:type_name -> omniview.sdk.exec.v2.Link
	62,  // 52: omniview.sdk.exec.v2.ServerFrame.detached:type_name -> omniview.sdk.exec.v2.Detached
	66,  // 53: omniview.sdk.exec.v2.Opened.facts:type_name -> omniview.sdk.exec.v2.SessionFacts
	68,  // 54: omniview.sdk.exec.v2.Opened.replay:type_name -> omniview.sdk.exec.v2.Replay
	66,  // 55: omniview.sdk.exec.v2.Started.facts:type_name -> omniview.sdk.exec.v2.SessionFacts
	14,  // 56: omniview.sdk.exec.v2.Detached.reason:type_name -> omniview.sdk.exec.v2.DetachReason
	100, // 57: omniview.sdk.exec.v2.TargetFacts.selection:type_name -> omniview.sdk.exec.v2.TargetFacts.SelectionEntry
	7,   // 58: omniview.sdk.exec.v2.IntegrationFacts.mode:type_name -> omniview.sdk.exec.v2.IntegrationMode
	63,  // 59: omniview.sdk.exec.v2.SessionFacts.target:type_name -> omniview.sdk.exec.v2.TargetFacts
	64,  // 60: omniview.sdk.exec.v2.SessionFacts.program:type_name -> omniview.sdk.exec.v2.Program
	33,  // 61: omniview.sdk.exec.v2.SessionFacts.size:type_name -> omniview.sdk.exec.v2.Size
	39,  // 62: omniview.sdk.exec.v2.SessionFacts.capabilities:type_name -> omniview.sdk.exec.v2.Capabilities
	40,  // 63: omniview.sdk.exec.v2.SessionFacts.limits:type_name -> omniview.sdk.exec.v2.SessionLimits
	65,  // 64: omniview.sdk.exec.v2.SessionFacts.integration:type_name -> omniview.sdk.exec.v2.IntegrationFacts
	16,  // 65: omniview.sdk.exec.v2.TerminalModes.mouse_tracking:type_name -> omniview.sdk.exec.v2.MouseTracking
	17,  // 66: omniview.sdk.exec.v2.TerminalModes.mouse_encoding:type_name -> omniview.sdk.exec.v2.MouseEncoding
	33,  // 67: omniview.sdk.exec.v2.Replay.start_size:type_name -> omniview.sdk.exec.v2.Size
	67,  // 68: omniview.sdk.exec.v2.Replay.start_modes:type_name -> omniview.sdk.exec.v2.TerminalModes
	33,  // 69: omniview.sdk.exec.v2.Replay.size:type_name -> omniview.sdk.exec.v2.Size
	67,  // 70: omniview.sdk.exec.v2.Replay.modes:type_name -> omniview.sdk.exec.v2.TerminalModes
	0,   // 71: omniview.sdk.exec.v2.Output.stream:type_name -> omniview.sdk.exec.v2.Stream
	33,  // 72: omniview.sdk.exec.v2.Resized.size:type_name -> omniview.sdk.exec.v2.Size
	18,  // 73: omniview.sdk.exec.v2.Resized.result:type_name -> omniview.sdk.exec.v2.ResizeResult
	19,  // 74: omniview.sdk.exec.v2.Mark.kind:type_name -> omniview.sdk.exec.v2.MarkKind
	20,  // 75: omniview.sdk.exec.v2.Mark.trust:type_name -> omniview.sdk.exec.v2.MarkTrust
	21,  // 76: omniview.sdk.exec.v2.Foreground.state:type_name -> omniview.sdk.exec.v2.ForegroundState
	22,  // 77: omniview.sdk.exec.v2.Foreground.reason:type_name -> omniview.sdk.exec.v2.UnknownReason
	22,  // 78: omniview.sdk.exec.v2.Directory.reason:type_name -> omniview.sdk.exec.v2.UnknownReason
	72,  // 79: omniview.sdk.exec.v2.Fact.foreground:type_name -> omniview.sdk.exec.v2.Foreground
	73,  // 80: omniview.sdk.exec.v2.Fact.cwd:type_name -> omniview.sdk.exec.v2.Directory
	15,  // 81: omniview.sdk.exec.v2.Fact.tier:type_name -> omniview.sdk.exec.v2.IntegrationTier
	0,   // 82: omniview.sdk.exec.v2.Dropped.stream:type_name -> omniview.sdk.exec.v2.Stream
	23,  // 83: omniview.sdk.exec.v2.Dropped.reason:type_name -> omniview.sdk.exec.v2.DropReason
	24,  // 84: omniview.sdk.exec.v2.Notice.kind:type_name -> omniview.sdk.exec.v2.NoticeKind
	27,  // 85: omniview.sdk.exec.v2.Exited.source:type_name -> omniview.sdk.exec.v2.ExitSource
	27,  // 86: omniview.sdk.exec.v2.Signaled.source:type_name -> omniview.sdk.exec.v2.ExitSource
	25,  // 87: omniview.sdk.exec.v2.Closed.by:type_name -> omniview.sdk.exec.v2.ClosedBy
	9,   // 88: omniview.sdk.exec.v2.Failed.class:type_name -> omniview.sdk.exec.v2.ErrorClass
	10,  // 89: omniview.sdk.exec.v2.Failed.reason:type_name -> omniview.sdk.exec.v2.ErrorReason
	36,  // 90: omniview.sdk.exec.v2.Failed.facts:type_name -> omniview.sdk.exec.v2.ErrorFacts
	26,  // 91: omniview.sdk.exec.v2.Failed.stage:type_name -> omniview.sdk.exec.v2.FailureStage
	28,  // 92: omniview.sdk.exec.v2.Lost.cause:type_name -> omniview.sdk.exec.v2.LostCause
	77,  // 93: omniview.sdk.exec.v2.End.exited:type_name -> omniview.sdk.exec.v2.Exited
	78,  // 94: omniview.sdk.exec.v2.End.signaled:type_name -> omniview.sdk.exec.v2.Signaled
	79,  // 95: omniview.sdk.exec.v2.End.closed:type_name -> omniview.sdk.exec.v2.Closed
	80,  // 96: omniview.sdk.exec.v2.End.failed:type_name -> omniview.sdk.exec.v2.Failed
	81,  // 97: omniview.sdk.exec.v2.End.lost:type_name -> omniview.sdk.exec.v2.Lost
	82,  // 98: omniview.sdk.exec.v2.Ended.end:type_name -> omniview.sdk.exec.v2.End
	29,  // 99: omniview.sdk.exec.v2.Ended.output:type_name -> omniview.sdk.exec.v2.OutputCompleteness
	30,  // 100: omniview.sdk.exec.v2.Link.state:type_name -> omniview.sdk.exec.v2.LinkState
	66,  // 101: omniview.sdk.exec.v2.SessionInfo.facts:type_name -> omniview.sdk.exec.v2.SessionFacts
	83,  // 102: omniview.sdk.exec.v2.SessionInfo.ended_with:type_name -> omniview.sdk.exec.v2.Ended
	30,  // 103: omniview.sdk.exec.v2.SessionInfo.link:type_name -> omniview.sdk.exec.v2.LinkState
	85,  // 104: omniview.sdk.exec.v2.ListSessionsResponse.sessions:type_name -> omniview.sdk.exec.v2.SessionInfo
	69,  // 105: omniview.sdk.exec.v2.ReadOutputResponse.chunks:type_name -> omniview.sdk.exec.v2.Output
	12,  // 106: omniview.sdk.exec.v2.EndSessionRequest.mode:type_name -> omniview.sdk.exec.v2.CloseMode
	31,  // 107: omniview.sdk.exec.v2.OutputLimits.policy:type_name -> omniview.sdk.exec.v2.TruncatePolicy
	35,  // 108: omniview.sdk.exec.v2.RunRequest.target:type_name -> omniview.sdk.exec.v2.TargetRef
	34,  // 109: omniview.sdk.exec.v2.RunRequest.env:type_name -> omniview.sdk.exec.v2.NameValue
	92,  // 110: omniview.sdk.exec.v2.RunRequest.output:type_name -> omniview.sdk.exec.v2.OutputLimits
	34,  // 111: omniview.sdk.exec.v2.RunRequest.labels:type_name -> omniview.sdk.exec.v2.NameValue
	29,  // 112: omniview.sdk.exec.v2.OutputAccount.completeness:type_name -> omniview.sdk.exec.v2.OutputCompleteness
	32,  // 113: omniview.sdk.exec.v2.RunResult.outcome:type_name -> omniview.sdk.exec.v2.Outcome
	94,  // 114: omniview.sdk.exec.v2.RunResult.stdout_account:type_name -> omniview.sdk.exec.v2.OutputAccount
	94,  // 115: omniview.sdk.exec.v2.RunResult.stderr_account:type_name -> omniview.sdk.exec.v2.OutputAccount
	63,  // 116: omniview.sdk.exec.v2.RunResult.target:type_name -> omniview.sdk.exec.v2.TargetFacts
	64,  // 117: omniview.sdk.exec.v2.RunResult.program:type_name -> omniview.sdk.exec.v2.Program
	27,  // 118: omniview.sdk.exec.v2.RunResult.exit_source:type_name -> omniview.sdk.exec.v2.ExitSource
	80,  // 119: omniview.sdk.exec.v2.RunResult.failed:type_name -> omniview.sdk.exec.v2.Failed
	97,  // 120: omniview.sdk.exec.v2.RunEvent.started:type_name -> omniview.sdk.exec.v2.RunStarted
	69,  // 121: omniview.sdk.exec.v2.RunEvent.output:type_name -> omniview.sdk.exec.v2.Output
	98,  // 122: omniview.sdk.exec.v2.RunEvent.progress:type_name -> omniview.sdk.exec.v2.RunProgress
	99,  // 123: omniview.sdk.exec.v2.RunEvent.ended:type_name -> omniview.sdk.exec.v2.RunEnded
	63,  // 124: omniview.sdk.exec.v2.RunStarted.target:type_name -> omniview.sdk.exec.v2.TargetFacts
	64,  // 125: omniview.sdk.exec.v2.RunStarted.program:type_name -> omniview.sdk.exec.v2.Program
	95,  // 126: omniview.sdk.exec.v2.RunEnded.result:type_name -> omniview.sdk.exec.v2.RunResult
	43,  // 127: omniview.sdk.exec.v2.Exec.Describe:input_type -> omniview.sdk.exec.v2.DescribeRequest
	45,  // 128: omniview.sdk.exec.v2.Exec.Options:input_type -> omniview.sdk.exec.v2.OptionsRequest
	53,  // 129: omniview.sdk.exec.v2.Exec.Open:input_type -> omniview.sdk.exec.v2.ClientFrame
	93,  // 130: omniview.sdk.exec.v2.Exec.Run:input_type -> omniview.sdk.exec.v2.RunRequest
	93,  // 131: omniview.sdk.exec.v2.Exec.RunStream:input_type -> omniview.sdk.exec.v2.RunRequest
	86,  // 132: omniview.sdk.exec.v2.Exec.ListSessions:input_type -> omniview.sdk.exec.v2.ListSessionsRequest
	88,  // 133: omniview.sdk.exec.v2.Exec.ReadOutput:input_type -> omniview.sdk.exec.v2.ReadOutputRequest
	90,  // 134: omniview.sdk.exec.v2.Exec.EndSession:input_type -> omniview.sdk.exec.v2.EndSessionRequest
	44,  // 135: omniview.sdk.exec.v2.Exec.Describe:output_type -> omniview.sdk.exec.v2.DescribeResponse
	48,  // 136: omniview.sdk.exec.v2.Exec.Options:output_type -> omniview.sdk.exec.v2.OptionsResponse
	59,  // 137: omniview.sdk.exec.v2.Exec.Open:output_type -> omniview.sdk.exec.v2.ServerFrame
	95,  // 138: omniview.sdk.exec.v2.Exec.Run:output_type -> omniview.sdk.exec.v2.RunResult
	96,  // 139: omniview.sdk.exec.v2.Exec.RunStream:output_type -> omniview.sdk.exec.v2.RunEvent
	87,  // 140: omniview.sdk.exec.v2.Exec.ListSessions:output_type -> omniview.sdk.exec.v2.ListSessionsResponse
	89,  // 141: omniview.sdk.exec.v2.Exec.ReadOutput:output_type -> omniview.sdk.exec.v2.ReadOutputResponse
	91,  // 142: omniview.sdk.exec.v2.Exec.EndSession:output_type -> omniview.sdk.exec.v2.EndSessionResponse
	135, // [135:143] is the sub-list for method output_type
	127, // [127:135] is the sub-list for method input_type
	127, // [127:127] is the sub-list for extension type_name
	127, // [127:127] is the sub-list for extension extendee
	0,   // [0:127] is the sub-list for field type_name
}

func init() { file_proto_exec_v2_exec_proto_init() }
func file_proto_exec_v2_exec_proto_init() {
	if File_proto_exec_v2_exec_proto != nil {
		return
	}
	file_proto_exec_v2_exec_proto_msgTypes[6].OneofWrappers = []any{}
	file_proto_exec_v2_exec_proto_msgTypes[19].OneofWrappers = []any{
		(*OpenRequest_Start)(nil),
		(*OpenRequest_Attach)(nil),
	}
	file_proto_exec_v2_exec_proto_msgTypes[20].OneofWrappers = []any{
		(*ClientFrame_Open)(nil),
		(*ClientFrame_Input)(nil),
		(*ClientFrame_Resize)(nil),
		(*ClientFrame_Signal)(nil),
		(*ClientFrame_CloseInput)(nil),
		(*ClientFrame_Query)(nil),
		(*ClientFrame_Close)(nil),
		(*ClientFrame_Detach)(nil),
	}
	file_proto_exec_v2_exec_proto_msgTypes[26].OneofWrappers = []any{
		(*ServerFrame_Opened)(nil),
		(*ServerFrame_Output)(nil),
		(*ServerFrame_Resized)(nil),
		(*ServerFrame_Mark)(nil),
		(*ServerFrame_Fact)(nil),
		(*ServerFrame_Dropped)(nil),
		(*ServerFrame_Notice)(nil),
		(*ServerFrame_Ended)(nil),
		(*ServerFrame_Started)(nil),
		(*ServerFrame_Link)(nil),
		(*ServerFrame_Detached)(nil),
	}
	file_proto_exec_v2_exec_proto_msgTypes[33].OneofWrappers = []any{}
	file_proto_exec_v2_exec_proto_msgTypes[38].OneofWrappers = []any{}
	file_proto_exec_v2_exec_proto_msgTypes[41].OneofWrappers = []any{
		(*Fact_Foreground)(nil),
		(*Fact_Cwd)(nil),
		(*Fact_Tier)(nil),
	}
	file_proto_exec_v2_exec_proto_msgTypes[42].OneofWrappers = []any{}
	file_proto_exec_v2_exec_proto_msgTypes[45].OneofWrappers = []any{}
	file_proto_exec_v2_exec_proto_msgTypes[49].OneofWrappers = []any{
		(*End_Exited)(nil),
		(*End_Signaled)(nil),
		(*End_Closed)(nil),
		(*End_Failed)(nil),
		(*End_Lost)(nil),
	}
	file_proto_exec_v2_exec_proto_msgTypes[50].OneofWrappers = []any{}
	file_proto_exec_v2_exec_proto_msgTypes[62].OneofWrappers = []any{}
	file_proto_exec_v2_exec_proto_msgTypes[63].OneofWrappers = []any{
		(*RunEvent_Started)(nil),
		(*RunEvent_Output)(nil),
		(*RunEvent_Progress)(nil),
		(*RunEvent_Ended)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_proto_exec_v2_exec_proto_rawDesc), len(file_proto_exec_v2_exec_proto_rawDesc)),
			NumEnums:      33,
			NumMessages:   68,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_proto_exec_v2_exec_proto_goTypes,
		DependencyIndexes: file_proto_exec_v2_exec_proto_depIdxs,
		EnumInfos:         file_proto_exec_v2_exec_proto_enumTypes,
		MessageInfos:      file_proto_exec_v2_exec_proto_msgTypes,
	}.Build()
	File_proto_exec_v2_exec_proto = out.File
	file_proto_exec_v2_exec_proto_goTypes = nil
	file_proto_exec_v2_exec_proto_depIdxs = nil
}
