package exec

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
)

// enumValue is what the generic checks need of an enum of Capabilities: a number, which can be left at its zero or set
// to a number that is no value, and a name.
type enumValue interface {
	~int
	fmt.Stringer
}

// widestCapabilities is a kind that can do everything: every field at the top of its order.
func widestCapabilities() Capabilities {
	return Capabilities{
		TTY:            true,
		SeparateStderr: true,
		Resize:         true,
		InitialSize:    true,
		StdinEOF:       true,
		Signals:        []Signal{SignalInt, SignalQuit, SignalTerm, SignalKill, SignalHup, SignalUsr1, SignalUsr2},
		Kill:           KillTerminates,
		Env:            true,
		Cwd:            true,
		CwdFact:        FactModePush,
		ForegroundFact: FactModePush,
		HostRestart:    SurvivalTargetSession,
		ExitReport:     ExitReportCodeAndSignal,
		Stall:          StallPropagates,
		LinkHealth:     true,
		Integration:    []IntegrationMode{IntegrationArgvWrapper},
	}
}

// narrowestCapabilities is a kind that can do nothing: every field at the bottom of its order. The three enums whose
// zero value means "unsaid" are at their lowest named value.
func narrowestCapabilities() Capabilities {
	return Capabilities{Kill: KillClosesStreamOnly, ExitReport: ExitReportNone, Stall: StallLossy}
}

// capabilitiesDiff names the fields in which two sets differ. It goes through reflection, so that a field added to
// Capabilities is compared without this helper being touched. A nil and an empty list are the same set.
func capabilitiesDiff(a, b Capabilities) []string {
	var diff []string
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	for i := range va.NumField() {
		x, y := va.Field(i), vb.Field(i)
		var same bool
		if x.Kind() == reflect.Slice {
			same = x.Len() == y.Len() && (x.Len() == 0 || reflect.DeepEqual(x.Interface(), y.Interface()))
		} else {
			same = x.Interface() == y.Interface()
		}
		if !same {
			diff = append(diff, va.Type().Field(i).Name)
		}
	}
	return diff
}

// assertNarrowed narrows reported against declared and requires the expected effective set and violations, and that
// the declared set and the report came out of the call as they went in.
func assertNarrowed(t *testing.T, declared, reported, want Capabilities, wantViolations []string) {
	t.Helper()
	declaredBefore, reportedBefore := cloneCapabilities(declared), cloneCapabilities(reported)
	got, violations := narrowCapabilities(declared, reported)
	if diff := capabilitiesDiff(got, want); len(diff) > 0 {
		t.Errorf("the effective set differs from the expected one in %v\n got  %+v\n want %+v", diff, got, want)
	}
	if !slices.Equal(violations, wantViolations) {
		t.Errorf("violations = %q, want %q", violations, wantViolations)
	}
	if diff := capabilitiesDiff(declared, declaredBefore); len(diff) > 0 {
		t.Errorf("the call changed the declared set in %v", diff)
	}
	if diff := capabilitiesDiff(reported, reportedBefore); len(diff) > 0 {
		t.Errorf("the call changed the report in %v", diff)
	}
}

// narrowField is one field of Capabilities as the narrowing tests see it: its names, the order the proto writes at it
// and the check of every way a session can report it.
type narrowField struct {
	name  string // the Go field
	proto string // the proto field
	order string // the order, as the proto writes it at the field
	check func(t *testing.T)
}

// narrowFlag is a bool field: false < true. A session may turn true into false, never false into true.
func narrowFlag(name, proto string, field func(*Capabilities) *bool) narrowField {
	return narrowField{name, proto, "false < true", func(t *testing.T) {
		for _, declared := range []bool{false, true} {
			for _, reported := range []bool{false, true} {
				t.Run(fmt.Sprintf("declared %t, reported %t", declared, reported), func(t *testing.T) {
					d, r, want := widestCapabilities(), widestCapabilities(), widestCapabilities()
					*field(&d), *field(&r), *field(&want) = declared, reported, declared && reported
					var violations []string
					if reported && !declared {
						violations = []string{name + ": reported true above the declared false"}
					}
					assertNarrowed(t, d, r, want, violations)
				})
			}
		}
	}}
}

// narrowOrdered is an enum field whose values are listed from the lowest to the highest, as the proto writes them.
func narrowOrdered[T enumValue](name, proto string, order []T, field func(*Capabilities) *T) narrowField {
	names := make([]string, len(order))
	for i, v := range order {
		names[i] = v.String()
	}
	return narrowField{name, proto, strings.Join(names, " < "), func(t *testing.T) { checkOrdered(t, name, order, field) }}
}

