package exectest

import (
	"context"
	"fmt"
	"sync"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// patternByte is the byte that a target of the stress writes at an offset of its output, and that a worker sends at an offset of
// its input: a reader that knows the offset of each byte it reads verifies every one, with no copy of what was written.
func patternByte(offset uint64) byte { return byte(offset*131 + offset>>7) }

// patternBytes returns the n bytes of the pattern that start at offset.
func patternBytes(offset uint64, n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = patternByte(offset + uint64(i))
	}
	return data
}

// The scripted targets of the stress. Each one is a kind of command with a routing key of its own, (stressProvider, its name),
// so that each has its own limits and the stress meets them, and each is a fake target whose script is a few of the steps of
// faketarget.go. They are the well-behaved commands (an echo, floods, the ways a command ends) and the ones that break the
// contract (a function that panics, one that ignores its cancelled context, a goroutine that writes after its function
// returned).

const (
	// stressProvider is the resource provider of every target of the stress: the fake connection registry has one.
	stressProvider = "stress"
	// stressThing is the id of the one resource the commands run in.
	stressThing = "thing-1"
	// propagatingFlood is how much a flood that a stalled reader slows writes: more than the 256 KiB of the output queue, so that
	// a reader that does not keep up really holds the function in Write.
	propagatingFlood = 320 << 10
	// lossyFlood is how much a flood that a stalled reader does not slow writes, and lossyRing the replay ring of its sessions: the
	// flood is many times the ring, so that a reader that is late loses the oldest of it.
	lossyFlood = 320 << 10
	lossyRing  = 4 << 10
)

// stressBehaviour is one kind of command of the stress: what its target does, how its command ends when nobody asks it to, and
// what the checks may assume about its output.
type stressBehaviour struct {
	name    string // the resource type of its target: the second half of its routing key
	weight  int    // how often a worker picks it
	stall   exec.StallBehavior
	session exec.SessionLimits // the limits it is served under: a small one makes the stress meet them
	run     exec.RunLimits
	// input says that the function reads its input: a run of it must carry some, or it fails at once.
	input bool
	// alone says that the command ends by itself whatever its reader does. A flood that a stalled reader slows does not, and a
	// command that waits for its input or to be asked does not.
	alone bool
	// finishes holds the end of a command that ended by itself to what its function returns. It is nil for a command that only
	// ends when it is asked to, so that one that "finished" is a violation.
	finishes expectEnd
	// wrote is how many bytes the function writes when it runs to its return, and unknownOut says it is not a fixed number (an echo
	// writes what it is sent). A command that finished by itself wrote exactly that.
	wrote      int
	unknownOut bool
	// flood says that what the function writes is a flood of the pattern, wrote bytes of it: with a late reader and a lossy ring,
	// exactly what is left of it is known.
	flood bool
	// script is the steps of the target. A step that needs something the round holds (a gate, the records of the late writers)
	// takes it from the round's gates.
	script func(g *stressGates) []Step
}

// stressGates are what the round holds for the targets that misbehave: the gate of the functions that ignore their context, the
// gate of the goroutines that write after their function returned, and the records of those.
type stressGates struct {
	stuck *Gate // opened at the end of the round: the functions that ignored their context return
	late  *Gate // opened at the end of the round: the goroutines that outlived their function write
	lates lateRecords
}

func newStressGates() *stressGates { return &stressGates{stuck: NewGate(), late: NewGate()} }

// open lets every function and every goroutine of the targets go on.
func (g *stressGates) open() {
	g.stuck.Open()
	g.late.Open()
}

// lateRecords are the records of the goroutines that outlived their function: one for each command of the late writer.
type lateRecords struct {
	mu      sync.Mutex
	records []*LateWrites
}

func (l *lateRecords) add(record *LateWrites) {
	l.mu.Lock()
	l.records = append(l.records, record)
	l.mu.Unlock()
}

func (l *lateRecords) all() []*LateWrites {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*LateWrites(nil), l.records...)
}

