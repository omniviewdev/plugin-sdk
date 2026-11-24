package exectest_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// The rig hands the function the request as the SDK does: the selection holds the choice of every option, the default choice of an
// option the request omitted included, and a choice that runs no program leaves no argv. An author's test of an ExecFunc that reads
// Request.Selection must see what the function sees in production.
func TestProcRigGivesTheFunctionTheResolvedSelectionAndNoArgvForAChoiceThatRunsNoProgram(t *testing.T) {
	options := []exec.Option{
		{Name: "container", Required: true, Choices: []exec.Choice{{Value: "app", IsDefault: true}, {Value: "sidecar"}}},
		{Name: "mode", Choices: []exec.Choice{{Value: "exec", IsDefault: true}, {Value: "attach", NoProgram: true}}},
		{Name: "image", Choices: []exec.Choice{{Value: "debian"}}},
	}
	for _, tc := range []struct {
		name     string
		in       map[string]string
		want     map[string]string
		wantArgv []string
	}{
		{"nothing selected", nil, map[string]string{"container": "app", "mode": "exec"}, []string{"/bin/sh"}},
		{"an attach", map[string]string{"mode": "attach"}, map[string]string{"container": "app", "mode": "attach"}, nil},
		{"a choice with no default that was selected", map[string]string{"image": "debian", "container": "sidecar"},
			map[string]string{"container": "sidecar", "mode": "exec", "image": "debian"}, []string{"/bin/sh"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := exectest.NewProcRig(t, exec.Request{Argv: []string{"/bin/sh"}, Selection: tc.in}, exectest.RigOptions(options...))
			got := rig.Proc().Request
			if !maps.Equal(got.Selection, tc.want) {
				t.Errorf("the function sees the selection %v, want %v", got.Selection, tc.want)
			}
			if !slices.Equal(got.Argv, tc.wantArgv) {
				t.Errorf("the function sees the argv %v, want %v", got.Argv, tc.wantArgv)
			}
			if tc.in != nil && maps.Equal(tc.in, got.Selection) && len(tc.want) != len(tc.in) {
				t.Error("the rig changed the selection of the request it was given")
			}
		})
	}
	// Without options the request is the one the test gave.
	rig := exectest.NewProcRig(t, exec.Request{Argv: []string{"ls"}, Selection: map[string]string{"x": "y"}})
	if got := rig.Proc().Request; !maps.Equal(got.Selection, map[string]string{"x": "y"}) || !slices.Equal(got.Argv, []string{"ls"}) {
		t.Errorf("a rig with no options changed the request: %+v", got)
	}
}
