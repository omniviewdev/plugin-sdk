package exec_test

// TestCompatV0210 proves, with real builds of SDK v0.21.0, that the exec v2 lane breaks nothing and that a new host
// reads an old plugin right. It builds
//
//   - oldplugin and oldhost against SDK v0.21.0 (in a module of their own, in a temporary directory), and
//   - newplugin against this tree,
//
// from the sources in testdata/compat-v0.21.0/helpers (oldplugin: one legacy handler compat/echo; newplugin: the
// same handler and a v2 target compat/echo on the same capability; oldhost: launches a plugin with go-plugin, runs the
// legacy scenario and prints its transcript), and runs the legacy scenario four ways:
//
//	T1  oldhost (v0.21.0)          -> oldplugin (v0.21.0)
//	T2  oldhost (v0.21.0)          -> newplugin (this tree)
//	T3  this tree's host (in test) -> oldplugin (v0.21.0)
//	T4  this tree's host (in test) -> newplugin (this tree)
//
// It requires the four transcripts (every request, response and stream frame of the legacy service, normalized, see
// scenario.Transcript) to be equal, and T1 to be equal to the golden file testdata/compat-v0.21.0/exec/legacy_transcript.json,
// which TestCompatFramesReplay replays against this tree on every run. T3 and T4 also probe the v2 lane: the new
// client gets a typed exec.ClassUnsupported from the v0.21.0 plugin, which answers Unimplemented, and reads
// compat/echo from newplugin's Describe. Every plugin process is stopped and must be gone when its host is done.
//
// The test is off by default: it builds SDK v0.21.0 and needs the Go module proxy or a warm module cache. A
// dedicated CI job runs it. Give it an explicit timeout of 90 minutes, as CI does: the fixture commands (the module
// download and the builds of the three helpers, on a cold cache) run one after the other and can take longer than
// the ten minutes go test allows by default:
//
//	OMNIVIEW_COMPAT_V0210=1 go test ./pkg/v1/exec/ -run '^TestCompatV0210$' -count=1 -v -timeout 90m
//
// OMNIVIEW_COMPAT_SDK_V0210=<dir> names a checkout of the tag v0.21.0 to build against instead of downloading the
// module. The checkout must be a git checkout at the commit the golden names (git rev-parse HEAD) with a clean tree
// (git status --porcelain prints nothing), or the test refuses it; and the build never reads the checkout itself: it
// reads a snapshot of that commit (git archive), so that no ignored, untracked or modified file can change the
// binaries behind the tag. OMNIVIEW_COMPAT_RECORD=1 (alone, it also turns the test on) records the
// golden file from T1 instead of comparing T1 with it; the other comparisons still run, and the golden is written
// only after T1 to T4 all passed (a failed matrix leaves the existing golden as it is). Re-record only for a
// compatible change of the scenario or of the normalization that the legacy lane of this tree and T1 to T4 all still
// agree on: an intentional change of a recorded legacy reply cannot be recorded (T2 fails the comparison and the
// golden stays unchanged), and needs its own review as a change of the legacy lane.
//
// The test is skipped on Windows, where the legacy exec lane cannot open sessions.

import (
	"archive/tar"
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/testutil"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/testdata/compat-v0.21.0/helpers/scenario"
)

const (
	compatGateEnv   = "OMNIVIEW_COMPAT_V0210"
	compatSDKEnv    = "OMNIVIEW_COMPAT_SDK_V0210"
	compatRecordEnv = "OMNIVIEW_COMPAT_RECORD"

	// compatBuildSafetyNet ends a go command that hangs; a cold build cache needs minutes.
	compatBuildSafetyNet = 15 * time.Minute
)

// compatFixture holds the binaries of the proof and the environment they run in.
type compatFixture struct {
	oldPlugin, oldHost, newPlugin string
}

