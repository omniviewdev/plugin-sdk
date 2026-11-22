package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/testutil"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// The example runs the conformance suite against itself: what an author does in their own go test.
func TestEchoHostConforms(t *testing.T) {
	exectest.Conformance(t, echoHost())
}

// The command copies its input to its output and ends with its own status 0.
func TestEchoHostEchoes(t *testing.T) {
	declared := echoHost().Info.Capabilities
	rig := exectest.NewProcRig(t, exec.Request{Argv: []string{"cat"}}, exectest.RigCapabilities(declared))
	done := make(chan struct{})
	var (
		exit exec.Exit
		err  error
	)
	go func() {
		defer close(done)
		exit, err = rig.Run(context.Background(), echoHost().Exec)
	}()
	if _, werr := rig.Input().Write([]byte("hello")); werr != nil {
		t.Fatal(werr)
	}
	out, werr := rig.WaitStdout(context.Background(), len("hello"))
	if werr != nil || string(out) != "hello" {
		t.Fatalf("output %q, %v", out, werr)
	}
	_ = rig.Input().Close() // stdin EOF ends the command
	<-done
	if err != nil || exit.Code == nil || *exit.Code != 0 || exit.Source != exec.SourceProcess {
		t.Fatalf("exit = %+v, err = %v", exit, err)
	}
	if len(rig.Started()) != 1 {
		t.Errorf("the command reported %d starts, want 1", len(rig.Started()))
	}
	if !reflect.DeepEqual(rig.Effective(), declared) {
		t.Errorf("a report with no capabilities changed the session's: %+v, declared %+v", rig.Effective(), declared)
	}
}

// ExitCode takes any integer type, and a Windows DWORD arrives exactly, as the guide says.
func TestExitCodeCarriesEveryPlatformStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		exit exec.Exit
		want int64
	}{
		"uint32 DWORD":    {exec.ExitCode(uint32(0xC0000005)), 0xC0000005},
		"the same, int32": {exec.ExitCode(int32(-1073741819)), -1073741819},
		"posix":           {exec.ExitCode(137), 137},
	} {
		if tc.exit.Code == nil || *tc.exit.Code != tc.want || tc.exit.Source != exec.SourceProcess {
			t.Errorf("%s: %+v, want code %d from the process", name, tc.exit, tc.want)
		}
	}
}

// The plugin.yaml the example ships is one NewPlugin accepts, and the example registers on it.
func TestShippedManifestLoads(t *testing.T) {
	manifest, err := os.ReadFile("plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	home, root := t.TempDir(), t.TempDir()
	testutil.SetHome(t, home)
	t.Setenv("OMNIVIEW_STATE_DIR", root)
	t.Setenv("OMNIVIEW_WORKSPACE_FILE", "")
	dir := filepath.Join(root, "plugins", "echo-exec")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := logging.Default()
	t.Cleanup(func() { logging.SetDefault(previous) })

	plugin := sdk.NewPlugin(sdk.PluginOpts{ID: "echo-exec"})
	if err := exec.RegisterPlugin(plugin, exec.PluginOpts{Targets: []exec.Target{echoHost()}}); err != nil {
		t.Fatal(err)
	}
	if plugin.GetMeta().ID != "echo-exec" {
		t.Errorf("manifest id = %q", plugin.GetMeta().ID)
	}
}

// The guide prints this directory's files as they are: main.go, plugin.yaml and the minimal ExecFunc. They must not
// drift apart, and main.go keeps the length the guide promises.
func TestTheGuideShowsTheExample(t *testing.T) {
	source := readFile(t, "main.go")
	if lines := strings.Count(source, "\n"); lines >= 50 {
		t.Errorf("the example is %d lines; the guide promises under 50", lines)
	}
	guide := readFile(t, "../../docs/exec.md")
	for name, part := range map[string]string{
		"main.go":     strings.TrimSpace(source),
		"plugin.yaml": strings.TrimSpace(readFile(t, "plugin.yaml")),
	} {
		if !strings.Contains(guide, part) {
			t.Errorf("docs/exec.md does not contain examples/exec-plugin/%s verbatim", name)
		}
	}

	minimal := funcSource(t, source, "func echo(")
	if lines := strings.Count(minimal, "\n") + 1; lines >= 20 {
		t.Errorf("the minimal ExecFunc is %d lines; the guide promises under 20", lines)
	}
	if !strings.Contains(guide, minimal) {
		t.Error("docs/exec.md does not contain the echo function of main.go verbatim")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// funcSource returns a top-level function of source, from its opening line to its closing brace.
func funcSource(t *testing.T, source, opening string) string {
	t.Helper()
	start := strings.Index(source, opening)
	if start < 0 {
		t.Fatalf("no %q in the source", opening)
	}
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("%q is not closed", opening)
	}
	return source[start : start+end+len("\n}")]
}

// guideBlock returns the lines of a file between "// guide:begin name" and "// guide:end name", which the guide
// prints verbatim in a code block.
func guideBlock(t *testing.T, path, name string) string {
	t.Helper()
	source := readFile(t, path)
	begin, end := "// guide:begin "+name+"\n", "// guide:end "+name+"\n"
	start := strings.Index(source, begin)
	stop := strings.Index(source, end)
	if start < 0 || stop < start {
		t.Fatalf("%s has no guide block %q", path, name)
	}
	return strings.TrimSpace(source[start+len(begin) : stop])
}

// assertGuideShows fails unless docs/exec.md contains the block of a file verbatim.
func assertGuideShows(t *testing.T, path, name string) {
	t.Helper()
	block := guideBlock(t, path, name)
	if !strings.Contains(readFile(t, "../../docs/exec.md"), block) {
		t.Errorf("docs/exec.md does not contain the %q block of examples/exec-plugin/%s verbatim", name, path)
	}
}
