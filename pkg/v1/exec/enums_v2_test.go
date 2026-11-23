package exec

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// wireEnum is what every generated enum type has.
type wireEnum interface {
	~int32
	String() string
	Descriptor() protoreflect.EnumDescriptor
}

// checkEnum verifies one author enum against its wire enum:
//   - every named wire value (every value but the zero value, which is UNSPECIFIED: "the producer did not say") has
//     exactly one Go value, so that no wire value an author can mean is missing from the vocabulary of this build;
//   - no Go value maps to the UNSPECIFIED wire value, and every pair is written as its wire value;
//   - the Go zero value is written as goZero: UNSPECIFIED for an enum whose zero is "not said", and the wire's explicit
//     NONE for an enum whose zero is the author's statement of none (FactMode, Survival, IntegrationMode);
//   - a Go value that is not one of the enum is written as UNSPECIFIED, never as a named value;
//   - String names every value with the wire name without its prefix.
func checkEnum[G ~int, W wireEnum](t *testing.T, pairs []enumPair[G, W], toProto func(G) W, str func(G) string, prefix string, goZero W) {
	t.Helper()
	var zeroWire W
	var zeroGo G
	if got := toProto(zeroGo); got != goZero {
		t.Errorf("the Go zero value is written as %v, want %v", got, goZero)
	}
	zeroInTable := false
	for _, p := range pairs {
		if p.g == zeroGo {
			zeroInTable = true
		}
	}
	if zeroInTable != (goZero != zeroWire) {
		t.Errorf("the Go zero value is in the table: %v, but it is written as %v: an enum whose zero is a statement of none has a "+
			"pair for it, and one whose zero is \"not said\" has none", zeroInTable, goZero)
	}

	values := zeroWire.Descriptor().Values()
	named := map[W]bool{}
	for i := range values.Len() {
		w := W(values.Get(i).Number())
		if w == zeroWire {
			if values.Get(i).Number() != 0 {
				t.Errorf("value %s is not the zero value", values.Get(i).Name())
			}
			continue
		}
		named[w] = true
	}
	seenGo := map[G]bool{}
	seenWire := map[W]bool{}
	for _, p := range pairs {
		if p.w == zeroWire {
			t.Errorf("Go value %v maps to the UNSPECIFIED wire value", p.g)
		}
		if seenGo[p.g] || seenWire[p.w] {
			t.Errorf("pair (%v, %v) repeats a value", p.g, p.w)
		}
		seenGo[p.g], seenWire[p.w] = true, true
		if !named[p.w] {
			t.Errorf("wire %v is not a value of the enum", p.w)
		}
		if got := toProto(p.g); got != p.w {
			t.Errorf("toProto(%v) = %v, want %v", p.g, got, p.w)
		}
		if want := p.w.String()[len(prefix):]; str(p.g) != want {
			t.Errorf("String(%v) = %q, want %q", p.g, str(p.g), want)
		}
	}
	for w := range named {
		if !seenWire[w] {
			t.Errorf("wire value %v has no Go value in this build", w)
		}
	}
	for _, notAValue := range []int{-1, 9999} {
		if got := toProto(G(notAValue)); got != zeroWire {
			t.Errorf("Go value %d (not one of the enum) is written as wire %v, want UNSPECIFIED", notAValue, got)
		}
	}
}

