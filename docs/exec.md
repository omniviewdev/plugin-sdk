# Exec: run commands in a target

[Docs home](README.md) · [Plugin anatomy and capabilities](capabilities.md) · [Manage connections](connections.md)

The exec lane runs a command in a **target** (a Pod, a container, a host, a VM) for the host: interactively (a terminal) or as one bounded run (an agent tool). You write **one function**, `ExecFunc`; the SDK owns sessions, ordering, backpressure, resize, replay, bounds, typed errors and the single end of every command.

This guide covers `omniview.sdk.exec.v2` (`proto/exec/v2/exec.proto`, `pkg/v1/exec`). The legacy lane (`exec.Handler`, `TTYHandlerFunc`) is unchanged and keeps working; a plugin may serve either or both.

The SDK serves the eight calls of the service: `Describe`, `Options`, `Open` (a new session, or an attach to one that lives), `ListSessions`, `ReadOutput`, `EndSession`, `Run` and `RunStream`. A host lists your targets, asks for the choices of one resource (when your plugin has the resource provider described [below](#options-needs-a-resource-provider)), opens an interactive session in a target, leaves it and comes back to it, and runs one bounded command in it, as one result or as events. For an `Open` the SDK runs your `ExecFunc` with a `Proc` and sends the host the frames of [What the host receives of a session](#what-the-host-receives-of-a-session); **a session does not live and die with its stream** ([A session outlives its stream](#a-session-outlives-its-stream)). For a `Run` the SDK runs the same function for a command without a terminal, keeps a bounded part of its output with an exact account, and answers with one `RunResult` ([Bounded runs](#bounded-runs-run-and-runstream)). Marks, terminal modes and shell integration are in the contract, and the SDK serves none of them ([What the SDK does not serve](#what-the-sdk-does-not-serve)).

**The rules, in plain words.** (1) A failure after the command was handed to your function is data (`Ended{failed}`, `RunResult` with `OUTCOME_FAILED`); only a refusal, when nothing ran, is a status. (2) A session has exactly one end and **at most one `Ended` frame**: `Ended` goes only to the stream that is attached when the session ends, so a session that ends while no stream is attached (a lease that expires, a retirement, a shutdown) has none, and its end is the tombstone's (`SessionInfo.ended_with`). A stream that can still receive frames ends with exactly one of `Ended` (the session is over) or `Detached` (this stream left, the session lives). (3) A session may only narrow the capabilities its kind declares, never widen them. Its mode (a Kubernetes exec or attach) is declared on the `Choice` of your `Options` (`Choice.Capabilities`) and is in `Opened`; `Started` carries only what you learn at run time, is sent at most once and always before the first output, and the SDK sends one made from the facts of `Opened` when your function writes before it reports. `exectest.ProcRig` (given `exectest.RigCapabilities` and `exectest.RigOptions`) tells your test when your function widens, reports late or reports twice, as the SDK will log it. (4) An exit code is carried exactly, in any integer type up to 32 bits unsigned. A stream that ended with no status is `Lost`, never exit 0, and a status that only the transport relayed (Kubernetes' `Success`) is `Exited{0, TRANSPORT}`, "ended normally", never the process's own exit 0. (5) Requests are strict (an unknown field is refused) and carry no maps: environment, labels and selection are lists of `NameValue`. (6) A session outlives its stream: a host that leaves (a `Detach`, a cancelled stream, a lost connection, the end of its side of the stream) leaves the session running for its lease, and only a host that breaks the protocol ends the session with its stream.

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
| `Options` | the choices of one resource with a state (a Pod's containers), and the mode of each choice | the RPC, the lease, the bounds (at most 8 options, 256 choices each, closed codes), and a 5 s cache of what the callback answered for the admission of a start |
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

Seven lines. `ExecFunc` must honor `ctx` (the SDK cancels it to close, kill, time out, end a session whose lease ran out, retire the connection or shut the plugin down) and returns when the command has ended: `(Exit, nil)` for any status, a non-nil error when the target failed, `ExitLost` when the stream to the target ended with nothing relayed (it broke, or closed with nothing on its status channel). `p.Started` reports what you learn once the command runs (the program that really runs, a pid, whether a terminal was granted, and the session's capabilities if they turn out narrower than the ones it was opened with): call it at most once, and before the first byte you write, because the host receives the session's `Started` before its first output. A session whose function writes without having called it gets a `Started` that the SDK makes from the facts of its opening (the requested program and the opened capabilities); a call after the first output, or a second one, is ignored and logged, and `exectest.ProcRig` tells your test. A session that neither writes nor calls it has none. A command's start is seen in three honest ways, and nothing else counts: its first byte (the SDK's own `Started`), the status it ends with (`start_observed` is then true and there is still no `Started` frame), and the first `Read` an executor makes of the stdin you handed it, which [`exec.OnFirstRead`](#ending-a-command-over-a-client-library) turns into your own `Started`. `p.Stdout` fails once the session has ended, `p.Stdin` reports `io.EOF` when the host closes the input or the session ends, and `p.Resize` is closed when the session ends, each of them after what the host sent before that, which the end does not discard (see [Control frames](#control-frames-resize-signals-stdin-eof-and-facts)).

**When your function breaks the contract.** The engine runs your function on its own goroutine, once for each command that was admitted, and contains what a function can get wrong. Nothing a misbehaving function does crosses the wire as its own text, and each of these is counted (`omniview.plugin.self.sdk.exec.violations`, once for each session and kind) and logged by the SDK, so you find it in your plugin's log:

- **A panic in `ExecFunc`** is recovered. The command ends `Failed{INTERNAL}` (a command that had not started fails at `START`, one that had at `RUN`). The panic value and the stack go to the plugin's own log at `error` and nowhere else: not into the end, the metrics or the trace. A panic in a goroutine you started yourself is plain Go and ends the process.
- **Which goroutine may call `p.Started` and `p.Link`:** any goroutine of your function, while it runs. Both are safe for concurrent use. `Started` counts once, and only before the first byte of output; a second call, a call after the first output, and one with facts that are not within the bounds of the session are refused and logged (a start gate that already opened with the SDK's own `Started` stays what it was). `Link` is for a target that declared `LinkHealth`; a state that is not a state of the link is refused. A call of either after the function returned is refused.
- **A write after your function returned,** or after the session ended, fails with `io.ErrClosedPipe`, is counted (`late writes` and `late bytes` of the result, `omniview.plugin.self.sdk.exec.violations{reason=late_write}`) and is never delivered: the end is already decided. Output that you wrote before the end is not lost to it: the attached stream reads it to its end before its last frame, and a host that was not attached finds what the ring held in the tombstone. A read of `p.Stdin` after the host closed the input or the session ended returns what the host sent before that, and then `io.EOF`.
- **A function that ignores `ctx`:** a close, a kill, a timeout, the end of a lease, a retirement and a shutdown all cancel `ctx`. The end is typed at once from who asked (`Closed{by}`), and the SDK then waits a bounded time for the function to return: the grace (5 s by default) after a graceful close, a lease that ran out, a retirement, a host that broke the protocol or a run whose caller went away; the **kill wait, 50 ms, counted from the kill and never the grace**, after a kill (a host's kill frame, `EndSession` with a kill, and a run's timeout); and 1 s (the shutdown grace) when the plugin is stopping, because the host stops a plugin two seconds after it asks. A function that honors `ctx` returns within its wait: it is not abandoned, and its session's place is free when the end is delivered, so a host that kills a session and starts another at once is not refused. A function that has not returned when its wait runs out is **abandoned**: the command ends without it, the violation is counted (`omniview.plugin.self.sdk.exec.violations{reason=ignored_cancel}` when the grace or the shutdown grace ran out, `did_not_return` when the kill wait did) and listed in the result, the plugin's log says so at `error` at that moment (the first time, and at most once a second for each target after that), and the gauge `omniview.plugin.self.sdk.exec.abandoned` shows it until it returns. The SDK cannot stop a goroutine, so an abandoned function keeps what it holds until it returns; `ctx` is the only way it is told. **It also keeps its session's place** (a run's, its place among `Run.MaxConcurrent`): the target's `Session.MaxSessions` bounds the sessions, the detached ones included, and the abandoned functions together. **Functions that never return keep their places until the plugin restarts, and the target is then dead for that kind of command.** A start that the abandoned functions make too many is refused `UNAVAILABLE` with the reason `TARGET_WEDGED`, which is neither `LIMIT` (live work fills the target) nor the bare `UNAVAILABLE` of a plugin that is shutting down: it tells a host to say "restart the plugin" and not "try again", because retrying does not help until the functions return, and they may never. The refusal is counted in `omniview.plugin.self.sdk.exec.abandoned.refused` and logged (the first time, and at most once a second for each target after that), and `Describe` still lists the target, with nothing marked. What a stuck target can leak is its declared maximum, however often a host asks, and a place is free the moment its function returns.
- **An `Options` callback that ignores its context, and a `Classify` that never returns.** Both are your code, and a start, a call of `Options` or an end waits for them, so all are bounded. The context of `Options` ends at its deadline (30 s), and at once when the caller gives up, the plugin shuts down or the connection is retired; the callback then has the same grace to return (the shutdown grace, 1 s, when the plugin is stopping). One that does not is given up on: the start or the call is refused (`TIMEOUT` for the deadline; the caller's own cancellation or timeout when it gave up; the cause's own refusal otherwise: `UNAVAILABLE` for a shutdown, `CONNECTION` / `CONNECTION_RETIRED` for a retirement), the connection's lease is released (a retirement of the connection does not wait for the callback), `violations{reason=options_hung}` is counted, the plugin's log says so (the first time, and at most once a second for each target after that), and the goroutine goes on until it returns: the gauge `abandoned` shows it, and it keeps a place. For a start that place is the session's. **A call of `Options` has no session, so its place is among the callbacks of its target that are in flight: at most 32 at once, the ones given up on that have not returned included.** A call that would be one more is refused `UNAVAILABLE` and counted in `omniview.plugin.self.sdk.exec.options.refused`: with the reason `TARGET_WEDGED` when one of them was given up on and has not returned (restart the plugin), and with no reason when every one is still inside its bound (the target is busy: try again later). The cap is for each target: a target whose callbacks hang does not refuse the calls to another. It may use the client it was given after the lease was released. `Classify` has no context to honor and is given 5 s: if it has not returned, the error is classified by the SDK's own rules (`INTERNAL` for one nobody can classify), `violations{reason=classify_hung}` is counted, and the session ends all the same.
- **A function that returns before the process ended** (the context was cancelled, or it returned a status early): what it returns decides, because the SDK knows only what the function reports. A function that returns after its `ctx` was cancelled ends `Closed{by}`, not `Exited`, whatever it returned; that is the cause of the end and the status is dropped. If you need the status of a command you closed, report it through the target's own channel.
- **A function that does not read its control channels.** For a target that declared `Resize`, a function that never reads `p.Resize` leaves the sizes waiting (the one it was handed and the newest: a resize storm costs two entries), and the SDK logs `resize_unread` once after 5 s. One that never reads `p.Signals` loses the oldest of the eight signals that wait for each new one (counted in `omniview.plugin.self.sdk.exec.signals.dropped` and logged), and one that never answers a `Query` has the host answered unknown after 5 s (`query_unanswered`). Nothing blocks the SDK or the stream; what the host is told is in [Control frames](#control-frames-resize-signals-stdin-eof-and-facts).
- **Exit 0 on a lost stream:** a status returned while the last `p.Link` report was `LinkStalled` is reported as the transport's (`TRANSPORT` source, never `PROCESS`) and logged as `stalled_status`; an error returned together with an exit status is a failure (`exit_and_error`); `ExitLost` is never turned into a code.

The queues are bounded. The input queue holds 256 KiB. The output of a session is its **replay ring**: your `Write` goes into it, and it holds the newest `Session.ReplayBytes` (1 MiB by default; a host may ask for up to `Session.MaxReplayBytes`, 4 MiB by default, with `Start.replay_bytes`; never less than 1 KiB) in whole chunks of 64 KiB (smaller for a ring of under 256 KiB), allocated as the session needs them and reused once the ring has filled. What a full ring does to your `Write` depends on who reads. For a target that declared `StallPropagates` (or declared nothing) and a stream that is attached, `Write` blocks when the host has left 256 KiB unread, and nothing is dropped, however many times the ring wraps. For a target that declared `StallLossy`, for a session with no stream attached (detached) and for a session that is ending, `Write` never blocks: the oldest whole chunk is dropped, the bytes that no reader had taken are counted (`DroppedBytes`; the end's output is then `TRUNCATED`) and the stream that was reading is told the exact range it lost (`Dropped`: `EAGER_DRAIN_OVERFLOW` for a lossy target, `REPLAY_FULL` for a session that is ending). The count is exact for every size of ring: the bytes a host read, the bytes in `Dropped` ranges and `bytes_out` add up, and each range ends at a chunk boundary. What leaves the ring while no stream is attached is counted as well (`DETACHED_OVERFLOW` in the metrics), and the host learns of it from `replay.start` when it attaches. A byte counts as dropped when it leaves the ring before a *stream* took it, whoever else read it: a `ReadOutput` is a cursor read that takes nothing from the ring's accounts, so a host that assembled the output with it knows better than `DroppedBytes` does. So a session costs up to `ReplayBytes` of memory (`MaxReplayBytes` for a host that asked for more) whatever the host does: size a plugin's memory from the target's declaration. The output of a run keeps its own queue of 256 KiB.

## How a command ends

Return exactly one of these. An exit is never guessed.

| You return | The consumer reads | What it means |
|---|---|---|
| `ExitCode(n)` | `Exited{n, PROCESS}` | the process's own status, as the target relayed it. **`n` may be any integer type** (`int`, `int32`, `uint32`, `int64`, ...): a Windows DWORD arrives exactly, `ExitCode(uint32(0xC0000005))` is 3221225477 on the wire, and a reporter's negative 32-bit reading of the same DWORD stays negative. Never inferred from a code range |
| `ExitSignal(name, n)` | `Signaled{..., PROCESS}` | the target said a signal killed it; **only** for a target whose `Capabilities.ExitReport` is `ExitReportCodeAndSignal`. From any other target a signal is never turned into a code: the end is a failure of the target (`INTERNAL`) with unknown survival, and your bug is logged. A name that is not valid text (UTF-8, no control characters, at most 256 bytes) or a number outside 1 to 64 is dropped, and the other identifier is kept |
| `ExitFolded(n)` | `Exited{n, SIGNAL_FOLDED}` | the target itself says the code is a signal death folded into 128+n: no signal is claimed. An ordinary exit can carry the same number, so never infer this from a code; a target that gives no provenance reports `ExitCode` |
| `ExitTransport(n)` | `Exited{n, TRANSPORT}` | the transport said the stream **ended normally**: its word, never the process's own status. Kubernetes' `Success` is this, and it is all that client-go's `nil` can say: after a kubelet or containerd restart the API server sends `Success` for a command that did not exit 0 (8 of 8 sessions in the proof), so `ExitTransport(0)` means "ended normally", not "exit 0". The end has its start observed and `remote_may_survive` **absent**, and a consumer shows "ended normally", never "exit 0". [`ExitOfStreamError`](#ending-a-command-over-a-client-library) returns it for a `nil` |
| `ExitLost(cause)` | `Lost{cause}` | the stream to the target ended with **nothing** relayed: it broke (a reset, a close with no status, a server restart) or closed with nothing on its status channel (`LostNoStatus`), or the keepalive gave up (`LostLinkDead`). **Lost is never exit 0**, never a failure of the command, never a reason to retry it |
| `Exit{}` (empty) or a mixed `Exit` | `Failed{INTERNAL}` | a failure of the target, never an exit 0 |

`ExitCode`, `ExitFolded` and `ExitTransport` take one of `int`, `int8`, `int16`, `int32`, `int64`, `uint8`, `uint16`, `uint32`. `uint` and `uint64` are not accepted, because a value above `math.MaxInt64` would not fit the signed 64-bit wire code. A target whose (effective) `ExitReport` is `ExitReportNone` cannot relay a process status: an `ExitCode` from it is reported with source `TRANSPORT`, and the SDK logs your bug.

**Does the process survive?** The end carries `remote_may_survive`, set from your `Exit` by the end conversion (`Exit.end`, which the engine calls): `Lost` is **true** (the process may still run); `Exited` or `Signaled` from `PROCESS` or `SIGNAL_FOLDED` is **false** (the target reported its status, so it is gone); `Exited` from `TRANSPORT` and a failure are **absent**, which means **unknown**, and a consumer treats unknown as "may survive". Absent is never "no". A `Closed` end says `false` only for a target that declared `Kill` as `KillTerminates`; for `KillBestEffort` and `KillClosesStreamOnly` it says `true`, and for a target that declared nothing it says nothing. Whoever closed the session (a host, the lease, a retirement, the plugin's shutdown), the declared `Kill` decides and the cause does not.

### Ending a command over a client library

A client library such as client-go ends a stream with one error (or `nil`) and offers no hook for the rest. An `ExecFunc` over it has three things to decide, and the SDK gives a helper for each, none of which imports the library.

**How it ended: `exec.ExitOfStreamError(err) (Exit, bool)`.** It reads the error of the executor in the shapes of client-go, ssh and os/exec:

| The executor returned | `ExitOfStreamError` | The consumer reads |
|---|---|---|
| `nil` | `ExitTransport(0)`, true | `Exited{0, TRANSPORT}`: ended normally, start observed, survival absent |
| an error in the chain with `ExitStatus() int` (client-go's `CodeExitError`, ssh's `ExitError`) or `ExitCode() int` (os/exec's `ExitError`) | `ExitCode(n)`, true | `Exited{n, PROCESS}`; nothing is inferred from the number (137 is a plain 137) |
| anything else (an error of the upgrade, a reset, a cancelled context) | `Exit{}`, false | yours to decide: return the error, which `Classify` or the SDK types, or `ExitLost` for a stream that broke while it was read |

An error that also has `Exited() bool` and says false (os/exec's `ExitError` of a process that a signal ended, whose `ExitCode` is -1) is no status. client-go returns `nil` both for a `Success` status and for a status channel that closed with nothing on it, and `remotecommand.Executor` has no hook for the status, so **the SDK cannot tell the two apart**; the decision is that a `Success` is "the stream ended normally" (`Exited{0, TRANSPORT}`) and not the process's exit 0, because the API server sends it for a command that did not exit 0, and that an empty channel or a break is `Lost`. An author who owns the WebSocket (the v5 channel protocol puts a status on channel 3) reads the status itself: a `Failure` with `NonZeroExitCode` is `ExitCode(n)`, a `Success` is still `ExitTransport(0)`, and a channel that closed with nothing on it is `ExitLost(LostNoStatus)`. A verified zero needs an in-band marker (a wrapper that prints `$?`), not a status. `exectest.CodeExitError` is an error with client-go's shape and message (`command terminated with exit code 3`) for the test of your mapping.

**When it started: `exec.OnFirstRead(r, fn) io.Reader`.** client-go has no "running" callback. A quiet command's start is seen in three ways: its first byte (the SDK makes the `Started` from the facts of `Opened`), its status, and the first `Read` that the executor makes of the stdin you handed it, which client-go does only once its streams exist. `OnFirstRead` wraps `p.Stdin` and calls your function once, on that first `Read` and before it reaches the stdin, without ever making another `Read` wait for it: `stdin := exec.OnFirstRead(p.Stdin, func() { p.Started(exec.SessionFacts{Transport: "k8s-ws-v5"}) })`. A nil stdin (a run with none) gives nil and no call. Do not report the start earlier: a `Started` before the stream exists would make a refusal of the API a failure at stage `RUN` for a command that never ran; at the first read a refusal of the upgrade is still a `START` failure and a failure that the runtime reports afterwards (no such executable) is a `RUN` failure. A command that ends with a status and never wrote and never reported has `start_observed` true and **no** `Started` frame: a session that never writes and never reports gets none, and the SDK does not make one at its end.

**What kind of failure: `exec.ErrorFromHTTPStatus(status, cause) *Error`.** A refusal of the API (a 401, a 403) must reach the host as a typed class, and client-go flattens the status of the upgrade into text. The SDK's own classification, which runs when your `Classify` is nil or says nothing, reads an error in the chain with an `HTTPStatus() int` or a `StatusCode() int` method and applies this table; an error whose status is only in its text has none, so read it in your `Classify`:

| Status | Class | Reason |
|---|---|---|
| 401 | `UNAUTHORIZED` | |
| 403 | `FORBIDDEN` | `TARGET_DENIED` |
| 404, 410 | `NOT_FOUND` | (no reason: add `TARGET_GONE` yourself when you know it is the target) |
| 409 | `CONFLICT` | |
| 408, 504 | `TIMEOUT` | |
| 429, 502, 503 | `UNAVAILABLE` | |
| 501 | `UNSUPPORTED` | |
| any other 4xx | `INVALID_REQUEST` | |
| any other 5xx, or no error status | `INTERNAL` | |

The texts below are what client-go really hands over on kind v1.34 (the proof of a Pod provider), and the example is [`examples/exec-plugin/classify_snippet_test.go`](../examples/exec-plugin/classify_snippet_test.go), compiled and tested in CI:

```go
// podClassify maps what client-go hands over as text. The API's own refusals of the upgrade arrive flattened, with no type to
// read ("unable to upgrade connection: pods \"x\" is forbidden: User ..." for a 403, "unable to upgrade connection: Unauthorized"
// for a 401), so the status is read from the text here and ErrorFromHTTPStatus gives it its class.
func podClassify(err error) (*exec.Error, bool) {
	text := err.Error()
	switch {
	case strings.Contains(text, "unable to upgrade connection") && strings.Contains(text, " is forbidden"):
		return exec.ErrorFromHTTPStatus(http.StatusForbidden, err), true // FORBIDDEN, TARGET_DENIED
	case strings.HasSuffix(text, "unable to upgrade connection: Unauthorized"):
		return exec.ErrorFromHTTPStatus(http.StatusUnauthorized, err), true // UNAUTHORIZED
	}
	return nil, false // anything else is the SDK's: the class of a status it can read, or INTERNAL
}
```

## What the host receives of a session

A host opens a session on one stream (`Open`), and the SDK sends frames in this order whatever your function does:

| Frame | When | What it carries |
|---|---|---|
| `Opened` | first, always | the facts the SDK knew before your function ran: the target, the requested program and size, the capabilities the session was opened with (your declared set narrowed by the choice the selection names) and the limits as you declared them. Nothing only the target can say is in it: the terminal, the pid, the TERM and the transport are absent. For an **attach** it carries the session's own latest facts (what `p.Started` reported, the capabilities as narrowed) and the description of the replay that follows: `replay{start, end, bytes, start_size, size}` |
| `Notice` | after `Opened` | `SIZE_DEFAULTED` when a terminal was asked for with no size and the SDK gave it 80 x 24 |
| `Started` | at most once, before the first `Output` | `Opened`'s facts refined by what you reported with `p.Started` (program, pid, terminal, size, TERM, transport, narrower capabilities), or the SDK's own from `Opened` when your function wrote output before it reported |
| `Output` | as your function writes | up to 64 KiB of one stream (`TTY`, or `STDOUT` or `STDERR`) with its `offset`, the position of its first byte in the one sequence of the session's output; offsets have no gap except across a `Dropped` range, and the bytes are untouched |
| `Dropped` | before the output that follows the range | exactly the range the ring lost that this stream had not read: the stream (of its first byte), `from_offset` and `bytes`, with `EAGER_DRAIN_OVERFLOW` for a target that declared `StallLossy` and `REPLAY_FULL` for a session that was ending. A replay is never cut by one: it is a copy of what `Opened` described |
| `Link` | when the state changes | the state you reported with `p.Link`, only for a target that declared `LinkHealth` (`silent_ms` is 0: you report a state, not how long it has lasted) |
| `Resized` | after a `Resize` of the host | the acknowledgement of its resizes: the newest `seq` it covers, that size, and `APPLIED` (your function took the size from `p.Resize`), `IGNORED` (the session has no terminal or no `Resize`: said at once) or `FAILED` (a function that declared `Resize` did not take it within 5 s). One ack covers every resize up to its seq, so a storm costs the host one. It goes to the stream that sent the resize and to no other: a stream that attaches later is never sent an ack for a resize of the stream before it, and learns the size in force from `replay.size` and `replay.start_size` in `Opened` |
| `Fact` | after a `Query` of the host, or when you push one | the fact (the foreground process, the current directory, or the integration tier) with the `query_id` it answers; `query_id` 0 is a fact you pushed with `p.Fact`. The answer goes to the stream that asked, and a stream that attaches is sent the newest pushed fact of each kind once |
| `Ended` or `Detached` | last, once | `Ended` is the end of the **session**, sent to the stream that is attached when it ends: how it ended (`End`), whether its start was observed, the duration, `bytes_out`, `bytes_in`, `dropped_bytes`, the output's completeness, `last_offset` (one past the last byte: equal to `bytes_out`) and `mode_reset` (present and empty for a session without a terminal; absent with one, which the host reads as "apply your own full reset"). `Detached` is the end of **this stream** while the session lives on: `REQUESTED` (the host sent `Detach`; `lease_expires_unix_ms` is when the SDK ends the session if nobody attaches, after clamping the lease to `Session.MaxDetachTimeout`), `SUPERSEDED` (another stream attached and took the session over; no lease, the session is attached to it) or no reason (the host closed its side of the stream; the session's own lease runs, unless the session was already ending). What the stream's frames earned before it left is sent before `Detached` |

After `Ended` or `Detached` the stream ends with status `OK` and nothing follows. A session has **at most one `Ended`**, however many streams it had: the stream that was told `Detached` is never told `Ended`, and a session that ends while no stream is attached (its lease ran out, the plugin stopped) has none: its end is the tombstone's. A stream that breaks is told neither, and neither is one whose host broke the protocol: it ends with the status it earned. A host that closes its side of its stream after the SDK decided the session's end and before the stream was sent `Ended` is told `Detached`, with no reason and no lease, even when the stream had read the output to its end: it left first, and the end is the tombstone's. When `Ended` arrives the session's **place** is free, so a host that starts another session at once is not refused for the one that has just ended, and its tombstone exists; the session's **id** stays held by the tombstone (see [A session outlives its stream](#a-session-outlives-its-stream)). A request that is refused is a status and no frame, and your function did not run; a stream that sends no open frame within 10 s is refused `TIMEOUT`.

The host sends input (frames of at most `Session.MaxInputFrame`; the SDK stops reading frames while the function's input queue is full, so a function that never reads stops the host's `Write` at 256 KiB), resizes (latest wins; the first value of `p.Resize` is the size the session was opened with), signals, `close_input`, queries, a close (graceful, or a kill that does not wait for the function) and `Detach`. The frames of a session are applied in the order they were sent, one at a time, and the receiver does not read the next while one waits: input waits for room in the function's queue, and the answers to queries for room in the stream's (64 answers), so **a close or a kill that is behind input the function never reads is not seen until it reads**. The way out is to cancel the stream (an implicit detach) and then `EndSession` with a kill. The host's cancel travels on a goroutine of its own client, so an `EndSession` that overtakes it can still find the stream attached and answer `CONFLICT`: the host asks again (a stream whose context has ended is not attached). A frame of a stream that another stream took the session from, or that left it, takes no effect on the session: it is applied before the session changes hands or never. A frame of a kind this SDK does not know is ignored and counted (`omniview.plugin.self.sdk.exec.frames.ignored`).

**A host that breaks the protocol** ends its stream with the typed status it earned, and no `Ended` or `Detached`, and ends its session with it (`Closed{CANCELLED}`, graceful; the end is the tombstone's): it does not leave its session running. These frames break it: a second `open` frame, an input frame after the `close_input` that the same stream sent, a resize with a zero dimension or one above `Session.MaxDimension`, and a query with the id 0 or no kind (all `INVALID_REQUEST`); an input frame over the maximum (`LIMIT` / `INPUT_TOO_LARGE`); and a signal that the session's effective `Capabilities.Signals` does not list, or a `close_input` for a session whose effective `StdinEOF` is false, which break what `Opened` told the host (`UNSUPPORTED`). A stream that breaks before the SDK could send `Opened` ends the session it started in the same way: the host was never told that the session exists. The input of a session stays closed when the stream that closed it leaves: the input frames of a stream that attaches later are dropped, as those of a command that has ended are, and are not its violation.

One stream is not slower for another's slowness: each has its own cursor into its ring, its own transport window and its own goroutines, so a host that stops reading one session stops that session's function (or, for a lossy target, makes it drop) and nobody else's.

### Control frames: resize, signals, stdin EOF and facts

**The end of a session discards nothing the host sent.** What the SDK accepted from the host before the session ended (the bytes of input, the signals, the queries and the size that your function has not read yet) is in the function's queues in the order the host sent it, before `ctx` is cancelled and before any channel is closed, and it stays there for as long as the function runs: a function that reads `p.Signals` and `p.Queries` until they close, `p.Stdin` until `io.EOF` and `p.Resize` until it closes finds all of it, whichever way the session ended (a close, a kill, `EndSession`, a lease that ran out, a stopped plugin, or a host that broke the protocol). A frame that comes once the end is decided is refused: nothing is queued for it, and a query is answered unknown at once. A function that stops reading when its `ctx` ends may not read what is left, and that is its choice and never the SDK's: read your channels to their end before you return. The SDK does not wait for a function it gave up on (one that ignored its cancelled `ctx` for the grace or the kill wait): `p.Resize` is closed for it at once, with the size it had not taken, and the other queues keep what they held. What a queue dropped while the session lived (the oldest of the eight signals, for a function that did not read) was counted and logged then, as below, and is not this.

- **Resize.** `p.Resize` delivers sizes, latest wins, first the size the session was opened with, and is closed when the session ends, once your function has taken the size it holds (a goroutine that ranges over it returns with the rest). Taking a size is what the host is told (`Resized` `APPLIED`, with the newest `seq` you took). A session that cannot be resized (no terminal, or no `Resize` in its effective capabilities) is told `IGNORED` at once and its function is handed nothing. A size that no function takes is told `FAILED` once, after 5 s, for the newest size the host sent; if the function takes it later the host is told `APPLIED` as well, and a size that waited for a function that narrowed its session to no `Resize` meanwhile is told `IGNORED`, not `FAILED`.
- **Signals.** `p.Signals` is never nil, delivers the signals the host asks for in the order it sent them (after the input it sent before them), and closes when the session ends, after the ones it holds. Only a signal in the session's effective `Capabilities.Signals` is delivered (a stream that sends another ends with `UNSUPPORTED`, as the frames above say); a signal is not an end, and what it does to the target's process is your function's. The queue holds eight: delivery never blocks the SDK, and a function that does not read loses the oldest signal for each new one, which is counted and logged.
- **Stdin EOF.** `close_input` makes `p.Stdin` return `io.EOF` once your function has read every byte the host sent before it, for a session whose effective `StdinEOF` is true (the SDK lists `FEATURE_STDIN_EOF` in `Describe`, and a host sends the frame only to a session whose capabilities say so). A `cat` that was sent `abc\n` and then `close_input` reads them, reads `io.EOF`, returns, and the session ends `Exited` by itself.
- **Facts.** The host asks for a fact with a query, and the target says how it provides each one (`CwdFact`, `ForegroundFact`: none, pull or push). A target that provides one has `p.Queries`, which is nil otherwise; answer each `Query` once with `Query.Answer(exec.Fact{...})` (an empty directory or an unspecified foreground state is unknown, with a `Reason`). A query for a fact the target does not provide, the integration tier (the SDK observes none, so it is unspecified), a kind the SDK does not know, and a query that would be the ninth to wait for your function are answered by the SDK at once, unknown (`UNSUPPORTED` or `QUERY_FAILED`); a query your function does not answer in 5 s is answered unknown and `QUERY_FAILED`, and a late answer is ignored. A target that pushes a fact calls `p.Fact(exec.Fact{Kind: ...})` when it changes: the SDK sends the newest value of each kind at most once every 100 ms (ten times a second) and never a value the host already has. `p.Fact` is never nil; for a kind that is not `FactModePush` it does nothing and is logged as your bug.

**When the plugin stops.** A host that stops a plugin with go-plugin's `Client.Kill` first calls the controller's `Shutdown`, which stops the plugin's server at once, and then waits 2 s for the process to exit before it kills it. The SDK holds that one call: it ends every session and every run with `Closed{PLUGIN_SHUTDOWN}`, cancels each function and gives it 1 s (not the grace of a close) to return, and lets the call go on, and the server stop, once the handler of every attached stream has sent its `Ended` and every `Run` has handed over its result, or after 1.5 s, whichever comes first. So **each attached stream receives its `Ended{Closed{PLUGIN_SHUTDOWN}}`, and then its clean end, when the plugin is stopped through the controller**; a function that has not returned after its 1 s is abandoned (see above) and its command still ends. A session that no stream is attached to ends the same way and has no `Ended`: its end is its tombstone's, readable until the tombstone expires for as long as the server runs. The `Ended` is handed to the transport before the server stops, and the transport writes it as the host's window allows: a host that is not reading, or is far behind on a flood, can still lose it with the connection. A host that kills the process without asking, a plugin that crashes, and a stream that was not read in time read the stream as broken (`CONNECTION`) with no `Ended`, as they always could. Once the server has stopped, however it was stopped, the SDK tells the plugin's capabilities once more, with at most 10 s: for a plugin that stopped itself, the functions are then cancelled and given the same 1 s, though no stream is left to receive an end; after a controller stop it finds nothing left to end.

## A session outlives its stream

A stream is one **attachment** of a session, and a session has at most one at a time. The host names a session by the id it chose in `OpenRequest.session_id` (1 to 128 bytes of printable ASCII). **A host that leaves does not end the session**: a `Detach` frame, a cancelled stream, a lost connection and a host that closes its side of the stream (the transport's half-close, which is not stdin EOF: that is `close_input`) all leave the session running, **for its lease**. A function's `ctx` is cancelled only when the session ends, never when its stream leaves: the command keeps running for the whole lease, and when the lease ends the session, the end says whether the process may outlive it, as the declared `Kill` says (a target that declared `KillClosesStreamOnly`, Kubernetes or Docker, leaves it running). What destroys a session is a `Close` frame, `EndSession`, the end of the command, the lease running out, the retirement of its connection and the plugin stopping; and a host that broke the protocol (its stream ends with the status it earned, and the session with it), or whose stream broke before the SDK could send `Opened` (the host was never told the session exists).

- **The replay ring** keeps the newest whole chunks of the output, with absolute offsets: offset 0 is the first byte of the session and offsets never restart. It also keeps the size log (each size your function took from `p.Resize`, with the offset the output had when the SDK saw it taken, at most 256 of them), so that `replay.start_size` is the size the bytes at the start of a replay were produced for and `replay.size` the size now; both are absent for a session without a terminal. The SDK sees a size taken a moment after the function took it, so the log can lag for a function that takes a size and writes at once, a flood: it may have written a few frames before the size is recorded, and a replay that starts among them names the size before. `replay.start_modes` and `replay.modes` are absent: the SDK tracks no terminal modes.
- **Attach** (`Open{attach{from_offset}}`, `from_offset` being the first offset the host does not have). The reply is `Opened{facts, replay}` and then exactly the `Output` frames of `[replay.start, replay.end)`; the live stream continues at `end` with no byte lost and none repeated, because the SDK takes a copy of the ring and registers the stream under one lock. `start` is `from_offset` when the ring holds it (a cursor inside a chunk resumes exactly there), and the front of the ring when it does not: `start` is never before `from_offset`, and `start > from_offset` says that bytes were dropped in between. An offset the session has not reached is `INVALID_REQUEST`; an id that is not a live session (never existed, ended, ending, or a run's) is `NOT_FOUND` / `SESSION_UNKNOWN`. The older stream is told `Detached{SUPERSEDED}` as its last frame and the session is not ended; what a superseded stream sends afterwards takes no effect. A session has at most one `Started`, in the life of the session and not in the life of a stream: a stream that attaches after it has its facts in `Opened`.
- **An attach whose stream has ended registers nothing.** An `Open{attach}` whose stream the host cancelled, or whose connection was lost, before the SDK registered it takes the session from no one: it is refused as cancelled, and the stream that holds the session keeps it. The SDK decides this in the step that registers the stream, under the session's lock, so on one connection a host that cancels an attach and attaches again at once is never superseded by the attach it gave up on, whichever of the two requests the SDK serves first (the cancel reaches the SDK before the stream the host makes next). An attach that did register, and whose stream ended before `Opened` could be sent, was an attach that left: the stream it took the session from is told `Detached{SUPERSEDED}` and the session lives for its lease, as for any stream that breaks. Attaches that arrive on different connections have no order, and the one that registers last has the session.
- **Detach** (`Detach{lease_ms}`) ends the stream with `Detached{REQUESTED, lease_expires_unix_ms}` as its last frame. `lease_ms` zero is the session's own lease (`Start.detach_timeout_ms`, or `Session.DetachTimeout`, 5 minutes by default) and a value above `Session.MaxDetachTimeout` (30 minutes by default) is clamped to it: `lease_expires_unix_ms` is when the lease ends as applied. A `Start` that asks for a lease above the maximum is refused `INVALID_REQUEST`. A `Detach` that comes after the session's end was decided is moot, and the stream is told `Ended`.
- **A host that leaves without `Detach`** is an implicit detach with the session's own lease. A stream that breaks (the host cancelled, the connection was lost) is told nothing, and a host that closes its side of the stream is told `Detached` with no reason, after every frame that was sent.
- **`Close` and `Detach`** in either order leave the session in the same place: `Close` then `Detach` on one stream ends the session (the `Detach` is moot and the stream is told `Ended`); `Detach` and then a `Close` from the stream that attached after it, or `EndSession`, end it too, with the same `End` and the same counts, the `Ended` going to the stream that was attached. The one exception is a `Close` that is pipelined behind a `Detach` on the same stream: the stream left with the `Detach`, and what a stream that is no longer the session's sends is ignored (as for a superseded one), so the session lives for its lease and the host ends it with `EndSession`.
- **The lease** ends a session that no stream attached to with `Closed{DETACH_TIMEOUT}` and no `Ended` frame: its end is the tombstone's, and its `remote_may_survive` is what the declared `Kill` says of any close. An attach before the lease ends cancels it, and a lease that has run out ends only a session nobody has attached to (the two are decided under one lock, so an attach and the end of a lease are never both true). A detached session holds its place, and its function keeps running; what it writes goes to the ring, which loses its oldest chunks and counts them while no stream reads it. The plugin sees a stream that broke at once; a host that vanished without closing its connection is found by the keepalive ([Stream health](#stream-health)), and the lease runs from then.
- **The tombstone.** When a session ends (its function returned, its lease ran out, a host closed or ended it, its connection was retired or the plugin stopped), attached or not, the SDK keeps its facts, its `Ended` and the bytes its ring held, for 60 seconds, readable with `ListSessions` and `ReadOutput`; the tombstone exists before `Ended` is sent, so a host that has read it finds it. **The id of a session that ended is held by its tombstone**: a `Start` of a session with the same id is `CONFLICT` / `SESSION_EXISTS` until the tombstone is gone, which is exactly at its time to live, when the store drops it for the count, and never because it lost its bytes (a run is not a session and keeps no tombstone: a run's id is free the moment it has ended). The store is bounded: at most 256 tombstones, the oldest going first with its id, and at most 32 MiB of output in all, the oldest tombstones losing their bytes and keeping their end (`ReadOutput` then answers no chunks, `ended`, with `next_offset` at the end of the output). A tombstone holds an id and that memory, not a place, and nothing else of its session: a replay that a stream was given and never read does not outlive the stream. A timer releases what expired when nobody asks, and stops when the plugin shuts down (what the store holds is then readable until it expires, and no timer is armed again).
- **`ListSessions`** lists the live sessions and the tombstones by id (`SessionInfo`: the facts as of the last `Started`, whether a stream is attached, `ended_with` for a tombstone, `next_offset`, `replay_start`, the time of the last output or input, the last link state, the lease of a detached session). A session that ends while the call runs is listed once, as ended. A session whose end is under way and whose stream has left (a `Close` was applied and the stream then broke, for one) is listed with no stream and no lease until its function has returned.
- **`ReadOutput{session_id, from_offset, max_bytes}`** is a cursor read of the ring, live or tombstoned: chunks contiguous from the later of `from_offset` and `replay_start` (each a run of one stream, as long as the answer allows: not a frame of 64 KiB), at most `max_bytes` (64 KiB when zero, never more than the ring's size or 1 MiB) and at most 1024 chunks, with `next_offset` and `ended`. It takes nothing from an attached stream, so a byte it read still counts as dropped for a stream that did not. An offset beyond the output is `INVALID_REQUEST`. A session that is ending, or that ends while the call runs, is never refused as unknown: it is read from its ring or from its tombstone, whichever holds it.
- **`EndSession{session_id, mode}`** destroys a session that has no stream (an unset mode, or one the SDK does not know, is graceful) and answers once it has ended, its place free and its tombstone readable; a caller whose context ends first stops waiting, and the session still ends. A session with a stream attached is `CONFLICT` (close it on its stream; a stream whose context has ended is not attached, but the host's cancel may still be on its way, so a host that has just cancelled asks again), an id that is unknown is `NOT_FOUND` / `SESSION_UNKNOWN`, and a session that has ended already, a tombstone, is not an error.

The three calls are strict requests and read the plugin's session context like `Options`. None takes a connection or a route: a session is named by its id alone, and the plugin does not check which connection started it. Whether it should is an open decision, recorded in [Before any tag that ships `exec.v2`](exec-before-any-tag.md).

## Bounded runs: `Run` and `RunStream`

A host that wants the answer of one command and not a terminal calls `Run`: one request, one `RunResult`. `RunStream` is the same run with its output as events. A target serves them when it declares `Info.Run` (`exec.DefaultRunLimits()` is the usual choice), and `Describe` lists `FEATURE_RUN` and `FEATURE_RUN_STREAM`. Your function is the same `ExecFunc` it is for a session, given a `Proc` for a command **without a terminal**: `p.Request.TTY` is false, `p.Stderr` is set (stdout and stderr are two streams), `p.Resize` never delivers a size, and `p.Stdin` is nil unless the request carried stdin.

**Safe by construction.** The request is strict, as every request is, and:

- **argv only.** `RunRequest` has no command string: `argv[0]` is a non-empty program with no NUL, and a shell is `["sh", "-c", script]`, visible as such to consent and audit.
- **Every bound is mandatory, and none is clamped.** `timeout_ms` is the target's `Run.DefaultTimeout` when zero and `INVALID_REQUEST` above `Run.MaxTimeout`; each output cap is `Run.DefaultOutputBytes` when zero and `INVALID_REQUEST` above `Run.MaxOutputBytes`; stdin is at most `Run.MaxStdinBytes` (`LIMIT` / `INPUT_TOO_LARGE`), argv and env at most `Run.MaxArgvBytes` and `Run.MaxEnvBytes`. A target that does not honor `env` or `cwd` refuses a request that carries one `UNSUPPORTED`; it is never ignored.
- **Places are bounded.** At most `Run.MaxConcurrent` runs of a target kind are in flight: one more is refused `LIMIT` / `TOO_MANY_RUNS`. A run whose function was given up on keeps its place until the function returns (see [A function that ignores `ctx`](#the-minimal-execfunc)), and while abandoned functions hold every place a new run is refused `UNAVAILABLE`, which is not a limit of concurrent runs: nothing ran, and retrying does not help until they return.
- **A refusal is a status, and nothing ran.** Everything after your function was called is a `RunResult`, whatever happened (below).

**The id.** A run is held under its `request_id` (at most 128 bytes of printable ASCII), or under `run-` and an identifier the SDK makes when the host sent none; the result echoes the request's own id, empty when there was none. Runs and sessions share **one id space**: an id that is live is refused `CONFLICT` / `SESSION_EXISTS`, for a run and for a session alike. The id is free again once the host has read the result (the call answers after the engine has released it), so a host that reads a result and runs the same id again is not refused for the run that has just ended.

**The output is kept in a window and counted exactly.** The SDK reads the output as your function writes it and keeps at most the cap of each stream, whatever the command writes: the middle is counted and never stored, and memory grows with the data up to the cap and never past it (a run that wrote ten bytes holds about ten bytes). `output.policy` says which part is kept:

| Policy | Kept |
|---|---|
| `HEAD_TAIL` (the default) | the first ceil(cap / 2) bytes and the last floor(cap / 2): an error is usually at the end |
| `HEAD` | the first cap bytes |
| `TAIL` | the last cap bytes |

`stdout` and `stderr` of the result are the kept bytes, the head and then the tail, **raw and untouched** (never decoded, repaired or cut at a character), and each stream has an account: `total_bytes` (every byte the command wrote, the ones the engine's queue lost before the SDK read them included), `kept_bytes`, `dropped_bytes` (always `total - kept`), `truncated` (some byte was dropped), `head_bytes` and `tail_bytes` (how the kept bytes split), `valid_utf8` and `completeness`. `valid_utf8` is about the bytes **as returned**: a sequence that the cap cut is not repaired, and a head and a tail joined across the elided middle are judged as joined; the host's tool boundary decides text or base64 from it. A target that declared `StallLossy` whose queue overwrote output before the SDK read it has those bytes counted as dropped too, which ends the head where the lost range falls and empties the tail, so that the head stays the beginning of the stream and the tail its end, each in one piece.

`completeness` is exactly one of: **`TRUNCATED`** when a byte of the stream was dropped, whatever else happened; **`COMPLETE`** when nothing was dropped, the run ended with a status the target relayed (`EXITED` or `SIGNALED`, and not the transport's word for a status it did not relay), the engine vouches for the output (nothing was refused after the end, nothing was left to a function that was given up on) and the output was read to its end before the post-exit drain (`Run.PostExitDrain`, 1 s by default) ran out; **`UNSPECIFIED`** for anything else (a timeout, a cancel, a loss, a failure, an abandoned function, a drain that ran out). A host never reads `UNSPECIFIED` as complete.

**How a run ends.** Each end of the command is one outcome of the result:

| The command | `outcome` | `exit_code`, `signal` | `exit_source` | `remote_may_survive` | `failed` |
|---|---|---|---|---|---|
| returned `ExitCode`, `ExitFolded` or `ExitTransport` | `EXITED` | the code, exactly as reported | the one you gave | the end's: `false` for `PROCESS` and `SIGNAL_FOLDED`, absent (unknown) for `TRANSPORT` | none |
| returned `ExitSignal` | `SIGNALED` | `signal` and `signal_number`, no code | the one you gave | as above | none |
| reached its timeout | `TIMED_OUT` | none | `HOST` | the end's: `false` only for a target that declared `KillTerminates` | none |
| was cancelled (its call, a close) | `CANCELLED` | none | `HOST` | as above | none |
| was ended because the plugin shut down | `FAILED` | none | none | as above | `UNAVAILABLE` |
| was ended because its connection was retired | `FAILED` | none | none | as above | `CONNECTION` / `CONNECTION_RETIRED` |
| returned an error, panicked, or broke the contract | `FAILED` | none | none | absent | the error's class and reason (`INTERNAL` for a panic) |
| returned `ExitLost` | `LOST` | none | none | **true** | none |

`failed` is present exactly for `FAILED`, with a `stage`: `START` when the start was not observed and `RUN` once it was (what the SDK makes of a close), or the stage the engine gave the failure (`STREAM` for a failure of the connection after the start; `RUN` for the failure the SDK makes of a broken `Exit`, whether or not the start was observed). The start is observed when your function calls `p.Started`, writes a byte or returns a status: `start_observed` says so, and `false` is not "the command never ran". `target` and `program` are reported only for a start that was observed (the program is the one your function reported with `p.Started`, or the one that was asked). A failure after the command was handed to your function is **data, never a status**, and carries the output read so far with its account.

**Timeout and cancel.** The timeout counts from the moment the SDK accepted the request, so a slow start takes from it. When it passes, the SDK ends the run with a kill: your function's `ctx` is cancelled, the SDK waits the kill wait (50 ms) and not the grace for it to return, and the call answers `TIMED_OUT` with the output read so far. A function that honors `ctx` is not abandoned; one that does not is given up on and keeps its place (above), and the call still returns. So the call returns at its timeout plus the kill wait. A caller that cancels its call, or whose deadline passes, ends the run too, gracefully (your function has the grace), and gets the status of the transport's own, which says nothing about whether the command ran. A plugin that shuts down ends the run with `FAILED` / `UNAVAILABLE`, and a retired connection with `FAILED` / `CONNECTION_RETIRED`: both are results.

**Stdin.** The stdin of the request is written to `p.Stdin` and then closed, so the function reads it and then `io.EOF`. The writer belongs to the command and ends with it: a function that never reads its stdin does not hold the run.

**`RunStream`.** The same run, with these events in this order and no other:

| Event | When |
|---|---|
| `RunStarted` | once, as soon as the start is observed, and before the first `Output`: the target and the program (the one your function reported with `p.Started`, or the one that was asked). A command that ended with a status having neither written nor reported gets it just before `RunEnded` |
| `Output` | as your function writes: up to 64 KiB of one stream with its `offset` (the position of its first byte in the one sequence of the run's output, as for a session) and the bytes untouched |
| `RunProgress` | at the SDK's pace: at most once a second, and only when output advanced since the last one; `elapsed_ms` and `bytes_out` (every byte the command has written so far) |
| `RunEnded` | last, once: the result without the payloads (`stdout` and `stderr` are empty, they were streamed), with the outcome, the exit, the accounts and the failure |

A stream **cannot keep a head and a tail**, because it cannot take back what it has sent: it delivers the bytes of each stream, in order, up to the cap of that stream, whatever policy the request named, and counts the rest as dropped (`TRUNCATED`; the offsets then have a gap where the delivery stopped). A run that failed or was lost before its start was observed sends `RunEnded` alone, with no `RunStarted`. A refusal ends the stream with a status and no event. Nothing follows `RunEnded`, and the stream then ends with status `OK`; the id of the run is free by then. A host that goes away (cancels, loses its connection) ends the run. A host that stops taking events stops your function (its `Write` blocks, or drops for a lossy target) and does not stop the run's timeout; once the command has ended the host has `Run.PostExitDrain` to take what is queued and `RunEnded`, and a stream whose host has not by then ends with the status `DEADLINE_EXCEEDED`, which carries no exec `ErrorInfo` (it is no refusal) and says nothing about the command.

**The host's client.** `plugin.V2().Run(ctx, req)` returns the plugin's `*execv2.RunResult` as it came: every outcome, `LOST` and `FAILED` included, is a result and never an error, and an absent `exit_code` is none, never 0. An error means the command was not taken or the answer never arrived: an `*exec.Error` for a refusal of the exec lane (nothing ran), the resource lane's own error for the connection and the catalog pin, or an `*exec.Error` of the transport's own class (`CONNECTION`, `TIMEOUT` or `CANCELLED`) when the call broke before the result, which says nothing about whether the command ran and is never read as a failure of it. A call whose `ctx` has no deadline is bounded by the request's `timeout_ms` plus the 30 seconds of the call timeout (an hour plus them when `timeout_ms` is 0); cancelling `ctx` cancels the run. `plugin.V2().RunStream(ctx, req)` returns an `exec.RunConn` once the first event, or the refusal, has arrived: a command that is silent makes it wait until the command ends (the first event of a run whose start is not observed is its end), so a caller that must act meanwhile calls it on a goroutine and cancels `ctx` to stop it. `Recv` returns the `RunEvent`s in order and `io.EOF` only after `RunEnded`; a stream that ends with status `OK` and no `RunEnded` is an error of the plugin, never an end; `Close` cancels the run. A caller with no deadline gets the same bound as `Run` for the whole stream. The client sends the request as it is: the plugin refuses what it does not accept, and a host that wants to know before it asks reads `Describe`.

**What a host never retries.** A refusal is retried only when its class and reason say a retry can help (`LIMIT`, `UNAVAILABLE` and `CONNECTION` may pass; `INVALID_REQUEST`, `FORBIDDEN` and `CONSENT_REQUIRED` stay refused until the request or the host's state changes). A `RunResult` of `FAILED` or `LOST`, one of `TIMED_OUT` or `CANCELLED` whose `remote_may_survive` is not `false`, and any status that is not a refusal after the command was handed over (a cancelled call, a deadline, a broken connection, a stream ended by the drain) authorize nothing: the command may have run, and may still be running.

## Capabilities: two levels

`Capabilities` has two levels, and a plugin author writes both.

1. **The kind's upper bound.** `TargetInfo.Capabilities` is what the *most capable* session of this kind can do. You declare it at registration; it is fixed for the life of the plugin process, and a consumer can read it before any session exists (what a picker offers, which request it never sends).
2. **The session's effective set.** Each session has its own capabilities, in its facts, and they only ever narrow. `Opened` carries the upper bound narrowed by the session's **mode**: each `Choice` of your `Options` may carry `Capabilities` (`exec.Choice.Capabilities`), the complete set a session that selects it is served with, and the SDK narrows the kind's bound by the choice that each option takes in the request's selection (for an option the selection omits, its default choice). `p.Capabilities` is a deep copy of that set, so changing it affects no other session. A target whose modes differ declares them on its choices (see the illustration below), and its function reports nothing for them; it narrows a copy and reports it with `p.Started(exec.SessionFacts{Capabilities: &caps})` only for what it learns at run time. A `SessionFacts` whose `Capabilities` is nil keeps the set `Opened` carried.

**A session may only narrow.** For every one of the 16 fields a choice's value is at most the kind's, and a `Started`'s is at most the value `Opened` carried, in the order below. When a claim is above its bound the SDK keeps the bound's value and logs your bug, naming the field: for a choice when it serves `Options` (the log names the option and the choice too), and for a `Started` when the engine takes it (it is counted as `violations{reason=capabilities_wide}`). `exectest.ProcRig` applies the same rules in your own tests: build it with `exectest.RigCapabilities(yourTarget.Info.Capabilities)` and `exectest.RigOptions(options...)`, the options your `Options` callback returns (`Proc.Capabilities` is then a deep copy of the set the session is opened with, which `rig.Opened()` returns; without `RigOptions` that is the declared set, and without `RigCapabilities` a copy of a default fake target's). A choice that widens a field, and a `Proc.Started` report that widens one, are clamped and reported with `t.Errorf` at your call; `rig.Effective()` returns the session's set (the opened one until a report narrows it). Every capability can be narrowed, so one target kind can serve modes that differ. A mode narrows what `Opened` says and what the SDK enforces on the session (resize, stdin EOF, signals, the fact modes, stall, and how its end is read: `Kill` and `ExitReport`); it is not a check on the request, which admission holds to the kind's bound: a start that asks for a terminal, an environment, a working directory or a size is refused only when the kind cannot give it. **A consumer that holds a session reads that session's facts and never `Describe`**; one that has only `Describe` reads each field as "the most it can do".

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

### Which stall behaviour to declare

`Stall` says what a reader that stops reading does to your function, and a kind declares it once. **Declare `StallPropagates` for a transport with a window and a reader of its own for each channel** (a local pty, ssh): a slow host then slows that one command, `Write` blocks at 256 KiB, and no byte is lost. **Declare `StallLossy` for Kubernetes, for a terminal**: client-go reads every channel in one loop, so a blocking `Write` would also stall the status channel and the executor's heartbeat, and a slow host would turn into a dead link. The queue is then the session's replay ring, the oldest bytes are dropped, and nothing is lost silently: each range is a `Dropped` frame, `Ended.dropped_bytes` and the `output` of the end (`TRUNCATED`) say so, and the bytes read plus the bytes dropped equal `bytes_out`, which equals `last_offset`, on the wire.

What it costs, measured on kind with a 64 MiB flood: the lane moves 170 to 185 MB/s; a consumer that only counts loses nothing, and one that hashes and checks every frame lost 1.4 to 1.9 MB in 2 of 3 runs (in 17 and 37 ranges); one that slept 20 ms for each frame kept 1.5 to 1.7 MB; one that stopped for 5 s after 8 MiB lost 57 MB (85 %) in one range. A consumer slower than about 100 MB/s loses bytes, and counts them. Two mitigations: the replay ring sized with `Start.replay_bytes` (up to `Session.MaxReplayBytes`) for a host that wants a deeper buffer, and `Run`, which is bounded and keeps the head and the tail with an exact account, for a big output that nobody is watching.

### Illustration: a Kubernetes Pod, exec versus attach

One Pod kind serves two modes that differ in what can be said about them. An **exec** session starts a new process: its end carries an exit code, and nothing of it can be reached again after a host restart. An **attach** session joins the container's own process: no status is relayed at all, and the process outlives the host. The kind declares the union as its upper bound (`ExitReportCode`, `SurvivalTargetProcess`); each mode is declared on its `Choice` as the kind narrowed to what is true of it (exec: `HostRestart` none; attach: `ExitReport` none). The session is therefore **opened** with its mode: the host reads it before it opens anything, and your function never reports it.

The code below is [`examples/exec-plugin/k8s_illustration_test.go`](../examples/exec-plugin/k8s_illustration_test.go), compiled and tested in CI. It is written against a tiny stand-in for the parts of client-go it uses (`streamOptions` is `remotecommand.StreamOptions` and `executor` is `remotecommand.Executor`), so the example needs no Kubernetes dependency; the stand-in is defined in the same file, and its tests use `exectest.CodeExitError` for the error of a command that exited with a status. The mode comes from the request's selection: the target's `Options` declares a `mode` option whose choices `exec` and `attach` carry their capabilities (see [the Options snippet](#options-needs-a-resource-provider), which builds the two sets from `podCapabilities`).

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
	// reported here. client-go has no callback for "the command is running"; what it does offer is its first Read of the
	// stdin it was handed, which comes once its streams exist, so that is the start this adapter reports, and a command
	// that stays silent still has its Started. A refusal of the API (a 403, a container that is not running) comes
	// before it, as a failure at START.
	attach := p.Request.Selection["mode"] == "attach" // the "mode" option of the target's Options
	run, err := executorFor(p, attach)
	if err != nil {
		return exec.Exit{}, err // Target.Classify maps "container not running", RBAC 403, "no such file"
	}
	stdin := exec.OnFirstRead(p.Stdin, func() { p.Started(exec.SessionFacts{Transport: "k8s-ws-v5"}) })
	opts := streamOptions{Stdin: stdin, Stdout: p.Stdout, Stderr: p.Stderr, Tty: p.Request.TTY,
		TerminalSizeQueue: sizeQueue{p.Resize}} // a terminal has no Stderr: it is nil, which client-go reads as none
	err = run.StreamWithContext(ctx, opts)
	if exit, ok := exec.ExitOfStreamError(err); ok {
		// nil: the stream ended normally, which is all the API says. It sends the same Success for a command that did not
		// exit 0 (after a kubelet or containerd restart), so this is Exited{0, TRANSPORT}, never the process's exit 0. A
		// CodeExitError is the code the API relayed, from the process; 137 can be a SIGKILL or a plain exit 137, so no
		// signal is inferred from the range.
		return exit, nil
	}
	if broke(err) {
		return exec.ExitLost(exec.LostNoStatus), nil // the stream broke: no status, and the process may still run
	}
	return exec.Exit{}, err // Target.Classify types what is left
}

// broke says that the stream to the API server ended while it was read, which is not a failure to open it.
func broke(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) || errors.Is(err, io.ErrUnexpectedEOF)
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

Four things the code is honest about. How the stream ended is read, never guessed: `exec.ExitOfStreamError` turns client-go's `nil` into `ExitTransport(0)`, "ended normally", and not into the process's exit 0 (the API server sends the same `Success` for a command that did not exit 0) and not into `ExitLost`; a `CodeExitError` into `ExitCode(code)` from the process, with no signal inferred from its range; and a stream that broke into `ExitLost`, the one end that claims the process may still run. The mode is declared where it is known, on the choice, so it is in `Opened` before the function runs: the host can show "attach: no exit status, survives a host restart" in its picker, an attach session never offers an exit status, and the function starts from the set `Opened` carried and reports nothing for it. The start is reported at the first read of the stdin the executor was handed (`exec.OnFirstRead`), the one observation of "the streams exist" that client-go has: before it a refusal of the API is a failure at `START`, after it a failure of the command is one at `RUN`, and a command that stays quiet still has its `Started`. There is no first-byte gate to write: the SDK guarantees that the host never receives output ahead of the session's `Started`, and it refuses, and logs, a `Started` that an author reports after the first output or twice.

## Absence is unknown

This is one rule for the whole contract: **a fact that a producer may not know is written so that "not known" is a value of its own, never the zero value of its type.** A bool fact is `optional bool`, a list whose emptiness is a fact (`signals`, `integration`) is wrapped in a message so that absent differs from empty, an enum has an `UNSPECIFIED` zero, a message fact is absent.

**What the SDK writes for you.** `Capabilities` is your declaration, so the SDK writes **every** field as present: an unset `bool` is a stated `false`, an empty `Signals` is a present empty set ("none"), `FactModeNone` is written `FACT_MODE_NONE`, `SurvivalNone` is `SURVIVAL_NONE`, an integration list without modes is a present empty set. It leaves a fact absent only when it does not know it: `SessionFacts.TTY` stays absent until you report it (`TTY *bool`: nil is "not said", a pointer to false is "no terminal"), a zero `Size`, empty `Program` fields and an empty `Term` or `Transport` mean "unknown", `Exit.Code` is a pointer, and `remote_may_survive` is written only when [the table above](#how-a-command-ends) says the SDK knows it. Do not state what you do not know: leave the field unset.

**What a consumer may assume.** Nothing from an absent value. An absent `Capabilities` message means nothing is promised; an absent bool or set inside a present one is not known, and `false`, an empty set and `NONE` are the producer's statement that the target cannot; an `UNSPECIFIED` enum is "unknown", never the nearest named value; a value a reader does not know (a newer peer) reads as `UNSPECIFIED`; an absent `remote_may_survive` reads as "may". Unknown is never turned into `false`, zero, none or exit 0. The rule is the "ABSENCE IS UNKNOWN" convention in the file header of [`proto/exec/v2/exec.proto`](../proto/exec/v2/exec.proto), and `TestAbsenceIsUnknown` and `TestBlocker1` to `TestBlocker5` in [`proto/exec/v2/contract_test.go`](../proto/exec/v2/contract_test.go) fail a field that breaks it.

## Requests: strict, and lists of names

A plugin **refuses** a request that carries a field it does not know (`UNSUPPORTED`), at any depth and inside the elements of a repeated message too, because a request field can change what a command may do. A host therefore sends a field an older plugin may not know only after `Describe` lists the feature that gates it. Frames and responses are lenient: an unknown field or frame kind is ignored and counted.

For that reason **a request carries no map**. The environment, the labels and the selection of a request (`Start.env`, `Start.labels`, `TargetRef.selection`, `RunRequest.env`, `RunRequest.labels`) are `repeated NameValue{name, value}`: wire-identical to a map entry, but an unknown field inside an entry can be seen and is refused. A name appears at most once in a list; a duplicate is `INVALID_REQUEST`, never "the last one wins". Your function sees them as the maps `Request.Selection`, `Request.Env` and `Request.Labels`, already validated. (`TargetFacts.selection`, a frame the plugin sends, stays a map.)

## A refusal is a status; everything after the hand-over is data

A **refusal** answers a request the SDK did not hand to your function, so nothing ran and a host may retry: a bad request, a stale connection, a limit, a reference that is not the caller's, an unsupported field. It is a gRPC status carrying `google.rpc.ErrorInfo` (domain `omniview.sdk.exec.v2`, reason = the error class, metadata `reason`, `program`, `selection_name`, `selection_value`, `retry_after_ms`) and, for a class other than `INTERNAL`, a bounded `LocalizedMessage` (`Error.Message`, at most 512 bytes). A plugin that does not serve the lane answers `Unimplemented`, which the client reports as `*exec.Error` with `ErrorClassUnsupported`.

**Once your function was called, the command may have run and may still be running, so how it ended is part of the result, never a status.** What you return from `ExecFunc` is therefore data:

- a session ends with an `End` (`exited`, `signaled`, `closed`, `failed{class, reason, facts, stage}` or `lost{cause}`), which reaches the host in `Ended` when a stream is attached and in the tombstone (`SessionInfo.ended_with`) when none is;
- a run ends with `RunResult` (and `RunEnded` for a stream), whose outcome is exited, signaled, timed out, cancelled, **failed** (`OUTCOME_FAILED`, with a `Failed` message) or lost. It keeps the output read so far, says whether the start was observed, and says whether the process may survive.

A **failure after the command was handed over** (your function returns `(exec.Exit{}, err)`, or a stream breaks) is `Ended{failed}` or `RunResult{OUTCOME_FAILED}`; the same classification (`*exec.Error{Class, Reason, Facts}` or `Target.Classify`) fills it. A host retries a refusal and never a failed or lost end: a failed end may have run, may still run. `start_observed` false means the SDK did not see the start, not that the command never ran.

## One end per session, one last frame per stream

On an `Open` stream, **every stream that can still receive frames ends with exactly one of two frames**, and nothing follows it (a stream whose connection breaks gets neither: the break is an implicit detach, and the session is read from its tombstone if its lease then expires; nor does a stream whose host broke the protocol, which ends with the status it earned, and the session with it):

- **`Ended`**: the end of the **session**, sent **at most once** in its life, to the stream attached when it ends. It holds the `End` above, counts and the output completeness. A session has exactly one **end**, and `Ended` is the frame that carries it to a stream: a session that ends while no stream is attached (a lease that expires, a retirement, a shutdown) has no `Ended` at all, and its end is the tombstone's, which `ListSessions` returns as `SessionInfo.ended_with`.
- **`Detached`**: the end of **this stream** while the session lives on. It carries a `reason`: `DETACH_REASON_REQUESTED` (the host sent `Detach`; the session lives for the lease), `DETACH_REASON_SUPERSEDED` (another stream attached and took the session over; the session continues attached to it) or no reason (the host closed its side of the stream, which is an implicit detach). **A stream that left is not an end**: there is no `Ended` for it and no `closed` value for it; a consumer keyed on `Ended` never marks a live session dead.

Your function runs once per session and returns once; the SDK turns that into the one end of the session, and into the `Ended` frame when a stream is attached to receive it.

## Stream health

A host that vanishes without closing its connection (a frozen process, a path that dropped its packets) leaves its streams open and silent for ever, and so does a plugin that stops answering. The transport tells a process that died, because the kernel closes the socket; it does not tell one that is alive and silent. Each side finds the other with gRPC's keepalive, and the SDK sets the two halves together:

- **The plugin's server** (`sdk.GRPCServerFactoryWithLogger`, which `Serve` hands to go-plugin) pings a connection that has been silent for 10 s (`Time`) and closes it when the ping is not answered within 5 s (`Timeout`). A host that vanished is found 15 s after its last word, and **every stream of its connection ends at once**. Each session whose stream ended is then **detached**, as [above](#a-session-outlives-its-stream) says of a stream that breaks: its function is not told to stop, it keeps its place, and its lease runs from that instant (5 minutes by default), so a host that comes back finds it with `ListSessions` and an attach. The server's enforcement policy (`MinTime` 5 s, `PermitWithoutStream`) lets a client ping every 5 s or less often, with a stream open or not, so the pings of the host below are never answered with a GOAWAY (`ENHANCE_YOUR_CALM`, `too_many_pings`); a peer that pings every second still is. A plugin that needs other times passes `grpc.KeepaliveParams` or `grpc.KeepaliveEnforcementPolicy` to the factory: its options come after the SDK's, and grpc keeps the last.
- **The host's client** sets the keepalive of `utils.HostKeepalive()` (ping after 30 s without anything received, with a stream open or not, give up when the ping is not answered within 10 s) in go-plugin's `ClientConfig.GRPCDialOptions`, next to the SDK's other dial options:

  ```go
  GRPCDialOptions: append(sdk.GRPCDialOptions(), sdk.HostKeepaliveDialOption()),
  ```

  A plugin that vanished is found within 40 s, and every stream of the connection fails at once with the gRPC status `Unavailable` (an `*exec.Error` of `ErrorClassConnection` from `Conn.Recv`), never with an `Ended` that did not arrive: the host knows only that the session may still run. **`sdk.GRPCDialOptions()` does not carry the keepalive**, and a host sets it only on connections to plugins whose SDK serves the policy above: a plugin built before it keeps grpc's default policy, which answers a client that pings every 30 s with a GOAWAY after the fourth ping and closes the connection.

What the keepalive finds is a peer that is gone. A host that is alive and does not read its streams is not gone: its transport answers the pings, a stream it does not read blocks the author as the session's stall policy says, and the way out is to cancel the stream (which detaches the session). Nothing here says anything of the leg between the plugin and the target, which is `Proc.Link`.

## Options needs a resource provider

`Options` lists what a user can choose for one resource before a command runs in it, so that a picker shows real states:

```go
result, err := plugin.V2().Options(ctx, exec.TargetRef{Reference: ref, Route: route}) // ctx carries the call's types.PluginContext and the catalog pin
// result.Options[0] = {Name: "container", Required: true, Choices: [{Value: "app", State: ChoiceStateReady, IsDefault: true, Kind: "container"}, ...]}
```

A host can reach your `Options` callback only if **all** of these hold:

1. **A resource provider is registered in the same plugin** (`sdk.RegisterResourcePlugin`), and your `Info.Provider` and `Info.Type` are that provider's id and a resource type it serves. `Options` is admitted like any resource utility call: the request must name an exact reference and a route with a connection, the connection must be the caller's, and the catalog pin the host sent must be current. A request that carries a selection is refused `INVALID_REQUEST`; a `Provider`/`Type` with no registered target is refused `UNSUPPORTED`.
2. **That provider owns connection lifecycle**: its `Impl` implements `resourcev2.ConnectionContextProvider` (a `WithConnection` method), which a `*resourcev2.Controller` has and which works only when the controller is configured with a `Lifecycle` (`LoadConnections`, `CreateClient`, `DestroyClient`, ...). The SDK binds that provider to the exec capability when both are registered, in either order, and your callback then runs inside the connection lease (cancelled when the connection is retired: the caller gets `ErrorClassConnection`/`ErrorReasonConnectionRetired`).

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
		{Value: "attach", State: exec.ChoiceStateReady, Capabilities: &attachMode, NoProgram: true}, // joins a process that runs: no program
	}}
	return []exec.Option{containers, mode}, nil
}
```

The imports are `resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"`, `"github.com/omniviewdev/plugin-sdk/pkg/types"` and the `exec` package.

A choice's `Capabilities` is the **mode** of the choice: the complete set a session that selects it is served with. Copy the kind's capabilities (`TargetInfo.Capabilities`, the upper bound of every session) and narrow the copy, each field only down in the order of [the table above](#capabilities-two-levels); `nil` means the choice does not narrow. The SDK serves it with the options and applies it at admission: `Opened` carries the kind's capabilities narrowed by the choice that each option takes in the request's selection (the default choice of an option the selection omits), and `p.Capabilities` starts from that set. A field above the kind's is the author's bug, not an error: the SDK serves the kind's value for it and names the option, the choice and the field in the plugin's log. A host reads a choice's capabilities with the options, so a picker can show "attach: no exit status, survives a host restart" before it opens anything.

**What a session's function sees of the options.** `Proc.Request.Selection` holds the choice of **every** option that the request resolved to: the one the host selected, or the default choice of an option it omitted (the choice the SDK used to narrow the capabilities), so the function never reads the resource again to find the container; an option with neither is absent. `Opened` still carries the selection as the host sent it.

**The callback is not run for every start.** The admission of an `Open` or a `Run` needs the options of the resource (to hold the selection to them and to narrow the capabilities), and a host has usually just called `Options` for the same resource, so the engine keeps the answer of the callback for **5 s** (a TTL of zero or less turns the cache off in the engine's own configuration) and the admission reuses it: `Options` and then an `Open` run the callback once. The unary `Options` call always runs the callback, never reads the cache, and refreshes it. An answer is kept for the whole identity of the request (the provider and type, the resource with its authority, scope, id **and incarnation**, the connection and the route), so two resources, two incarnations or two connections never share one; at most 64 are kept, the oldest going first; a callback that failed, panicked or broke its contract is never kept; and what a retired connection cached is dropped when a command or an `Options` call sees the retirement (the resource lane has no hook for it, so a connection started again under the same id is asked again after that). Concurrent starts of a cold request may each run the callback once. A stale choice only moves a refusal later, because a choice's state is advisory: the target refuses a container that stopped, and `Classify` types the refusal.

**A choice that runs no program: `Choice.NoProgram`.** A Kubernetes attach joins the container's own process and runs no program, so its choice declares `NoProgram: true`. A session that selects it has **no program** in `Opened` and in `Started` (absent, never the target's `DefaultArgv`, which only fills a request that names none), `Proc.Request.Argv` is empty, a program that the function reports with `p.Started` is dropped and logged, a request that names an argv of its own is refused `UNSUPPORTED` (an argv equal to `DefaultArgv` is what a request with none is given, and is dropped), and so is a `Run`, which is a program to run. A host reads the flag with the options and does not offer a command for the choice. Declare `DefaultArgv` on a target with such a choice, so that a request with no argv is admitted.

### Describe

`plugin.V2().Describe(ctx)` (the `Client` a host gets from `exec.PluginClient.V2()`) returns the wire `*execv2.DescribeResponse`: `contract_version`, the `features` the SDK serves and the `targets`, each with its upper-bound capabilities (every field present) and the limits in force, in registration order. It is static, needs no connection, and the client validates it before you see it. The limits are effective and never zero: a present `session` or `run` section carries non-zero bounds, the SDK never sends an all-zero one, a consumer refuses it as a malformed description, and an absent section means that service is not offered. A target with neither section is one that only offers `Options`. The SDK lists `FEATURE_SESSION`, `FEATURE_RUN`, `FEATURE_RUN_STREAM`, `FEATURE_ENV`, `FEATURE_CWD`, `FEATURE_STDIN_EOF` and `FEATURE_SESSION_MANAGEMENT`, in that order, for every plugin: the features are the plugin's, and what a target can do of each is in its capabilities and limits.

## Test your target

```go
func TestConformance(t *testing.T) { exectest.Conformance(t, myTarget()) }
```

`exectest.Conformance` serves your target through the real SDK over a real transport (a unix socket) with a fake connection registry standing in for the resource provider. It checks that registration agrees with `Describe` and claims no capability you did not declare, that `Describe` is static, that your `Options` answer is within bounds, and the wiring of the lease and the catalog pin; if your target has an `Options` callback, pass the id it knows: `exectest.Conformance(t, target, exectest.ConformanceResource("web-0"))`. It then runs the cases of the contract under `engine`: the numbered cases 1 to 28, the pair tests and the stress harness.

**None of the engine cases calls your `ExecFunc`.** Each serves a second plugin whose one target is a fake that has the `Info` you declared and a script of its own, so what you declared decides what each expects: a terminal only when you declare one, a signal only when `ExitReport` is `ExitReportCodeAndSignal`, a closed command's process gone only when `Kill` is `KillTerminates`, a lossy case only for a target that declared `StallLossy`, and the limits you declare. A target that declares neither sessions nor runs has nothing to run, and they skip. The cases run over the wire, as a host drives the plugin; a row marked *also in process* runs a second time against the engine in the test's own process, with no transport.

| Case | What it checks |
|---|---|
| 1 | `Opened` is first and its facts agree with `Describe`; its selection is the one the host sent, never the defaults the SDK resolved; a start that is refused sends no frame |
| 2 | input and resize frames reach the function in order, byte for byte however the input is split |
| 3 | output offsets are contiguous, frames are at most 64 KiB and the bytes are untouched |
| 4 | a slow consumer bounds memory: a blocking target holds its function and drops nothing (a target that declares `StallLossy` skips it) |
| 5 | a session whose host stops reading (its function held in a `Write`, or dropping) does not move another session's echo |
| 6 | resize: one that the session cannot take is acknowledged `IGNORED`, a malformed one ends the stream, a storm costs the function two sizes and the newest `seq` is acknowledged `APPLIED`, the size the session was opened with first |
| 7 | signals: a declared one reaches the function in order, an undeclared one ends the stream `UNSUPPORTED`; `Close` graceful and kill end the session `Closed` with `remote_may_survive` as `Kill` says, and a kill ends a function that ignores its context at the kill wait and not at the grace |
| 8 | stdin EOF: a `cat` that was sent `abc\n` and then `close_input` ends `Exited` by itself (the case waits five seconds for it and says so when it does not happen); an input frame after it, and the frame for a target that declares no `StdinEOF`, end the stream with a typed status |
| 9 | every kind of end is typed as your declaration says, with exact counts; a session has exactly one end and at most one `Ended`: the last frame of the stream attached when it ends, which the tombstone says alike, and, when no stream is attached, the tombstone's alone, read with `ListSessions` and `ReadOutput`, a lease that ran out included (it is read by its end alone: the case gives it a millisecond, which is over before a host can be counted on to have listed the session, so it is awaited as an event and nothing is read of the session before its tombstone), and a session left for a lease that the host asked for, which the case reads while the session lives with no stream and then ends with `EndSession` (also in process: a command has one end and nothing follows it) |
| 10 | detach and reattach: what the host had, the replay and the live stream are one uninterrupted transcript by offset; the tombstone of an ended session is readable |
| 11 | the lease ends a detached session when it runs out, `Closed{DETACH_TIMEOUT}` with no `Ended`, and an attach cancels it |
| 12 | limits: a session over the target's limit, an input frame one byte over the maximum, an environment, a working directory or a terminal the target does not honor, a size over the largest dimension, an id that is no id and the id of a live session are refused with a typed status and no frame, or end the stream with `LIMIT` / `INPUT_TOO_LARGE`, and the function never runs |
| 13 | errors: the class, reason and facts of an error round-trip, in a refusal, in an `Ended` and in a `RunResult`, and a canary placed in the local cause of an error and in a panic appears in no frame, status, header, trailer, result or event (each part runs for a target that declares what it drives) |
| 14 | a retired connection ends its sessions `Closed{CONNECTION_RETIRED}`, at every stage of their start (also in process: its commands) |
| 15 | `Run`: an empty argv and every bound of the request are refused and never clamped, the exit is as your `ExitReport` declares it, a timeout and a cancel end the run, the head and the tail that the cap and the policy keep come with an exact account of each stream, `valid_utf8` is of the bytes as returned, and the limit on runs in flight and the id of a run hold (skipped for a target that declares no `Run` limits) |
| 16 | a stream that ended with no status ends the session `Lost` and the run `OUTCOME_LOST`, never exit 0 (each part runs for a target that declares what it drives) |
| 17 | an exit reaches the host as its source says (`PROCESS`, `SIGNAL_FOLDED` or `TRANSPORT`), a code above 32 bits whole, a signal only for a kind that reports signals and never folded into a code; `Exited{0, TRANSPORT}` is a legitimate end, with its start observed and its survival not known; a break is `Lost` (in process: an `Exit` that the kind cannot give is a violation of the function, which the engine records) |
| 18 | completeness: a lossy target counts what it lost, a blocking one ends `COMPLETE` |
| 21 | an attach is atomic: `replay.bytes == end - start`, replay plus live is the transcript, and an attach at many moments of a flood loses no byte and repeats none |
| 22 | a detach ends the stream and not the session, `Detached` is its last frame, the lease is clamped to the maximum, and a superseded stream ends with `Detached` and no lease |
| 23 | a `Link` frame for each change of the link and for nothing else; a stalled link that gives up ends the session `Lost{LINK_DEAD}`, and a status returned while it is stalled is the transport's word |
| 25 | every stream ends with exactly one of `Ended` or `Detached`: many sessions at once each end with their own `Ended`, never another's and never twice, and for the streams of one session that attach, supersede and detach a stream that left is never told `Ended` and a session ends with at most one, none when it ends while no stream is attached |
| 26 | the capabilities of a session are its kind's narrowed by its mode and by what its function reports in `Started`, and a widening, a choice's or a `Started`'s, is clamped |
| 27 | a failure after the command was handed to the target is a result, `OUTCOME_FAILED` with the partial output and its completeness, while a refusal before it is a status |
| 28 | `remote_may_survive` per end, for each `Kill` that the kind allows: a kind that cannot promise that ending a session ends the process, `KillClosesStreamOnly`, is told present and true whoever closed the session (also in process) |
| P1 | `Run` equals `Open` without a terminal, collected to the end |
| P2 | `Open`, detach and attach from the offset equal one uninterrupted `Open`: the same bytes at the same offsets, with `replay.bytes == end - start` every time |
| P3 | the engine over gRPC equals the engine in process |
| P5 | ending twice (gracefully or with a kill, in either order, from two senders at once), after the exit and during the start give the same end (also in process) |
| stress | `exectest.Stress`, in process, on the SDK's own scripted targets, so it checks the engine's end state and not your declaration. Workers open sessions and runs, send input, resize, signal, detach, attach, take sessions over from one another, end them with `Close` and `EndSession`, cancel runs and read `ListSessions` and `ReadOutput` while it all goes on, and a `Close` of the engine lands at a random moment. At rest every command has ended once, with an end that its cause explains; the window of each session holds its whole output by offset, every byte read once or told lost, whichever streams read it; every stream ended with exactly one of `Ended` (the stream attached when the session ended) or `Detached`; the tombstone of each ended session is listed, with its end, until its time to live runs out, and holds its id until then; the store is within its bounds, and the goroutines are back where they were. It runs 2 s (1 s under `-short`, or as long as `OMNIVIEW_LIFECYCLE_HAMMER` says); a failure prints the seed and how to run it again |

The run cases write what a result must say from what their script wrote (the bytes of each stream and where the cap cuts them, the exit and the survival your declaration implies), never from what the plugin answered. A target that declares `StallLossy` skips the check of the default cap, which a lossy queue may cut before the capture sees it.

Five rows skip, each saying which step fills it: 19 and 24 (X4a: marks and terminal modes) and 20 (X4b: shell integration), because the SDK serves none of them ([What the SDK does not serve](#what-the-sdk-does-not-serve)), and the pair tests P4 (H1a-2: the host's local provider equals the engine with a pty-backed fake) and P6 (H1a-1: the legacy adapter equals the new lane on the same fake remote, for the bytes), which are steps of the host: they compare the host's own code with the engine. Every other row is complete. A suite that meets an SDK listing a feature in `Describe` whose cases are not written fails instead of skipping, so a feature cannot be served without being checked.

To test your own function, build a rig: `exectest.NewProcRig(t, request, exectest.RigCapabilities(target.Info.Capabilities), exectest.RigOptions(options...))` runs an `ExecFunc` with a `Proc` whose stdio you control, opened with the mode that your options declare for the request's selection (`rig.Opened()`), records the `Started` the host would receive (yours, or the one the SDK makes at the first output) and what you report through `Link`, drives and reads the signals, queries and facts of your function (`rig.Signal`, `rig.Ask`, `rig.Facts`), and holds your report to the rules described above; `exectest.NewFakeTarget` is a scriptable target. The [Kubernetes illustration's tests](../examples/exec-plugin/k8s_illustration_test.go) run an exec and an attach session this way, a quiet container and a container that writes among them, and show a session that widens, and a late and a second `Started`, being refused.

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
| sessions that outlive a stream | `FEATURE_SESSION_MANAGEMENT` | the replay ring, the lease and the tombstone behind `Open`, `ListSessions`, `ReadOutput`, `EndSession` |
| a stream left, session lives | `Detached{reason}` | the last frame `Conn.Recv` returns, and the `Detach` frame |

## Compatibility

The v2 lane is a new gRPC service on the existing `exec` capability: additive, no protocol bump. A plugin built before it answers `Unimplemented`, the client says `ErrorClassUnsupported`, and the host stays on the legacy lane; a v0.21.0 host talking to a plugin that registers both lanes sees the legacy lane byte for byte as before.

**How the contract is pinned.** Nothing of it is released yet, so its names and shapes can still change; once a release carries it, it only grows. Two checks hold that line. The reviewed listing of the contract, [`proto/exec/v2/exec.contract.golden`](../proto/exec/v2/exec.contract.golden), is compared by a test with the compiled descriptor of `exec.proto`, and its first line, `contract_version N`, must equal `exec.ContractVersion`. While the contract is unreleased you regenerate the listing after an intended change with `go test ./proto/exec/v2 -run TestContractMatchesItsReviewedGolden -update` and review the diff; once it is released the same failure means a breaking change. The CI job `proto-breaking` runs `buf breaking` against the latest stable `v*` tag, so it guards nothing until a release carries the file. The Go package exports what an author writes and tests (`Target`, `ExecFunc`, `Proc`, `exectest`) and what a host calls (`Client`, `Conn`, `RunConn`); the exports that should still be hidden are listed in [Before any tag that ships `exec.v2`](exec-before-any-tag.md).

**Before any tag.** What must be met before any tag ships `exec.v2` is the gate in [Before any tag that ships `exec.v2`](exec-before-any-tag.md): each item with its evidence, its fix and the check that closes it, and where the gate stands now. What is built, what remains and the branch rules are in [Exec v2: hand-over status](exec-status.md).

## What the SDK does not serve

The contract defines three parts that the SDK does not serve. `Describe` lists none of `FEATURE_MARKS`, `FEATURE_MODES` and `FEATURE_INTEGRATION`, and a host does not offer what a plugin does not list.

- **Marks.** No `Mark` frame is sent, and a `Start` that asks for the feature (`Start.features`) is refused `UNSUPPORTED`.
- **Terminal modes.** `replay.start_modes`, `replay.modes` and `Dropped.mode_reset` are absent, and `Ended.mode_reset` is absent for a session with a terminal (the host applies its own full reset) and present and empty for a session without one. A `Start` that asks for the feature is refused `UNSUPPORTED`. The SDK scans no output, so the ground-state rule of `Attach` is not applied either: a replay starts at the offset the host asked for when the ring holds it, and at the front of the ring (a chunk boundary) when it does not.
- **Shell integration.** A `Start` with `integration` is refused `UNSUPPORTED`, and a query for the integration tier is answered `INTEGRATION_TIER_UNSPECIFIED`: the SDK observes no tier.

A session has no idle timeout either: it is bounded by its command, a close and its lease, no limit declares an idle bound, and nothing produces `CLOSED_BY_IDLE_TIMEOUT`. `exectest.Conformance` skips the cases of these parts (19, 20 and 24) with the step that fills each ([Test your target](#test-your-target)).
