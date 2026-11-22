package exec

import (
	"slices"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
)

// exectest.ProcRig opens a command with the capabilities the SDK puts in Opened. The rule is not exported, so it is
// handed over through internal/exechook, which is the only place that knows it.
func init() { exechook.SetOpenedCapabilities(openedCapabilities) }

// openedCapabilities returns the capabilities a session is opened with: what Opened carries and what the author's
// function starts from (Proc.Capabilities). It starts from declared, the kind's upper bound (TargetInfo.Capabilities),
// and narrows it by the Capabilities of the choice that each option takes in the request's selection: the choice the
// selection names, or the default choice (IsDefault) of an option the selection omits. An option with no such choice
// narrows nothing, and so does a choice with no Capabilities.
//
// A value that names no choice of its option (admission refuses the request elsewhere) narrows nothing: it is ignored,
// and it does not stand for the default choice. A selection key that names no option is ignored.
//
// A choice cannot widen the kind: what it declares above the set it narrows is replaced by that set, which is the rule
// of narrowCapabilities, and not reported here (clampChoiceCapabilities reports it where the options are served). The
// result is the narrowest set that the kind and every chosen choice allow, so it does not depend on the order of the
// options; a set (Signals, Integration) is written in the order the kind declared it, so that the order of a choice's own
// list does not matter either.
//
// The result shares no storage with the arguments, which the call does not change.
func openedCapabilities(declared Capabilities, options []Option, selection map[string]string) Capabilities {
	opened := cloneCapabilities(declared)
	for i := range options {
		choice := chosenChoice(&options[i], selection)
		if choice == nil || choice.Capabilities == nil {
			continue
		}
		opened, _ = narrowCapabilities(opened, *choice.Capabilities)
	}
	opened.Signals = inOrderOf(declared.Signals, opened.Signals)
	opened.Integration = inOrderOf(declared.Integration, opened.Integration)
	return opened
}

// chosenChoice returns the choice of an option that a selection takes: the one it names, or the option's default choice
// when the selection omits the option. It returns nil when the selection names a value the option does not have, or when
// it omits an option that has no default.
func chosenChoice(o *Option, selection map[string]string) *Choice {
	if value, named := selection[o.Name]; named {
		for i := range o.Choices {
			if o.Choices[i].Value == value {
				return &o.Choices[i]
			}
		}
		return nil
	}
	for i := range o.Choices {
		if o.Choices[i].IsDefault {
			return &o.Choices[i]
		}
	}
	return nil
}

// inOrderOf returns the members of kept that order lists, in the order of order and once each. The result is nil when it
// is empty.
func inOrderOf[T comparable](order, kept []T) []T {
	var out []T
	for _, v := range order {
		if slices.Contains(kept, v) && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// choiceViolation is one field of one choice that the author declared above the kind.
type choiceViolation struct {
	option, choice string
	// text names the field and the values, as narrowCapabilities writes it.
	text string
}

// clampChoiceCapabilities holds the capabilities that the choices of an author's options declare to the kind's upper
// bound with the rule of narrowCapabilities: a field declared above the kind's is replaced by the kind's value, and is
// returned as a violation that names the option, the choice and the field. The author's options are not changed: when
// nothing is above the bound the result is options itself, and otherwise it is a copy that shares only what it did not
// change.
func clampChoiceCapabilities(bound Capabilities, options []Option) (clamped []Option, violations []choiceViolation) {
	clamped = options
	copiedOptions, copiedChoices := false, -1
	for i := range options {
		for j := range options[i].Choices {
			choice := &options[i].Choices[j]
			if choice.Capabilities == nil {
				continue
			}
			effective, found := narrowCapabilities(bound, *choice.Capabilities)
			if len(found) == 0 {
				continue
			}
			if !copiedOptions {
				clamped, copiedOptions = slices.Clone(options), true
			}
			if copiedChoices != i {
				clamped[i].Choices, copiedChoices = slices.Clone(options[i].Choices), i
			}
			clamped[i].Choices[j].Capabilities = &effective
			for _, text := range found {
				violations = append(violations, choiceViolation{option: options[i].Name, choice: choice.Value, text: text})
			}
		}
	}
	return clamped, violations
}