func checkOrdered[T enumValue](t *testing.T, name string, order []T, field func(*Capabilities) *T) {
	t.Helper()
	var zero T
	// For Kill, ExitReport and Stall the zero value is the author leaving the field unsaid. For the others it is the
	// lowest value, NONE, which is a statement.
	unsaid := !slices.Contains(order, zero)
	// one reports a value in a set that is otherwise the widest, so that any other field that moves shows in the diff.
	one := func(declared, reported, effective T, violation string) func(*testing.T) {
		return func(t *testing.T) {
			d, r, want := widestCapabilities(), widestCapabilities(), widestCapabilities()
			*field(&d), *field(&r), *field(&want) = declared, reported, effective
			var violations []string
			if violation != "" {
				violations = []string{violation}
			}
			assertNarrowed(t, d, r, want, violations)
		}
	}
	for di, declared := range order {
		for ri, reported := range order {
			switch {
			case ri > di:
				t.Run(fmt.Sprintf("declared %s, reported %s: above it, clamped", declared, reported),
					one(declared, reported, declared, fmt.Sprintf("%s: reported %s above the declared %s", name, reported, declared)))
			case ri == di:
				t.Run(fmt.Sprintf("declared %s, reported %s: equal, accepted", declared, reported), one(declared, reported, reported, ""))
			default:
				t.Run(fmt.Sprintf("declared %s, reported %s: below it, accepted", declared, reported), one(declared, reported, reported, ""))
			}
		}
	}
	if unsaid {
		for _, declared := range order {
			t.Run(fmt.Sprintf("declared %s, reported unspecified: the declared value is kept", declared), one(declared, zero, declared, ""))
			t.Run(fmt.Sprintf("declared unspecified, reported %s: above it, clamped", declared),
				one(zero, declared, zero, fmt.Sprintf("%s: reported %s above the declared %s", name, declared, zero)))
		}
		t.Run("declared unspecified, reported unspecified", one(zero, zero, zero, ""))
	}
	for _, declared := range order {
		t.Run(fmt.Sprintf("declared %s, reported a number that is no value: the declared value is kept and named", declared),
			one(declared, 99, declared, fmt.Sprintf("%s: reported 99 is not a defined value", name)))
	}
}

// setRow is one way a session can report a set (Signals, Integration): inclusion, the session's set a subset of the
// kind's.
type setRow[T any] struct {
	name      string
	declared  []T
	reported  []T
	want      []T    // the effective set
	violation string // the one violation the report draws, empty for none
}

func narrowSet[T comparable](name, proto string, field func(*Capabilities) *[]T, rows []setRow[T]) narrowField {
	return narrowField{name, proto, "inclusion", func(t *testing.T) {
		for _, row := range rows {
			t.Run(row.name, func(t *testing.T) {
				d, r, want := widestCapabilities(), widestCapabilities(), widestCapabilities()
				*field(&d), *field(&r), *field(&want) = row.declared, row.reported, row.want
				var violations []string
				if row.violation != "" {
					violations = []string{row.violation}
				}
				assertNarrowed(t, d, r, want, violations)
			})
		}
	}}
}

func signalRows() []setRow[Signal] {
	kind := []Signal{SignalInt, SignalTerm, SignalKill}
	return []setRow[Signal]{
		{"no signals reported narrows to none", kind, nil, nil, ""},
		{"an empty set is none", kind, []Signal{}, nil, ""},
		{"a subset is accepted", kind, []Signal{SignalInt}, []Signal{SignalInt}, ""},
		{"a subset keeps the order of the report", kind, []Signal{SignalTerm, SignalInt}, []Signal{SignalTerm, SignalInt}, ""},
		{"the declared set is accepted", kind, []Signal{SignalInt, SignalTerm, SignalKill}, kind, ""},
		{"the declared set in another order is accepted", kind, []Signal{SignalKill, SignalTerm, SignalInt}, []Signal{SignalKill, SignalTerm, SignalInt}, ""},
		{"a signal outside the declared set is dropped and named", kind, []Signal{SignalInt, SignalHup}, []Signal{SignalInt},
			"Signals: reported HUP not among the declared INT, TERM, KILL"},
		{"every signal outside is dropped and named", kind, []Signal{SignalHup, SignalUsr1}, nil,
			"Signals: reported HUP, USR1 not among the declared INT, TERM, KILL"},
		{"a signal listed twice counts once", kind, []Signal{SignalInt, SignalInt}, []Signal{SignalInt}, ""},
		{"a signal outside listed twice is named once", kind, []Signal{SignalHup, SignalInt, SignalHup}, []Signal{SignalInt},
			"Signals: reported HUP not among the declared INT, TERM, KILL"},
		{"the unspecified signal is not deliverable", kind, []Signal{SignalUnspecified, SignalInt}, []Signal{SignalInt},
			"Signals: reported UNSPECIFIED not among the declared INT, TERM, KILL"},
		{"a number that is no signal is named by the number", kind, []Signal{Signal(99)}, nil,
			"Signals: reported 99 not among the declared INT, TERM, KILL"},
		{"a kind that declares none accepts none", nil, nil, nil, ""},
		{"a kind that declares none refuses any", nil, []Signal{SignalInt}, nil,
			"Signals: reported INT not among the declared (none)"},
	}
}

