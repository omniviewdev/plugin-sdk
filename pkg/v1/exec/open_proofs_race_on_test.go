//go:build race

package exec_test

// proofRace says the tests run under the race detector, which makes everything several times slower: the proofs that assert a
// speed or a latency keep only the floors that a slow run cannot break, and log what they measured.
const proofRace = true
