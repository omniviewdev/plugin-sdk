package exec

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

func fullTarget() Target {
	t := validTarget()
	t.Info.Capabilities = fullCapabilities()
	return t
}

// fullCapabilities sets every field of Capabilities to a value that is not its zero value.
func fullCapabilities() Capabilities {
	return Capabilities{
		TTY: true, SeparateStderr: true, Resize: true, InitialSize: true, StdinEOF: true,
		Signals: []Signal{SignalInt, SignalTerm, SignalKill}, Kill: KillClosesStreamOnly,
		Env: true, Cwd: true, CwdFact: FactModePull, ForegroundFact: FactModePush, HostRestart: SurvivalTargetProcess,
		ExitReport: ExitReportCodeAndSignal, Stall: StallLossy, LinkHealth: true,
		Integration: []IntegrationMode{IntegrationArgvWrapper},
	}
}

// validDescription is the answer of a plugin that registered fullTarget.
func validDescription(t testing.TB) *execv2.DescribeResponse {
	t.Helper()
	r, err := newTargetRegistry([]Target{fullTarget()})
	if err != nil {
		t.Fatal(err)
	}
	return r.description()
}

func TestDescriptionSurvivesTheWireAndStatesWhatIsAbsent(t *testing.T) {
	r, err := newTargetRegistry([]Target{fullTarget(), {Exec: noopExec, Info: TargetInfo{Provider: "k8s", Type: "nodes", DisplayName: "Node"}}})
	if err != nil {
		t.Fatal(err)
	}
	wire := r.description()
	// The wire form must survive a real encode and decode.
	raw, err := proto.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &execv2.DescribeResponse{}
	if err := proto.Unmarshal(raw, decoded); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(decoded, wire) {
		t.Errorf("the wire changed the description:\n got  %v\n want %v", decoded, wire)
	}
	if err := validateDescribeResponse(decoded); err != nil {
		t.Errorf("what a registry describes does not validate: %v", err)
	}
	if decoded.GetContractVersion() != ContractVersion {
		t.Errorf("contract version %d", decoded.GetContractVersion())
	}
	pods, nodes := decoded.GetTargets()[0], decoded.GetTargets()[1]
	if pods.GetProvider() != "k8s" || pods.GetType() != "pods" || nodes.GetType() != "nodes" || nodes.GetDisplayName() != "Node" {
		t.Errorf("targets = %v", decoded.GetTargets())
	}
	// An options-only target states neither session nor run limits on the wire: absent, not zero.
	if nodes.GetSession() != nil || nodes.GetRun() != nil {
		t.Errorf("a target that serves no sessions and no runs sent limits: %v", nodes)
	}
	if pods.GetSession() == nil || pods.GetRun() == nil {
		t.Error("a target that serves sessions and runs omitted its limits")
	}
	if got, want := sessionLimitsFromProto(pods.GetSession()), DefaultSessionLimits(); got != want {
		t.Errorf("session limits = %+v, want %+v", got, want)
	}
	if got, want := runLimitsFromProto(pods.GetRun()), DefaultRunLimits(); got != want {
		t.Errorf("run limits = %+v, want %+v", got, want)
	}
}

// The features this build lists are the ones whose frames it serves and whose conformance cases are written: sessions on one
// stream and bounded runs (as one result or as events), with the environment and the working directory of a start or a run, and
// stdin EOF (close_input). Session management, marks, modes and integration are served by later steps; each lists its feature when
// it lands, so that a host never offers what the plugin cannot do.
func TestThisBuildListsTheFeaturesItServes(t *testing.T) {
	r, _ := newTargetRegistry([]Target{validTarget()})
	want := []execv2.Feature{
		execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_RUN, execv2.Feature_FEATURE_RUN_STREAM,
		execv2.Feature_FEATURE_ENV, execv2.Feature_FEATURE_CWD, execv2.Feature_FEATURE_STDIN_EOF,
		execv2.Feature_FEATURE_SESSION_MANAGEMENT,
	}
	if got := r.description().GetFeatures(); !slices.Equal(got, want) {
		t.Errorf("this build lists features %v, want %v", got, want)
	}
}

