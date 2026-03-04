package logs

import (
	"time"
	"unsafe"
)

// extractTimestamp tries to extract a leading timestamp from a log line.
// Returns the parsed time and the remaining content as a string.
//
// Supported formats (tried in order):
//   - RFC3339Nano (e.g. 2024-01-15T10:30:00.123456789Z)
//   - RFC3339     (e.g. 2024-01-15T10:30:00Z or +00:00)
//   - Bare UTC    (e.g. 2024-01-15T10:30:00Z — exact 20 chars)
//   - Bare local  (e.g. 2024-01-15T10:30:00 — exact 19 chars)
//
// Performance: uses byte-level pre-screening to avoid calling time.Parse on
// formats that clearly won't match. Each failed time.Parse allocates 4-5
// objects (error, quote, clone), so skipping them is critical at high
// throughput. This function is called once per log line.
//
// The returned content string is a proper copy (safe to store), while
// time.Parse candidates use unsafe.String (consumed immediately).
func extractTimestamp(line []byte) (time.Time, string) {
	n := len(line)

	// Minimum timestamp is "2006-01-02T15:04:05" = 19 bytes.
	// Quick reject: if the line is too short or doesn't start with a digit,
	// skip all parsing.
	if n < 19 || !isDigit(line[0]) {
		return time.Time{}, string(line)
	}

	// Byte-level structural check: NNNN-NN-NNTNN:NN:NN
	// Verifying separators at fixed positions is much cheaper than time.Parse.
	if !(line[4] == '-' && line[7] == '-' && line[10] == 'T' && line[13] == ':' && line[16] == ':') {
		return time.Time{}, string(line)
	}

	// We have a plausible timestamp prefix. Now determine the exact format
	// by scanning forward from position 19 to find where the timestamp ends.
	//
	// After "2006-01-02T15:04:05":
	//   - '.' → fractional seconds (RFC3339Nano), followed by timezone
	//   - 'Z' → RFC3339 with UTC
	//   - '+'/'-' → RFC3339 with offset
	//   - anything else → bare local time (19 chars)

	if n == 19 || (n > 19 && line[19] != '.' && line[19] != 'Z' && line[19] != '+' && line[19] != '-') {
		// Bare local: exactly "2006-01-02T15:04:05"
		ts, err := time.Parse("2006-01-02T15:04:05", unsafeString(line[:19]))
		if err != nil {
			return time.Time{}, string(line)
		}
		return ts, trimCopyContent(line[19:])
	}

	if line[19] == 'Z' {
		// Bare UTC: "2006-01-02T15:04:05Z"
		ts, err := time.Parse("2006-01-02T15:04:05Z", unsafeString(line[:20]))
		if err != nil {
			return time.Time{}, string(line)
		}
		return ts, trimCopyContent(line[20:])
	}

	// RFC3339 or RFC3339Nano — find the end of the timestamp.
	tsEnd := findTimestampEnd(line)
	if tsEnd < 0 {
		return time.Time{}, string(line)
	}

	// Use unsafeString for the candidate — consumed immediately by time.Parse,
	// the resulting time.Time doesn't reference the original bytes.
	candidate := unsafeString(line[:tsEnd])

	// Try RFC3339Nano first (has fractional seconds), then RFC3339.
	if line[19] == '.' {
		ts, err := time.Parse(time.RFC3339Nano, candidate)
		if err == nil {
			return ts, trimCopyContent(line[tsEnd:])
		}
	}

	ts, err := time.Parse(time.RFC3339, candidate)
	if err == nil {
		return ts, trimCopyContent(line[tsEnd:])
	}

	return time.Time{}, string(line)
}

// findTimestampEnd scans from position 19 to find where the RFC3339 timestamp
// ends (after the timezone offset). Returns -1 if no valid end is found.
func findTimestampEnd(line []byte) int {
	n := len(line)
	i := 19

	// Skip fractional seconds: ".123456789"
	if i < n && line[i] == '.' {
		i++
		for i < n && isDigit(line[i]) {
			i++
		}
	}

	// Expect timezone: 'Z' or '+HH:MM' / '-HH:MM'
	if i >= n {
		return -1
	}

	if line[i] == 'Z' {
		return i + 1
	}

	if line[i] == '+' || line[i] == '-' {
		// Expect at least "+HH:MM" (6 chars)
		if i+6 <= n {
			return i + 6
		}
		return -1
	}

	return -1
}

// isDigit returns true if b is an ASCII digit.
func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// trimCopyContent trims leading spaces and returns a proper string copy.
// The copy is necessary because the input bytes may be reused by bufio.Scanner.
func trimCopyContent(b []byte) string {
	i := 0
	for i < len(b) && b[i] == ' ' {
		i++
	}
	return string(b[i:])
}

// unsafeString converts a byte slice to a string without copying.
// ONLY safe when the string is consumed immediately and not stored (e.g.,
// passed to time.Parse which returns a time.Time value, not a string).
func unsafeString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}