func integrationRows() []setRow[IntegrationMode] {
	kind := []IntegrationMode{IntegrationArgvWrapper}
	return []setRow[IntegrationMode]{
		{"no mode reported narrows to none", kind, nil, nil, ""},
		{"the declared mode is accepted", kind, []IntegrationMode{IntegrationArgvWrapper}, kind, ""},
		{"a kind that declares none refuses a mode", nil, []IntegrationMode{IntegrationArgvWrapper}, nil,
			"Integration: reported ARGV_WRAPPER not among the declared (none)"},
		{"off is not a mode a target accepts", kind, []IntegrationMode{IntegrationOff}, nil,
			"Integration: reported NONE not among the declared ARGV_WRAPPER"},
		{"a number that is no mode is named by the number", kind, []IntegrationMode{IntegrationMode(99), IntegrationArgvWrapper}, kind,
			"Integration: reported 99 not among the declared ARGV_WRAPPER"},
		{"a mode listed twice counts once", kind, []IntegrationMode{IntegrationArgvWrapper, IntegrationArgvWrapper}, kind, ""},
	}
}

// narrowFields lists every field of Capabilities in the order of the struct, which is the order of the proto message.
// The order of each is the one the proto writes at the field (checked against the proto source below); the Go values
// of KillBehavior and StallBehavior are numbered the other way round (KillTerminates is 1 and the highest), which is
// why the orders are lists and not numbers.
var narrowFields = []narrowField{
	narrowFlag("TTY", "tty", func(c *Capabilities) *bool { return &c.TTY }),
	narrowFlag("SeparateStderr", "separate_stderr", func(c *Capabilities) *bool { return &c.SeparateStderr }),
	narrowFlag("Resize", "resize", func(c *Capabilities) *bool { return &c.Resize }),
	narrowFlag("InitialSize", "initial_size", func(c *Capabilities) *bool { return &c.InitialSize }),
	narrowFlag("StdinEOF", "stdin_eof", func(c *Capabilities) *bool { return &c.StdinEOF }),
	narrowSet("Signals", "signals", func(c *Capabilities) *[]Signal { return &c.Signals }, signalRows()),
	narrowOrdered("Kill", "kill", []KillBehavior{KillClosesStreamOnly, KillBestEffort, KillTerminates},
		func(c *Capabilities) *KillBehavior { return &c.Kill }),
	narrowFlag("Env", "env", func(c *Capabilities) *bool { return &c.Env }),
	narrowFlag("Cwd", "cwd", func(c *Capabilities) *bool { return &c.Cwd }),
	narrowOrdered("CwdFact", "cwd_fact", []FactMode{FactModeNone, FactModePull, FactModePush},
		func(c *Capabilities) *FactMode { return &c.CwdFact }),
	narrowOrdered("ForegroundFact", "foreground_fact", []FactMode{FactModeNone, FactModePull, FactModePush},
		func(c *Capabilities) *FactMode { return &c.ForegroundFact }),
	narrowOrdered("HostRestart", "host_restart", []Survival{SurvivalNone, SurvivalTargetProcess, SurvivalTargetSession},
		func(c *Capabilities) *Survival { return &c.HostRestart }),
	narrowOrdered("ExitReport", "exit_report", []ExitReport{ExitReportNone, ExitReportCode, ExitReportCodeAndSignal},
		func(c *Capabilities) *ExitReport { return &c.ExitReport }),
	narrowOrdered("Stall", "stall", []StallBehavior{StallLossy, StallPropagates},
		func(c *Capabilities) *StallBehavior { return &c.Stall }),
	narrowFlag("LinkHealth", "link_health", func(c *Capabilities) *bool { return &c.LinkHealth }),
	narrowSet("Integration", "integration", func(c *Capabilities) *[]IntegrationMode { return &c.Integration }, integrationRows()),
}

