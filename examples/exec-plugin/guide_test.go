package main

import (
	"strings"
	"testing"
)

// lf returns text with its line endings as LF: a checkout on Windows may convert them, and a phrase that the guide
// says must not depend on that.
func lf(text string) string { return strings.ReplaceAll(text, "\r\n", "\n") }

// A session has exactly one end and at most one Ended frame: a session that ends while no stream is attached (a lease
// that expires, a retirement, a shutdown) has no stream to carry one, and its end is the tombstone's. The guide says so
// where it states the rule and where it describes the two last frames, and nowhere says that Ended is sent exactly once
// per session.
func TestTheGuideSaysASessionHasAtMostOneEnded(t *testing.T) {
	guide := lf(readFile(t, "../../docs/exec.md"))
	for _, phrase := range []string{
		"at most one `Ended` frame",
		"has exactly one **end**",
		"has no `Ended` at all, and its end is the tombstone's",
		"`SessionInfo.ended_with`",
	} {
		if !strings.Contains(guide, phrase) {
			t.Errorf("docs/exec.md does not say %q", phrase)
		}
	}
	for _, phrase := range []string{
		"sent once per session",
		"`Ended` once per session",
		"exactly once** in its life",
		"exactly one `Ended`",
		"the single `Ended`",
	} {
		if strings.Contains(guide, phrase) {
			t.Errorf("docs/exec.md says %q: a session that ends while no stream is attached has no Ended frame", phrase)
		}
	}
}

// The mode of a session is declared on its choice and is in Opened, so the guide no longer teaches an author to report
// it with Started, nor to gate the first byte of output: the SDK sends the Started before it.
func TestTheGuideDoesNotTeachReportingTheModeOrGatingTheFirstByte(t *testing.T) {
	guide := lf(readFile(t, "../../docs/exec.md"))
	for _, phrase := range []string{
		"Choice.Capabilities",
		"never reports it",
		"There is no first-byte gate to write",
	} {
		if !strings.Contains(guide, phrase) {
			t.Errorf("docs/exec.md does not say %q", phrase)
		}
	}
	for _, phrase := range []string{
		"firstOutput",
		"awaitStream",
		"reportsStart",
		"is reported as started when its first output arrives",
		"keeps the kind's upper bound as its facts",
		"narrows a copy to what is true of it",
	} {
		if strings.Contains(guide, phrase) {
			t.Errorf("docs/exec.md still says %q", phrase)
		}
	}
}

// The guide points at the gate that must be cleared before any tag that ships exec.v2 and at the hand-over status of the lane. Both
// pages exist and say what they were written to say (the gate: where its conditions come from, that it is not cleared, its first item
// and how it is cleared; the status: the branch rule, the tag rule and the Kubernetes spike), and the docs index links both. Whoever
// clears the gate changes its state sentence and this test together.
func TestTheGuidePointsAtTheConditionsBeforeAnyTag(t *testing.T) {
	guide := lf(readFile(t, "../../docs/exec.md"))
	for _, link := range []string{"(exec-before-any-tag.md)", "(exec-status.md)"} {
		if !strings.Contains(guide, link) {
			t.Errorf("docs/exec.md does not link %s", link)
		}
	}
	for page, phrases := range map[string][]string{
		"docs/exec-before-any-tag.md": {"ed6f26f8445c2e756de5506882c3927e9f228b47", "the tag gate is NOT cleared", "### C3. Process: what a stable tag freezes", "## Clearing the gate"},
		"docs/exec-status.md":         {"`feature/exec-v2` carries the lane", "Never tag from `main`", "wip/spike/exec-v2-provider"},
	} {
		text := lf(readFile(t, "../../"+page))
		for _, phrase := range phrases {
			if !strings.Contains(text, phrase) {
				t.Errorf("%s does not say %q", page, phrase)
			}
		}
	}
	index := lf(readFile(t, "../../docs/README.md"))
	for _, link := range []string{"(exec-before-any-tag.md)", "(exec-status.md)"} {
		if !strings.Contains(index, link) {
			t.Errorf("docs/README.md has no row for %s", link)
		}
	}
}
