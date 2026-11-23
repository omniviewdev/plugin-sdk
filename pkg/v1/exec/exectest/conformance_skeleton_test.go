package exectest

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
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
	for _, want := range []struct {
		number   int
		step     string
		requires execv2.Feature
		says     []string
	}{
		{25, "X3a (the one-stream wire body) and X3c", execv2.Feature_FEATURE_UNSPECIFIED, []string{
			"exactly one of Ended or Detached", "at most one Ended", "none when it ends while no stream is attached", "never to a detached or superseded stream",
		}},
		{26, "X3a (the wire body)", execv2.Feature_FEATURE_UNSPECIFIED, []string{
			"different ExitReport and HostRestart in Opened", "Choice.capabilities", "not in Started", "widening is clamped",
		}},
		{27, "", execv2.Feature_FEATURE_UNSPECIFIED, []string{
			"OUTCOME_FAILED", "partial output", "completeness", "start_observed", "remote_may_survive",
			"refusal before the hand-off is a status", "never retries a failed or lost end",
		}},
		{28, "X3a (the wire body)", execv2.Feature_FEATURE_UNSPECIFIED, []string{
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

// A row ends as the gate says. A row with no step is complete: its bodies are all of it, so it has a body and concluding it
// neither skips nor fails. A row that names no feature (a step of the host, or the wire body of a case that is not written yet) always
// skips with its step, however many features the build lists. Any other row makes the build list a feature the SDK names: failed
// once the build lists that feature (what is left of the case must be written by then), skipped with its step otherwise.
func TestAnEngineCaseFailsOnceItsFeatureIsListedAndIsSkippedUntilThen(t *testing.T) {
	every := func(except execv2.Feature) []execv2.Feature {
		var all []execv2.Feature
		for number := range execv2.Feature_name {
			if feature := execv2.Feature(number); feature != execv2.Feature_FEATURE_UNSPECIFIED && feature != except {
				all = append(all, feature)
			}
		}
		slices.Sort(all)
		return all
	}
	for i, c := range engineCases() {
		t.Run(fmt.Sprintf("%02d", i), func(t *testing.T) {
			if _, named := execv2.Feature_name[int32(c.requires)]; !named {
				t.Fatalf("case %d (%q) makes the build list %d, which is no feature", c.number, c.name, int32(c.requires))
			}
			switch {
			case c.step == "":
				if c.inProcess == nil && c.wire == nil {
					t.Errorf("case %d (%q) has no step and no body: nothing checks it", c.number, c.name)
				}
				var done concluded
				c.conclude(&done, every(execv2.Feature_FEATURE_UNSPECIFIED))
				if len(done.fatal) != 0 || len(done.skip) != 0 {
					t.Errorf("a case with nothing left: %d failures and %d skips, want neither", len(done.fatal), len(done.skip))
				}
				return
			case c.requires == execv2.Feature_FEATURE_UNSPECIFIED:
				for name, served := range map[string][]execv2.Feature{"no feature": nil, "every feature": every(execv2.Feature_FEATURE_UNSPECIFIED)} {
					var skipped concluded
					c.conclude(&skipped, served)
					if len(skipped.skip) != 1 || len(skipped.fatal) != 0 || !strings.Contains(skipped.skip[0], "step "+c.step) {
						t.Errorf("a row that names no feature, in a build that lists %s: skips %q and failures %q, want one skip that names the step %s", name, skipped.skip, skipped.fatal, c.step)
					}
				}
				return
			}

			var listed concluded
			c.conclude(&listed, []execv2.Feature{execv2.Feature_FEATURE_RUN_STREAM, c.requires})
			if len(listed.fatal) != 1 || len(listed.skip) != 0 {
				t.Fatalf("a build that lists %s: %d failures and %d skips, want one failure and no skip", c.requires, len(listed.fatal), len(listed.skip))
			}
			if !strings.Contains(listed.fatal[0], c.requires.String()) || !strings.Contains(listed.fatal[0], "step "+c.step) {
				t.Errorf("the failure %q names neither the feature %s nor the step %s", listed.fatal[0], c.requires, c.step)
			}
			for name, served := range map[string][]execv2.Feature{"no feature": nil, "every other feature": every(c.requires)} {
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

// The build lists FEATURE_SESSION now, so no row that is left may need it: a row that did would fail every author's run. What each
// of them waits for is a feature the build does not list yet, or a step that lists none.
func TestNoRowWaitsForAFeatureThatTheBuildListsNow(t *testing.T) {
	listed := []execv2.Feature{execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_ENV, execv2.Feature_FEATURE_CWD}
	for _, c := range engineCases() {
		var concluded concluded
		c.conclude(&concluded, listed)
		if len(concluded.fatal) != 0 {
			t.Errorf("case %d (%q) fails in a build that lists %v: %q", c.number, c.name, listed, concluded.fatal)
		}
	}
}

// Listing the engine cases asks the build for its features once per case, in the subtest of that case, and a build that
// lists none skips every one that has something left: the numbered cases are named by their number. The bodies are left out
// here: they are run, and held to what they claim, by the tests of conformance_inprocess_test.go and conformance_open_test.go.
func TestListingTheEngineCasesAsksTheBuildOncePerCase(t *testing.T) {
	var asked []string
	cases := engineCases()
	for i := range cases {
		cases[i].inProcess, cases[i].wire = nil, nil
	}
	t.Run("engine", func(t *testing.T) {
		listCases(t, cases, exec.Target{}, &inProcessEnv{}, &wireEnv{}, func(t testing.TB) *execv2.DescribeResponse {
			asked = append(asked, t.Name())
			return &execv2.DescribeResponse{}
		})
	})
	if len(asked) != len(cases) {
		t.Fatalf("the build was asked %d times for %d cases", len(asked), len(cases))
	}
	for i, c := range cases {
		if c.number > 0 && !strings.Contains(asked[i], fmt.Sprintf("/case_%02d:", c.number)) {
			t.Errorf("the subtest of case %d is %q, want it named by its number", c.number, asked[i])
		}
	}
}

// The rows of the bounded run are written. Cases 15 and 27 and the pairs P1 and P3 have nothing left, so they have no step and no
// feature that a build must not list before they are written; the Run part of cases 13 and 16 is written, and the step they keep is
// the one that writes their Open part. Each has a wire body, and it runs only where the build lists what it drives: FEATURE_RUN, and
// FEATURE_SESSION as well for the pair that runs a command both ways.
func TestTheRowsOfTheBoundedRunAreWrittenAndDriveTheFeaturesTheyNeed(t *testing.T) {
	session, run := execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_RUN
	byName := func(name string) engineCase {
		for _, c := range engineCases() {
			if c.name == name {
				return c
			}
		}
		t.Fatalf("the skeleton has no case %q", name)
		return engineCase{}
	}
	for _, want := range []struct {
		c     engineCase
		step  string
		needs []execv2.Feature
	}{
		{caseNumbered(t, 13), "X3a (the Open wire body)", []execv2.Feature{run}},
		{caseNumbered(t, 15), "", []execv2.Feature{run}},
		{caseNumbered(t, 16), "X3a (the Open wire body)", []execv2.Feature{run}},
		{caseNumbered(t, 27), "", []execv2.Feature{run}},
		{byName(pairRunOpenName), "", []execv2.Feature{session, run}},
		{byName(pairEngineWireName), "", []execv2.Feature{run}},
	} {
		c := want.c
		if c.wire == nil {
			t.Errorf("case %d (%q) has no wire body", c.number, c.name)
		}
		if c.step != want.step || c.requires != execv2.Feature_FEATURE_UNSPECIFIED {
			t.Errorf("case %d (%q) is filled by step %q and makes the build list %s, want step %q and no feature", c.number, c.name, c.step, c.requires, want.step)
		}
		if got := c.wireFeatures(); !slices.Equal(got, want.needs) {
			t.Errorf("case %d (%q) needs %v for its wire body, want %v", c.number, c.name, got, want.needs)
		}
	}
	// The cases of Open still need only FEATURE_SESSION.
	for _, number := range []int{1, 2, 3, 4, 5, 18} {
		if got := caseNumbered(t, number).wireFeatures(); !slices.Equal(got, []execv2.Feature{session}) {
			t.Errorf("case %d needs %v for its wire body, want %v", number, got, []execv2.Feature{session})
		}
	}
}

// The wire body of a case runs when the build lists every feature that the case drives, and is skipped, with the feature it names, when
// the build lists less: a build that serves Open and not Run runs the cases of Open and skips the cases of Run, and the other way round.
func TestAWireBodyRunsOnlyWhenTheBuildListsEveryFeatureItDrives(t *testing.T) {
	session, run := execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_RUN
	for _, tc := range []struct {
		listed []execv2.Feature
		ran    []string
	}{
		{nil, nil},
		{[]execv2.Feature{session}, []string{"case 01", "case 02", "case 03", "case 04", "case 05", "case 18"}},
		{[]execv2.Feature{run}, []string{"case 13", "case 15", "case 16", "case 27", pairEngineWireName}},
		{[]execv2.Feature{session, run}, []string{
			"case 01", "case 02", "case 03", "case 04", "case 05", "case 13", "case 15", "case 16", "case 18", "case 27", pairRunOpenName, pairEngineWireName,
		}},
	} {
		t.Run(fmt.Sprint(tc.listed), func(t *testing.T) {
			var ran []string
			cases := engineCases()
			for i := range cases {
				cases[i].inProcess = nil
				if cases[i].wire == nil {
					continue
				}
				name := cases[i].name
				if cases[i].number > 0 {
					name = fmt.Sprintf("case %02d", cases[i].number)
				}
				cases[i].wire = func(checkT, exec.Target, *wireEnv) { ran = append(ran, name) }
			}
			t.Run("engine", func(t *testing.T) {
				listCases(t, cases, exec.Target{}, &inProcessEnv{}, &wireEnv{}, func(testing.TB) *execv2.DescribeResponse {
					return &execv2.DescribeResponse{Features: tc.listed}
				})
			})
			slices.Sort(ran)
			want := slices.Clone(tc.ran)
			slices.Sort(want)
			if !slices.Equal(ran, want) {
				t.Errorf("a build that lists %v ran the wire bodies of %q, want %q", tc.listed, ran, want)
			}
		})
	}
}
