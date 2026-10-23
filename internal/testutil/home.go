// Package testutil contains shared SDK test fixtures, never runtime behavior.
package testutil

import "testing"

// SetHome isolates os.UserHomeDir on Unix (HOME) and Windows (USERPROFILE).
// Setenv registers restoration with the test, including originally unset values.
func SetHome(t testing.TB, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}