// What an author declared is written to the wire with every field present: an optional bool that is false is
// present and false, a list that is empty is a present empty set, and a "none" the author left at the Go zero value is
// NONE. A consumer reads an absent field as "not known"; the SDK knows every one of them, so it leaves none absent.
func TestCapabilitiesToProtoWritesEveryFieldPresent(t *testing.T) {
	t.Run("the zero declaration says no, none and nothing, never nothing at all", func(t *testing.T) {
		got := capabilitiesToProto(Capabilities{})
		for name, field := range map[string]*bool{
			"tty": got.Tty, "separate_stderr": got.SeparateStderr, "resize": got.Resize, "initial_size": got.InitialSize,
			"stdin_eof": got.StdinEof, "env": got.Env, "cwd": got.Cwd, "link_health": got.LinkHealth,
		} {
			if field == nil {
				t.Errorf("%s is absent: a declared false must be present and false", name)
			} else if *field {
				t.Errorf("%s is true for a zero declaration", name)
			}
		}
		if got.Signals == nil || len(got.Signals.Signals) != 0 {
			t.Errorf("signals = %v: an empty declaration is a present empty set (none), not an absent one", got.Signals)
		}
		if got.Integration == nil || len(got.Integration.Modes) != 0 {
			t.Errorf("integration = %v: an empty declaration is a present empty set (none), not an absent one", got.Integration)
		}
		if got.CwdFact != execv2.FactMode_FACT_MODE_NONE || got.ForegroundFact != execv2.FactMode_FACT_MODE_NONE {
			t.Errorf("facts = %v, %v: the Go zero value is the author's NONE, not UNSPECIFIED", got.CwdFact, got.ForegroundFact)
		}
		if got.HostRestart != execv2.Survival_SURVIVAL_NONE {
			t.Errorf("host_restart = %v: the Go zero value is the author's NONE, not UNSPECIFIED", got.HostRestart)
		}
		// The three that must be declared stay unknown while the author has not said: UNSPECIFIED is the wire's word.
		if got.Kill != execv2.KillBehavior_KILL_BEHAVIOR_UNSPECIFIED || got.ExitReport != execv2.ExitReport_EXIT_REPORT_UNSPECIFIED ||
			got.Stall != execv2.StallBehavior_STALL_BEHAVIOR_UNSPECIFIED {
			t.Errorf("kill, exit_report and stall = %v, %v, %v: an author who did not say left them unknown", got.Kill, got.ExitReport, got.Stall)
		}
	})

	t.Run("every field that has presence is set, for every declaration", func(t *testing.T) {
		for name, c := range map[string]Capabilities{"zero": {}, "command target": commandCapabilities(), "full": fullCapabilities()} {
			wire := capabilitiesToProto(c).ProtoReflect()
			fields := wire.Descriptor().Fields()
			for i := range fields.Len() {
				fd := fields.Get(i)
				if fd.HasPresence() && !wire.Has(fd) {
					t.Errorf("%s: field %s has presence and is absent", name, fd.Name())
				}
			}
		}
	})

	t.Run("a full declaration reaches the wire field by field", func(t *testing.T) {
		got := capabilitiesToProto(fullCapabilities())
		for name, field := range map[string]*bool{
			"tty": got.Tty, "separate_stderr": got.SeparateStderr, "resize": got.Resize, "initial_size": got.InitialSize,
			"stdin_eof": got.StdinEof, "env": got.Env, "cwd": got.Cwd, "link_health": got.LinkHealth,
		} {
			if field == nil || !*field {
				t.Errorf("%s = %v, want present and true", name, field)
			}
		}
		if want := []execv2.Signal{execv2.Signal_SIGNAL_INT, execv2.Signal_SIGNAL_TERM, execv2.Signal_SIGNAL_KILL}; !reflect.DeepEqual(got.GetSignals().GetSignals(), want) {
			t.Errorf("signals = %v, want %v", got.GetSignals().GetSignals(), want)
		}
		if want := []execv2.IntegrationMode{execv2.IntegrationMode_INTEGRATION_MODE_ARGV_WRAPPER}; !reflect.DeepEqual(got.GetIntegration().GetModes(), want) {
			t.Errorf("integration = %v, want %v", got.GetIntegration().GetModes(), want)
		}
		if got.Kill != execv2.KillBehavior_KILL_BEHAVIOR_CLOSES_STREAM_ONLY || got.CwdFact != execv2.FactMode_FACT_MODE_PULL ||
			got.ForegroundFact != execv2.FactMode_FACT_MODE_PUSH || got.HostRestart != execv2.Survival_SURVIVAL_TARGET_PROCESS ||
			got.ExitReport != execv2.ExitReport_EXIT_REPORT_CODE_AND_SIGNAL || got.Stall != execv2.StallBehavior_STALL_BEHAVIOR_LOSSY {
			t.Errorf("enums = %v", got)
		}
	})

	t.Run("a field of Capabilities that nothing writes would show up here", func(t *testing.T) {
		// Capabilities and its wire message have the same sixteen fields: a field added to one and not to the other, or
		// one that capabilitiesToProto forgets, leaves a wire field at its zero value for a declaration that sets all.
		if goFields, wireFields := reflect.TypeFor[Capabilities]().NumField(), (&execv2.Capabilities{}).ProtoReflect().Descriptor().Fields().Len(); goFields != wireFields {
			t.Fatalf("Capabilities has %d fields and the wire message %d", goFields, wireFields)
		}
		wire := capabilitiesToProto(fullCapabilities()).ProtoReflect()
		fields := wire.Descriptor().Fields()
		for i := range fields.Len() {
			if fd := fields.Get(i); !wire.Has(fd) {
				t.Errorf("field %s is not written for a declaration that sets every field", fd.Name())
			}
		}
	})
}