func TestCompatV0210(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the legacy exec lane cannot open sessions on Windows")
	}
	record := os.Getenv(compatRecordEnv) == "1"
	if os.Getenv(compatGateEnv) != "1" && !record {
		t.Skipf("the interop proof with a real SDK %s build is off by default; run it with "+
			"%s=1 go test ./pkg/v1/exec/ -run '^TestCompatV0210$' -count=1 -v -timeout 90m "+
			"(it needs the Go module proxy, or %s=<checkout of the tag>; add %s=1 to re-record the golden)",
			compatSDKTag, compatGateEnv, compatSDKEnv, compatRecordEnv)
	}

	fx := newCompatFixture(t)

	// T1 is the reference: SDK v0.21.0 on both sides.
	t1 := fx.runOldHost(t, "T1", fx.oldPlugin)
	if !record {
		golden, _ := readCompatGolden(t)
		assertCompatTranscript(t, "T1 against the golden", golden.Transcript, t1.Transcript)
	}

	t.Run("T2 old host to new plugin", func(t *testing.T) {
		t2 := fx.runOldHost(t, "T2", fx.newPlugin)
		assertCompatTranscript(t, "T2 against T1", t1.Transcript, t2.Transcript)
		assertCompatLifecycle(t, "T2", t1.Lifecycle, t2.Lifecycle)
	})

	t.Run("T3 new host to old plugin", func(t *testing.T) {
		host, client := launchCompatHost(t, fx.oldPlugin)
		ctx, cancel := context.WithTimeout(context.Background(), compatSafetyNet)
		defer cancel()

		// The v0.21.0 plugin does not serve the v2 service: it answers Unimplemented, and the client turns that into
		// the typed refusal a host branches on, without a crash on either side.
		_, err := client.V2().Describe(ctx)
		assertCompatUnsupported(t, "Describe", err)
		_, err = client.V2().Options(compatOptionsContext(ctx), compatTargetRef())
		assertCompatUnsupported(t, "Options", err)

		// The plugin is alive and the legacy lane through the new client is what it was.
		got, err := scenario.Run(ctx, host.Exec, host.Recorder)
		if err != nil {
			t.Fatalf("run the legacy scenario after the v2 probes: %v", err)
		}
		assertCompatTranscript(t, "T3 against T1", t1.Transcript, got)
		lifecycle, err := host.Info(ctx)
		if err != nil {
			t.Fatalf("lifecycle: %v", err)
		}
		assertCompatLifecycle(t, "T3", t1.Lifecycle, lifecycle)
		assertCompatChildGone(t, "T3", host.Stop())
	})

	t.Run("T4 new host to new plugin", func(t *testing.T) {
		host, client := launchCompatHost(t, fx.newPlugin)
		ctx, cancel := context.WithTimeout(context.Background(), compatSafetyNet)
		defer cancel()

		description, err := client.V2().Describe(ctx)
		if err != nil {
			t.Fatalf("Describe: %v", err)
		}
		assertCompatDescription(t, description)

		// The child has no resource provider, so the connection of the call is not admitted: the resource lane's own
		// refusal, which is not the refusal of a plugin that does not serve the lane. The full Options path is tested
		// in process.
		_, err = client.V2().Options(compatOptionsContext(ctx), compatTargetRef())
		assertCompatMissingProvider(t, "Options with a connection", err, "UNSUPPORTED")
		// A call that carries no connection cannot be admitted at all: the resource lane asks for a reset.
		_, err = client.V2().Options(ctx, compatTargetRef())
		assertCompatMissingProvider(t, "Options without a connection", err, "RESET_REQUIRED")

		got, err := scenario.Run(ctx, host.Exec, host.Recorder)
		if err != nil {
			t.Fatalf("run the legacy scenario after the v2 probes: %v", err)
		}
		assertCompatTranscript(t, "T4 against T1", t1.Transcript, got)
		lifecycle, err := host.Info(ctx)
		if err != nil {
			t.Fatalf("lifecycle: %v", err)
		}
		assertCompatLifecycle(t, "T4", t1.Lifecycle, lifecycle)
		assertCompatChildGone(t, "T4", host.Stop())
	})

	// The recording is the golden only when T2, T3 and T4 confirmed it.
	if record {
		written, err := commitCompatGolden(compatGoldenPath(), t1.Transcript, !t.Failed())
		if err != nil {
			t.Fatal(err)
		}
		if written {
			t.Logf("recorded %s from T1 (%d steps)", compatGoldenPath(), len(t1.Transcript.Steps))
		} else {
			t.Logf("the compatibility matrix failed: %s was left as it was", compatGoldenPath())
		}
	}
}

