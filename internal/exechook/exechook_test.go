package exechook_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
)

type set struct{ n int }

type otherSet struct{ n int }

// panics reports what call panicked with, or nil.
func panics(call func()) (recovered any) {
	defer func() { recovered = recover() }()
	call()
	return nil
}

// A registered rule is applied as it is, for its own type. A type that no rule was registered for (which is also what a
// program that does not link pkg/v1/exec has) is refused with a panic that says so, and never answered with an
// effective set that nobody narrowed. The test registers the same rule every time it runs, so that it can be repeated.
func TestTheHookAppliesTheRegisteredRuleAndRefusesAnyOtherType(t *testing.T) {
	exechook.SetNarrowCapabilities(func(declared, reported set) (set, []string) {
		return set{min(declared.n, reported.n)}, []string{"narrowed"}
	})
	effective, violations := exechook.NarrowCapabilities(set{1}, set{2})
	if effective != (set{1}) || !slices.Equal(violations, []string{"narrowed"}) {
		t.Errorf("the registered rule gave %+v and %q, want {1} and [narrowed]", effective, violations)
	}

	got := panics(func() { exechook.NarrowCapabilities(otherSet{1}, otherSet{2}) })
	if message, _ := got.(string); !strings.Contains(message, "no capabilities narrowing rule is registered") {
		t.Errorf("a type with no rule was answered, or refused with the wrong panic: %v", got)
	}
}

// The rule that opens a session is applied as it is, for its own types; a combination no rule was registered for is
// refused with a panic that says so.
func TestTheHookAppliesTheRegisteredOpenedCapabilitiesRuleAndRefusesAnyOtherTypes(t *testing.T) {
	exechook.SetOpenedCapabilities(func(declared set, options []string, selection map[string]string) set {
		return set{declared.n + len(options)*10 + len(selection)*100}
	})
	got := exechook.OpenedCapabilities(set{1}, []string{"a", "b"}, map[string]string{"x": "y"})
	if got != (set{121}) {
		t.Errorf("the registered rule gave %+v, want {121}", got)
	}

	for name, call := range map[string]func(){
		"another capabilities type": func() { exechook.OpenedCapabilities(otherSet{1}, []string{"a"}, nil) },
		"another options type":      func() { exechook.OpenedCapabilities(set{1}, []int{1}, nil) },
	} {
		got := panics(call)
		if message, _ := got.(string); !strings.Contains(message, "no opened capabilities rule is registered") {
			t.Errorf("%s: answered, or refused with the wrong panic: %v", name, got)
		}
	}
}

// facts is the report of a command, for the gate's types.
type facts struct{ program string }

// The start gate of a session is made by the registered constructor for its own types, and what it is given is what it
// receives: the hook adds nothing. A combination of types no constructor was registered for is refused with a panic
// that says so.
func TestTheHookMakesTheRegisteredStartGateAndRefusesAnyOtherTypes(t *testing.T) {
	exechook.SetStartGate(func(request string, opened set) exechook.StartGate[facts, set] {
		return exechook.StartGate[facts, set]{
			Report: func(f facts, send func(facts)) []string {
				send(facts{request + "/" + f.program})
				return []string{"reported"}
			},
			Output:    func(send func(facts)) { send(facts{"synthesized from " + request}) },
			Effective: func() set { return opened },
		}
	})
	gate := exechook.NewStartGate[string, facts]("argv", set{5})
	var sent []facts
	violations := gate.Report(facts{"sh"}, func(f facts) { sent = append(sent, f) })
	gate.Output(func(f facts) { sent = append(sent, f) })
	if !slices.Equal(violations, []string{"reported"}) || !slices.Equal(sent, []facts{{"argv/sh"}, {"synthesized from argv"}}) {
		t.Errorf("the gate answered %q and sent %+v", violations, sent)
	}
	if got := gate.Effective(); got != (set{5}) {
		t.Errorf("effective = %+v, want {5}", got)
	}

	got := panics(func() { exechook.NewStartGate[int, facts](1, otherSet{1}) })
	if message, _ := got.(string); !strings.Contains(message, "no start gate is registered") {
		t.Errorf("types with no gate were answered, or refused with the wrong panic: %v", got)
	}
}
