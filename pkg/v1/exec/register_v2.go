package exec

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// routeKey is the routing key of a target: the resource provider and type a request's reference must carry, the
// Provider and Type of its Info. It is a pair, not the string Provider + "/" + Type, because a resource type may
// itself contain a slash.
type routeKey struct{ provider, resourceType string }

// registeredTarget is a Target as the registry holds it: a defensive copy whose info is effective (the limits
// default-filled), so that nothing the author does later changes what was described.
type registeredTarget struct {
	target Target
	info   TargetInfo
}

// runsCommands reports whether the target serves interactive sessions or runs, as opposed to only Options.
func (t *registeredTarget) runsCommands() bool {
	return !t.target.Info.Session.IsZero() || !t.target.Info.Run.IsZero()
}

// targetRegistry holds the validated targets of a plugin, in registration order.
type targetRegistry struct {
	targets []*registeredTarget
	byKey   map[routeKey]*registeredTarget
}

// newTargetRegistry validates targets and builds the registry. It refuses a target with a missing routing key or
// Exec, a duplicate routing key, a capability or limit that breaks the contract, and a target that runs commands
// without declaring how it ends and stalls and what ending it does.
func newTargetRegistry(targets []Target) (*targetRegistry, error) {
	if len(targets) > maxTargets {
		return nil, fmt.Errorf("%d exec targets exceed the limit of %d", len(targets), maxTargets)
	}
	r := &targetRegistry{byKey: make(map[routeKey]*registeredTarget, len(targets))}
	for i := range targets {
		t, err := prepareTarget(targets[i])
		if err != nil {
			return nil, fmt.Errorf("exec target %d (%q): %w", i, targets[i].key(), err)
		}
		key := routeKey{provider: t.info.Provider, resourceType: t.info.Type}
		if _, dup := r.byKey[key]; dup {
			return nil, fmt.Errorf("duplicate exec target key %q", t.target.key())
		}
		r.byKey[key] = t
		r.targets = append(r.targets, t)
	}
	return r, nil
}

// lookup returns the target registered for a resource provider and type.
func (r *targetRegistry) lookup(provider, resourceType string) (*registeredTarget, bool) {
	t, ok := r.byKey[routeKey{provider: provider, resourceType: resourceType}]
	return t, ok
}

// prepareTarget validates one target and returns its registered form.
func prepareTarget(t Target) (*registeredTarget, error) {
	if err := validateRoutingPart("Info.Provider", t.Info.Provider); err != nil {
		return nil, err
	}
	if err := validateRoutingPart("Info.Type", t.Info.Type); err != nil {
		return nil, err
	}
	if t.Exec == nil {
		return nil, errors.New("the Exec function is required")
	}
	info := cloneInfo(t.Info)
	if len(info.DisplayName) > maxDisplayName || !validText(info.DisplayName) {
		return nil, fmt.Errorf("Info.DisplayName is too long or not plain text")
	}
	if err := validateDefaultArgv(info.DefaultArgv); err != nil {
		return nil, err
	}
	if err := validateCapabilities(info.Capabilities); err != nil {
		return nil, err
	}
	registered := &registeredTarget{target: t, info: info}
	registered.target.Info = info
	if err := applyLimits(registered); err != nil {
		return nil, err
	}
	return registered, nil
}

// applyLimits default-fills and validates the limits of a target, and checks that a target that runs commands has
// declared the behaviors that decide how its sessions and runs are read.
func applyLimits(t *registeredTarget) error {
	declaredSession, declaredRun := !t.info.Session.IsZero(), !t.info.Run.IsZero()
	if declaredSession {
		t.info.Session = t.info.Session.withDefaults()
		if err := t.info.Session.validate(false); err != nil {
			return err
		}
	}
	if declaredRun {
		t.info.Run = t.info.Run.withDefaults()
		if err := t.info.Run.validate(false); err != nil {
			return err
		}
	}
	if declaredSession || declaredRun {
		caps := t.info.Capabilities
		var missing []string
		if caps.ExitReport == ExitReportUnspecified {
			missing = append(missing, "ExitReport")
		}
		if caps.Stall == StallUnspecified {
			missing = append(missing, "Stall")
		}
		if caps.Kill == KillUnspecified {
			missing = append(missing, "Kill")
		}
		if len(missing) > 0 {
			return fmt.Errorf("a target that runs commands must declare %s in Capabilities: "+
				"they decide how its end is read, what a stalled reader does to it and what closing it does to the process",
				strings.Join(missing, ", "))
		}
	}
	t.target.Info = t.info
	return nil
}