func TestAuthorEnumsMapToTheWireValuesTheyNameAndNeverToAnotherOne(t *testing.T) {
	t.Run("Signal", func(t *testing.T) {
		checkEnum(t, signalPairs, Signal.toProto, Signal.String, "SIGNAL_", execv2.Signal_SIGNAL_UNSPECIFIED)
	})
	t.Run("FactMode", func(t *testing.T) {
		checkEnum(t, factModePairs, FactMode.toProto, FactMode.String, "FACT_MODE_", execv2.FactMode_FACT_MODE_NONE)
	})
	t.Run("KillBehavior", func(t *testing.T) {
		checkEnum(t, killBehaviorPairs, KillBehavior.toProto, KillBehavior.String, "KILL_BEHAVIOR_", execv2.KillBehavior_KILL_BEHAVIOR_UNSPECIFIED)
	})
	t.Run("Survival", func(t *testing.T) {
		checkEnum(t, survivalPairs, Survival.toProto, Survival.String, "SURVIVAL_", execv2.Survival_SURVIVAL_NONE)
	})
	t.Run("ExitReport", func(t *testing.T) {
		checkEnum(t, exitReportPairs, ExitReport.toProto, ExitReport.String, "EXIT_REPORT_", execv2.ExitReport_EXIT_REPORT_UNSPECIFIED)
	})
	t.Run("StallBehavior", func(t *testing.T) {
		checkEnum(t, stallBehaviorPairs, StallBehavior.toProto, StallBehavior.String, "STALL_BEHAVIOR_", execv2.StallBehavior_STALL_BEHAVIOR_UNSPECIFIED)
	})
	t.Run("IntegrationMode", func(t *testing.T) {
		checkEnum(t, integrationModePairs, IntegrationMode.toProto, IntegrationMode.String, "INTEGRATION_MODE_", execv2.IntegrationMode_INTEGRATION_MODE_NONE)
	})
	t.Run("ErrorClass", func(t *testing.T) {
		checkEnum(t, errorClassPairs, ErrorClass.toProto, ErrorClass.String, "ERROR_CLASS_", execv2.ErrorClass_ERROR_CLASS_UNSPECIFIED)
	})
	t.Run("ErrorReason", func(t *testing.T) {
		checkEnum(t, errorReasonPairs, ErrorReason.toProto, ErrorReason.String, "ERROR_REASON_", execv2.ErrorReason_ERROR_REASON_UNSPECIFIED)
	})
	t.Run("ChoiceState", func(t *testing.T) {
		checkEnum(t, choiceStatePairs, ChoiceState.toProto, ChoiceState.String, "CHOICE_STATE_", execv2.ChoiceState_CHOICE_STATE_UNSPECIFIED)
	})
	t.Run("ExitSource", func(t *testing.T) {
		checkEnum(t, exitSourcePairs, ExitSource.toProto, ExitSource.String, "EXIT_SOURCE_", execv2.ExitSource_EXIT_SOURCE_UNSPECIFIED)
	})
	t.Run("LostCause", func(t *testing.T) {
		checkEnum(t, lostCausePairs, LostCause.toProto, LostCause.String, "LOST_CAUSE_", execv2.LostCause_LOST_CAUSE_UNSPECIFIED)
	})
	t.Run("LinkState", func(t *testing.T) {
		checkEnum(t, linkStatePairs, LinkState.toProto, LinkState.String, "LINK_STATE_", execv2.LinkState_LINK_STATE_UNSPECIFIED)
	})
}

// The known choice states and the wire's own, written out here and not read from the table that maps them: a table that
// swapped two of them (a ready choice sent as stopped, and a wire STOPPED read as ready) passes every check that takes
// its expectations from that table, the structural ones of checkEnum included. Each state maps to its own wire value in
// both directions and is named as the wire names it.
func TestEveryKnownChoiceStateMapsToItsOwnWireValueInBothDirections(t *testing.T) {
	known := []struct {
		goValue ChoiceState
		wire    execv2.ChoiceState
		name    string
	}{
		{ChoiceStateReady, execv2.ChoiceState_CHOICE_STATE_READY, "READY"},
		{ChoiceStateStarting, execv2.ChoiceState_CHOICE_STATE_STARTING, "STARTING"},
		{ChoiceStateStopped, execv2.ChoiceState_CHOICE_STATE_STOPPED, "STOPPED"},
	}
	for _, tc := range known {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.goValue.toProto(); got != tc.wire {
				t.Errorf("Go %s is written as wire %v, want %v", tc.name, got, tc.wire)
			}
			if got := choiceStateFromProto(tc.wire); got != tc.goValue {
				t.Errorf("wire %v reads as the Go state numbered %d, want %s (%d)", tc.wire, int(got), tc.name, int(tc.goValue))
			}
			if got := tc.goValue.String(); got != tc.name {
				t.Errorf("Go %s is named %q", tc.name, got)
			}
		})
	}
}

// The one enum an author's code reads back from the wire is ChoiceState (the host decodes the answer of Options): a
// state this build does not know, and the one a producer leaves unsaid, read as unspecified and never as a named state.
func TestAnUnknownChoiceStateNeverReadsAsReadyOrStopped(t *testing.T) {
	for _, w := range []execv2.ChoiceState{execv2.ChoiceState_CHOICE_STATE_UNSPECIFIED, 9999, -1} {
		if got := choiceStateFromProto(w); got != ChoiceStateUnspecified {
			t.Errorf("wire choice state %v reads as %v, want unspecified", w, got)
		}
	}
}

// A declared none is written as the wire's NONE, and a reason keeps the name it had: the names of the constants are
// the author's contract and the wire's renaming (Reason to ErrorReason) does not reach them.
func TestTheNamesAnAuthorWritesAreTheWireNamesWithoutTheirPrefix(t *testing.T) {
	if FactModeNone.String() != "NONE" || SurvivalNone.String() != "NONE" || IntegrationOff.String() != "NONE" {
		t.Errorf("none is spelled %q, %q, %q", FactModeNone, SurvivalNone, IntegrationOff)
	}
	if ErrorReasonTargetDenied.String() != "TARGET_DENIED" || ErrorReasonInputBacklog.String() != "INPUT_BACKLOG" {
		t.Errorf("reasons are spelled %q, %q", ErrorReasonTargetDenied, ErrorReasonInputBacklog)
	}
	if ErrorClassInvalidRequest.String() != "INVALID_REQUEST" {
		t.Errorf("a class is spelled %q", ErrorClassInvalidRequest)
	}
}
