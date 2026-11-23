package exec

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Two numbers of the contract are printed in the guide (docs/exec.md) and are pinned here as numbers, not as the
// constants that hold them: a change of either constant, which would change what the guide tells every author, fails
// these tests, and the change of the guide, the constant and the number below is made together.

// A target that declares sessions and leaves MaxDimension unset is served terminals of up to 1024 cells a side, and one
// cell more is refused, as the guide says. 1024 is accepted on either side and in both at once, and 1025 is refused on
// either side and in both.
func TestATerminalMayBe1024CellsASideByDefaultAndNoMore(t *testing.T) {
	if got := DefaultSessionLimits().MaxDimension; got != 1024 {
		t.Errorf("DefaultSessionLimits().MaxDimension = %d, the guide says 1024", got)
	}
	_, target := rqtTarget(t, func(tg *Target) { tg.Info.Session = SessionLimits{MaxSessions: 8} })
	if got := target.info.Session.MaxDimension; got != 1024 {
		t.Fatalf("a session section with MaxDimension unset is served with %d, the guide says 1024", got)
	}

	start := func(cols, rows uint32) *execv2.Start {
		wire := rqtWireStart()
		wire.Size = &execv2.Size{Cols: cols, Rows: rows}
		return wire
	}
	for _, size := range [][2]uint32{{1024, 24}, {80, 1024}, {1024, 1024}} {
		if _, refusal := admitStart(start(size[0], size[1]), target, rqtServed()); refusal != nil {
			t.Errorf("a terminal of %d x %d was refused: %v", size[0], size[1], refusal.Err)
		}
	}
	for _, size := range [][2]uint32{{1025, 24}, {80, 1025}, {1025, 1025}} {
		_, refusal := admitStart(start(size[0], size[1]), target, rqtServed())
		if refusal == nil || refusal.Class != ErrorClassInvalidRequest || refusal.Err == nil ||
			refusal.Err.Error() != "a size dimension is above the target's limit of 1024" {
			t.Errorf("a terminal of %d x %d: refusal = %+v, want INVALID_REQUEST, \"a size dimension is above the target's limit of 1024\"", size[0], size[1], refusal)
		}
	}
}

// pinTargets returns n valid targets that no two share a routing key with.
func pinTargets(n int) []Target {
	targets := make([]Target, n)
	for i := range targets {
		targets[i] = rqtBaseTarget()
		targets[i].Info.Type = fmt.Sprintf("kind-%d", i)
	}
	return targets
}

// A plugin registers 256 targets and not 257: the 256th registers, the 257th is refused with the error the guide prints,
// and the host refuses a Describe that lists 257 in the same way.
func TestAPluginMayRegister256TargetsAndNoMore(t *testing.T) {
	registry, err := newTargetRegistry(pinTargets(256))
	if err != nil || len(registry.targets) != 256 {
		t.Fatalf("256 targets: %v, %d registered", err, len(registry.targets))
	}
	if _, last := registry.lookup("k8s", "kind-255"); !last {
		t.Error("the 256th target did not register")
	}
	if _, err := newTargetRegistry(pinTargets(257)); err == nil || err.Error() != "257 exec targets exceed the limit of 256" {
		t.Errorf("257 targets: err = %v, want \"257 exec targets exceed the limit of 256\"", err)
	}

	description := registry.description()
	if err := validateDescribeResponse(description); err != nil {
		t.Errorf("a Describe that lists 256 targets was refused: %v", err)
	}
	description.Targets = append(description.Targets, targetInfoToProto(pinTargets(257)[256].Info))
	if err := validateDescribeResponse(description); err == nil || err.Error() != "257 targets exceed the limit of 256" {
		t.Errorf("a Describe that lists 257 targets: err = %v, want \"257 targets exceed the limit of 256\"", err)
	}
}

// The guide prints the numbers above, and they are the constants'.
func TestTheGuidePrintsTheNumbersTheLimitsPin(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/exec.md")
	if err != nil {
		t.Fatal(err)
	}
	guide := strings.ReplaceAll(string(raw), "\r\n", "\n")
	number := func(pattern string) int {
		t.Helper()
		match := regexp.MustCompile(pattern).FindStringSubmatch(guide)
		if match == nil {
			t.Fatalf("docs/exec.md does not match %q", pattern)
		}
		n, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for name, tc := range map[string]struct {
		printed  int
		constant int
		pinned   int
	}{
		"the terminal dimension limit":     {number(`terminals up to (\d+) cells a side`), defaultMaxDimension, 1024},
		"the targets a plugin may have":    {number("registered more than (\\d+) targets"), maxTargets, 256},
		"the targets in the error it gets": {number("`N exec targets exceed the limit of (\\d+)`"), maxTargets, 256},
	} {
		if tc.printed != tc.constant || tc.printed != tc.pinned {
			t.Errorf("%s: the guide prints %d, the constant is %d, the pin above is %d", name, tc.printed, tc.constant, tc.pinned)
		}
	}
}