// newCompatFixture prepares the old SDK, builds the three helpers and then points the environment at a temporary
// home and state root, in that order: the go commands run with the developer's own caches.
func newCompatFixture(t *testing.T) *compatFixture {
	t.Helper()
	root := compatModuleRoot(t)
	work := compatTempDir(t)
	oldSDK := compatOldSDK(t, work)
	oldModule := compatOldModule(t, work, root, oldSDK)
	bin := filepath.Join(work, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fx := &compatFixture{
		oldPlugin: filepath.Join(bin, "oldplugin"),
		oldHost:   filepath.Join(bin, "oldhost"),
		newPlugin: filepath.Join(bin, "newplugin"),
	}
	oldEnv := []string{"GOFLAGS=-mod=mod"}
	compatGo(t, oldModule, oldEnv, "build", "-o", fx.oldPlugin, "./oldplugin")
	compatGo(t, oldModule, oldEnv, "build", "-o", fx.oldHost, "./oldhost")
	compatGo(t, root, nil, "build", "-o", fx.newPlugin, "./testdata/compat-v0.21.0/helpers/newplugin")
	assertCompatBuild(t, fx.oldPlugin, oldSDK)
	assertCompatBuild(t, fx.oldHost, oldSDK)
	assertCompatBuild(t, fx.newPlugin, "")

	compatIsolateEnvironment(t, work)
	return fx
}

// compatIsolateEnvironment points the environment at a home and a state root under work, with the manifest the
// helper plugins read: they keep their state under the home and read the manifest from the state root of the host.
func compatIsolateEnvironment(t *testing.T, work string) {
	t.Helper()
	// The plugins read their manifest from the state root of the host and keep their state under the home.
	home, state := filepath.Join(work, "home"), filepath.Join(work, "state")
	manifestDir := filepath.Join(state, "plugins", scenario.PluginID)
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := "id: " + scenario.PluginID + "\nversion: 0.0.1\n"
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.SetHome(t, home)
	t.Setenv("OMNIVIEW_STATE_DIR", state)
	t.Setenv("OMNIVIEW_WORKSPACE_FILE", "")
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "false")
	t.Setenv("OMNIVIEW_TELEMETRY_PROFILING", "false")
}

// assertCompatBuild requires a binary to have been built against the SDK it stands for, as the binary itself says:
// the old helpers against SDK v0.21.0 as replaced by oldSDK (a directory), the new plugin from this module.
func assertCompatBuild(t *testing.T, binary, oldSDK string) {
	t.Helper()
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatalf("read the build info of %s: %v", filepath.Base(binary), err)
	}
	if oldSDK == "" {
		if info.Main.Path != compatSDKModule {
			t.Fatalf("%s is built from module %s, want this tree's %s", filepath.Base(binary), info.Main.Path, compatSDKModule)
		}
		return
	}
	for _, dep := range info.Deps {
		if dep.Path != compatSDKModule {
			continue
		}
		if dep.Version != compatSDKTag || dep.Replace == nil || dep.Replace.Path != oldSDK {
			t.Fatalf("%s is built against %s %s (replaced by %v), want %s as %s", filepath.Base(binary), dep.Path, dep.Version, dep.Replace, compatSDKTag, oldSDK)
		}
		return
	}
	t.Fatalf("%s does not depend on %s", filepath.Base(binary), compatSDKModule)
}

// compatTempDir returns a short temporary directory (unix socket paths are short on macOS), removed with the test.
func compatTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "x1c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// compatModuleRoot returns the root of this module, two levels above the directory go test runs in.
func compatModuleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !strings.HasPrefix(string(gomod), "module "+compatSDKModule+"\n") {
		t.Fatalf("%s is not the root of %s (go.mod: %v)", root, compatSDKModule, err)
	}
	return root
}

