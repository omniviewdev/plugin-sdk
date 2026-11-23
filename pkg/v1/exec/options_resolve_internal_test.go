package exec

import (
	"maps"
	"slices"
	"testing"
)

// The request that a function receives is its own: what it does to its selection or its argv reaches neither the command nor the next
// call, and the selection of the request that the host sent is not changed by the defaults that the SDK resolved into it.
func TestTheFunctionsRequestSharesNoStorageWithTheCommand(t *testing.T) {
	c := &command{
		request:   Request{Selection: map[string]string{"container": "sidecar"}, Argv: []string{"/bin/sh"}},
		selection: map[string]string{"container": "sidecar", "mode": "exec"},
		phase:     phaseRunning,
	}
	first := c.functionRequest()
	first.Selection["container"] = "mutated"
	first.Selection["added"] = "x"
	first.Argv[0] = "mutated"
	again := c.functionRequest()
	if !maps.Equal(again.Selection, map[string]string{"container": "sidecar", "mode": "exec"}) || !slices.Equal(again.Argv, []string{"/bin/sh"}) {
		t.Errorf("a second request is %+v after the function changed the first: the two share storage", again)
	}
	if !maps.Equal(c.selection, map[string]string{"container": "sidecar", "mode": "exec"}) || !maps.Equal(c.request.Selection, map[string]string{"container": "sidecar"}) {
		t.Errorf("the command holds %v and %v after the function changed its request", c.selection, c.request.Selection)
	}
	if c.request.Argv[0] != "/bin/sh" {
		t.Errorf("the command's argv is %v after the function changed its own", c.request.Argv)
	}
}

// Before the hand-over a command has resolved nothing, so its request is the one that was admitted, as it was sent.
func TestACommandThatWasNotHandedOverHasItsRequestAsAdmitted(t *testing.T) {
	c := &command{
		request:   Request{Selection: map[string]string{"container": "app"}, Argv: []string{"ls"}},
		selection: map[string]string{"container": "app", "mode": "exec"},
		noProgram: true,
		phase:     phaseAdmitting,
	}
	got := c.functionRequest()
	if !maps.Equal(got.Selection, map[string]string{"container": "app"}) || !slices.Equal(got.Argv, []string{"ls"}) {
		t.Errorf("the request of a command not yet handed over is %+v, want what was admitted", got)
	}
}

// Resolving the selection never changes the selection it was given, and says whether a chosen choice runs no program.
func TestResolvingASelectionLeavesTheRequestsOwnAlone(t *testing.T) {
	options := []Option{
		{Name: "container", Choices: []Choice{{Value: "app", IsDefault: true}, {Value: "sidecar"}}},
		{Name: "mode", Choices: []Choice{{Value: "exec", IsDefault: true}, {Value: "attach", NoProgram: true}}},
	}
	asked := map[string]string{"mode": "attach"}
	resolved, noProgram := resolvedModeOf(options, asked)
	if !maps.Equal(asked, map[string]string{"mode": "attach"}) {
		t.Errorf("the request's selection became %v", asked)
	}
	if !maps.Equal(resolved, map[string]string{"container": "app", "mode": "attach"}) || !noProgram {
		t.Errorf("resolved %v, no program %v; want the default container, the attach and no program", resolved, noProgram)
	}
	if _, noProgram := resolvedModeOf(options, nil); noProgram {
		t.Error("the default choice (an exec) was read as one that runs no program")
	}
	if resolved, _ := resolvedModeOf(nil, nil); resolved != nil {
		t.Errorf("no options and no selection resolved to %v, want nothing", resolved)
	}
}
