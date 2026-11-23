//go:build race

package exec

// raceEnabled says the tests run under the race detector, which allocates for its own bookkeeping and makes everything slower:
// a test that counts allocations or asserts a speed is skipped, or keeps only the floors that a slow run cannot break.
const raceEnabled = true