// compatOldSDK returns the directory of SDK v0.21.0: the checkout OMNIVIEW_COMPAT_SDK_V0210 names, or the module as
// the module proxy serves it (read-only, in the module cache).
func compatOldSDK(t *testing.T, work string) string {
	t.Helper()
	dir := os.Getenv(compatSDKEnv)
	if dir == "" {
		probe := filepath.Join(work, "probe")
		if err := os.MkdirAll(probe, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(probe, "go.mod"), []byte("module compat/probe\n\ngo 1.27\n"), 0o644); err != nil { //nolint:gosec // a scratch module
			t.Fatal(err)
		}
		var download struct {
			Dir    string
			Origin *struct{ Hash string }
		}
		if err := json.Unmarshal([]byte(compatGo(t, probe, nil, "mod", "download", "-json", compatSDKModule+"@"+compatSDKTag)), &download); err != nil {
			t.Fatalf("decode go mod download: %v", err)
		}
		if download.Origin != nil && download.Origin.Hash != "" && download.Origin.Hash != compatSDKCommit {
			t.Fatalf("the module proxy serves %s for %s, want commit %s", download.Origin.Hash, compatSDKTag, compatSDKCommit)
		}
		dir = download.Dir
	} else {
		// The early check gives the friendly errors (wrong commit, local changes). The snapshot is the guarantee: the
		// build never reads the checkout, whose ignored files git status does not show.
		if err := verifyCompatCheckout(dir, compatSDKCommit); err != nil {
			t.Fatalf("%s=%s is not accepted: %v", compatSDKEnv, dir, err)
		}
		snapshot := filepath.Join(work, "sdk-"+compatSDKTag)
		if err := snapshotCompatCheckout(dir, compatSDKCommit, snapshot); err != nil {
			t.Fatalf("%s=%s: %v", compatSDKEnv, dir, err)
		}
		dir = snapshot
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil || !strings.HasPrefix(string(gomod), "module "+compatSDKModule+"\n") {
		t.Fatalf("%s is not a checkout of %s (go.mod: %v)", dir, compatSDKModule, err)
	}
	// A guard against the wrong checkout: v0.21.0 predates the exec v2 contract.
	for _, v2 := range []string{filepath.Join("proto", "exec"), filepath.Join("pkg", "v1", "exec", "target.go")} {
		if _, err := os.Stat(filepath.Join(dir, v2)); err == nil {
			t.Fatalf("%s has %s: it is not SDK %s", dir, v2, compatSDKTag)
		}
	}
	return dir
}

// verifyCompatCheckout requires a local checkout that stands in for SDK v0.21.0 to be that very source: the commit
// the golden names, checked out clean. A checkout at the right commit with local changes would pass every other
// check and label a transcript of modified source with the tag.
func verifyCompatCheckout(dir, wantCommit string) error {
	git := func(args ...string) (string, error) {
		var stderr bytes.Buffer
		cmd := osexec.Command("git", slices.Concat([]string{"-C", dir}, args)...)
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(string(output)), nil
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("%s must be a git checkout of the tag %s (commit %s): %w", dir, compatSDKTag, wantCommit, err)
	}
	if head != wantCommit {
		return fmt.Errorf("%s is at commit %s, want %s (the tag %s)", dir, head, wantCommit, compatSDKTag)
	}
	status, err := git("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	if status != "" {
		return fmt.Errorf("%s has local changes, want a clean checkout of the tag %s (git status --porcelain):\n%s", dir, compatSDKTag, status)
	}
	return nil
}

// snapshotCompatCheckout writes the tree of a commit of a local checkout to dest (git archive, read as a tar stream).
// The build uses that snapshot and never the checkout: git status does not list ignored files, so a checked-out tree
// that looks clean can still hold a file that changes the build, and only the tree of the commit is the SDK the
// golden names.
func snapshotCompatCheckout(dir, commit, dest string) error {
	var stderr bytes.Buffer
	cmd := osexec.Command("git", "-C", dir, "archive", "--format=tar", commit)
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git archive %s: %w", commit, err)
	}
	extractErr := extractCompatTar(stdout, dest)
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("commit %s is not in the checkout %s (git archive: %w: %s)", commit, dir, err, strings.TrimSpace(stderr.String()))
	}
	if extractErr != nil {
		return fmt.Errorf("extract commit %s of %s: %w", commit, dir, extractErr)
	}
	return nil
}

// extractCompatTar extracts a tar stream of regular files and directories under dest; a name that leaves dest is an
// error. Symbolic links and the global header git adds are skipped: the SDK's build needs neither.
func extractCompatTar(r io.Reader, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	reader := tar.NewReader(r)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(header.Name))
		if target != dest && !strings.HasPrefix(target, dest+string(filepath.Separator)) {
			return fmt.Errorf("the archive entry %q is outside the snapshot", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader) //nolint:gosec // a test reading the tree of a commit
			if err := errors.Join(copyErr, file.Close()); err != nil {
				return err
			}
		}
	}
}

// commitCompatGolden writes the transcript recorded from T1 as the golden file, and only when the whole matrix
// passed: a recording that T2, T3 or T4 did not confirm must leave the existing golden as it is. It reports whether
// it wrote the file.
func commitCompatGolden(path string, transcript *scenario.Transcript, matrixPassed bool) (bool, error) {
	if !matrixPassed {
		return false, nil
	}
	encoded, err := encodeCompatGolden(&compatGolden{SDK: compatSDKModule, Tag: compatSDKTag, Commit: compatSDKCommit, Transcript: transcript})
	if err != nil {
		return false, fmt.Errorf("encode the golden: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create the golden directory: %w", err)
	}
	if err := writeFileAtomic(path, 0o644, func(w io.Writer) error { _, err := w.Write(encoded); return err }); err != nil {
		return false, fmt.Errorf("write the golden: %w", err)
	}
	return true, nil
}

