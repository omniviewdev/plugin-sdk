package exec

import (
	"errors"
	"maps"
	"slices"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
)

// ProcRig resolves the mode of its command with the same rule as the engine, which is handed over through internal/exechook.
func init() { exechook.SetResolvedMode(resolvedModeOf) }

// resolvedMode is what the options of a resource and the request's selection decide about a command before its function runs, besides
// the capabilities of its session (openedCapabilities): the choice of every option, and whether the session runs a program.
type resolvedMode struct {
	// opened is what Opened carries: the kind's capabilities narrowed by the choices the selection names.
	opened Capabilities
	// selection is the choice of every option that the request resolved to: the one it selected, or the default choice of an option it
	// omitted. It is what the function sees as Request.Selection.
	selection map[string]string
	// noProgram is true when the choice of some option runs no program (Choice.NoProgram): the session has no program of its own.
	noProgram bool
}

// resolvedModeOf returns the choice of every option that a selection resolves to, and whether the session runs no program. The resolved
// selection is the request's own (a name the options do not have is kept as it is: admission refuses it, and a test rig does not) with the
// default choice of every option the selection omits and that has one; an option with neither is absent. It is the rule the rig of
// exectest applies too, through internal/exechook.
func resolvedModeOf(options []Option, selection map[string]string) (resolved map[string]string, noProgram bool) {
	resolved = maps.Clone(selection)
	for i := range options {
		o := &options[i]
		if _, chosen := selection[o.Name]; !chosen {
			if d := slices.IndexFunc(o.Choices, func(c Choice) bool { return c.IsDefault }); d >= 0 {
				if resolved == nil {
					resolved = map[string]string{}
				}
				resolved[o.Name] = o.Choices[d].Value
			}
		}
		if choice := chosenChoice(o, selection); choice != nil && choice.NoProgram {
			noProgram = true
		}
	}
	return resolved, noProgram
}

// noProgramChoice returns the option and the value of the first choice that the selection takes and that runs no program.
func noProgramChoice(options []Option, selection map[string]string) (option, value string, found bool) {
	for i := range options {
		if choice := chosenChoice(&options[i], selection); choice != nil && choice.NoProgram {
			return options[i].Name, choice.Value, true
		}
	}
	return "", "", false
}

// resolveMode holds the request's selection to the options of its resource and decides the mode of the command: the capabilities it is
// opened with, the choice of every option, and whether it runs a program. A selection that names something the options do not offer, or
// omits a required option that has no default, is a refusal, as is a request that names a program for a choice that runs none.
//
// A choice that runs no program (an attach joins the container's own process) cannot honor an argv, and an argv is never ignored: a
// request that names one is refused UNSUPPORTED, and so is a run, which is a program to run. An argv that equals the target's default
// is what a request with none is given, which is not told apart from one the host sent: it is dropped.
func (c *command) resolveMode(options []Option) (resolvedMode, error) {
	if refusal := checkSelection(options, c.request.Selection); refusal != nil {
		return resolvedMode{}, refusal
	}
	selection, noProgram := resolvedModeOf(options, c.request.Selection)
	if noProgram {
		option, value, _ := noProgramChoice(options, c.request.Selection)
		facts := ErrorFacts{SelectionName: option, SelectionValue: value}
		switch {
		case c.kind == kindRun:
			return resolvedMode{}, &Error{Class: ErrorClassUnsupported, Facts: facts, Err: errors.New("the choice runs no program, and a run is a program to run")}
		case len(c.request.Argv) > 0 && !slices.Equal(c.request.Argv, c.target.info.DefaultArgv):
			return resolvedMode{}, &Error{Class: ErrorClassUnsupported, Facts: facts, Err: errors.New("the choice runs no program, and the request names an argv of its own")}
		}
	}
	return resolvedMode{
		opened:    openedCapabilities(c.target.info.Capabilities, options, c.request.Selection),
		selection: selection,
		noProgram: noProgram,
	}, nil
}

// functionRequest is the request the function receives: the validated request, with the choice of every option that it resolved to as
// its selection and with no argv when the choice runs no program. Before the hand-over it is the validated request as it is. The result
// is the caller's own copy.
func (c *command) functionRequest() Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.functionRequestLocked()
}

// functionRequestLocked is functionRequest for a caller that holds c.mu.
func (c *command) functionRequestLocked() Request {
	r := cloneRequest(c.request)
	if c.phase == phaseRunning {
		r.Selection = maps.Clone(c.selection)
		if c.noProgram {
			r.Argv = nil
		}
	}
	return r
}