// stressBehaviours is the table of the stress. The weights make a worker pick the quick ones most often and the ones that hold
// a limit, or a goroutine, until the end of the round least often.
var stressBehaviours = []*stressBehaviour{
	{
		name: "echo", weight: 16, stall: exec.StallPropagates, input: true, finishes: wantExit(0), unknownOut: true,
		script: func(*stressGates) []Step { return []Step{CopyInput()} },
	},
	{
		name: "flood", weight: 4, stall: exec.StallPropagates, finishes: wantExit(0), flood: true, wrote: propagatingFlood,
		script: func(*stressGates) []Step { return []Step{patternFlood(propagatingFlood), EndWith(exec.ExitCode(0))} },
	},
	{
		name: "lossy", weight: 5, stall: exec.StallLossy, alone: true, finishes: wantExit(0), flood: true, wrote: lossyFlood,
		session: exec.SessionLimits{MaxSessions: 8, ReplayBytes: lossyRing},
		script:  func(*stressGates) []Step { return []Step{patternFlood(lossyFlood), EndWith(exec.ExitCode(0))} },
	},
	{
		name: "exit0", weight: 10, stall: exec.StallPropagates, alone: true, finishes: wantExit(0),
		script: func(*stressGates) []Step { return []Step{EndWith(exec.ExitCode(0))} },
	},
	{
		name: "exitn", weight: 8, stall: exec.StallPropagates, alone: true, finishes: wantExit(7), wrote: 40,
		script: func(*stressGates) []Step {
			return []Step{WriteChunks(patternBytes(0, 40), 7), EndWith(exec.ExitCode(7))}
		},
	},
	{
		name: "signal", weight: 6, stall: exec.StallPropagates, alone: true, finishes: wantSignal("SIGKILL", 9),
		script: func(*stressGates) []Step { return []Step{EndWith(exec.ExitSignal("SIGKILL", 9))} },
	},
	{
		name: "lost", weight: 6, stall: exec.StallPropagates, alone: true, finishes: wantLost(execv2.LostCause_LOST_CAUSE_LINK_DEAD), wrote: 8,
		script: func(*stressGates) []Step {
			return []Step{WriteChunks(patternBytes(0, 8), 0), EndWith(exec.ExitLost(exec.LostLinkDead))}
		},
	},
	{
		name: "failure", weight: 6, stall: exec.StallPropagates, alone: true,
		finishes: wantFailed(execv2.ErrorClass_ERROR_CLASS_NOT_FOUND, execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE),
		script: func(*stressGates) []Step {
			return []Step{Fail(&exec.Error{Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonNoSuchExecutable, Facts: exec.ErrorFacts{Program: "stress"}})}
		},
	},
	{
		name: "blocker", weight: 14, stall: exec.StallPropagates,
		session: exec.SessionLimits{MaxSessions: 6}, run: exec.RunLimits{MaxConcurrent: 4},
		script: func(*stressGates) []Step { return []Step{WriteChunks(patternBytes(0, 8), 0), Block()} },
	},
	{
		name: "panics", weight: 5, stall: exec.StallPropagates, alone: true, wrote: 24,
		finishes: wantFailed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED),
		script: func(*stressGates) []Step {
			return []Step{WriteChunks(patternBytes(0, 24), 5), Panic("stress: a function that panics")}
		},
	},
	{
		name: "stuck", weight: 3, stall: exec.StallPropagates,
		session: exec.SessionLimits{MaxSessions: 4}, run: exec.RunLimits{MaxConcurrent: 4},
		script: func(g *stressGates) []Step { return []Step{IgnoreCancel(g.stuck), EndWith(exec.ExitCode(0))} },
	},
	{
		name: "late", weight: 3, stall: exec.StallPropagates, input: true, finishes: wantExit(0), unknownOut: true,
		session: exec.SessionLimits{MaxSessions: 4}, run: exec.RunLimits{MaxConcurrent: 4},
		script: func(g *stressGates) []Step { return []Step{lateWriter(g), CopyInput()} },
	},
}

// stressBudget is how many commands of a behaviour a round starts at most, for the behaviours whose commands hold a goroutine, or a
// function, until the end of the round: a round that started them without a bound would end with as many goroutines as it had
// operations.
var stressBudget = map[string]int{"stuck": 48, "late": 48}