// writeFileAtomic replaces the file at path with what write produces, and only when all of it was written: the
// content goes to a temporary file in the same directory, which is synced, closed and renamed over the file, so that
// a failed write leaves the old file whole and no temporary file behind.
func writeFileAtomic(path string, perm os.FileMode, write func(io.Writer) error) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if err = write(f); err != nil {
		return err
	}
	if err = f.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// compatOldModule writes the module of the old helpers: copies of the sources of scenario, oldplugin and oldhost
// under the very import path they have in this tree, a go.mod that requires SDK v0.21.0 replaced by oldSDK, with the
// replace directives the SDK's own go.mod carries (a dependency's replaces are ignored by go) and its go.sum.
func compatOldModule(t *testing.T, work, root, oldSDK string) string {
	t.Helper()
	module := filepath.Join(work, "old")
	helpers := filepath.Join(root, "testdata", "compat-v0.21.0", "helpers")
	for _, name := range []string{"scenario", "oldplugin", "oldhost"} {
		compatCopyGoFiles(t, filepath.Join(helpers, name), filepath.Join(module, name))
	}

	sdkGoMod, err := os.ReadFile(filepath.Join(oldSDK, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	var gomod strings.Builder
	gomod.WriteString("module " + compatSDKModule + "/testdata/compat-v0.21.0/helpers\n\n")
	gomod.WriteString("go " + compatGoDirective(string(sdkGoMod)) + "\n\n")
	gomod.WriteString("require " + compatSDKModule + " " + compatSDKTag + "\n\n")
	gomod.WriteString("replace " + compatSDKModule + " => " + strconv.Quote(oldSDK) + "\n")
	for _, directive := range compatReplaceDirectives(string(sdkGoMod)) {
		gomod.WriteString("replace " + directive + "\n")
	}
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte(gomod.String()), 0o644); err != nil { //nolint:gosec // a scratch module
		t.Fatal(err)
	}
	sdkGoSum, err := os.ReadFile(filepath.Join(oldSDK, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "go.sum"), sdkGoSum, 0o644); err != nil { //nolint:gosec // a scratch module
		t.Fatal(err)
	}
	compatGo(t, module, []string{"GOFLAGS=-mod=mod"}, "mod", "tidy")
	return module
}

// compatCopyGoFiles copies the Go files of the directory from (not its subdirectories) to the directory to.
func compatCopyGoFiles(t *testing.T, from, to string) {
	t.Helper()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		source, err := os.ReadFile(filepath.Join(from, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, entry.Name()), source, 0o644); err != nil { //nolint:gosec // a scratch module
			t.Fatal(err)
		}
	}
}

// compatGoDirective returns the version of the go directive of a go.mod.
func compatGoDirective(gomod string) string {
	for _, line := range strings.Split(gomod, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return "1.27"
}

// compatReplaceDirectives returns the replace directives of a go.mod, without the keyword: both the single-line and
// the block form.
func compatReplaceDirectives(gomod string) []string {
	var directives []string
	inBlock := false
	for _, line := range strings.Split(gomod, "\n") {
		line, _, _ = strings.Cut(line, "//")
		line = strings.TrimSpace(line)
		switch {
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "":
			directives = append(directives, line)
		case line == "replace (":
			inBlock = true
		case strings.HasPrefix(line, "replace "):
			directives = append(directives, strings.TrimSpace(strings.TrimPrefix(line, "replace ")))
		}
	}
	return directives
}

// compatGo runs the go command in dir with module mode pinned (GOWORK=off, GOTOOLCHAIN=local) and extra environment,
// and returns its combined output; a failure fails the test with the output.
func compatGo(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	goBinary, err := osexec.LookPath("go")
	if err != nil {
		t.Fatalf("the go command is needed to build the helpers: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), compatBuildSafetyNet)
	defer cancel()
	cmd := osexec.CommandContext(ctx, goBinary, args...)
	cmd.Dir = dir
	cmd.Env = slices.Concat(os.Environ(), []string{"GOWORK=off", "GOTOOLCHAIN=local"}, env)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, output)
	}
	return string(output)
}

// runOldHost runs oldhost (built against SDK v0.21.0) against a plugin binary and returns its report. The host
// stops its plugin on every path, a termination signal included; the plugin must be gone afterwards.
func (fx *compatFixture) runOldHost(t *testing.T, what, pluginBinary string) *scenario.Report {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*compatSafetyNet)
	defer cancel()
	cmd := osexec.CommandContext(ctx, fx.oldHost, "-plugin", pluginBinary, "-id", scenario.PluginID)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	// oldhost, once told to end, aborts the run (at most cleanupTimeout) and stops its plugin (at most the grace
	// period and the wait after the kill of scenario.Host.Stop): the delay has to outlast both, or oldhost is killed
	// with its plugin still running.
	cmd.WaitDelay = 90 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: oldhost against %s: %v\nstderr:\n%s", what, filepath.Base(pluginBinary), err, stderr.String())
	}
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	var report scenario.Report
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("%s: decode the report of oldhost: %v\nstdout:\n%s\nstderr:\n%s", what, err, stdout.String(), stderr.String())
	}
	if decoder.More() {
		t.Fatalf("%s: oldhost printed more than one JSON document", what)
	}
	if report.Transcript == nil {
		t.Fatalf("%s: the report of oldhost holds no transcript", what)
	}
	if report.Lifecycle.PluginID != scenario.PluginID {
		t.Errorf("%s: the plugin says it is %q, want %q", what, report.Lifecycle.PluginID, scenario.PluginID)
	}
	assertCompatChildGone(t, what, report.Child)
	return &report
}