// capabilityFieldNames lists the fields of Capabilities in the order of the struct.
func capabilityFieldNames() []string {
	typ := reflect.TypeFor[Capabilities]()
	names := make([]string, typ.NumField())
	for i := range names {
		names[i] = typ.Field(i).Name
	}
	return names
}

// A field of Capabilities that has no row is a field that is never checked: the table must follow the struct.
func TestNarrowCapabilitiesTableCoversEveryField(t *testing.T) {
	var tabled []string
	for _, f := range narrowFields {
		tabled = append(tabled, f.name)
	}
	if fields := capabilityFieldNames(); !slices.Equal(fields, tabled) {
		t.Errorf("Capabilities has the fields %v and the narrowing table has %v: add the row of a new field, in the order of the struct", fields, tabled)
	}
}

// Every field: a narrower report is accepted, an equal one is accepted, a wider one is clamped to the declared value and
// named in a violation, and an enum left unspecified keeps the declared value.
func TestNarrowCapabilitiesEveryField(t *testing.T) {
	for _, f := range narrowFields {
		t.Run(f.name, f.check)
	}
}

// The order of each field is the one the proto writes at the field, in the comment the consumers read.
func TestNarrowCapabilitiesOrdersAreTheOnesTheProtoWrites(t *testing.T) {
	source, err := os.ReadFile("../../../proto/exec/v2/exec.proto")
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout may give the file CRLF line endings: the parser below reads LF.
	orders, names := protoCapabilityOrders(t, strings.ReplaceAll(string(source), "\r\n", "\n"))
	var tabled []string
	for _, f := range narrowFields {
		tabled = append(tabled, f.proto)
	}
	if !slices.Equal(names, tabled) {
		t.Errorf("message Capabilities has the fields %v and the narrowing table has %v", names, tabled)
	}
	for _, f := range narrowFields {
		if got := orders[f.proto]; got != f.order {
			t.Errorf("%s: the proto writes the order %q, the narrowing tests use %q", f.proto, got, f.order)
		}
	}
}

var (
	protoFieldLine = regexp.MustCompile(`^(?:optional\s+)?\w+\s+([a-z_]+)\s*=\s*\d+;$`)
	protoOrder     = regexp.MustCompile(`Order:\s*([^.(]+)`)
)

// protoCapabilityOrders reads message Capabilities of the proto source: its field names in the order of the file, and
// the order each field's comment states ("Order: false < true.").
func protoCapabilityOrders(t *testing.T, source string) (orders map[string]string, names []string) {
	t.Helper()
	_, body, found := strings.Cut(source, "\nmessage Capabilities {\n")
	if !found {
		t.Fatal("proto/exec/v2/exec.proto has no message Capabilities")
	}
	body, _, _ = strings.Cut(body, "\n}\n")
	orders = map[string]string{}
	var comment []string
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if text, isComment := strings.CutPrefix(line, "//"); isComment {
			comment = append(comment, strings.TrimSpace(text))
			continue
		}
		if m := protoFieldLine.FindStringSubmatch(line); m != nil {
			names = append(names, m[1])
			if order := protoOrder.FindStringSubmatch(strings.Join(comment, " ")); order != nil {
				orders[m[1]] = strings.TrimSpace(order[1])
			}
		}
		comment = nil
	}
	return orders, names
}

// A report that is narrower in every field is accepted whole.
func TestNarrowCapabilitiesAReportNarrowerInEveryFieldIsAccepted(t *testing.T) {
	assertNarrowed(t, widestCapabilities(), narrowestCapabilities(), narrowestCapabilities(), nil)
}

// A report that is wider in every field gets the declared set back, with one violation per field naming it, in the
// order of the fields.
func TestNarrowCapabilitiesAReportWiderInEveryFieldIsClampedAndEveryFieldIsNamed(t *testing.T) {
	declared := narrowestCapabilities()
	got, violations := narrowCapabilities(declared, widestCapabilities())
	if diff := capabilitiesDiff(got, declared); len(diff) > 0 {
		t.Errorf("the effective set is wider than the declared one in %v: %+v", diff, got)
	}
	names := capabilityFieldNames()
	if len(violations) != len(names) {
		t.Fatalf("%d violations for a report that widens %d fields: %q", len(violations), len(names), violations)
	}
	for i, name := range names {
		if !strings.HasPrefix(violations[i], name+": ") {
			t.Errorf("violation %d is %q: want one per field, in the order of the fields, here %s", i, violations[i], name)
		}
	}
}

