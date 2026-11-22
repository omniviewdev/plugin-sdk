package exec

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
)

// exectest.ProcRig narrows what a command reports exactly as the SDK does. The rule is not exported, so it is handed
// over through internal/exechook, which is the only place that knows it.
func init() { exechook.SetNarrowCapabilities(narrowCapabilities) }

// The orders of the enum fields of Capabilities, each from the lowest value to the highest, as the proto writes them at
// the field (proto/exec/v2/exec.proto, message Capabilities). They are lists and not numbers: KillBehavior and
// StallBehavior are numbered the other way round (KillTerminates is 1 and the highest, StallPropagates is 1 and the
// highest), and a comparison of their numbers would put a session above its kind exactly when it is below.
var (
	killOrder       = []KillBehavior{KillClosesStreamOnly, KillBestEffort, KillTerminates}
	factModeOrder   = []FactMode{FactModeNone, FactModePull, FactModePush}
	survivalOrder   = []Survival{SurvivalNone, SurvivalTargetProcess, SurvivalTargetSession}
	exitReportOrder = []ExitReport{ExitReportNone, ExitReportCode, ExitReportCodeAndSignal}
	stallOrder      = []StallBehavior{StallLossy, StallPropagates}
)

// narrowCapabilities applies to what an author declared for a session the rule that the proto writes at message
// Capabilities: a session may only narrow, and every field has an order. declared is the set that bounds the claim: the
// kind's upper bound (TargetInfo.Capabilities) for the Capabilities of a choice, which declares the mode of a session,
// and the capabilities the session was opened with for the set the author puts in SessionFacts.Capabilities. reported
// is the claim.
//
// The effective set has, for each field, the reported value when it is equal to the declared one or below it. A reported
// value above the declared one is replaced by the declared value and named in a violation, one per field, in the order of
// the fields: the author broke the contract, and the caller tells them (the SDK logs it, ProcRig fails the author's
// test). The orders, lowest first:
//
//	TTY, SeparateStderr, Resize, InitialSize,
//	StdinEOF, Env, Cwd, LinkHealth      false, true
//	Signals, Integration                inclusion: a subset of the declared set
//	Kill                                CLOSES_STREAM_ONLY, BEST_EFFORT, TERMINATES
//	CwdFact, ForegroundFact             NONE, PULL, PUSH
//	HostRestart                         NONE, TARGET_PROCESS, TARGET_SESSION
//	ExitReport                          NONE, CODE, CODE_AND_SIGNAL
//	Stall                               LOSSY, PROPAGATES
//
// An enum reported UNSPECIFIED (Kill, ExitReport and Stall: the author left it unsaid) keeps the declared value and is no
// violation; a number that is no value of the enum keeps the declared value and is named. A declared UNSPECIFIED is
// below every value, because a session cannot claim what its kind did not declare. A set keeps the reported values that
// the declared set holds, in the reported order, and loses the others, named together in one violation; a value that is
// listed twice counts once.
//
// The result shares no storage with the arguments, which the call does not change. An empty set in the result is nil.
func narrowCapabilities(declared, reported Capabilities) (effective Capabilities, violations []string) {
	var n narrowing
	effective.TTY = n.flag("TTY", declared.TTY, reported.TTY)
	effective.SeparateStderr = n.flag("SeparateStderr", declared.SeparateStderr, reported.SeparateStderr)
	effective.Resize = n.flag("Resize", declared.Resize, reported.Resize)
	effective.InitialSize = n.flag("InitialSize", declared.InitialSize, reported.InitialSize)
	effective.StdinEOF = n.flag("StdinEOF", declared.StdinEOF, reported.StdinEOF)
	effective.Signals = narrowSubset(&n, "Signals", declared.Signals, reported.Signals, enumLabel(signalPairs))
	effective.Kill = narrowEnum(&n, "Kill", killOrder, declared.Kill, reported.Kill)
	effective.Env = n.flag("Env", declared.Env, reported.Env)
	effective.Cwd = n.flag("Cwd", declared.Cwd, reported.Cwd)
	effective.CwdFact = narrowEnum(&n, "CwdFact", factModeOrder, declared.CwdFact, reported.CwdFact)
	effective.ForegroundFact = narrowEnum(&n, "ForegroundFact", factModeOrder, declared.ForegroundFact, reported.ForegroundFact)
	effective.HostRestart = narrowEnum(&n, "HostRestart", survivalOrder, declared.HostRestart, reported.HostRestart)
	effective.ExitReport = narrowEnum(&n, "ExitReport", exitReportOrder, declared.ExitReport, reported.ExitReport)
	effective.Stall = narrowEnum(&n, "Stall", stallOrder, declared.Stall, reported.Stall)
	effective.LinkHealth = n.flag("LinkHealth", declared.LinkHealth, reported.LinkHealth)
	effective.Integration = narrowSubset(&n, "Integration", declared.Integration, reported.Integration, enumLabel(integrationModePairs))
	return effective, n.violations
}

// narrowing collects the violations of one call of narrowCapabilities.
type narrowing struct{ violations []string }

func (n *narrowing) violate(format string, args ...any) {
	n.violations = append(n.violations, fmt.Sprintf(format, args...))
}

// flag narrows a bool field: false < true.
func (n *narrowing) flag(field string, declared, reported bool) bool {
	if reported && !declared {
		n.violate("%s: reported true above the declared false", field)
		return false
	}
	return reported
}

// narrowEnum narrows an enum field whose values are listed in order, lowest first.
func narrowEnum[T interface {
	~int
	fmt.Stringer
}](n *narrowing, field string, order []T, declared, reported T) T {
	rank := slices.Index(order, reported)
	if rank < 0 {
		// The report is not in the order. Zero is the enum's UNSPECIFIED, the author leaving the field unsaid: the
		// declared value stays. Any other number is no value of the enum.
		if reported != 0 {
			n.violate("%s: reported %d is not a defined value", field, int(reported))
		}
		return declared
	}
	// A declared value that is not in the order (UNSPECIFIED) has rank -1: every named value is above it.
	if rank > slices.Index(order, declared) {
		n.violate("%s: reported %s above the declared %s", field, reported, declared)
		return declared
	}
	return reported
}

// narrowSubset narrows a set field: inclusion.
func narrowSubset[T comparable](n *narrowing, field string, declared, reported []T, label func(T) string) []T {
	var effective, dropped []T
	for _, v := range reported {
		switch {
		case slices.Contains(effective, v), slices.Contains(dropped, v):
			// Listed twice: it counts once.
		case slices.Contains(declared, v):
			effective = append(effective, v)
		default:
			dropped = append(dropped, v)
		}
	}
	if len(dropped) > 0 {
		n.violate("%s: reported %s not among the declared %s", field, labelList(dropped, label), labelList(declared, label))
	}
	return effective
}

// enumLabel names a value of an author's enum in a message: its name, or its number when it is no value of the enum.
func enumLabel[G interface {
	~int
	fmt.Stringer
}, W ~int32](pairs []enumPair[G, W]) func(G) string {
	return func(g G) string {
		if !definedValue(pairs, g) {
			return strconv.Itoa(int(g))
		}
		return g.String()
	}
}

func labelList[T any](values []T, label func(T) string) string {
	if len(values) == 0 {
		return "(none)"
	}
	names := make([]string, len(values))
	for i, v := range values {
		names[i] = label(v)
	}
	return strings.Join(names, ", ")
}
