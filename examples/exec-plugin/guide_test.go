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

// The guide points at the page that keeps the conditions of the review, and the page exists.
func TestTheGuidePointsAtTheConditionsBeforeAnyTag(t *testing.T) {
	if !strings.Contains(lf(readFile(t, "../../docs/exec.md")), "(exec-before-any-tag.md)") {
		t.Error("docs/exec.md does not link docs/exec-before-any-tag.md")
	}
	page := lf(readFile(t, "../../docs/exec-before-any-tag.md"))
	for _, phrase := range []string{"ed6f26f8445c2e756de5506882c3927e9f228b47", "ReviewSdk87", "verbatim", "**C3, process.**", "#### B6. The open follow-up"} {
		if !strings.Contains(page, phrase) {
			t.Errorf("docs/exec-before-any-tag.md does not say %q", phrase)
		}
	}
	if !strings.Contains(lf(readFile(t, "../../docs/README.md")), "(exec-before-any-tag.md)") {
		t.Error("docs/README.md has no row for docs/exec-before-any-tag.md")
	}
}