// What a Pod kind does: attach narrows what it reports, a careless author widens a few fields, and each field is judged
// on its own.
func TestNarrowCapabilitiesEachFieldIsJudgedOnItsOwn(t *testing.T) {
	declared := Capabilities{
		TTY: true, SeparateStderr: true, Resize: true, InitialSize: true, StdinEOF: true,
		Signals:     []Signal{SignalInt, SignalTerm},
		Kill:        KillClosesStreamOnly,
		ExitReport:  ExitReportCode,
		Stall:       StallLossy,
		HostRestart: SurvivalTargetProcess,
		Integration: []IntegrationMode{IntegrationArgvWrapper},
	}
	reported := Capabilities{
		TTY: false, SeparateStderr: true, Resize: true, InitialSize: true, StdinEOF: true, // below one, equal the others
		Signals:     []Signal{SignalInt, SignalHup}, // HUP is not the kind's
		Kill:        KillTerminates,                 // above
		Env:         true,                           // above
		CwdFact:     FactModePull,                   // above
		HostRestart: SurvivalNone,                   // below
		ExitReport:  ExitReportNone,                 // below
		Stall:       StallUnspecified,               // unsaid: the declared value stays
		LinkHealth:  true,                           // above
	}
	want := declared
	want.TTY = false
	want.Signals = []Signal{SignalInt}
	want.HostRestart = SurvivalNone
	want.ExitReport = ExitReportNone
	want.Integration = nil
	assertNarrowed(t, declared, reported, want, []string{
		"Signals: reported HUP not among the declared INT, TERM",
		"Kill: reported TERMINATES above the declared CLOSES_STREAM_ONLY",
		"Env: reported true above the declared false",
		"CwdFact: reported PULL above the declared NONE",
		"LinkHealth: reported true above the declared false",
	})
}

// The effective set is the session's own: writing to it does not reach the declared set or the report, and writing to
// either afterwards does not reach it.
func TestNarrowCapabilitiesSharesNoStorageWithItsArguments(t *testing.T) {
	for name, signals := range map[string][]Signal{
		"a subset":      {SignalInt, SignalTerm},
		"the whole set": widestCapabilities().Signals,
	} {
		t.Run(name, func(t *testing.T) {
			declared, reported := widestCapabilities(), widestCapabilities()
			reported.Signals = slices.Clone(signals)
			declaredBefore, reportedBefore := cloneCapabilities(declared), cloneCapabilities(reported)

			effective, violations := narrowCapabilities(declared, reported)
			if len(violations) > 0 || len(effective.Signals) != len(signals) || len(effective.Integration) != 1 {
				t.Fatalf("effective = %+v, violations = %q", effective, violations)
			}
			effective.Signals[0], effective.Integration[0] = SignalUsr2, IntegrationOff
			if diff := capabilitiesDiff(declared, declaredBefore); len(diff) > 0 {
				t.Errorf("writing to the effective set changed the declared set in %v", diff)
			}
			if diff := capabilitiesDiff(reported, reportedBefore); len(diff) > 0 {
				t.Errorf("writing to the effective set changed the report in %v", diff)
			}

			effective, _ = narrowCapabilities(declared, reported)
			declared.Signals[0], reported.Signals[0], declared.Integration[0], reported.Integration[0] = SignalUsr2, SignalUsr2, IntegrationOff, IntegrationOff
			if effective.Signals[0] != signals[0] || effective.Integration[0] != IntegrationArgvWrapper {
				t.Errorf("writing to the arguments changed the effective set: %+v", effective)
			}
		})
	}
}

// exectest cannot call the unexported rule, so the rig of exectest reaches it through internal/exechook: what it gets
// is the rule itself, with the same effective set and the same violations.
func TestNarrowCapabilitiesIsTheRuleExectestApplies(t *testing.T) {
	declared, reported := narrowestCapabilities(), widestCapabilities()
	want, wantViolations := narrowCapabilities(declared, reported)
	got, violations := exechook.NarrowCapabilities(declared, reported)
	if diff := capabilitiesDiff(got, want); len(diff) > 0 {
		t.Errorf("the rule exectest applies gives another effective set, it differs in %v", diff)
	}
	if len(wantViolations) == 0 || !slices.Equal(violations, wantViolations) {
		t.Errorf("the rule exectest applies names %q, the SDK's names %q", violations, wantViolations)
	}
}
