// Package exechook hands pkg/v1/exec/exectest the few unexported rules of pkg/v1/exec that its test rig must apply
// exactly as the SDK does, without exporting them from the author's package. pkg/v1/exec registers each rule when it
// is initialized; exectest, which imports pkg/v1/exec, calls it here. Nothing else uses this package.
package exechook

// narrowCapabilities holds the registered rule, a func(declared, reported C) (effective C, violations []string).
var narrowCapabilities any

// SetNarrowCapabilities registers the rule that narrows the capabilities a session reports to the ones its kind
// declared (pkg/v1/exec's narrowCapabilities), for the capabilities type C.
func SetNarrowCapabilities[C any](rule func(declared, reported C) (effective C, violations []string)) {
	narrowCapabilities = rule
}

// NarrowCapabilities applies the registered rule to a session's report. It panics when no rule is registered for C,
// which means pkg/v1/exec is not linked into the program or registered another type: a bug of this module, which no
// author can cause.
func NarrowCapabilities[C any](declared, reported C) (effective C, violations []string) {
	rule, ok := narrowCapabilities.(func(C, C) (C, []string))
	if !ok {
		panic("exechook: no capabilities narrowing rule is registered for this type: pkg/v1/exec is not linked")
	}
	return rule(declared, reported)
}

// openedCapabilities holds the registered rule, a func(declared C, options []O, selection map[string]string) C.
var openedCapabilities any

// SetOpenedCapabilities registers the rule that gives the capabilities a session is opened with, the ones Opened
// carries (pkg/v1/exec's openedCapabilities): the kind's declared capabilities C narrowed by the choice that each of the
// options O takes in the request's selection.
func SetOpenedCapabilities[C, O any](rule func(declared C, options []O, selection map[string]string) C) {
	openedCapabilities = rule
}

// OpenedCapabilities applies the registered rule. It panics when no rule is registered for C and O, which means
// pkg/v1/exec is not linked into the program or registered other types: a bug of this module, which no author can cause.
func OpenedCapabilities[C, O any](declared C, options []O, selection map[string]string) C {
	rule, ok := openedCapabilities.(func(C, []O, map[string]string) C)
	if !ok {
		panic("exechook: no opened capabilities rule is registered for these types: pkg/v1/exec is not linked")
	}
	return rule(declared, options, selection)
}

// StartGate is the rule of pkg/v1/exec for the Started of one session, as a set of functions: at most one Started, and
// always before the first output (proto/exec/v2/exec.proto, message Started). F is what an author reports with
// Proc.Started and C the capabilities of a session.
type StartGate[F, C any] struct {
	// Report takes what the author reported with Proc.Started, once and before the first output, and hands what the host
	// receives to send before it returns; a report that is late or a second one is refused and changes nothing. It
	// returns what the report broke, named, for the caller to tell the author.
	Report func(facts F, send func(F)) (violations []string)
	// Output is called before the first output of the session is delivered. When nothing was reported it hands the
	// Started the SDK makes from the facts of Opened to send, which is sent before the output; any later call does
	// nothing.
	Output func(send func(F))
	// Effective returns the capabilities of the session now: what Opened carried, narrowed by what was reported.
	Effective func() C
}

// startGate holds the registered constructor, a func(request R, opened C) StartGate[F, C].
var startGate any

// SetStartGate registers the constructor of the start gate (pkg/v1/exec's newStartGate): it is given the request R of a
// command and the capabilities C its session is opened with, and returns the gate of that session.
func SetStartGate[R, F, C any](newGate func(request R, opened C) StartGate[F, C]) {
	startGate = newGate
}

// NewStartGate makes the start gate of one session with the registered constructor. It panics when none is registered
// for R, F and C, which means pkg/v1/exec is not linked into the program or registered other types: a bug of this
// module, which no author can cause.
func NewStartGate[R, F, C any](request R, opened C) StartGate[F, C] {
	newGate, ok := startGate.(func(R, C) StartGate[F, C])
	if !ok {
		panic("exechook: no start gate is registered for these types: pkg/v1/exec is not linked")
	}
	return newGate(request, opened)
}