// launchCompatHost launches a plugin binary with this tree's host (go-plugin, with the recording interceptors on
// the connection). The plugin is stopped when the test ends, if the test did not stop it.
func launchCompatHost(t *testing.T, pluginBinary string) (*scenario.Host, *exec.PluginClient) {
	t.Helper()
	host, err := scenario.Launch(pluginBinary, scenario.PluginID)
	if err != nil {
		t.Fatalf("launch %s: %v", filepath.Base(pluginBinary), err)
	}
	t.Cleanup(func() { host.Stop() })
	client, ok := host.Exec.(*exec.PluginClient)
	if !ok {
		t.Fatalf("the exec capability is a %T, want this tree's *exec.PluginClient", host.Exec)
	}
	return host, client
}

// compatTargetRef is a reference to a resource of provider compat and type echo, with its route.
func compatTargetRef() exec.TargetRef {
	return exec.TargetRef{
		Reference: resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{
			Provider:  "compat",
			Authority: "authority",
			Type:      "echo",
			Scope:     []resourcev2.ScopeEntry{},
			ID:        "item",
		}},
		Route: resourcev2.AccessRoute{Connection: "compat-connection"},
	}
}

// compatOptionsContext is the context of an Options call: the plugin context of the caller, with its connection.
func compatOptionsContext(ctx context.Context) context.Context {
	return types.WithPluginContext(ctx, &types.PluginContext{
		RequestID:   "compat-options",
		RequesterID: "compat-host",
		Connection:  &types.Connection{ID: "compat-connection"},
	})
}

// assertCompatUnsupported requires err to be the typed refusal of a plugin that does not serve the v2 lane.
func assertCompatUnsupported(t *testing.T, method string, err error) {
	t.Helper()
	var execErr *exec.Error
	if !errors.As(err, &execErr) || execErr.Class != exec.ClassUnsupported {
		t.Fatalf("%s of a v0.21.0 plugin = %v (%T), want an *exec.Error of class %s", method, err, err, exec.ClassUnsupported)
	}
}

// assertCompatMissingProvider requires err to be the resource lane's own refusal, with the given code, of a call a
// plugin that serves the exec lane cannot admit because it has no resource provider. It rejects success, the exec
// lane's classified errors (ClassUnsupported is the answer of a plugin that does not serve the lane, ClassInternal
// a broken contract) and every error that did not come from the resource lane's admission.
func assertCompatMissingProvider(t *testing.T, what string, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s of a plugin without a resource provider succeeded", what)
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		t.Fatalf("%s = %v: the exec lane classified (%s) what is the resource lane's refusal", what, err, execErr.Class)
	}
	var resourceErr *resourcev2.ResourceOperationError
	if !errors.As(err, &resourceErr) {
		t.Fatalf("%s = %v (%T), want a *resourcev2.ResourceOperationError", what, err, err)
	}
	if got := resourcev2.ErrorCode(err); got != code {
		t.Fatalf("%s = %v: code %q, want %q", what, err, got, code)
	}
}

// assertCompatTranscript requires two transcripts to be equal and lists the differences when they are not.
func assertCompatTranscript(t *testing.T, what string, want, got *scenario.Transcript) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("%s: the legacy lane differs:\n%s", what, strings.Join(scenario.Diff(want, got), "\n"))
	}
}

