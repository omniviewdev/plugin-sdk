# Exec: run commands in a target

[Docs home](README.md) · [Plugin anatomy and capabilities](capabilities.md) · [Manage connections](connections.md)

The exec lane runs a command in a **target** (a Pod, a container, a host, a VM) for the host: interactively (a terminal) or as one bounded run (an agent tool). You write **one function**, `ExecFunc`; once the engine steps land, the SDK owns sessions, ordering, backpressure, resize, replay, bounds, typed errors and the single end of every command.

This guide covers `omniview.sdk.exec.v2` (`proto/exec/v2/exec.proto`, `pkg/v1/exec`). The legacy lane (`exec.Handler`, `TTYHandlerFunc`) is unchanged and keeps working; a plugin may serve either or both.

> **What this SDK build serves.** `Describe` and `Options` are live: a host can list your targets and, when your plugin has the resource provider described [below](#options-needs-a-resource-provider), ask for the choices of one resource. **Every other RPC** (`Open`, `Run`, `RunStream`, `ListSessions`, `ReadOutput`, `EndSession`) is answered with a typed `UNSUPPORTED` refusal: gRPC code `Unimplemented`, a `google.rpc.ErrorInfo` of domain `omniview.sdk.exec.v2` with reason `UNSUPPORTED`, which the Go client reports as an `*exec.Error` with `ClassUnsupported`. `Describe` lists **no feature** (`features` is empty), so a host that asks first never calls them. **The SDK never calls your `ExecFunc` yet**: it builds no `Proc` and sends no `Ended`. What this guide says about running a command is the contract the engine steps will implement; `exectest.Conformance` checks today only registration, `Describe` and `Options`, and skips each engine case with the step that fills it.

**The rules, in plain words.** (1) A failure after the command was handed to your function is data (`Ended{failed}`, `RunResult` with `OUTCOME_FAILED`); only a refusal, when nothing ran, is a status. (2) A session has exactly one end and **at most one `Ended` frame**: `Ended` goes only to the stream that is attached when the session ends, so a session that ends while no stream is attached (a lease that expires, a retirement, a shutdown) has none, and its end is the tombstone's (`SessionInfo.ended_with`). A stream that can still receive frames ends with exactly one of `Ended` (the session is over) or `Detached` (this stream left, the session lives). (3) A session may only narrow the capabilities its kind declares, never widen them. Its mode (a Kubernetes exec or attach) is declared on the `Choice` of your `Options` (`Choice.Capabilities`) and is in `Opened`; `Started` carries only what you learn at run time, is sent at most once and always before the first output, and the SDK sends one made from the facts of `Opened` when your function writes before it reports. `exectest.ProcRig` (given `exectest.RigCapabilities` and `exectest.RigOptions`) tells your test when your function widens, reports late or reports twice, as the SDK will log it. (4) An exit code is carried exactly, in any integer type up to 32 bits unsigned, and a stream that ended with no status is `Lost`, never exit 0. (5) Requests are strict (an unknown field is refused) and carry no maps: environment, labels and selection are lists of `NameValue`.

## A complete plugin

```go
// Command exec-plugin registers one exec target, an echo host. It has no resource provider, so a host can Describe
// it but cannot reach it: docs/exec.md says what a reachable plugin adds.
package main

import (
	"context"
	"io"

	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

func main() {
	// NewPlugin reads the installed plugin.yaml of this ID and panics without it.
	plugin := sdk.NewPlugin(sdk.PluginOpts{ID: "echo-exec"})
	if err := exec.RegisterPlugin(plugin, exec.PluginOpts{Targets: []exec.Target{echoHost()}}); err != nil {
		panic(err)
	}
	plugin.Serve()
}

// echoHost is the one kind of thing this plugin runs commands in. Its "command" copies its input back.
func echoHost() exec.Target {
	return exec.Target{
		Info: exec.TargetInfo{
			Provider: "echo", Type: "host", // the routing key: the resource provider id and type of a request's reference
			DisplayName: "Echo host",
			DefaultArgv: []string{"cat"},
			Capabilities: exec.Capabilities{ // declared, never guessed: what closing does, how it ends and stalls
				Kill: exec.KillTerminates, ExitReport: exec.ExitReportCode, Stall: exec.StallPropagates,
			},
			Session: exec.DefaultSessionLimits(), // a target with neither Session nor Run serves only Options
		},
		Exec: echo,
	}
}

// echo is the whole command: copy the input back until it ends.
func echo(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	p.Started(exec.SessionFacts{Program: exec.Program{Argv: p.Request.Argv, Name: "cat"}})
	if _, err := io.Copy(p.Stdout, p.Stdin); err != nil && ctx.Err() == nil {
		return exec.Exit{}, err // the target failed: the SDK classifies the error
	}
	return exec.ExitCode(0), nil // the command ended with its own status
}
```

The file is [`examples/exec-plugin/main.go`](../examples/exec-plugin/main.go); its tests run the conformance suite against it and keep this block identical to the file.

**This plugin needs an installed manifest.** `sdk.NewPlugin` reads `plugins/<id>/plugin.yaml` from the host's state root (`OMNIVIEW_STATE_DIR`, default `~/.omniview`) and **panics** when the file is missing or its `id` differs from the one you pass; the host installs it before it launches the plugin. The example ships this one, [`plugin.yaml`](../examples/exec-plugin/plugin.yaml), and a test installs it in a temporary state root and registers the example on it:

```yaml
id: echo-exec
name: Echo exec
version: 0.1.0
description: Run commands in a host that echoes its input back.
repository: https://github.com/omniviewdev/plugin-sdk
capabilities:
  - exec
sdk_protocol_version: 1
```

**What it is and is not.** The example is a complete provider of the exec side: it registers, `Describe` lists its target, and `exectest.Conformance` passes on it. It has **no resource provider**, so it has no connection a host could name, and it declares no `Options` callback: an example must not advertise a callback no host can reach. [Options needs a resource provider](#options-needs-a-resource-provider) says what a reachable plugin adds.

## What you write and what the SDK owns

| | You write | The SDK owns |
|---|---|---|
| `exec.Target` | `Info`, `Exec`, and optionally `Options` and `Classify` | routing, admission of the canonical reference and route against the registered type and the caller's connection, the connection lease |
| `Target.Info` (`TargetInfo`) | `Provider` and `Type`: **the routing key**, the resource provider id and resource type that a request's reference must carry to reach this target (`"k8s"`, `"pods"`). Required. Then `DisplayName`, `DefaultArgv` | the check that no two targets share a key; `Describe` |
| `Info.Capabilities` | what the target can do **at most**: the kind's upper bound, [two levels](#capabilities-two-levels) | restating it in `Describe` and in every session's facts |
| `Info.Session`, `Info.Run` | the limits you serve under: `exec.DefaultSessionLimits()` is the usual choice for a target with sessions, `exec.DefaultRunLimits()` for one with runs. **A section you leave zero is a service you do not offer: a target with neither `Session` nor `Run` serves only `Options`** (and registers without an error: `Exec` is still required and never called). A section with any field set takes the SDK's default for each field you leave at zero | default-filling, validating against the contract's ceilings, and reporting the effective, never-zero limits |
| `Exec` | `ExecFunc(ctx, *Proc) (Exit, error)`: copy between `p.Stdin`/`p.Stdout`/`p.Stderr` and the target, return how the command ended | stdio queues, frames, offsets, ordering, resize (latest wins), flow control, replay, the typed end |
| `Options` | the choices of one resource with a state (a Pod's containers) | the RPC, the lease, the bounds (at most 8 options, 256 choices each, closed codes) |
| `Classify` | a map from your API's errors to `*exec.Error{Class, Reason, Facts}` | the closed vocabulary, the bounds, never sending free text or your local cause (`Error.Err` is logged, never sent) |

`Provider` and `Type` are the only routing key (there is no second pair on `Target`). A target registered under a `Provider` that no resource provider of your plugin owns is describable and unreachable.

A target that runs commands (any `Session` or `Run`) **must** declare `Kill`, `ExitReport` and `Stall` in its capabilities. `exec.NewTargetPlugin` and `exec.RegisterPlugin` return an error at construction for anything the contract refuses (each is prefixed `exec target N ("provider/type"): `):

| You did | The error says |
|---|---|
| left `Kill`, `ExitReport` or `Stall` unset on a target with `Session` or `Run` | `a target that runs commands must declare ExitReport, Stall, Kill in Capabilities: ...` (it lists the ones you left out) |
| left `Info.Provider` or `Info.Type` empty, longer than 256 bytes or not plain text | `Info.Provider must be 1 to 256 bytes of plain text` (or `Info.Type`) |
| left `Exec` nil | `the Exec function is required` |
| registered two targets with one `Provider` and `Type` | `duplicate exec target key "provider/type"` |
| set `Integration` without `TTY` | `Capabilities.Integration needs Capabilities.TTY: ...` |
| registered more than 256 targets | `N exec targets exceed the limit of 256` |

**Limits.** The defaults of `DefaultSessionLimits()`: 32 sessions, a 64 KiB input frame, a 1 MiB replay ring (a host may ask for up to 4 MiB), a 5 minute detach lease (up to 30 minutes), terminals up to 1024 cells a side (at least 80, because a start without a size gets 80 x 24). Those of `DefaultRunLimits()`: a 30 s timeout (up to 5 minutes), 256 KiB of output per stream (up to 1 MiB), 64 KiB of stdin, 8 concurrent runs, 64 KiB of argv and of env, a 1 s post-exit drain. A value above the contract's ceiling (more than 4096 sessions, a lease over 24 hours, a run timeout over 1 hour, ...) is a registration error.

### The minimal ExecFunc

```go
func echo(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	p.Started(exec.SessionFacts{Program: exec.Program{Argv: p.Request.Argv, Name: "cat"}})
	if _, err := io.Copy(p.Stdout, p.Stdin); err != nil && ctx.Err() == nil {
		return exec.Exit{}, err // the target failed: the SDK classifies the error
	}
	return exec.ExitCode(0), nil // the command ended with its own status
}
```

Seven lines. `ExecFunc` must honor `ctx` (the SDK is to cancel it to close, kill, time out, retire the connection or shut the plugin down) and returns when the command has ended: `(Exit, nil)` for any status, a non-nil error when the target failed, `ExitLost` when the stream to the target ended with no status. `p.Started` reports what you learn once the command runs (the program that really runs, a pid, whether a terminal was granted, and the session's capabilities if they turn out narrower than the ones it was opened with): call it at most once, and before the first byte you write, because the host receives the session's `Started` before its first output. A session whose function writes without having called it gets a `Started` that the SDK makes from the facts of its opening (the requested program and the opened capabilities); a call after the first output, or a second one, is ignored and logged, and `exectest.ProcRig` tells your test. A session that neither writes nor calls it has none. `p.Stdout` fails once the session has ended, `p.Stdin` reports `io.EOF` when the host closes the input or the session ends, and `p.Resize` is closed when the command ends.

**When your function breaks the contract.** The engine runs your function on its own goroutine, once for each command that was admitted, and contains what a function can get wrong. Nothing a misbehaving function does crosses the wire as its own text, and each of these is counted (`omniview.plugin.self.sdk.exec.violations`, once for each session and kind) and logged by the SDK, so you find it in your plugin's log:

- **A panic in `ExecFunc`** is recovered. The command ends `Failed{INTERNAL}` (a command that had not started fails at `START`, one that had at `RUN`). The panic value and the stack go to the plugin's own log at `error` and nowhere else: not into the end, the metrics or the trace. A panic in a goroutine you started yourself is plain Go and ends the process.
- **Which goroutine may call `p.Started` and `p.Link`:** any goroutine of your function, while it runs. Both are safe for concurrent use. `Started` counts once, and only before the first byte of output; a second call, a call after the first output, and one with facts that are not within the bounds of the session are refused and logged (a start gate that already opened with the SDK's own `Started` stays what it was). `Link` is for a target that declared `LinkHealth`; a state that is not a state of the link is refused. A call of either after the function returned is refused.
- **A write after your function returned,** or after the session ended, fails with `io.ErrClosedPipe`, is counted (`late writes` and `late bytes` of the result, `omniview.plugin.self.sdk.exec.violations{reason=late_write}`) and is never delivered: the end is already decided. Output that you wrote and the host did not read is still there to read until the stream's last frame. A read of `p.Stdin` after the host closed the input or the session ended returns `io.EOF`.
- **A function that ignores `ctx`:** a close, a kill, a timeout, a retirement and a shutdown all cancel `ctx`. The end is typed at once from who asked (`Closed{by}`), the function has the grace (5 s by default) to return, and a function that has not returned by then is abandoned: the command ends without it, `violations{reason=ignored_cancel}` is counted, and the gauge `omniview.plugin.self.sdk.exec.abandoned` shows it until it returns. A kill does not wait for the function. The SDK cannot stop a goroutine, so an abandoned function keeps what it holds until it returns; `ctx` is the only way it is told.
- **A function that returns before the process ended** (the context was cancelled, or it returned a status early): what it returns decides, because the SDK knows only what the function reports. A function that returns after its `ctx` was cancelled ends `Closed{by}`, not `Exited`, whatever it returned; that is the cause of the end and the status is dropped. If you need the status of a command you closed, report it through the target's own channel.
- **A function that never reads `p.Resize`** for a target that declared `Resize`: sizes wait in a queue that holds the first size and the newest (a resize storm costs two entries), and the SDK logs `resize_unread` once, after 5 s.
- **Exit 0 on a lost stream:** a status returned while the last `p.Link` report was `LinkStalled` is reported as the transport's (`TRANSPORT` source, never `PROCESS`) and logged as `stalled_status`; an error returned together with an exit status is a failure (`exit_and_error`); `ExitLost` is never turned into a code.

The queues are bounded: a session holds at most 256 KiB of input and 256 KiB of output (at most 4,096 runs of one stream, and 64 reported drops). A target that declared `StallPropagates` (or declared nothing) blocks your `Write` at that bound until the host reads; a session that is ending never waits for a reader, so a blocked `Write` is released at the end and what the queue could not hold is counted (`DroppedBytes`, and the end's output is then `TRUNCATED`). A target that declared `StallLossy` never blocks your `Write`: the oldest bytes are dropped, counted and reported to the reader as the exact range that was lost.

## How a command ends

Return exactly one of these. An exit is never guessed.

| You return | The consumer reads | What it means |
|---|---|---|
| `ExitCode(n)` | `Exited{n, PROCESS}` | the process's own status, as the target relayed it. **`n` may be any integer type** (`int`, `int32`, `uint32`, `int64`, ...): a Windows DWORD arrives exactly, `ExitCode(uint32(0xC0000005))` is 3221225477 on the wire, and a reporter's negative 32-bit reading of the same DWORD stays negative. Never inferred from a code range |
| `ExitSignal(name, n)` | `Signaled{..., PROCESS}` | the target said a signal killed it; **only** for a target whose `Capabilities.ExitReport` is `ExitReportCodeAndSignal`. From any other target a signal is never turned into a code: the end is a failure of the target (`INTERNAL`) with unknown survival, and your bug is logged. A name that is not valid text (UTF-8, no control characters, at most 256 bytes) or a number outside 1 to 64 is dropped, and the other identifier is kept |
| `ExitFolded(n)` | `Exited{n, SIGNAL_FOLDED}` | the target itself says the code is a signal death folded into 128+n: no signal is claimed. An ordinary exit can carry the same number, so never infer this from a code; a target that gives no provenance reports `ExitCode` |
| `ExitTransport(n)` | `Exited{n, TRANSPORT}` | the transport said the stream ended and relayed a code that is not the process's own status. Use it only when the transport really hands you one; a stream that ended with **no** status is `ExitLost` |
| `ExitLost(cause)` | `Lost{cause}` | the stream to the target ended with no terminal status (`LostNoStatus`: it closed with nothing on its status channel; `LostLinkDead`: the keepalive gave up). **Lost is never exit 0**, never a failure of the command, never a reason to retry it |
| `Exit{}` (empty) or a mixed `Exit` | `Failed{INTERNAL}` | a failure of the target, never an exit 0 |

`ExitCode`, `ExitFolded` and `ExitTransport` take one of `int`, `int8`, `int16`, `int32`, `int64`, `uint8`, `uint16`, `uint32`. `uint` and `uint64` are not accepted, because a value above `math.MaxInt64` would not fit the signed 64-bit wire code. A target whose (effective) `ExitReport` is `ExitReportNone` cannot relay a process status: an `ExitCode` from it is reported with source `TRANSPORT`, and the SDK logs your bug.

**Does the process survive?** The end carries `remote_may_survive`, set from your `Exit` by the end conversion (`Exit.end`, which the engine calls): `Lost` is **true** (the process may still run); `Exited` or `Signaled` from `PROCESS` or `SIGNAL_FOLDED` is **false** (the target reported its status, so it is gone); `Exited` from `TRANSPORT` and a failure are **absent**, which means **unknown**, and a consumer treats unknown as "may survive". Absent is never "no". A `Closed` end says `false` only for a target that declared `Kill` as `KillTerminates`; for `KillBestEffort` and `KillClosesStreamOnly` it says `true`, and for a target that declared nothing it says nothing.

## Capabilities: two levels

`Capabilities` has two levels, and a plugin author writes both.

1. **The kind's upper bound.** `TargetInfo.Capabilities` is what the *most capable* session of this kind can do. You declare it at registration; it is fixed for the life of the plugin process, and a consumer can read it before any session exists (what a picker offers, which request it never sends).
2. **The session's effective set.** Each session has its own capabilities, in its facts, and they only ever narrow. `Opened` carries the upper bound narrowed by the session's **mode**: each `Choice` of your `Options` may carry `Capabilities` (`exec.Choice.Capabilities`), the complete set a session that selects it is served with, and the SDK narrows the kind's bound by the choice that each option takes in the request's selection (for an option the selection omits, its default choice). `p.Capabilities` is a deep copy of that set, so changing it affects no other session. A target whose modes differ declares them on its choices (see the illustration below), and its function reports nothing for them; it narrows a copy and reports it with `p.Started(exec.SessionFacts{Capabilities: &caps})` only for what it learns at run time. A `SessionFacts` whose `Capabilities` is nil keeps the set `Opened` carried.

**A session may only narrow.** For every one of the 16 fields a choice's value is at most the kind's, and a `Started`'s is at most the value `Opened` carried, in the order below. When a claim is above its bound the SDK keeps the bound's value and logs your bug: for a choice when it serves `Options` (live in this build: the log names the option, the choice and the field), and for a `Started` in the engine (this build does not run sessions yet, so that clamp arrives with it). `exectest.ProcRig` applies the same rules in your own tests today: build it with `exectest.RigCapabilities(yourTarget.Info.Capabilities)` and `exectest.RigOptions(options...)`, the options your `Options` callback returns (`Proc.Capabilities` is then a deep copy of the set the session is opened with, which `rig.Opened()` returns; without `RigOptions` that is the declared set, and without `RigCapabilities` a copy of a default fake target's). A choice that widens a field, and a `Proc.Started` report that widens one, are clamped and reported with `t.Errorf` at your call; `rig.Effective()` returns the session's set (the opened one until a report narrows it). Every capability can be narrowed, so one target kind can serve modes that differ. **A consumer that holds a session reads that session's facts and never `Describe`**; one that has only `Describe` reads each field as "the most it can do".

| Field | Says | Order (narrowest first) |
|---|---|---|
| `TTY`, `SeparateStderr`, `Resize`, `InitialSize`, `StdinEOF`, `Env`, `Cwd` | what the target honors (false: a request that needs it is refused `UNSUPPORTED`) | false < true |
| `LinkHealth` | the author reports the transport's health through `Proc.Link` | false < true |
| `Signals` | the signals that can be delivered; empty: only Close | inclusion: a subset |
| `Kill` | what ending a session does to the process | `KillClosesStreamOnly` < `KillBestEffort` < `KillTerminates` |
| `CwdFact`, `ForegroundFact` | how the target provides the fact; none: shown as unknown, never probed | `FactModeNone` < `FactModePull` < `FactModePush` |
| `HostRestart` | what outlives a host restart; none: never offer reattach | `SurvivalNone` < `SurvivalTargetProcess` < `SurvivalTargetSession` |
| `ExitReport` | what can ever be said about an end | `ExitReportNone` < `ExitReportCode` < `ExitReportCodeAndSignal` |
| `Stall` | what a reader that stops reading does to the target | `StallLossy` < `StallPropagates` |
| `Integration` | shell-integration modes accepted, never applied without the host's consent | inclusion: a subset |

`StallPropagates` means the SDK blocks `Write` and nothing is lost; `StallLossy` means it drains into the ring and counts what it overwrites.

### Illustration: a Kubernetes Pod, exec versus attach

One Pod kind serves two modes that differ in what can be said about them. An **exec** session starts a new process: its end carries an exit code, and nothing of it can be reached again after a host restart. An **attach** session joins the container's own process: no status is relayed at all, and the process outlives the host. The kind declares the union as its upper bound (`ExitReportCode`, `SurvivalTargetProcess`); each mode is declared on its `Choice` as the kind narrowed to what is true of it (exec: `HostRestart` none; attach: `ExitReport` none). The session is therefore **opened** with its mode: the host reads it before it opens anything, and your function never reports it.

The code below is [`examples/exec-plugin/k8s_illustration_test.go`](../examples/exec-plugin/k8s_illustration_test.go), compiled and tested in CI. It is written against a tiny stand-in for the parts of client-go it uses (`streamOptions` is `remotecommand.StreamOptions`, `codeExitError` is `utilexec.CodeExitError`, `executor` is `remotecommand.Executor`), so the example needs no Kubernetes dependency; the stand-in is defined in the same file. The mode comes from the request's selection: the target's `Options` declares a `mode` option whose choices `exec` and `attach` carry their capabilities (see [the Options snippet](#options-needs-a-resource-provider), which builds the two sets from `podCapabilities`).

```go
// podCapabilities is what a Pod can do at most: the upper bound of every session of the kind, which the two modes of a
// Pod narrow in different fields (see podOptions).
func podCapabilities() exec.Capabilities {
	return exec.Capabilities{
		TTY: true, SeparateStderr: true, Resize: true, InitialSize: true, StdinEOF: true,
		Kill: exec.KillClosesStreamOnly, ExitReport: exec.ExitReportCode, Stall: exec.StallLossy,
		HostRestart: exec.SurvivalTargetProcess, // attach reaches the container's own process again
		Integration: []exec.IntegrationMode{exec.IntegrationArgvWrapper},
	}
}

// podTarget is a Pod: one kind that serves two modes, exec (a new process) and attach (the container's own).
func podTarget() exec.Target {
	return exec.Target{
		Info: exec.TargetInfo{
			Provider: "k8s", Type: "pods", DisplayName: "Pod", DefaultArgv: []string{"/bin/sh"},
			Capabilities: podCapabilities(), // the upper bound: the most any session of this kind can do
			Session:      exec.DefaultSessionLimits(),
		},
		Exec: podExec,
	}
}

func podExec(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	// p.Capabilities is what the host read in Opened: the kind's upper bound narrowed by the mode this request chose
	// (the Capabilities of the choice, see podOptions). The mode is known before this function runs, so it is not
	// reported here, and nothing else is learned at run time, so Started is not called. client-go has no callback for
	// "the command is running"; the start this adapter observes is the first byte the command writes, and the SDK sends
	// the session's Started before it, made from the facts of Opened. A command that stays silent has none.
	attach := p.Request.Selection["mode"] == "attach" // the "mode" option of the target's Options
	run, err := executorFor(p, attach)
	if err != nil {
		return exec.Exit{}, err // Target.Classify maps "container not running", RBAC 403, "no such file"
	}
	opts := streamOptions{Stdin: p.Stdin, Stdout: p.Stdout, Stderr: p.Stderr, Tty: p.Request.TTY,
		TerminalSizeQueue: sizeQueue{p.Resize}} // a terminal has no Stderr: it is nil, which client-go reads as none
	err = run.StreamWithContext(ctx, opts)
	var code codeExitError
	switch {
	case !attach && errors.As(err, &code):
		// The API reports a code and nothing about where it came from: 137 can be a SIGKILL or a plain exit 137, so
		// no signal is inferred from the range. ExitFolded is only for a target that says a code is a folded signal.
		return exec.ExitCode(code.Code), nil
	case err == nil:
		// client-go returns nil both for a Success status and for a status channel that closed empty, so nil says
		// nothing about the exit: the stream ended and the command's status is not known. It is never exit 0.
		// (A plugin that reads the status channel itself can return ExitCode(0) for a Success status.)
		return exec.ExitLost(exec.LostNoStatus), nil
	default:
		return exec.Exit{}, err
	}
}

// sizeQueue hands Proc.Resize to client-go: Next waits for the next size and returns nil when the command has ended.
type sizeQueue struct{ resize <-chan exec.Size }

func (q sizeQueue) Next() *terminalSize {
	size, ok := <-q.resize
	if !ok {
		return nil
	}
	return &terminalSize{Width: size.Cols, Height: size.Rows}
}
```

Four things the code is honest about. A stream that ends with no status is `exec.ExitLost(exec.LostNoStatus)`, never `ExitTransport(0)` and never `ExitCode(0)`: the consumer then shows "connection lost, status unknown", not "exit 0". A real exit code is `exec.ExitCode(code)`, never inferred from a range. The mode is declared where it is known, on the choice, so it is in `Opened` before the function runs: the host can show "attach: no exit status, survives a host restart" in its picker, an attach session never offers an exit status, and the function starts from the set `Opened` carried and reports nothing for it. Last, it never calls `p.Started`: client-go has no start callback and a stream can read its stdin and still fail before the command starts, so the only start this adapter can observe is the first byte the command writes, and the SDK sends the session's `Started` before that byte, made from the facts of `Opened` (so its capabilities are the ones the mode already narrowed); a command that stays silent has none, and a stream that fails before any output reports nothing. There is no first-byte gate to write: the SDK guarantees that the host never receives output ahead of the session's `Started`, and it refuses, and logs, a `Started` that an author reports after the first output or twice.

## Absence is unknown

This is one rule for the whole contract: **a fact that a producer may not know is written so that "not known" is a value of its own, never the zero value of its type.** A bool fact is `optional bool`, a list whose emptiness is a fact (`signals`, `integration`) is wrapped in a message so that absent differs from empty, an enum has an `UNSPECIFIED` zero, a message fact is absent.

**What the SDK writes for you.** `Capabilities` is your declaration, so the SDK writes **every** field as present: an unset `bool` is a stated `false`, an empty `Signals` is a present empty set ("none"), `FactModeNone` is written `FACT_MODE_NONE`, `SurvivalNone` is `SURVIVAL_NONE`, an integration list without modes is a present empty set. It leaves a fact absent only when it does not know it: `SessionFacts.TTY` stays absent until you report it (`TTY *bool`: nil is "not said", a pointer to false is "no terminal"), a zero `Size`, empty `Program` fields and an empty `Term` or `Transport` mean "unknown", `Exit.Code` is a pointer, and `remote_may_survive` is written only when [the table above](#how-a-command-ends) says the SDK knows it. Do not state what you do not know: leave the field unset.

**What a consumer may assume.** Nothing from an absent value. An absent `Capabilities` message means nothing is promised; an absent bool or set inside a present one is not known, and `false`, an empty set and `NONE` are the producer's statement that the target cannot; an `UNSPECIFIED` enum is "unknown", never the nearest named value; a value a reader does not know (a newer peer) reads as `UNSPECIFIED`; an absent `remote_may_survive` reads as "may". Unknown is never turned into `false`, zero, none or exit 0. The rule is the "ABSENCE IS UNKNOWN" convention in the file header of [`proto/exec/v2/exec.proto`](../proto/exec/v2/exec.proto), and `TestAbsenceIsUnknown` and `TestBlocker1` to `TestBlocker5` in [`proto/exec/v2/contract_test.go`](../proto/exec/v2/contract_test.go) fail a field that breaks it.

## Requests: strict, and lists of names

A plugin **refuses** a request that carries a field it does not know (`UNSUPPORTED`), at any depth and inside the elements of a repeated message too, because a request field can change what a command may do. A host therefore sends a field an older plugin may not know only after `Describe` lists the feature that gates it. Frames and responses are lenient: an unknown field or frame kind is ignored and counted.

For that reason **a request carries no map**. The environment, the labels and the selection of a request (`Start.env`, `Start.labels`, `TargetRef.selection`, `RunRequest.env`, `RunRequest.labels`) are `repeated NameValue{name, value}`: wire-identical to a map entry, but an unknown field inside an entry can be seen and is refused. A name appears at most once in a list; a duplicate is `INVALID_REQUEST`, never "the last one wins". Your function sees them as the maps `Request.Selection`, `Request.Env` and `Request.Labels`, already validated. (`TargetFacts.selection`, a frame the plugin sends, stays a map.)

## A refusal is a status; everything after the hand-over is data

A **refusal** answers a request the SDK did not hand to your function, so nothing ran and a host may retry: a bad request, a stale connection, a limit, a reference that is not the caller's, an unsupported field. It is a gRPC status carrying `google.rpc.ErrorInfo` (domain `omniview.sdk.exec.v2`, reason = the error class, metadata `reason`, `program`, `selection_name`, `selection_value`, `retry_after_ms`) and, for a class other than `INTERNAL`, a bounded `LocalizedMessage` (`Error.Message`, at most 512 bytes). A plugin that does not serve the lane answers `Unimplemented`, which the client reports as `*exec.Error` with `ClassUnsupported`.

**Once your function was called, the command may have run and may still be running, so how it ended is part of the result, never a status.** What you return from `ExecFunc` is therefore data:

- a session ends with `Ended{end}`: `exited`, `signaled`, `closed`, `failed{class, reason, facts, stage}` or `lost{cause}`;
- a run ends with `RunResult` (and `RunEnded` for a stream), whose outcome is exited, signaled, timed out, cancelled, **failed** (`OUTCOME_FAILED`, with a `Failed` message) or lost. It keeps the output read so far, says whether the start was observed, and says whether the process may survive.

A **failure after the command was handed over** (your function returns `(exec.Exit{}, err)`, or a stream breaks) is `Ended{failed}` or `RunResult{OUTCOME_FAILED}`; the same classification (`*exec.Error{Class, Reason, Facts}` or `Target.Classify`) fills it. A host retries a refusal and never a failed or lost end: a failed end may have run, may still run. `start_observed` false means the SDK did not see the start, not that the command never ran.

## One end per session, one last frame per stream

On an `Open` stream, **every stream that can still receive frames ends with exactly one of two frames**, and nothing follows it (a stream whose connection breaks gets neither: the break is an implicit detach, and the session is read from its tombstone if its lease then expires):

- **`Ended`**: the end of the **session**, sent **at most once** in its life, to the stream attached when it ends. It holds the `End` above, counts and the output completeness. A session has exactly one **end**, and `Ended` is the frame that carries it to a stream: a session that ends while no stream is attached (a lease that expires, a retirement, a shutdown) has no `Ended` at all, and its end is the tombstone's, which `ListSessions` returns as `SessionInfo.ended_with`.
- **`Detached`**: the end of **this stream** while the session lives on. It carries a `reason`: `DETACH_REASON_REQUESTED` (the host sent `Detach`; the session lives for the lease) or `DETACH_REASON_SUPERSEDED` (another stream attached and took the session over; the session continues attached to it). **A superseded stream is not an end**: there is no `Ended` for it and no `closed` value for it; a consumer keyed on `Ended` never marks a live session dead.

Your function runs once per session and returns once; the SDK turns that into the one end of the session, and into the `Ended` frame when a stream is attached to receive it.

## Options needs a resource provider

`Options` lists what a user can choose for one resource before a command runs in it, so that a picker shows real states:

```go
result, err := plugin.V2().Options(ctx, exec.TargetRef{Reference: ref, Route: route}) // ctx carries the call's types.PluginContext and the catalog pin
// result.Options[0] = {Name: "container", Required: true, Choices: [{Value: "app", State: ChoiceStateReady, IsDefault: true, Kind: "container"}, ...]}
```

A host can reach your `Options` callback only if **all** of these hold:

1. **A resource provider is registered in the same plugin** (`sdk.RegisterResourcePlugin`), and your `Info.Provider` and `Info.Type` are that provider's id and a resource type it serves. `Options` is admitted like any resource utility call: the request must name an exact reference and a route with a connection, the connection must be the caller's, and the catalog pin the host sent must be current. A request that carries a selection is refused `INVALID_REQUEST`; a `Provider`/`Type` with no registered target is refused `UNSUPPORTED`.
2. **That provider owns connection lifecycle**: its `Impl` implements `resourcev2.ConnectionContextProvider` (a `WithConnection` method), which a `*resourcev2.Controller` has and which works only when the controller is configured with a `Lifecycle` (`LoadConnections`, `CreateClient`, `DestroyClient`, ...). The SDK binds that provider to the exec capability when both are registered, in either order, and your callback then runs inside the connection lease (cancelled when the connection is retired: the caller gets `ClassConnection`/`ReasonConnectionRetired`).

The SDK's own [`examples/resource-plugin`](../examples/resource-plugin) provider is a controller with **no** `Lifecycle`, so on a plugin built from it `Options` is refused `UNSUPPORTED` (by the resource lane: the caller sees a `*resourcev2.ResourceOperationError`, not an `*exec.Error`); with a `Lifecycle` it works. **Every target is admitted the same way, whether or not it has an `Options` callback**: a request with no connection, no or a stale catalog pin, or another connection's route is refused by the resource lane (`RESET_REQUIRED`, `INVALID_REQUEST`) before anything else; once admitted, a target with **no** `Options` callback answers an empty list. The SDK bounds what your callback returns: at most 8 options with at most 256 choices each, option names, details and kinds as closed codes (`^[a-z0-9_.-]{1,40}$`, never prose), at most one default choice per option; a callback that breaks them is answered `INTERNAL`, and so is one that panics (recovered and logged: the plugin keeps running). `Options` is a read, so a host may retry any failure of it. [Manage connections](connections.md) shows how to wire a controller and its hooks (the runnable `ExampleLifecycle` is in [`pkg/resource/v2/example_test.go`](../pkg/resource/v2/example_test.go)). The exec example above has neither a provider nor a callback for exactly this reason.

With such a provider, the Options callback of the Pod target is the snippet below ([`examples/exec-plugin/options_snippet_test.go`](../examples/exec-plugin/options_snippet_test.go), compiled and tested in CI; `getPod` and the `pod` type stand in for your client). Set it as `target.Options`. It declares the `mode` option that `podExec` reads, with the capabilities of each mode on its choices, and shows how a container's state maps to a `Choice`: `Ready` runs a command now, `Starting` will probably be ready soon, and `Stopped` cannot run one now, with a closed-code `Detail` (`terminated`, `crash-loop`) that a host never has to parse as prose.

```go
// podOptions is the target's Options callback: the choices a picker shows for one Pod. ctx carries the connection the
// host named; use it to get your client.
func podOptions(ctx *types.PluginContext, ref resourcev2.ResourceReference) ([]exec.Option, error) {
	info, err := getPod(ctx, ref.Address.ID)
	if err != nil {
		return nil, err
	}
	containers := exec.Option{Name: "container", Required: true}
	for _, c := range info.containers {
		choice := exec.Choice{Value: c.name, Kind: c.kind, IsDefault: c.name == info.defaultContainer}
		switch c.phase {
		case "running":
			choice.State = exec.ChoiceStateReady
		case "starting":
			choice.State = exec.ChoiceStateStarting // will probably be ready soon, and is not now
		default: // "terminated", "crash-loop": cannot run a command now; Detail is a closed code, never prose
			choice.State, choice.Detail = exec.ChoiceStateStopped, c.phase
		}
		containers.Choices = append(containers.Choices, choice)
	}
	// The mode of a session is declared on its choice, before anything opens: the complete set a session that selects it
	// can do, which is the kind's upper bound (podCapabilities) narrowed. The SDK opens the session with it, so the
	// host reads it in Opened, and podExec starts from it and reports nothing.
	execMode := podCapabilities()
	execMode.HostRestart = exec.SurvivalNone // an exec'd process cannot be reached again after a host restart
	attachMode := podCapabilities()
	attachMode.ExitReport = exec.ExitReportNone // attach relays no status at all

	mode := exec.Option{Name: "mode", Choices: []exec.Choice{ // the "mode" podExec reads from the selection
		{Value: "exec", State: exec.ChoiceStateReady, IsDefault: true, Capabilities: &execMode},
		{Value: "attach", State: exec.ChoiceStateReady, Capabilities: &attachMode},
	}}
	return []exec.Option{containers, mode}, nil
}
```

The imports are `resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"`, `"github.com/omniviewdev/plugin-sdk/pkg/types"` and the `exec` package.

A choice's `Capabilities` is the **mode** of the choice: the complete set a session that selects it is served with. Copy the kind's capabilities (`TargetInfo.Capabilities`, the upper bound of every session) and narrow the copy, each field only down in the order of [the table above](#capabilities-two-levels); `nil` means the choice does not narrow. The SDK serves it with the options and applies it at admission: `Opened` carries the kind's capabilities narrowed by the choice that each option takes in the request's selection (the default choice of an option the selection omits), and `p.Capabilities` starts from that set. A field above the kind's is the author's bug, not an error: the SDK serves the kind's value for it and names the option, the choice and the field in the plugin's log. A host reads a choice's capabilities with the options, so a picker can show "attach: no exit status, survives a host restart" before it opens anything.

### Describe

`plugin.V2().Describe(ctx)` (the `Client` a host gets from `exec.PluginClient.V2()`) returns the wire `*execv2.DescribeResponse`: `contract_version`, the `features` this build serves and the `targets`, each with its upper-bound capabilities (every field present) and the limits in force, in registration order. It is static, needs no connection, and the client validates it before you see it. The limits are effective and never zero: a present `session` or `run` section carries non-zero bounds, the SDK never sends an all-zero one, a consumer refuses it as a malformed description, and an absent section means that service is not offered. A target with neither section is one that only offers `Options`. In this build `features` is empty.

## Test your target

```go
func TestConformance(t *testing.T) { exectest.Conformance(t, myTarget()) }
```

`exectest.Conformance` serves your target through the real SDK over a real transport (a unix socket) with a fake connection registry standing in for the resource provider, and runs what this build serves: that registration agrees with `Describe` and claims no capability you did not declare, that `Describe` is static, that your `Options` answer is within bounds, and the wiring of the lease and the catalog pin. If your target has an `Options` callback, pass the id it knows: `exectest.Conformance(t, target, exectest.ConformanceResource("web-0"))`.

**Five of the engine cases run the SDK's real engine in this process, and none of them calls your `ExecFunc`.** Case 9 (every kind of end is typed, a command has exactly one, nothing follows it), case 14 (a retired connection ends its commands), case 28 (`remote_may_survive` per end), the pair test P5 (ending twice, after the exit and during the start give the same end) and the stress harness (`exectest.Stress`) run in a subtest `in process` that passes or fails. The first four run against a fake target that has the `Info` you declared and a script of their own, so what you declared decides what they expect: a signal only when `ExitReport` is `ExitReportCodeAndSignal`, a closed command's process gone only when `Kill` is `KillTerminates`. The stress harness runs the SDK's own scripted targets (it checks the engine's end state, not your declaration; it runs 5 s, 1 s under `-short`, or as long as `OMNIVIEW_LIFECYCLE_HAMMER` says). A target that declares neither sessions nor runs has nothing to run, and they skip. What is left of those five is what crosses the wire (the `Ended` frame and the tombstone; for the stress, signal, detach and attach). The other cases (1 to 8, 10 to 13, 15 to 27, the pair tests P1 to P4 and P6) are listed under `engine` and **skipped**, each with the step that fills it (X2b, X3a, ...), because they need a served `Open` or `Run`, which this build does not list. A build that lists a feature in `Describe` without its cases written fails instead of skipping. Meanwhile, test your function yourself: `exectest.NewProcRig(t, request, exectest.RigCapabilities(target.Info.Capabilities), exectest.RigOptions(options...))` runs an `ExecFunc` with a `Proc` whose stdio you control, opened with the mode that your options declare for the request's selection (`rig.Opened()`), records the `Started` the host would receive (yours, or the one the SDK makes at the first output) and what you report through `Link`, and holds your report to the rules described above; `exectest.NewFakeTarget` is a scriptable target. The [Kubernetes illustration's tests](../examples/exec-plugin/k8s_illustration_test.go) run an exec and an attach session this way, a quiet container and a container that writes among them, and show a session that widens, and a late and a second `Started`, being refused. The rig runs your function with a `Proc` you control; the engine that runs it for a host is in this build too, but it is reached only in process until the transport lands.

## Names: one word per thing

| Thing | Wire (`omniview.sdk.exec.v2`) | Go (`pkg/v1/exec`) |
|---|---|---|
| a kind of thing commands run in | `TargetInfo` | `Target` (registration), `TargetInfo` |
| its routing key | `provider`, `type` | `Info.Provider`, `Info.Type` (one pair) |
| the one function | (none) | `ExecFunc`, `Proc`, `Request` |
| the command is running | frame `Started`, `RunStarted` | `Proc.Started(SessionFacts)` |
| facts the author states then | `SessionFacts` | `SessionFacts` (author subset) |
| the mode of a session | `Choice.capabilities` | `Choice.Capabilities` |
| how a command ended | `End`, `Ended`, `Exited`, `Signaled`, `Closed`, `Failed`, `Lost` | `Exit` (`ExitCode`/`ExitSignal`/`ExitFolded`/`ExitTransport`/`ExitLost`) |
| why a refusal or failure | `ErrorClass`, `ErrorReason`, `ErrorFacts` | `ErrorClass`, `ErrorReason`, `ErrorFacts`, `Error` |
| sessions that outlive a stream | `FEATURE_SESSION_MANAGEMENT` | (engine step) |
| a stream left, session lives | `Detached{reason}` | (engine step) |

## Compatibility

The v2 lane is a new gRPC service on the existing `exec` capability: additive, no protocol bump. A plugin built before it answers `Unimplemented`, the client says `ClassUnsupported`, and the host stays on the legacy lane; a v0.21.0 host talking to a plugin that registers both lanes sees the legacy lane byte for byte as before.

**How the contract is pinned.** Nothing of it is released yet, so its names and shapes can still change; once a release carries it, it only grows. Two checks hold that line. The reviewed listing of the contract, [`proto/exec/v2/exec.contract.golden`](../proto/exec/v2/exec.contract.golden), is compared by a test with the compiled descriptor of `exec.proto`, and its first line, `contract_version N`, must equal `exec.ContractVersion`. While the contract is unreleased you regenerate the listing after an intended change with `go test ./proto/exec/v2 -run TestContractMatchesItsReviewedGolden -update` and review the diff; once it is released the same failure means a breaking change. The CI job `proto-breaking` runs `buf breaking` against the latest `v*` tag (and does nothing while there is no tag). The Go package exports only what an author or the live `Describe`/`Options` path uses, and each engine step adds the host types it needs.

**Before any tag.** The conditions that the independent review of the head `ed6f26f8` of this contract sets before any tag that ships `exec.v2` are kept, verbatim, in [Before any tag that ships `exec.v2`](exec-before-any-tag.md).

## What is not served yet

- `Open`, `Run`, `RunStream`, `ListSessions`, `ReadOutput` and `EndSession` exist in the service and answer a typed `UNSUPPORTED` refusal; `Describe` lists no feature for them (`FEATURE_SESSION`, `FEATURE_RUN`, `FEATURE_RUN_STREAM`, `FEATURE_SESSION_MANAGEMENT`, ...), so a host that asks first never calls them. Each step lists its feature when it lands.
- The engine exists and runs your `ExecFunc` with a `Proc` (stdin, stdout, stderr, a latest-wins `Resize`, the start gate, a typed end with a source and a survival), with the bounds above and a lifecycle that leaves no goroutine and no process behind (`internal/session`, built on `internal/producers.Group`). Nothing reaches it over the wire yet: the in-process `exechook` is its only caller, and `exectest.Conformance` drives it through a fake target built from your declared `Info`. What stays unwritten is the transport: the frames of `Open` and `Run`, the replay ring, detach and re-attach, `ListSessions`, the tombstone, the heartbeat and the signal path.
- `exectest.Conformance` checks registration, `Describe` and `Options`, and runs the engine cases that the in-process engine satisfies; the others stay skipped with the step that fills each.
- Request admission is written and tested but has no served caller yet (`Options` uses it today): an unknown field at any depth is refused `UNSUPPORTED`; a request that asks for a lease, a replay ring or an `Attach` needs `FEATURE_SESSION_MANAGEMENT` (and `Attach` also `FEATURE_SESSION`) in the features `Describe` lists, otherwise it is `UNSUPPORTED`.
- Not yet written: `Detach` and the `DETACH_TIMEOUT` clock (the cause exists, nothing produces it), `IDLE_TIMEOUT`, the `ResizeResult` frame, and an abandon rule for a connection that a function holds past its end.
