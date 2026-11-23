package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// This file is the Classify snippet of docs/exec.md, kept compiling and tested against the texts that client-go really hands over on a
// cluster (kind v1.34.0, the proof of a Pod provider on this contract).

// guide:begin podClassify
// podClassify maps what client-go hands over as text. The API's own refusals of the upgrade arrive flattened, with no type to
// read ("unable to upgrade connection: pods \"x\" is forbidden: User ..." for a 403, "unable to upgrade connection: Unauthorized"
// for a 401), so the status is read from the text here and ErrorFromHTTPStatus gives it its class.
func podClassify(err error) (*exec.Error, bool) {
	text := err.Error()
	switch {
	case strings.Contains(text, "unable to upgrade connection") && strings.Contains(text, " is forbidden"):
		return exec.ErrorFromHTTPStatus(http.StatusForbidden, err), true // FORBIDDEN, TARGET_DENIED
	case strings.HasSuffix(text, "unable to upgrade connection: Unauthorized"):
		return exec.ErrorFromHTTPStatus(http.StatusUnauthorized, err), true // UNAUTHORIZED
	}
	return nil, false // anything else is the SDK's: the class of a status it can read, or INTERNAL
}

// guide:end podClassify

// The two refusals of the upgrade that client-go flattens into text are typed, with the texts the API really sent, and the other
// texts of the proof are left to the author's other rules and to the SDK.
func TestPodClassifyTypesTheRefusalsOfTheUpgradeThatClientGoFlattensIntoText(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		class  exec.ErrorClass
		reason exec.ErrorReason
		mine   bool
	}{
		{"RBAC denied", `unable to upgrade connection: pods "quiet" is forbidden: User "system:serviceaccount:kproof:noexec" cannot create resource "pods/exec" in API group "" in the namespace "kproof"`,
			exec.ErrorClassForbidden, exec.ErrorReasonTargetDenied, true},
		{"401", "unable to upgrade connection: Unauthorized", exec.ErrorClassUnauthorized, exec.ErrorReasonUnspecified, true},
		{"a container that is not running", `Internal error occurred: unable to upgrade connection: container not found ("app")`, 0, 0, false},
		{"a pod the kubelet lost", "Internal error occurred: unable to upgrade connection: pod does not exist", 0, 0, false},
		{"no such executable", `OCI runtime exec failed: exec: "bash": executable file not found in $PATH: unknown`, 0, 0, false},
		{"a token in a message is not a status", "Forbidden: token abc", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New(tc.text)
			got, ok := podClassify(cause)
			if ok != tc.mine {
				t.Fatalf("podClassify(%q) = %+v, %v; want mine %v", tc.text, got, ok, tc.mine)
			}
			if !ok {
				return
			}
			if got.Class != tc.class || got.Reason != tc.reason || !errors.Is(got, cause) {
				t.Errorf("podClassify(%q) = %v / %v (cause kept %v), want %v / %v", tc.text, got.Class, got.Reason, errors.Is(got, cause), tc.class, tc.reason)
			}
		})
	}
}

// The guide prints the snippet as it is in this file.
func TestTheGuideShowsTheClassifySnippet(t *testing.T) {
	assertGuideShows(t, "classify_snippet_test.go", "podClassify")
}

// The guide says what was decided of a Success status and of the Started of a quiet command, and no longer says what contradicted it.
func TestTheGuideTeachesTheDecisionOnASuccessStatusAndTheStartOfAQuietCommand(t *testing.T) {
	guide := lf(readFile(t, "../../docs/exec.md"))
	for _, phrase := range []string{
		"exec.ExitOfStreamError", "exec.OnFirstRead", "exec.ErrorFromHTTPStatus", "Which stall behaviour to declare",
		"`Exited{0, TRANSPORT}`", "never \"exit 0\"", "**the SDK cannot tell the two apart**", "StallLossy` for Kubernetes",
		"1.4 to 1.9 MB", "unable to upgrade connection: pods", "There is no first-byte gate to write",
	} {
		if !strings.Contains(guide, phrase) {
			t.Errorf("docs/exec.md does not say %q", phrase)
		}
	}
	for _, phrase := range []string{
		"never `ExitTransport(0)` and never `ExitCode(0)`",
		"a stream that ended with **no** status is `ExitLost`",
		"can return ExitCode(0) for a Success status",
		"so nil says\n\t\t// nothing about the exit",
	} {
		if strings.Contains(guide, phrase) {
			t.Errorf("docs/exec.md still says %q, which the decision on a Success status contradicts", phrase)
		}
	}
}