// assertCompatLifecycle requires what the lifecycle service says of a plugin to be what it says of the v0.21.0
// plugin, with nothing removed: the same identity and the same capabilities, exec among them (a capability may be
// added, never removed). Both lists are logged.
func assertCompatLifecycle(t *testing.T, what string, old, got scenario.Lifecycle) {
	t.Helper()
	t.Logf("%s: capabilities %v, the v0.21.0 plugin's: %v", what, got.Capabilities, old.Capabilities)
	if got.PluginID != old.PluginID || got.Version != old.Version ||
		got.SDKProtocolVersion != old.SDKProtocolVersion || got.HostConnectionsVersion != old.HostConnectionsVersion {
		t.Errorf("%s: lifecycle info = %+v, want the v0.21.0 plugin's %+v", what, got, old)
	}
	if !slices.Contains(got.Capabilities, "exec") {
		t.Errorf("%s: the exec capability is gone: %v", what, got.Capabilities)
	}
	for _, capability := range old.Capabilities {
		if !slices.Contains(got.Capabilities, capability) {
			t.Errorf("%s: the capability %q was removed: %v, want at least %v", what, capability, got.Capabilities, old.Capabilities)
		}
	}
}

// assertCompatChildGone requires a plugin process to have exited and to be gone.
func assertCompatChildGone(t *testing.T, what string, child scenario.Child) {
	t.Helper()
	if !child.Exited {
		t.Errorf("%s: the plugin process did not exit", what)
	}
	if child.PID <= 0 {
		t.Errorf("%s: the host did not report the plugin's process id", what)
		return
	}
	if process, err := os.FindProcess(child.PID); err == nil && process.Signal(syscall.Signal(0)) == nil {
		t.Errorf("%s: plugin process %d is still running", what, child.PID)
	}
}