// target builds the fake target of the behaviour: the defaults of a fake target, its routing key, its stall policy and its limits.
func (b *stressBehaviour) target(g *stressGates) exec.Target {
	info := NewFakeTarget().Info
	info.Capabilities.Stall = b.stall
	if !b.session.IsZero() {
		info.Session = b.session
	}
	if !b.run.IsZero() {
		info.Run = b.run
	}
	return NewFakeTarget(FakeInfo(info), FakeRouting(stressProvider, b.name), FakeScript(b.script(g)...))
}

// patternFlood writes total bytes of the pattern in writes of changing sizes, and stops when its command is cancelled or a write
// fails, which is how a function that honors its context ends.
func patternFlood(total int) Step {
	return stepFunc(func(ctx context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		buf := make([]byte, 40000)
		for offset, size := 0, 1; offset < total; size = size%37000 + 997 {
			if err := ctx.Err(); err != nil {
				return true, exec.Exit{}, err
			}
			n := min(size, total-offset)
			for i := range n {
				buf[i] = patternByte(uint64(offset + i))
			}
			if _, err := p.Stdout.Write(buf[:n]); err != nil {
				return true, exec.Exit{}, err
			}
			offset += n
		}
		return false, exec.Exit{}, nil
	})
}

// lateWriter is OutliveWith for a target that many commands run at once: each command has a record of its own, which the round
// keeps, because the record of OutliveWith is written by one goroutine and closed by it.
func lateWriter(g *stressGates) Step {
	return stepFunc(func(ctx context.Context, p *exec.Proc) (bool, exec.Exit, error) {
		record := NewLateWrites()
		g.lates.add(record)
		return OutliveWith(g.late, "late", record).run(ctx, p)
	})
}

// check holds the end of a command of the behaviour to what its function does, on top of what must be true of the end of any
// command (checkResult). run says the command was a run, and mode how its reader read.
func (b *stressBehaviour) check(res exechook.Result, a outputAudit, run bool, mode readMode) []string {
	arm, ok := endArm(res.End)
	if !ok {
		return nil // checkResult says so
	}
	var found []string
	finished := res.Cause == "finished"
	switch {
	case finished && b.finishes == nil:
		found = append(found, fmt.Sprintf("a command of %s finished by itself, and its function returns only when its context is cancelled", b.name))
	case finished:
		if complaint := b.finishes(res.End); complaint != "" {
			found = append(found, complaint)
		}
	case arm == "closed":
		// Every target of the stress declares that ending a session terminates its process.
		if survive := res.End.RemoteMaySurvive; survive == nil || *survive {
			found = append(found, fmt.Sprintf("a Closed end of a target whose Kill is TERMINATES says remote_may_survive is %s, want false", survivalText(survive)))
		}
	}
	if !finished {
		return found
	}
	if !b.unknownOut && res.BytesOut != uint64(b.wrote) {
		found = append(found, fmt.Sprintf("the command wrote %d bytes and its function writes %d", res.BytesOut, b.wrote))
	}
	if b.stall == exec.StallPropagates {
		// A target whose stalled reader slows the process loses nothing: the output queue blocks the function's Write.
		if res.DroppedBytes != 0 {
			found = append(found, fmt.Sprintf("a command whose stalled reader slows the process finished by itself with %d bytes dropped", res.DroppedBytes))
		}
		if (arm == "exited" || arm == "signaled") && res.Output != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE {
			found = append(found, fmt.Sprintf("the output is %s, want COMPLETE: nothing was dropped and the status is the process's own", res.Output))
		}
	}
	if b.flood && b.stall == exec.StallLossy && !run && mode == readLate {
		// Nobody read until the command had ended, so the replay ring holds the newest lossyRing bytes and the rest, the oldest, was dropped.
		if a.read != lossyRing || a.dropped != uint64(b.wrote-lossyRing) {
			found = append(found, fmt.Sprintf("a lossy flood of %d bytes read after its end left %d bytes read and %d dropped, want the newest %d read and the other %d dropped",
				b.wrote, a.read, a.dropped, lossyRing, b.wrote-lossyRing))
		}
	}
	return found
}

