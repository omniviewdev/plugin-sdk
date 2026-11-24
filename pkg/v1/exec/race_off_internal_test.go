//go:build !race

package exec

// raceEnabled says the tests run under the race detector; see race_on_internal_test.go.
const raceEnabled = false
