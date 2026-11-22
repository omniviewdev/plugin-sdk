package exec

import (
	"context"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ContractVersion is the version of the exec v2 contract (proto/exec/v2/exec.proto) this SDK build implements. It
// is reported in Describe and changes only with the contract. It is informational for a consumer, which gates on
// DescribeResponse.features and never on the version.
const ContractVersion uint32 = 1

// ExecFunc runs one command in the target and returns when the command has ended. It is the one function an
// author writes: copy between p.Stdin, p.Stdout and p.Stderr and the target, and return how the command ended.
//
// It must honor ctx: the SDK cancels it to close the session, kill it, time it out, retire the connection or shut
// the plugin down, and expects the function to return soon after. Return (Exit, nil) when the command ended,
// whatever its status; return a non-nil error when the command could not run or the target failed (the SDK
// classifies it, see Target.Classify). Return ExitLost when the stream to the target ended with no status.
type ExecFunc func(ctx context.Context, p *Proc) (Exit, error)

// OptionsFunc lists what a user can choose for one resource before running a command in it (for a Pod: its
// containers, each with its state). ctx carries the admitted, connection-bound plugin context. The SDK validates
// and bounds the result (at most 8 options and 256 choices each), so the function does not.
type OptionsFunc func(ctx *types.PluginContext, ref resourcev2.ResourceReference) ([]Option, error)

// ClassifyFunc maps an error of the target's own API to a class and reason. It returns false for an error that is
// not the target's (the SDK classifies context, resource-lane, gRPC and network errors itself). Only the class,
// the reason and the facts of the result leave the plugin; the matching of text, if any, stays here.
type ClassifyFunc func(err error) (*Error, bool)

// Target is one kind of thing a command can run in: a Pod, a container, a host, a VM. Register Targets with
// PluginOpts.Targets, or with NewTargetPlugin.
//
// The routing key of a target is Info.Provider and Info.Type: the resource provider and the resource type that a
// request's reference must carry to reach it ("k8s", "pods"). Both are required, and no two targets of a plugin share
// the pair.
type Target struct {
	// Info is the static description of the target: its routing key, what it can do and the limits it is served
	// under.
	Info TargetInfo
	// Exec runs a command in the target. Required.
	Exec ExecFunc
	// Options lists the choices for a resource of this target. Optional: without it Options answers an empty
	// list.
	Options OptionsFunc
	// Classify maps the target's own errors to a class and reason. Optional.
	Classify ClassifyFunc
}

// key returns the routing key as text, Info.Provider + "/" + Info.Type, for messages. The registry keys on the pair.
func (t Target) key() string { return t.Info.Provider + "/" + t.Info.Type }

// TargetInfo describes one kind of thing a command can run in.
type TargetInfo struct {
	// Provider and Type are the resource address provider and type of the target: its routing key. Required, 1 to
	// 256 bytes of plain text each ("k8s" and "pods").
	Provider, Type string
	// DisplayName is the presentation name of the kind ("Pod"), at most 128 bytes.
	DisplayName string
	// DefaultArgv is the command a session whose request has no argv runs ("/bin/sh"). Empty means a request must
	// give an argv.
	DefaultArgv []string
	// Capabilities is what the target can do, at most: the upper bound of every session of the kind.
	Capabilities Capabilities
	// Session is the limits interactive sessions are served under. The zero value means the target serves none.
	Session SessionLimits
	// Run is the limits bounded runs are served under. The zero value means the target serves none.
	Run RunLimits
}

// Capabilities is what a target kind can do, as its author declares it. It is the kind's upper bound: a session is
// narrower by its mode, which the author declares on Choice.Capabilities and the SDK applies at admission, and may be
// narrower still by what it reports in SessionFacts.Capabilities, and never wider. A consumer branches on these and
// never on strings.
//
// Every field is a declaration. The SDK writes all of them to the wire, a false, an empty list and a "none" value
// included, so that a consumer reads "the target cannot", never "the producer did not say". The two fields that
// cannot say "not declared" in Go (an enum whose zero is a real choice) are written as the choice: FactModeNone as
// NONE, SurvivalNone as NONE.
type Capabilities struct {
	// TTY: the target can run a command with a terminal.
	TTY bool
	// SeparateStderr: without a terminal, stderr arrives as its own stream.
	SeparateStderr bool
	// Resize: the target honors a resize after the command started.
	Resize bool
	// InitialSize: the target honors the requested size before the first output.
	InitialSize bool
	// StdinEOF: closing the input half-closes the command's stdin.
	StdinEOF bool
	// Signals lists the signals the target can deliver. Empty means none: the host offers only Close.
	Signals []Signal
	// Kill says what ending a session does to the process. Required for a target that runs commands.
	Kill KillBehavior
	// Env: the target honors the request's env; false means a request that carries env is refused.
	Env bool
	// Cwd: the target honors the request's working directory; false means a request that carries one is refused.
	Cwd bool
	// CwdFact says how the target provides the session's current directory.
	CwdFact FactMode
	// ForegroundFact says how the target provides whether a command runs in the foreground.
	ForegroundFact FactMode
	// HostRestart says what outlives a restart of the host.
	HostRestart Survival
	// ExitReport says what the target can say about how a command ended. Required for a target that runs commands.
	ExitReport ExitReport
	// Stall says what a reader that stops reading does to the target. Required for a target that runs commands.
	Stall StallBehavior
	// LinkHealth: the author reports the health of the transport to the target through Proc.Link.
	LinkHealth bool
	// Integration lists the shell-integration modes the target accepts; never applied without the host's consent.
	Integration []IntegrationMode
}