func TestADeclaredNoneIsWrittenAsNoneAndNeverAsUnspecified(t *testing.T) {
	// The enums whose wire value for "the author declared none" is NONE: the Go zero value of each.
	for name, wire := range map[string]int32{
		"FactMode":        int32(FactModeNone.toProto()),
		"Survival":        int32(SurvivalNone.toProto()),
		"IntegrationMode": int32(IntegrationOff.toProto()),
		"ExitReport":      int32(ExitReportNone.toProto()),
	} {
		if wire == 0 {
			t.Errorf("%s: a declared none is written as UNSPECIFIED, which means the producer did not say", name)
		}
	}
	if got := FactModeNone.toProto().String(); got != "FACT_MODE_NONE" {
		t.Errorf("FactModeNone is written as %s", got)
	}
	if got := SurvivalNone.toProto().String(); got != "SURVIVAL_NONE" {
		t.Errorf("SurvivalNone is written as %s", got)
	}
	if got := IntegrationOff.toProto().String(); got != "INTEGRATION_MODE_NONE" {
		t.Errorf("IntegrationOff is written as %s", got)
	}
}

func TestValidateDescribeResponse(t *testing.T) {
	t.Run("what a registry describes is valid", func(t *testing.T) {
		if err := validateDescribeResponse(validDescription(t)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a value this build does not know is not an error and is not changed", func(t *testing.T) {
		w := validDescription(t)
		w.Features = []execv2.Feature{execv2.Feature_FEATURE_SESSION, 9999, execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_UNSPECIFIED}
		c := w.GetTargets()[0].GetCapabilities()
		c.Signals.Signals = append(c.Signals.Signals, 9999)
		c.Integration.Modes = append(c.Integration.Modes, 9999)
		c.Kill, c.Stall, c.ExitReport, c.HostRestart, c.CwdFact = 9999, 9999, 9999, 9999, 9999
		before := proto.Clone(w)
		if err := validateDescribeResponse(w); err != nil {
			t.Fatalf("a response of a newer plugin was refused: %v", err)
		}
		if !proto.Equal(before, w) {
			t.Errorf("validation changed the message:\n got  %v\n want %v", w, before)
		}
	})
	t.Run("a field the plugin did not send stays absent", func(t *testing.T) {
		w := validDescription(t)
		c := w.GetTargets()[0].GetCapabilities()
		c.Tty, c.SeparateStderr, c.Resize, c.InitialSize, c.StdinEof, c.Env, c.Cwd, c.LinkHealth = nil, nil, nil, nil, nil, nil, nil, nil
		c.Signals, c.Integration = nil, nil
		before := proto.Clone(w)
		if err := validateDescribeResponse(w); err != nil {
			t.Fatalf("a producer that does not know its capabilities was refused: %v", err)
		}
		wire := w.GetTargets()[0].GetCapabilities().ProtoReflect()
		fields := wire.Descriptor().Fields()
		for i := range fields.Len() {
			if fd := fields.Get(i); fd.HasPresence() && wire.Has(fd) {
				t.Errorf("validation filled in %s, which the plugin did not send", fd.Name())
			}
		}
		if !proto.Equal(before, w) {
			t.Errorf("validation changed the message:\n got  %v\n want %v", w, before)
		}
	})
	t.Run("a target with no capabilities at all is valid", func(t *testing.T) {
		w := validDescription(t)
		w.GetTargets()[0].Capabilities = nil
		if err := validateDescribeResponse(w); err != nil {
			t.Errorf("a producer that declares no capabilities was refused: %v", err)
		}
	})
	t.Run("limits another producer left partly unstated are valid", func(t *testing.T) {
		w := validDescription(t)
		w.GetTargets()[0].Session = &execv2.SessionLimits{MaxSessions: 3}
		w.GetTargets()[0].Run = &execv2.RunLimits{MaxConcurrent: 2}
		if err := validateDescribeResponse(w); err != nil {
			t.Errorf("partial limits were refused: %v", err)
		}
	})

	refused := []struct {
		name   string
		change func(w *execv2.DescribeResponse)
		want   string
	}{
		{"an empty target", func(w *execv2.DescribeResponse) { w.Targets = append(w.Targets, nil) }, "empty target"},
		{"no provider", func(w *execv2.DescribeResponse) { w.Targets[0].Provider = "" }, "provider"},
		{"no type", func(w *execv2.DescribeResponse) { w.Targets[0].Type = "" }, "type"},
		{"a provider with a control character", func(w *execv2.DescribeResponse) { w.Targets[0].Provider = "k8\x00s" }, "provider"},
		{"the same target twice", func(w *execv2.DescribeResponse) {
			w.Targets = append(w.Targets, proto.Clone(w.Targets[0]).(*execv2.TargetInfo))
		}, "listed twice"},
		{"the same target twice, not next to each other", func(w *execv2.DescribeResponse) {
			w.Targets = append(w.Targets, &execv2.TargetInfo{Provider: "k8s", Type: "nodes"}, proto.Clone(w.Targets[0]).(*execv2.TargetInfo))
		}, "listed twice"},
		{"too many targets", func(w *execv2.DescribeResponse) {
			for i := range maxTargets {
				w.Targets = append(w.Targets, &execv2.TargetInfo{Provider: "p", Type: strings.Repeat("t", 1+i%10) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))})
			}
		}, "exceed the limit"},
		{"a display name that is too long", func(w *execv2.DescribeResponse) { w.Targets[0].DisplayName = strings.Repeat("d", 129) }, "display name"},
		{"a default argv with a NUL", func(w *execv2.DescribeResponse) { w.Targets[0].DefaultArgv = []string{"sh\x00"} }, "NUL"},
		{"a present session section that states nothing", func(w *execv2.DescribeResponse) { w.Targets[0].Session = &execv2.SessionLimits{} }, "state no limit"},
		{"a present run section that states nothing", func(w *execv2.DescribeResponse) { w.Targets[0].Run = &execv2.RunLimits{} }, "state no limit"},
		{"a session limit over its ceiling", func(w *execv2.DescribeResponse) { w.Targets[0].Session.MaxDimension = 1 << 30 }, "MaxDimension"},
		{"a run limit over its ceiling", func(w *execv2.DescribeResponse) { w.Targets[0].Run.MaxStdinBytes = 1 << 30 }, "MaxStdinBytes"},
		{"a ring larger than its maximum", func(w *execv2.DescribeResponse) {
			w.Targets[0].Session.ReplayBytes, w.Targets[0].Session.MaxReplayBytes = 2<<20, 1<<20
		}, "exceeds MaxReplayBytes"},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			w := validDescription(t)
			tc.change(w)
			if err := validateDescribeResponse(w); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
	if err := validateDescribeResponse(nil); err == nil {
		t.Error("a nil response was accepted")
	}
}

