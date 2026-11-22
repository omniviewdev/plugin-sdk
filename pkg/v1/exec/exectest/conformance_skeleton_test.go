package exectest

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// concluded records how an engine case ended instead of ending the test.
type concluded struct{ fatal, skip []string }

func (c *concluded) Fatalf(format string, args ...any) {
	c.fatal = append(c.fatal, fmt.Sprintf(format, args...))
}

func (c *concluded) Skipf(format string, args ...any) {
	c.skip = append(c.skip, fmt.Sprintf(format, args...))
}

// caseNumbered returns the engine case with a number.
func caseNumbered(t *testing.T, number int) engineCase {
	t.Helper()
	for _, c := range engineCases() {
		if c.number == number {
			return c
		}
	}
	t.Fatalf("the skeleton has no case %d", number)
	return engineCase{}
}

// The numbered cases run from 1 without a gap or a repeat, and the pair tests and the stress harness, which carry no
// number, come after them.
func TestEngineCasesAreNumberedFromOneWithoutGaps(t *testing.T) {
	next, unnumbered := 1, false
	for _, c := range engineCases() {
		if c.number == 0 {
			unnumbered = true
			continue
		}
		if unnumbered {
			t.Errorf("case %d comes after a pair test or the stress harness", c.number)
		}
		if c.number != next {
			t.Errorf("case %d where case %d is next", c.number, next)
		}
		next = c.number + 1
	}
}

// The review of the contract added four cases to the plan's 24. Each is listed with the step that fills it and the
// feature that step makes the build list, and says what the review found missing, so that it cannot be reworded into
// something that checks less.
func TestTheEngineCasesTheReviewAddedAreListedWithTheirSteps(t *testing.T) {
	session, run := execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_RUN
	for _, want := range []struct {
		number   int
		step     string
		requires execv2.Feature
		says     []string
	}{
		{25, "X3a and X3c", session, []string{
			"exactly one of Ended or Detached", "at most one Ended", "none when it ends while no stream is attached", "never to a detached or superseded stream",
		}},
		{26, "X3a", session, []string{
			"different ExitReport and HostRestart in Opened", "Choice.capabilities", "not in Started", "widening is clamped",
		}},
		{27, "X2b", run, []string{
			"OUTCOME_FAILED", "partial output", "completeness", "start_observed", "remote_may_survive",
			"refusal before the hand-off is a status", "never retries a failed or lost end",
		}},
		{28, "X2a and X3a", session, []string{
			"remote_may_survive", "Lost true", "process exit and signal false", "transport exit and failure absent", "never read as no",
		}},
	} {
		c := caseNumbered(t, want.number)
		if c.step != want.step || c.requires != want.requires {
			t.Errorf("case %d is filled by step %q and makes the build list %s, want step %q and %s", want.number, c.step, c.requires, want.step, want.requires)
		}
		for _, phrase := range want.says {
			if !strings.Contains(c.name, phrase) {
				t.Errorf("case %d (%q) does not say %q", want.number, c.name, phrase)
			}
		}
	}
}

// Case 9 is about how a session ends, and the kinds of end are the arms of End. A superseded stream is Detached with
// DETACH_REASON_SUPERSEDED: it is not an end, so it is not one of the kinds.
func TestCaseNineNamesEveryKindOfEndAndNoOther(t *testing.T) {
	c := caseNumbered(t, 9)
	arms := execv2.File_proto_exec_v2_exec_proto.Messages().ByName("End").Oneofs().ByName("kind").Fields()
	for i := range arms.Len() {
		if arm := string(arms.Get(i).Name()); !strings.Contains(c.name, arm) {
			t.Errorf("case 9 (%q) does not name the end kind %s", c.name, arm)
		}
	}
	if strings.Contains(strings.ToLower(c.name), "supersed") {
		t.Errorf("case 9 (%q) lists superseded as a kind of end", c.name)
	}
}

