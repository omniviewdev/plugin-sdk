package session

import "testing"

func TestCauseIsNamedByALowercaseWord(t *testing.T) {
	for cause, want := range map[Cause]string{
		CauseNone:      "none",
		CauseFinished:  "finished",
		CauseRequested: "requested",
		CauseShutdown:  "shutdown",
		CauseRetired:   "retired",
		CauseExpired:   "expired",
		CauseTimeout:   "timeout",
		CauseCancelled: "cancelled",
	} {
		if got := cause.String(); got != want {
			t.Errorf("Cause(%d).String() = %q, want %q", uint8(cause), got, want)
		}
	}
}

func TestACauseThatIsNotOneIsNamedByItsNumber(t *testing.T) {
	for cause, want := range map[Cause]string{
		CauseCancelled + 1: "cause(8)",
		Cause(200):         "cause(200)",
		Cause(255):         "cause(255)",
	} {
		if got := cause.String(); got != want {
			t.Errorf("Cause(%d).String() = %q, want %q", uint8(cause), got, want)
		}
	}
}

func TestEveryCauseHasItsOwnName(t *testing.T) {
	seen := make(map[string]Cause)
	for cause := CauseNone; cause <= CauseCancelled; cause++ {
		name := cause.String()
		if other, dup := seen[name]; dup {
			t.Errorf("causes %d and %d are both named %q", other, cause, name)
		}
		seen[name] = cause
	}
}