func TestOptionsRoundTripAndTheStateOfAChoiceIsNeverGuessed(t *testing.T) {
	options := []Option{
		{Name: "container", Required: true, Choices: []Choice{
			{Value: "app", DisplayName: "app", State: ChoiceStateReady, IsDefault: true, Kind: "container"},
			{Value: "migrate", DisplayName: "migrate", State: ChoiceStateStopped, Detail: "terminated", Kind: "init"},
			{Value: "debug", State: ChoiceStateStarting, Kind: "ephemeral"},
			{Value: "legacy"}, // the producer did not observe a state
		}},
		{Name: "empty"},
	}
	if err := validateOptions(options); err != nil {
		t.Fatal(err)
	}
	wire := optionsToProto(options)
	raw, err := proto.Marshal(&execv2.OptionsResponse{Options: wire})
	if err != nil {
		t.Fatal(err)
	}
	decoded := &execv2.OptionsResponse{}
	if err := proto.Unmarshal(raw, decoded); err != nil {
		t.Fatal(err)
	}
	got, err := optionsFromProto(decoded.GetOptions())
	if err != nil {
		t.Fatal(err)
	}
	// An option with no choices is present and empty: not absent.
	want := []Option{options[0], {Name: "empty", Choices: []Choice{}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed the options:\n got  %+v\n want %+v", got, want)
	}
	if got[0].Choices[3].State != ChoiceStateUnspecified {
		t.Error("a choice whose state was not observed read as a state")
	}
	wire[0].Choices[0].State = 9999
	if again, _ := optionsFromProto(wire); again[0].Choices[0].State != ChoiceStateUnspecified {
		t.Error("an unknown state of a newer peer was not read as unspecified")
	}
}

func TestOptionsValidation(t *testing.T) {
	choice := func(v string) Choice { return Choice{Value: v} }
	tests := []struct {
		name    string
		options []Option
		want    string // empty means valid
	}{
		{"none", nil, ""},
		{"a name that is not a closed code", []Option{{Name: "Container"}}, "not a closed code"},
		{"a name with a space", []Option{{Name: "my container"}}, "not a closed code"},
		{"an empty name", []Option{{Name: ""}}, "not a closed code"},
		{"a name of 40 bytes", []Option{{Name: strings.Repeat("a", 40)}}, ""},
		{"a name of 41 bytes", []Option{{Name: strings.Repeat("a", 41)}}, "not a closed code"},
		{"the same option twice", []Option{{Name: "a"}, {Name: "a"}}, "listed twice"},
		{"an empty value", []Option{{Name: "a", Choices: []Choice{{}}}}, ""},
		{"an empty value and another", []Option{{Name: "a", Choices: []Choice{{}, choice("x")}}}, ""},
		{"an empty value listed twice", []Option{{Name: "a", Choices: []Choice{{}, {DisplayName: "again"}}}}, "listed twice"},
		{"a value that is too long", []Option{{Name: "a", Choices: []Choice{choice(strings.Repeat("v", 257))}}}, "too long or not plain text"},
		{"a value with a control character", []Option{{Name: "a", Choices: []Choice{choice("a\nb")}}}, "not plain text"},
		{"a value that is not UTF-8", []Option{{Name: "a", Choices: []Choice{choice("a\xffb")}}}, "not plain text"},
		{"a value listed twice", []Option{{Name: "a", Choices: []Choice{choice("x"), choice("x")}}}, "listed twice"},
		{"a display name that is too long", []Option{{Name: "a", Choices: []Choice{{Value: "x", DisplayName: strings.Repeat("d", 257)}}}}, "display name"},
		{"a detail that is prose", []Option{{Name: "a", Choices: []Choice{{Value: "x", Detail: "It crashed"}}}}, "detail is not a closed code"},
		{"a kind that is prose", []Option{{Name: "a", Choices: []Choice{{Value: "x", Kind: "init container"}}}}, "kind is not a closed code"},
		{"two defaults", []Option{{Name: "a", Choices: []Choice{{Value: "x", IsDefault: true}, {Value: "y", IsDefault: true}}}}, "are the default"},
		{"one default", []Option{{Name: "a", Choices: []Choice{{Value: "x", IsDefault: true}, {Value: "y"}}}}, ""},
		{"closed codes with the whole alphabet", []Option{{Name: "a", Choices: []Choice{{Value: "x", Detail: "a-z_0.9", Kind: "k.1_x-y"}}}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOptions(tc.options)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
			// The decode of a plugin's answer applies the same bounds.
			if _, err := optionsFromProto(optionsToProto(tc.options)); (err != nil) != (tc.want != "") {
				t.Errorf("the decode of an answer disagrees with the validation: %v", err)
			}
		})
	}
}

// The contract says at most 8 options, and at most 256 choices in each. The numbers are written here and not read
// from the constants of limits.go: a test that reads the bound from the constant it checks passes whatever the
// constant says.
func TestTheNumbersOfOptionsAndChoicesAreThoseOfTheContract(t *testing.T) {
	// distinct gives n options with distinct closed-code names, each with the given number of distinct choices.
	distinct := func(options, choices int) []Option {
		out := make([]Option, options)
		for i := range out {
			out[i] = Option{Name: "o" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Choices: make([]Choice, choices)}
			for j := range out[i].Choices {
				out[i].Choices[j] = Choice{Value: "v" + string(rune('a'+j%26)) + string(rune('a'+j/26%26)) + string(rune('a'+j/676))}
			}
		}
		return out
	}
	tests := []struct {
		name    string
		options []Option
		valid   bool
	}{
		{"8 options", distinct(8, 0), true},
		{"9 options", distinct(9, 0), false},
		{"256 choices in an option", distinct(1, 256), true},
		{"257 choices in an option", distinct(1, 257), false},
		{"8 options of 256 choices each", distinct(8, 256), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateOptions(tc.options); (err == nil) != tc.valid {
				t.Errorf("validateOptions = %v, valid = %v", err, tc.valid)
			}
			// The answer of a plugin is held to the same numbers.
			if _, err := optionsFromProto(optionsToProto(tc.options)); (err == nil) != tc.valid {
				t.Errorf("optionsFromProto = %v, valid = %v", err, tc.valid)
			}
		})
	}
}

func TestClosedCode(t *testing.T) {
	for _, ok := range []string{"a", "container", "crash-loop", "a.b_c-d", "0", strings.Repeat("z", 40)} {
		if !closedCode(ok) {
			t.Errorf("%q is a closed code", ok)
		}
	}
	for _, bad := range []string{"", "A", "a b", "a/b", "é", "a\x00", strings.Repeat("z", 41), "a:b"} {
		if closedCode(bad) {
			t.Errorf("%q is not a closed code", bad)
		}
	}
}

func TestClampIntSaturatesInsteadOfWrapping(t *testing.T) {
	if clampInt(5) != 5 || clampInt(0) != 0 {
		t.Error("a small value changed")
	}
	if got := clampInt(^uint32(0)); got <= 0 {
		t.Errorf("the largest uint32 became %d", got)
	}
	if time.Duration(clampInt(1000))*time.Millisecond != time.Second {
		t.Error("clampInt of 1000")
	}
}