func validateRoutingPart(name, value string) error {
	if value == "" || len(value) > maxChoiceName || !validText(value) {
		return fmt.Errorf("%s must be 1 to %d bytes of plain text", name, maxChoiceName)
	}
	return nil
}

func validateDefaultArgv(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	if len(argv) > maxDefaultArgv {
		return fmt.Errorf("Info.DefaultArgv has %d elements, at most %d", len(argv), maxDefaultArgv)
	}
	if argv[0] == "" {
		return fmt.Errorf("Info.DefaultArgv[0] must not be empty")
	}
	total := 0
	for i, a := range argv {
		if len(a) > maxArgvElementBytes || strings.ContainsRune(a, 0) {
			return fmt.Errorf("Info.DefaultArgv[%d] is too long or holds a NUL", i)
		}
		if !utf8.ValidString(a) {
			return fmt.Errorf("Info.DefaultArgv[%d] is not valid UTF-8", i)
		}
		total += len(a)
	}
	if total > defaultArgvBytes {
		return fmt.Errorf("Info.DefaultArgv is %d bytes, at most %d", total, defaultArgvBytes)
	}
	return nil
}

// definedValue reports whether g is the zero value or has a wire pair: a Go value that is neither (an arbitrary
// int converted to the type) is not a value of the enum.
func definedValue[G ~int, W ~int32](pairs []enumPair[G, W], g G) bool {
	if g == 0 {
		return true
	}
	for _, p := range pairs {
		if p.g == g {
			return true
		}
	}
	return false
}

func validateCapabilities(c Capabilities) error {
	checks := []struct {
		name string
		ok   bool
	}{
		{"Kill", definedValue(killBehaviorPairs, c.Kill)},
		{"CwdFact", definedValue(factModePairs, c.CwdFact)},
		{"ForegroundFact", definedValue(factModePairs, c.ForegroundFact)},
		{"HostRestart", definedValue(survivalPairs, c.HostRestart)},
		{"ExitReport", definedValue(exitReportPairs, c.ExitReport)},
		{"Stall", definedValue(stallBehaviorPairs, c.Stall)},
	}
	for _, check := range checks {
		if !check.ok {
			return fmt.Errorf("target Capabilities.%s is not a defined value", check.name)
		}
	}
	if len(c.Signals) > maxSignals {
		return fmt.Errorf("Capabilities.Signals lists %d signals, at most %d", len(c.Signals), maxSignals)
	}
	seen := map[Signal]struct{}{}
	for _, s := range c.Signals {
		if s == SignalUnspecified || !definedValue(signalPairs, s) {
			return fmt.Errorf("Capabilities.Signals lists %d, which is not a deliverable signal", int(s))
		}
		if _, dup := seen[s]; dup {
			return fmt.Errorf("Capabilities.Signals lists %s twice", s)
		}
		seen[s] = struct{}{}
	}
	if len(c.Integration) > 0 && !c.TTY {
		return fmt.Errorf("Capabilities.Integration needs Capabilities.TTY: integration is a shell's, and a shell has a terminal")
	}
	modes := map[IntegrationMode]struct{}{}
	for _, m := range c.Integration {
		if m == IntegrationOff || !definedValue(integrationModePairs, m) {
			return fmt.Errorf("Capabilities.Integration lists %d, which is not an integration mode a target accepts", int(m))
		}
		if _, dup := modes[m]; dup {
			return fmt.Errorf("Capabilities.Integration lists %s twice", m)
		}
		modes[m] = struct{}{}
	}
	return nil
}

// cloneInfo copies the slices of an info so that the registry never shares them with the author.
func cloneInfo(i TargetInfo) TargetInfo {
	i.DefaultArgv = slices.Clone(i.DefaultArgv)
	i.Capabilities = cloneCapabilities(i.Capabilities)
	return i
}

func cloneCapabilities(c Capabilities) Capabilities {
	c.Signals = slices.Clone(c.Signals)
	c.Integration = slices.Clone(c.Integration)
	return c
}