// ---------------------------------------------------------------------------------------------------------------
// the end a function's return becomes, for a command that finished by itself
// ---------------------------------------------------------------------------------------------------------------

// expectEnd holds the End of a command that ended by itself to what its function returned. It returns what is wrong with it, or
// "" when it is right.
type expectEnd func(end *execv2.End) string

// survivalText says what remote_may_survive holds: "absent", "true" or "false".
func survivalText(survive *bool) string {
	switch {
	case survive == nil:
		return "absent"
	case *survive:
		return "true"
	}
	return "false"
}

// armText names the arm of an end, or says it has none.
func armText(end *execv2.End) string {
	if arm, ok := endArm(end); ok {
		return arm
	}
	return "no arm"
}

// wantExit expects Exited with the code, as the process's own status: remote_may_survive is false.
func wantExit(code int64) expectEnd {
	return func(end *execv2.End) string {
		exited := end.GetExited()
		switch {
		case exited == nil:
			return fmt.Sprintf("the end is %s, want Exited with code %d", armText(end), code)
		case exited.GetExitCode() != code || exited.GetSource() != execv2.ExitSource_EXIT_SOURCE_PROCESS:
			return fmt.Sprintf("the end is Exited with code %d by %s, want code %d by EXIT_SOURCE_PROCESS", exited.GetExitCode(), exited.GetSource(), code)
		case end.RemoteMaySurvive == nil || *end.RemoteMaySurvive:
			return fmt.Sprintf("an exit by the process's own status says remote_may_survive is %s, want false", survivalText(end.RemoteMaySurvive))
		}
		return ""
	}
}

// wantSignal expects Signaled with the signal, as the process's own status: remote_may_survive is false.
func wantSignal(name string, number int32) expectEnd {
	return func(end *execv2.End) string {
		signaled := end.GetSignaled()
		switch {
		case signaled == nil:
			return fmt.Sprintf("the end is %s, want Signaled by %s", armText(end), name)
		case signaled.GetSignal() != name || signaled.GetSignalNumber() != number || signaled.GetSource() != execv2.ExitSource_EXIT_SOURCE_PROCESS:
			return fmt.Sprintf("the end is Signaled by %q (%d) from %s, want %q (%d) from EXIT_SOURCE_PROCESS",
				signaled.GetSignal(), signaled.GetSignalNumber(), signaled.GetSource(), name, number)
		case end.RemoteMaySurvive == nil || *end.RemoteMaySurvive:
			return fmt.Sprintf("a signal from the process's own status says remote_may_survive is %s, want false", survivalText(end.RemoteMaySurvive))
		}
		return ""
	}
}

// wantLost expects Lost for the cause: the process may still run, so remote_may_survive is true.
func wantLost(cause execv2.LostCause) expectEnd {
	return func(end *execv2.End) string {
		lost := end.GetLost()
		switch {
		case lost == nil:
			return fmt.Sprintf("the end is %s, want Lost", armText(end))
		case lost.GetCause() != cause:
			return fmt.Sprintf("the end is Lost for %s, want %s", lost.GetCause(), cause)
		case end.RemoteMaySurvive == nil || !*end.RemoteMaySurvive:
			return fmt.Sprintf("a lost stream says remote_may_survive is %s, want true", survivalText(end.RemoteMaySurvive))
		}
		return ""
	}
}

// wantFailed expects Failed with the class and reason: the SDK does not know whether the command still runs, so
// remote_may_survive is absent.
func wantFailed(class execv2.ErrorClass, reason execv2.ErrorReason) expectEnd {
	return func(end *execv2.End) string {
		failed := end.GetFailed()
		switch {
		case failed == nil:
			return fmt.Sprintf("the end is %s, want Failed as %s", armText(end), class)
		case failed.GetClass() != class || failed.GetReason() != reason:
			return fmt.Sprintf("the end is Failed as %s/%s, want %s/%s", failed.GetClass(), failed.GetReason(), class, reason)
		case end.RemoteMaySurvive != nil:
			return fmt.Sprintf("a failure says remote_may_survive is %s, want it absent", survivalText(end.RemoteMaySurvive))
		}
		return ""
	}
}
