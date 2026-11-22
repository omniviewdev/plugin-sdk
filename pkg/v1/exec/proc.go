package exec

import (
	"io"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Size is a terminal size in character cells. The zero value means no size.
type Size struct{ Cols, Rows uint16 }

// IsZero reports whether the size is absent.
func (s Size) IsZero() bool { return s == Size{} }

// Proc is the command's world, owned by the SDK: the validated request and the stdio the author copies between
// the target and the host. An ExecFunc receives one per command.
type Proc struct {
	// Context is the admitted, connection-bound plugin context of the request.
	Context *types.PluginContext
	// Request is the validated request.
	Request Request
	// Capabilities is a deep copy of the capabilities this session was opened with, which are what the host read in
	// Opened: the target's declared capabilities, the upper bound of its kind, narrowed by the Capabilities of the choice
	// that each option takes in Request.Selection (see Choice.Capabilities). The mode of a target whose modes differ (a
	// Kubernetes attach reports no exit status, an exec does) is declared on its choices, so it is in this set already and
	// the function does not report it. The function may change its copy freely, and reports it in
	// SessionFacts.Capabilities only for what it learns at run time and that narrows the session further: a narrower
	// value is taken as it is, a wider one is replaced by the value this set holds.
	Capabilities Capabilities
	// Stdin is the command's input. It is nil only for a run whose request carries no stdin; a session always has
	// one. It reports io.EOF after the host closed the input or when the session ends.
	Stdin io.Reader
	// Stdout is the command's output; the only output of a command that runs with a terminal. For a target whose
	// Capabilities.Stall is StallPropagates, Write blocks while the host is slow, so that the pace of the host
	// reaches the target; for StallLossy it is never blocked for more than 50 ms and the SDK counts what it
	// overwrites. It fails once the session has ended.
	Stdout io.Writer
	// Stderr is the command's error output. It is nil for a command that runs with a terminal.
	Stderr io.Writer
	// Resize delivers terminal sizes, latest wins, and never blocks the SDK. For a terminal its first value is
	// Request.Size. It is closed when the command ends.
	Resize <-chan Size
	// Started reports what the target knows once the command is running: the program that was resolved, a pid, the
	// terminal and size the target really granted and, if the run showed something narrower than Opened carried, this
	// session's capabilities. The mode is not reported here: it is declared on Choice.Capabilities. Never nil.
	//
	// The host receives the session's Started at most once, and always before the first output, so call it at most once
	// and before the first byte the function writes. A function that writes output without having called it gets a
	// Started that the SDK makes from the facts of the open (the requested program and the capabilities of
	// Proc.Capabilities), sent before that output: it is how a command whose start can only be observed in its output
	// is reported. A call after the first output, or a second one, is refused: it changes nothing and is logged as the
	// author's bug. A function that neither writes nor calls it has no Started.
	Started func(SessionFacts)
	// Link reports a change of the transport's health. Never nil; the SDK sends a Link frame on a change, and only
	// for a target whose Capabilities.LinkHealth is set.
	Link func(LinkState)
}

// Request is the validated request a command runs under.
type Request struct {
	// Ref is the canonical reference of the target thing: provider, type, route and connection match the
	// registered target and the caller's connection, and the incarnation is present when the host sent one.
	Ref resourcev2.ResourceReference
	// Selection holds exactly the choices the target's Options declared, keyed by option name.
	Selection map[string]string
	// Argv is the command. It is never empty: the target's DefaultArgv is filled in when the host sent none.
	Argv []string
	// TTY says the command runs with a terminal.
	TTY bool
	// Size is the initial terminal size; the first value of Proc.Resize. Zero when no terminal or no size.
	Size Size
	// Env is the environment to add; only if Capabilities.Env.
	Env map[string]string
	// Cwd is the working directory; only if Capabilities.Cwd.
	Cwd string
	// Labels are opaque, bounded labels of the host; never authority.
	Labels map[string]string
}

// SessionFacts is what the author reports when the command is running, and refines the session's facts: the SDK
// sends it to the host as the Started frame. Every field says what its zero value means.
type SessionFacts struct {
	// Program is what really runs (the default shell resolved), not what was asked.
	Program Program
	// PID is the process id when the author has one (a local process), else zero.
	PID uint32
	// TTY is whether the target really granted a terminal. Nil means the author does not say: the fact stays absent,
	// which is not "no terminal".
	TTY *bool
	// Size is the size the target really applied. Zero means not applied or unknown.
	Size Size
	// Term is the TERM the target applied, at most 64 bytes. Empty means unknown.
	Term string
	// Transport is a closed code for the channel to the target ("k8s-ws-v5"), matching ^[a-z0-9_.-]{1,40}$. Empty
	// means unknown.
	Transport string
	// Capabilities is what this session can do when the report is made. Nil means the set the session was opened with
	// (Proc.Capabilities as the function received it), which the host has read already. Otherwise it is this session's
	// effective set, which may only narrow the opened one: report it only for what the run showed, because the mode of
	// the session (a Kubernetes attach reports no exit status, an exec does) is declared on Choice.Capabilities and is in
	// Opened. Every field can be narrowed, each in the order the proto writes at the field (proto/exec/v2/exec.proto,
	// message Capabilities): a flag from true to false, Signals and Integration to a subset of the opened list, an enum
	// to a lower value. Kill is the usual one (a tty shell ends when its stream closes, a non-tty command may not). A
	// field reported above the opened one is replaced by the opened one and logged as the author's bug; an enum left
	// unspecified keeps the opened value. Narrow a copy of Proc.Capabilities: a Capabilities built from nothing declares
	// that the session can do nothing.
	Capabilities *Capabilities
}

// Program is what runs, as the producer knows it. Every field is optional; an empty one is unknown.
type Program struct {
	// Argv is the argv that was run (the requested one, or the default).
	Argv []string
	// Name is the program's base name ("bash").
	Name string
	// Path is the program's full path when the producer resolved one.
	Path string
}