// A session has exactly one END and at most one Ended frame: one that ends while no stream is attached (a lease that
// expires, a retirement, a shutdown) has no stream to carry a frame, and its end is the tombstone's. The cases that
// are about Ended say so, and none says that Ended is sent exactly once.
func TestNoEngineCaseSaysThatASessionHasExactlyOneEnded(t *testing.T) {
	for _, c := range engineCases() {
		lower := strings.ToLower(c.name)
		for _, phrase := range []string{"exactly one ended", "ended exactly once", "once per session", "sent once"} {
			if strings.Contains(lower, phrase) {
				t.Errorf("case %d (%q) says %q: a session that ends while no stream is attached has no Ended frame", c.number, c.name, phrase)
			}
		}
	}
	for _, number := range []int{9, 25} {
		if c := caseNumbered(t, number); !strings.Contains(c.name, "at most one Ended") {
			t.Errorf("case %d (%q) does not say that a session has at most one Ended", number, c.name)
		}
	}
}

// A case names what it checks, never a file: a file it points at can be deleted, as the one that case 24 named was.
func TestNoEngineCaseNamesAFile(t *testing.T) {
	for _, c := range engineCases() {
		if strings.Contains(c.name, ".go") {
			t.Errorf("case %d (%q) names a file", c.number, c.name)
		}
	}
}

// Every case is filled by a step and makes the build list a feature the SDK names, and ends as the gate says: failed
// once the build lists that feature (the case must be written by then), skipped with its step otherwise.
func TestAnEngineCaseFailsOnceItsFeatureIsListedAndIsSkippedUntilThen(t *testing.T) {
	for i, c := range engineCases() {
		t.Run(fmt.Sprintf("%02d", i), func(t *testing.T) {
			if _, named := execv2.Feature_name[int32(c.requires)]; !named || c.requires == execv2.Feature_FEATURE_UNSPECIFIED || c.step == "" {
				t.Fatalf("case %d (%q) has step %q and feature %s", c.number, c.name, c.step, c.requires)
			}

			var listed concluded
			c.conclude(&listed, []execv2.Feature{execv2.Feature_FEATURE_RUN_STREAM, c.requires})
			if len(listed.fatal) != 1 || len(listed.skip) != 0 {
				t.Fatalf("a build that lists %s: %d failures and %d skips, want one failure and no skip", c.requires, len(listed.fatal), len(listed.skip))
			}
			if !strings.Contains(listed.fatal[0], c.requires.String()) || !strings.Contains(listed.fatal[0], "step "+c.step) {
				t.Errorf("the failure %q names neither the feature %s nor the step %s", listed.fatal[0], c.requires, c.step)
			}

			var others []execv2.Feature
			for number := range execv2.Feature_name {
				if feature := execv2.Feature(number); feature != execv2.Feature_FEATURE_UNSPECIFIED && feature != c.requires {
					others = append(others, feature)
				}
			}
			slices.Sort(others)
			for name, served := range map[string][]execv2.Feature{"no feature": nil, "every other feature": others} {
				var skipped concluded
				c.conclude(&skipped, served)
				if len(skipped.skip) != 1 || len(skipped.fatal) != 0 {
					t.Fatalf("a build that lists %s: %d skips and %d failures, want one skip and no failure", name, len(skipped.skip), len(skipped.fatal))
				}
				if !strings.Contains(skipped.skip[0], "step "+c.step) {
					t.Errorf("the skip %q does not name the step %s", skipped.skip[0], c.step)
				}
			}
		})
	}
}

// Listing the engine cases asks the build for its features once per case, in the subtest of that case, and a build that
// lists none skips every one: the numbered cases are named by their number.
func TestListingTheEngineCasesAsksTheBuildOncePerCase(t *testing.T) {
	var asked []string
	t.Run("engine", func(t *testing.T) {
		listEngineCases(t, func(t testing.TB) *execv2.DescribeResponse {
			asked = append(asked, t.Name())
			return &execv2.DescribeResponse{}
		})
	})
	cases := engineCases()
	if len(asked) != len(cases) {
		t.Fatalf("the build was asked %d times for %d cases", len(asked), len(cases))
	}
	for i, c := range cases {
		if c.number > 0 && !strings.Contains(asked[i], fmt.Sprintf("/case_%02d:", c.number)) {
			t.Errorf("the subtest of case %d is %q, want it named by its number", c.number, asked[i])
		}
	}
}
