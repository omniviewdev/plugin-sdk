package exec

import (
	"fmt"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Option is one dimension a user chooses along before running a command in a resource ("container") and its
// choices. It is what a target's OptionsFunc returns and what Options answers.
type Option struct {
	// Name is the selection key, a closed code matching ^[a-z0-9_.-]{1,40}$ ("container"). Unique among the
	// options of a resource.
	Name string
	// Required means a request for this resource must carry the option in its selection or have a default choice.
	Required bool
	// Choices lists what can be picked, at most 256. Empty means the resource has nothing to choose now.
	Choices []Choice
}

// Choice is one thing a user can pick for an option (one container of a Pod).
type Choice struct {
	// Value is the string a request's selection carries, at most 256 bytes. Unique within the option.
	Value string
	// DisplayName is presentation data, at most 256 bytes of UTF-8 without control characters; empty means show
	// the value.
	DisplayName string
	// State is what the choice can do now. ChoiceStateUnspecified means the producer does not know.
	State ChoiceState
	// Detail is a closed code that refines State ("crash-loop", "terminated"), matching ^[a-z0-9_.-]{1,40}$,
	// never prose; empty means none.
	Detail string
	// IsDefault marks the choice a request runs in when its selection omits the option. At most one choice of an
	// option.
	IsDefault bool
	// Kind is a closed code for the kind of choice ("container", "init", "ephemeral"), matching
	// ^[a-z0-9_.-]{1,40}$; empty means unspecified.
	Kind string
	// Capabilities is the mode of the choice: the complete set of capabilities a session that selects it is served
	// with, declared before anything opens. Copy the kind's (TargetInfo.Capabilities, the upper bound of every
	// session) and narrow the copy, the way a session narrows Proc.Capabilities: every field only down, in the order
	// the proto writes at it (see Capabilities). A Kubernetes target declares ExitReportNone on its attach choice and
	// SurvivalNone on its exec choice, and its function never reports the mode.
	//
	// The SDK applies it at admission: Opened carries the kind's capabilities narrowed by the choice that each option
	// takes in the request's selection (the default choice of an option the selection omits), and Proc.Capabilities
	// starts from that set. A host reads it with the options, so it can show "attach: no exit status, survives a
	// host restart" before it opens anything. Nil means the choice does not narrow. A field above the kind's is the
	// author's bug: the SDK serves the kind's value for it and names the field in the plugin's log. A set built from
	// nothing declares that a session can do nothing, except for Kill, ExitReport and Stall, which keep the kind's
	// value when left unset.
	Capabilities *Capabilities
}

// closedCode reports whether s is a closed code: 1 to 40 bytes of [a-z0-9_.-].
func closedCode(s string) bool {
	if len(s) < 1 || len(s) > maxCodeBytes {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// validateOptions checks the options an author's callback returned, or a plugin's response, against the bounds of
// the contract. The returned error describes the first violation; it is local text and never crosses the wire.
func validateOptions(options []Option) error {
	if len(options) > maxOptions {
		return fmt.Errorf("%d options exceed the limit of %d", len(options), maxOptions)
	}
	names := make(map[string]struct{}, len(options))
	for i := range options {
		o := &options[i]
		if !closedCode(o.Name) {
			return fmt.Errorf("option %d: name %q is not a closed code", i, o.Name)
		}
		if _, dup := names[o.Name]; dup {
			return fmt.Errorf("option %q is listed twice", o.Name)
		}
		names[o.Name] = struct{}{}
		if err := validateChoices(o); err != nil {
			return err
		}
	}
	return nil
}

func validateChoices(o *Option) error {
	if len(o.Choices) > maxChoices {
		return fmt.Errorf("option %q: %d choices exceed the limit of %d", o.Name, len(o.Choices), maxChoices)
	}
	values := make(map[string]struct{}, len(o.Choices))
	defaults := 0
	for i := range o.Choices {
		c := &o.Choices[i]
		if len(c.Value) > maxSelectionValue || !validText(c.Value) {
			return fmt.Errorf("option %q choice %d: value is too long or not plain text", o.Name, i)
		}
		if _, dup := values[c.Value]; dup {
			return fmt.Errorf("option %q: value %q is listed twice", o.Name, c.Value)
		}
		values[c.Value] = struct{}{}
		if len(c.DisplayName) > maxChoiceName || !validText(c.DisplayName) {
			return fmt.Errorf("option %q choice %q: display name is too long or not plain text", o.Name, c.Value)
		}
		if c.Detail != "" && !closedCode(c.Detail) {
			return fmt.Errorf("option %q choice %q: detail is not a closed code", o.Name, c.Value)
		}
		if c.Kind != "" && !closedCode(c.Kind) {
			return fmt.Errorf("option %q choice %q: kind is not a closed code", o.Name, c.Value)
		}
		if c.IsDefault {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("option %q: %d choices are the default", o.Name, defaults)
	}
	return nil
}

func optionsToProto(options []Option) []*execv2.Option {
	out := make([]*execv2.Option, 0, len(options))
	for i := range options {
		o := &options[i]
		wire := &execv2.Option{Name: o.Name, Required: o.Required, Choices: make([]*execv2.Choice, 0, len(o.Choices))}
		for j := range o.Choices {
			c := &o.Choices[j]
			wireChoice := &execv2.Choice{
				Value:       c.Value,
				DisplayName: c.DisplayName,
				State:       c.State.toProto(),
				Detail:      c.Detail,
				IsDefault:   c.IsDefault,
				Kind:        c.Kind,
			}
			if c.Capabilities != nil {
				wireChoice.Capabilities = capabilitiesToProto(*c.Capabilities)
			}
			wire.Choices = append(wire.Choices, wireChoice)
		}
		out = append(out, wire)
	}
	return out
}

// optionsFromProto decodes the options of a response. It is lenient about values this build does not know (a state
// reads as ChoiceStateUnspecified) and strict about bounds: the result is validated, and a violation is an error. A
// choice's capabilities must be complete, because a Go Capabilities cannot say that a flag is not known (see
// capabilitiesFromProto).
func optionsFromProto(wire []*execv2.Option) ([]Option, error) {
	if len(wire) > maxOptions {
		return nil, fmt.Errorf("%d options exceed the limit of %d", len(wire), maxOptions)
	}
	out := make([]Option, 0, len(wire))
	for _, w := range wire {
		if len(w.GetChoices()) > maxChoices {
			return nil, fmt.Errorf("option %q: %d choices exceed the limit of %d", w.GetName(), len(w.GetChoices()), maxChoices)
		}
		o := Option{Name: w.GetName(), Required: w.GetRequired(), Choices: make([]Choice, 0, len(w.GetChoices()))}
		for _, c := range w.GetChoices() {
			choice := Choice{
				Value:       c.GetValue(),
				DisplayName: c.GetDisplayName(),
				State:       choiceStateFromProto(c.GetState()),
				Detail:      c.GetDetail(),
				IsDefault:   c.GetIsDefault(),
				Kind:        c.GetKind(),
			}
			if wireCaps := c.GetCapabilities(); wireCaps != nil {
				caps, err := capabilitiesFromProto(wireCaps)
				if err != nil {
					return nil, fmt.Errorf("option %q choice %q: %w", w.GetName(), c.GetValue(), err)
				}
				choice.Capabilities = &caps
			}
			o.Choices = append(o.Choices, choice)
		}
		out = append(out, o)
	}
	if err := validateOptions(out); err != nil {
		return nil, err
	}
	return out, nil
}