// TestCompatCommitGolden requires a recording to become the golden file only when the matrix passed.
func TestCompatCommitGolden(t *testing.T) {
	transcript := &scenario.Transcript{
		Steps:   []scenario.Step{{Name: "get-supported-resources"}},
		Sent:    []scenario.Message{},
		Data:    []scenario.Message{},
		Signals: []scenario.Message{},
	}
	path := filepath.Join(t.TempDir(), "golden", "legacy_transcript.json")

	// Nothing is written, and no directory is made, when the matrix failed.
	written, err := commitCompatGolden(path, transcript, false)
	if err != nil || written {
		t.Fatalf("commitCompatGolden(failed matrix) = %v, %v, want false and no error", written, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed matrix created the golden directory (stat: %v)", err)
	}

	// An existing golden stays as it is when the matrix failed.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("existing golden\n"), 0o644); err != nil { //nolint:gosec // a test fixture
		t.Fatal(err)
	}
	if written, err := commitCompatGolden(path, transcript, false); err != nil || written {
		t.Fatalf("commitCompatGolden(failed matrix, existing golden) = %v, %v, want false and no error", written, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "existing golden\n" {
		t.Fatalf("a failed matrix changed the existing golden: %q (read: %v)", got, err)
	}

	// A passed matrix replaces it with the encoding the golden test requires.
	if written, err := commitCompatGolden(path, transcript, true); err != nil || !written {
		t.Fatalf("commitCompatGolden(passed matrix) = %v, %v, want true and no error", written, err)
	}
	want, err := encodeCompatGolden(&compatGolden{SDK: compatSDKModule, Tag: compatSDKTag, Commit: compatSDKCommit, Transcript: transcript})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("the golden is %q (read: %v), want %q", got, err, want)
	}
}

// A write that fails half-way leaves the existing file as it was, and no temporary file behind; a write that
// succeeds replaces the file whole.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "golden.json")
	if err := os.WriteFile(path, []byte("old golden\n"), 0o644); err != nil { //nolint:gosec // a test fixture
		t.Fatal(err)
	}
	boom := errors.New("disk full")
	err := writeFileAtomic(path, 0o644, func(w io.Writer) error {
		_, _ = w.Write([]byte("new gol"))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("writeFileAtomic = %v, want the write's error", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "old golden\n" {
		t.Fatalf("a failed write changed the golden: %q (read: %v)", got, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("directory = %v (read: %v), want only the golden", entries, err)
	}

	if err := writeFileAtomic(path, 0o644, func(w io.Writer) error { _, err := w.Write([]byte("new golden\n")); return err }); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new golden\n" {
		t.Fatalf("golden = %q (read: %v), want the new content", got, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("directory = %v (read: %v), want only the golden", entries, err)
	}
}

// TestCompatCheckoutOverride requires a local checkout that replaces the downloaded SDK to be at the expected commit
// with a clean tree.
// newCompatTestRepo makes a git repository in a temporary directory with one commit, which holds go.mod and a
// .gitignore that ignores ignored.go. It returns the directory, a runner of git commands in it, and the commit.
func newCompatTestRepo(t *testing.T) (dir string, git func(args ...string) string, head string) {
	t.Helper()
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git is needed to make the checkout")
	}
	dir = t.TempDir()
	git = func(args ...string) string {
		t.Helper()
		cmd := osexec.Command("git", slices.Concat([]string{
			"-c", "user.name=compat", "-c", "user.email=compat@example.invalid",
			"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main",
		}, args)...)
		cmd.Dir = dir
		cmd.Env = slices.Concat(os.Environ(), []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"})
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	// Git for Windows applies core.autocrlf to what it adds and archives: the tests compare bytes, so the committed
	// content is the written content, with LF, on every platform.
	git("config", "core.autocrlf", "false")
	git("config", "core.eol", "lf")
	for name, content := range map[string]string{
		"go.mod":     "module " + compatSDKModule + "\n",
		".gitignore": "ignored.go\n",
		"sub/a.go":   "package sub\n",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // a test fixture
			t.Fatal(err)
		}
	}
	git("add", "go.mod", ".gitignore", "sub/a.go")
	git("commit", "-q", "-m", "the tag")
	return dir, git, git("rev-parse", "HEAD")
}

// TestCompatCheckoutOverride requires a local checkout that replaces the downloaded SDK to be at the expected commit
// with a clean tree.
func TestCompatCheckoutOverride(t *testing.T) {
	dir, _, head := newCompatTestRepo(t)

	if err := verifyCompatCheckout(dir, head); err != nil {
		t.Fatalf("a clean checkout at the expected commit is refused: %v", err)
	}
	if err := verifyCompatCheckout(dir, compatSDKCommit); err == nil || !strings.Contains(err.Error(), head) {
		t.Fatalf("a checkout at another commit is accepted or the error does not name its commit: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "untracked.go"), []byte("package x\n"), 0o644); err != nil { //nolint:gosec // a test fixture
		t.Fatal(err)
	}
	if err := verifyCompatCheckout(dir, head); err == nil || !strings.Contains(err.Error(), "untracked.go") {
		t.Fatalf("a checkout with an untracked file is accepted or the error does not name it: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "untracked.go")); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+compatSDKModule+"\n\ngo 1.27\n"), 0o644); err != nil { //nolint:gosec // a test fixture
		t.Fatal(err)
	}
	if err := verifyCompatCheckout(dir, head); err == nil || !strings.Contains(err.Error(), "go.mod") {
		t.Fatalf("a checkout with a modified file is accepted or the error does not name it: %v", err)
	}

	if err := verifyCompatCheckout(t.TempDir(), head); err == nil {
		t.Fatal("a directory that is not a git checkout is accepted")
	}
}

// TestCompatCheckoutSnapshot requires the SDK that is built from a local checkout to be a snapshot of the expected
// commit: the guarantee that no file of the working tree that is not in the commit (an ignored one, which git status
// does not list, an untracked one) and no local change to a tracked file reaches the build.
func TestCompatCheckoutSnapshot(t *testing.T) {
	dir, _, head := newCompatTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "ignored.go"), []byte("package x // ignored, not committed\n"), 0o644); err != nil { //nolint:gosec // a test fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module modified\n"), 0o644); err != nil { //nolint:gosec // a test fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.go"), []byte("package x\n"), 0o644); err != nil { //nolint:gosec // a test fixture
		t.Fatal(err)
	}

	snapshot := filepath.Join(t.TempDir(), "sdk")
	if err := snapshotCompatCheckout(dir, head, snapshot); err != nil {
		t.Fatalf("snapshot the checkout: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(snapshot, "go.mod")); err != nil || string(got) != "module "+compatSDKModule+"\n" {
		t.Errorf("the snapshot's go.mod is %q (read: %v), want the committed content", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(snapshot, "sub", "a.go")); err != nil || string(got) != "package sub\n" {
		t.Errorf("the snapshot's sub/a.go is %q (read: %v), want the committed content", got, err)
	}
	for _, name := range []string{"ignored.go", "untracked.go"} {
		if _, err := os.Stat(filepath.Join(snapshot, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the snapshot has %s, which is not in the commit (stat: %v)", name, err)
		}
	}

	missing := strings.Repeat("0", len(head))
	if err := snapshotCompatCheckout(dir, missing, filepath.Join(t.TempDir(), "sdk")); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("a commit that is not in the checkout is accepted or the error does not name it: %v", err)
	}
}
