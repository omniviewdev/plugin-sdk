package exec

import (
	"fmt"
	"slices"
	"strings"
	"time"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// servedFeatures lists the features this SDK build serves, in a stable order. This build serves Describe, Options, Open (a
// session on one stream: FEATURE_SESSION), Run and RunStream (a bounded run, as one result or as events: FEATURE_RUN and
// FEATURE_RUN_STREAM), with the environment and the working directory of a start or a run (FEATURE_ENV, FEATURE_CWD); each later
// part of the contract (session management, stdin EOF, marks, modes, integration) is listed by the step that implements it, so
// that a host never offers what the plugin cannot do.
func servedFeatures() []execv2.Feature {
	return []execv2.Feature{
		execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_RUN, execv2.Feature_FEATURE_RUN_STREAM,
		execv2.Feature_FEATURE_ENV, execv2.Feature_FEATURE_CWD,
	}
}

// description builds the answer to Describe for a registry: static, the same for the life of the process.
func (r *targetRegistry) description() *execv2.DescribeResponse {
	out := &execv2.DescribeResponse{
		ContractVersion: ContractVersion,
		Features:        slices.Clone(servedFeatures()),
		Targets:         make([]*execv2.TargetInfo, 0, len(r.targets)),
	}
	for _, t := range r.targets {
		out.Targets = append(out.Targets, targetInfoToProto(t.info))
	}
	return out
}

func targetInfoToProto(i TargetInfo) *execv2.TargetInfo {
	out := &execv2.TargetInfo{
		Provider:     i.Provider,
		Type:         i.Type,
		DisplayName:  i.DisplayName,
		DefaultArgv:  slices.Clone(i.DefaultArgv),
		Capabilities: capabilitiesToProto(i.Capabilities),
	}
	if !i.Session.IsZero() {
		out.Session = sessionLimitsToProto(i.Session)
	}
	if !i.Run.IsZero() {
		out.Run = runLimitsToProto(i.Run)
	}
	return out
}

// validateDescribeResponse checks the answer of a plugin and returns nil when it is well formed: a list that breaks
// a bound is refused, and each target must be well formed, with a present limits section that states a limit and no
// target listed twice. The answer of a plugin that breaks the contract is an error, never partially trusted. A
// value this build does not know (a feature, an enum, a signal of a newer plugin) is not an error, and nothing is
// changed or filled in: the message a consumer reads is the message the plugin sent, with its absent fields absent.
func validateDescribeResponse(p *execv2.DescribeResponse) error {
	if p == nil {
		return fmt.Errorf("empty describe response")
	}
	if len(p.GetTargets()) > maxTargets {
		return fmt.Errorf("%d targets exceed the limit of %d", len(p.GetTargets()), maxTargets)
	}
	seen := make(map[routeKey]struct{}, len(p.GetTargets()))
	for _, t := range p.GetTargets() {
		if err := validateTargetInfo(t); err != nil {
			return err
		}
		key := routeKey{provider: t.GetProvider(), resourceType: t.GetType()}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("target %s/%s is listed twice", key.provider, key.resourceType)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// validateTargetInfo checks one described target.
func validateTargetInfo(p *execv2.TargetInfo) error {
	if p == nil {
		return fmt.Errorf("empty target")
	}
	provider, resourceType := p.GetProvider(), p.GetType()
	if err := validateRoutingPart("provider", provider); err != nil {
		return err
	}
	if err := validateRoutingPart("type", resourceType); err != nil {
		return err
	}
	if len(p.GetDisplayName()) > maxDisplayName || !validText(p.GetDisplayName()) {
		return fmt.Errorf("target %s/%s: display name is too long or not plain text", provider, resourceType)
	}
	if err := validateDefaultArgv(p.GetDefaultArgv()); err != nil {
		return fmt.Errorf("target %s/%s: %w", provider, resourceType, err)
	}
	if session := p.GetSession(); session != nil {
		limits := sessionLimitsFromProto(session)
		if limits.IsZero() {
			return fmt.Errorf("target %s/%s: session limits are present and state no limit", provider, resourceType)
		}
		if err := limits.validate(true); err != nil {
			return fmt.Errorf("target %s/%s: %w", provider, resourceType, err)
		}
	}
	if run := p.GetRun(); run != nil {
		limits := runLimitsFromProto(run)
		if limits.IsZero() {
			return fmt.Errorf("target %s/%s: run limits are present and state no limit", provider, resourceType)
		}
		if err := limits.validate(true); err != nil {
			return fmt.Errorf("target %s/%s: %w", provider, resourceType, err)
		}
	}
	return nil
}

// capabilitiesToProto writes the capabilities an author declared, and writes every field: an optional bool is always
// present, false included; the signal set and the integration set are always present, empty when the author listed
// none; and an enum the author left at its Go zero value, where that value is a declared "none" (FactModeNone,
// SurvivalNone), is the wire's NONE. A consumer reads a field that is absent as "not known", so a producer that knows
// a field and leaves it out would say the opposite of what it means. Kill, ExitReport and Stall that the author did
// not say are the wire's UNSPECIFIED (unknown): registration refuses that for a target that runs commands.
func capabilitiesToProto(c Capabilities) *execv2.Capabilities {
	out := &execv2.Capabilities{
		Tty:            new(c.TTY),
		SeparateStderr: new(c.SeparateStderr),
		Resize:         new(c.Resize),
		InitialSize:    new(c.InitialSize),
		StdinEof:       new(c.StdinEOF),
		Signals:        &execv2.SignalSet{},
		Kill:           c.Kill.toProto(),
		Env:            new(c.Env),
		Cwd:            new(c.Cwd),
		CwdFact:        c.CwdFact.toProto(),
		ForegroundFact: c.ForegroundFact.toProto(),
		HostRestart:    c.HostRestart.toProto(),
		ExitReport:     c.ExitReport.toProto(),
		Stall:          c.Stall.toProto(),
		LinkHealth:     new(c.LinkHealth),
		Integration:    &execv2.IntegrationModeSet{},
	}
	for _, s := range c.Signals {
		out.Signals.Signals = append(out.Signals.Signals, s.toProto())
	}
	for _, m := range c.Integration {
		out.Integration.Modes = append(out.Integration.Modes, m.toProto())
	}
	return out
}

// capabilitiesFromProto reads the capabilities of a choice a host got with Options into the author's Go type. The SDK
// writes every field (see capabilitiesToProto), so a message that leaves a flag or a list out was written by another
// producer, and a Go Capabilities cannot say that a flag is not known: reading it as false would say that the target
// cannot. Such a message is refused, and the fields it leaves out are named. A value this build does not know (a newer
// peer) is not an error and is read as narrow as it can be: an enum with an unsaid value (Kill, ExitReport, Stall) reads
// as unsaid, one without (FactMode, Survival) as none, and a signal or an integration mode this build cannot name is
// left out, so that nothing is offered that this build cannot name. A list keeps the order it has on the wire and
// counts a value listed twice once.
func capabilitiesFromProto(p *execv2.Capabilities) (Capabilities, error) {
	var absent []string
	flag := func(name string, v *bool) bool {
		if v == nil {
			absent = append(absent, name)
		}
		return v != nil && *v
	}
	c := Capabilities{
		TTY:            flag("tty", p.Tty),
		SeparateStderr: flag("separate_stderr", p.SeparateStderr),
		Resize:         flag("resize", p.Resize),
		InitialSize:    flag("initial_size", p.InitialSize),
		StdinEOF:       flag("stdin_eof", p.StdinEof),
		Env:            flag("env", p.Env),
		Cwd:            flag("cwd", p.Cwd),
		LinkHealth:     flag("link_health", p.LinkHealth),
		Kill:           enumFromProto(killBehaviorPairs, p.GetKill()),
		CwdFact:        enumFromProto(factModePairs, p.GetCwdFact()),
		ForegroundFact: enumFromProto(factModePairs, p.GetForegroundFact()),
		HostRestart:    enumFromProto(survivalPairs, p.GetHostRestart()),
		ExitReport:     enumFromProto(exitReportPairs, p.GetExitReport()),
		Stall:          enumFromProto(stallBehaviorPairs, p.GetStall()),
	}
	if p.GetSignals() == nil {
		absent = append(absent, "signals")
	}
	if p.GetIntegration() == nil {
		absent = append(absent, "integration")
	}
	if len(absent) > 0 {
		return Capabilities{}, fmt.Errorf("capabilities are not complete: %s absent", strings.Join(absent, ", "))
	}
	c.Signals = setFromProto(signalPairs, p.GetSignals().GetSignals())
	c.Integration = setFromProto(integrationModePairs, p.GetIntegration().GetModes())
	return c, nil
}

// setFromProto reads a list of wire values into the Go values this build names, in the wire's order and once each. The
// Go zero value of an enum is its unsaid or its none value, which a list never holds, so it is also what a wire value
// that this build does not know reads as: it is left out.
func setFromProto[G ~int, W ~int32](pairs []enumPair[G, W], wire []W) []G {
	var out []G
	for _, w := range wire {
		if g := enumFromProto(pairs, w); g != 0 && !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out
}

func sessionLimitsToProto(l SessionLimits) *execv2.SessionLimits {
	return &execv2.SessionLimits{
		MaxSessions:        uint32(l.MaxSessions),                         //nolint:gosec // validated within its ceiling
		MaxInputFrame:      uint32(l.MaxInputFrame),                       //nolint:gosec // validated within its ceiling
		ReplayBytes:        uint32(l.ReplayBytes),                         //nolint:gosec // validated within its ceiling
		MaxReplayBytes:     uint32(l.MaxReplayBytes),                      //nolint:gosec // validated within its ceiling
		DetachTimeoutMs:    uint32(l.DetachTimeout / time.Millisecond),    //nolint:gosec // validated within its ceiling
		MaxDetachTimeoutMs: uint32(l.MaxDetachTimeout / time.Millisecond), //nolint:gosec // validated within its ceiling
		MaxDimension:       uint32(l.MaxDimension),                        //nolint:gosec // validated within its ceiling
	}
}

func sessionLimitsFromProto(p *execv2.SessionLimits) SessionLimits {
	return SessionLimits{
		MaxSessions:      clampInt(p.GetMaxSessions()),
		MaxInputFrame:    clampInt(p.GetMaxInputFrame()),
		ReplayBytes:      clampInt(p.GetReplayBytes()),
		MaxReplayBytes:   clampInt(p.GetMaxReplayBytes()),
		DetachTimeout:    time.Duration(p.GetDetachTimeoutMs()) * time.Millisecond,
		MaxDetachTimeout: time.Duration(p.GetMaxDetachTimeoutMs()) * time.Millisecond,
		MaxDimension:     clampInt(p.GetMaxDimension()),
	}
}

func runLimitsToProto(l RunLimits) *execv2.RunLimits {
	return &execv2.RunLimits{
		DefaultTimeoutMs:   uint32(l.DefaultTimeout / time.Millisecond), //nolint:gosec // validated within its ceiling
		MaxTimeoutMs:       uint32(l.MaxTimeout / time.Millisecond),     //nolint:gosec // validated within its ceiling
		DefaultOutputBytes: uint32(l.DefaultOutputBytes),                //nolint:gosec // validated within its ceiling
		MaxOutputBytes:     uint32(l.MaxOutputBytes),                    //nolint:gosec // validated within its ceiling
		MaxStdinBytes:      uint32(l.MaxStdinBytes),                     //nolint:gosec // validated within its ceiling
		MaxConcurrent:      uint32(l.MaxConcurrent),                     //nolint:gosec // validated within its ceiling
		MaxArgvBytes:       uint32(l.MaxArgvBytes),                      //nolint:gosec // validated within its ceiling
		MaxEnvBytes:        uint32(l.MaxEnvBytes),                       //nolint:gosec // validated within its ceiling
		PostExitDrainMs:    uint32(l.PostExitDrain / time.Millisecond),  //nolint:gosec // validated within its ceiling
	}
}

func runLimitsFromProto(p *execv2.RunLimits) RunLimits {
	return RunLimits{
		DefaultTimeout:     time.Duration(p.GetDefaultTimeoutMs()) * time.Millisecond,
		MaxTimeout:         time.Duration(p.GetMaxTimeoutMs()) * time.Millisecond,
		DefaultOutputBytes: clampInt(p.GetDefaultOutputBytes()),
		MaxOutputBytes:     clampInt(p.GetMaxOutputBytes()),
		MaxStdinBytes:      clampInt(p.GetMaxStdinBytes()),
		MaxConcurrent:      clampInt(p.GetMaxConcurrent()),
		MaxArgvBytes:       clampInt(p.GetMaxArgvBytes()),
		MaxEnvBytes:        clampInt(p.GetMaxEnvBytes()),
		PostExitDrain:      time.Duration(p.GetPostExitDrainMs()) * time.Millisecond,
	}
}

// clampInt converts a wire uint32 to int; on a platform where int is 32 bits a value above the int range reads
// as the largest int, which no limit validation accepts.
func clampInt(v uint32) int {
	if uint64(v) > uint64(^uint(0)>>1) {
		return int(^uint(0) >> 1)
	}
	return int(v)
}
