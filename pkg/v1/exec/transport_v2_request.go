package exec

import (
	"errors"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// This file holds the validators of the bounds of a request that every target shares. The plugin's admission
// (admission_request_v2.go) calls them. Each reads the count of its list first, so that it holds when it is called on
// its own, though admission has bounded the lists before: rejectUnknownFields every list, nameValueMap the lists of
// names.
//
// Every refusal is an *Error: ErrorClassInvalidRequest, or ErrorClassUnsupported for a feature or an integration mode that
// this build does not know. The detail is local text and never quotes the request: argv elements, environment
// values and ids can hold secrets.

// maxRequestedFeatures is how many features a Start may ask for: marks and modes, each once.
const maxRequestedFeatures = 2

// printableASCII reports whether s holds only the bytes 0x21 to 0x7e.
func printableASCII(s string) bool {
	for i := range len(s) {
		if c := s[i]; c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// validateSessionID checks a session id: 1 to maxSessionIDBytes bytes of printable ASCII.
func validateSessionID(id string) *Error {
	if len(id) < 1 || len(id) > maxSessionIDBytes || !printableASCII(id) {
		return invalidRequest("session id must be 1 to %d bytes of printable ASCII", maxSessionIDBytes)
	}
	return nil
}

// validateRequestID checks the id of a run: at most maxRequestIDBytes bytes of printable ASCII, and it may be empty.
func validateRequestID(id string) *Error {
	if len(id) > maxRequestIDBytes || !printableASCII(id) {
		return invalidRequest("request id must be at most %d bytes of printable ASCII", maxRequestIDBytes)
	}
	return nil
}

// validateRequestArgv checks an argv against the bounds of the wire and returns its size in bytes: at most
// maxArgvElements elements, each at most maxArgvElementBytes bytes, valid UTF-8 (the wire field is a string) and
// without a NUL, the first one not empty, and at most maxBytes bytes in all. The count is read first, so that a huge
// argv is refused without walking it. With required an empty argv is refused (a run has no default argv); a Start's
// empty argv means the target's default.
func validateRequestArgv(argv []string, maxBytes int, required bool) (int, *Error) {
	if len(argv) == 0 {
		if required {
			return 0, invalidRequest("argv is required")
		}
		return 0, nil
	}
	if len(argv) > maxArgvElements {
		return 0, invalidRequest("argv has %d elements, at most %d", len(argv), maxArgvElements)
	}
	if argv[0] == "" {
		return 0, invalidRequest("argv[0] must not be empty")
	}
	total := 0
	for i, a := range argv {
		if len(a) > maxArgvElementBytes {
			return 0, invalidRequest("argv[%d] is %d bytes, at most %d", i, len(a), maxArgvElementBytes)
		}
		if strings.IndexByte(a, 0) >= 0 {
			return 0, invalidRequest("argv[%d] holds a NUL", i)
		}
		if !utf8.ValidString(a) {
			return 0, invalidRequest("argv[%d] is not valid UTF-8", i)
		}
		total += len(a)
		if total > maxBytes {
			return 0, invalidRequest("argv is more than %d bytes in all", maxBytes)
		}
	}
	return total, nil
}

// validEnvName reports whether s matches ^[A-Za-z_][A-Za-z0-9_]*$ and is at most maxEnvNameBytes bytes.
func validEnvName(s string) bool {
	if len(s) == 0 || len(s) > maxEnvNameBytes {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// validateRequestEnv checks an environment against the bounds of the wire and returns its size, names plus values:
// at most maxEnvEntries entries, every name valid, every value at most maxEnvValueBytes bytes, valid UTF-8 and
// without a NUL, and at most maxBytes bytes in all. The count is read first. A refusal never quotes a name or a value.
func validateRequestEnv(env map[string]string, maxBytes int) (int, *Error) {
	if len(env) > maxEnvEntries {
		return 0, invalidRequest("env has %d entries, at most %d", len(env), maxEnvEntries)
	}
	total := 0
	for name, value := range env {
		if !validEnvName(name) {
			return 0, invalidRequest("an env name is empty, longer than %d bytes or not [A-Za-z_][A-Za-z0-9_]*", maxEnvNameBytes)
		}
		if len(value) > maxEnvValueBytes {
			return 0, invalidRequest("an env value is %d bytes, at most %d", len(value), maxEnvValueBytes)
		}
		if strings.IndexByte(value, 0) >= 0 {
			return 0, invalidRequest("an env value holds a NUL")
		}
		if !utf8.ValidString(value) {
			return 0, invalidRequest("an env value is not valid UTF-8")
		}
		total += len(name) + len(value)
		if total > maxBytes {
			return 0, invalidRequest("env is more than %d bytes in all", maxBytes)
		}
	}
	return total, nil
}

// validateRequestCwd checks a working directory: at most maxCwdBytes bytes, valid UTF-8 and no NUL.
func validateRequestCwd(cwd string) *Error {
	if len(cwd) > maxCwdBytes {
		return invalidRequest("cwd is %d bytes, at most %d", len(cwd), maxCwdBytes)
	}
	if strings.IndexByte(cwd, 0) >= 0 {
		return invalidRequest("cwd holds a NUL")
	}
	if !utf8.ValidString(cwd) {
		return invalidRequest("cwd is not valid UTF-8")
	}
	return nil
}

// validateRequestLabels checks labels: at most maxLabelEntries, a key at most maxLabelKeyBytes bytes, a value at
// most maxLabelValueBytes bytes, and both plain text.
func validateRequestLabels(labels map[string]string) *Error {
	if len(labels) > maxLabelEntries {
		return invalidRequest("%d labels exceed the limit of %d", len(labels), maxLabelEntries)
	}
	for key, value := range labels {
		if len(key) > maxLabelKeyBytes || len(value) > maxLabelValueBytes || !validText(key) || !validText(value) {
			return invalidRequest("a label key is longer than %d bytes, a value longer than %d bytes, or one is not plain text",
				maxLabelKeyBytes, maxLabelValueBytes)
		}
	}
	return nil
}

// requestSizeFromProto decodes the size of a Start: no size (absent, or both dimensions zero), or both dimensions
// non-zero and at most 65535. The Go Size holds uint16, so a larger wire value is refused and never truncated.
func requestSizeFromProto(p *execv2.Size) (Size, *Error) {
	cols, rows := p.GetCols(), p.GetRows()
	if cols == 0 && rows == 0 {
		return Size{}, nil
	}
	if cols == 0 || rows == 0 {
		return Size{}, invalidRequest("a size needs both dimensions or neither")
	}
	if cols > math.MaxUint16 || rows > math.MaxUint16 {
		return Size{}, invalidRequest("a size dimension is above %d", math.MaxUint16)
	}
	return Size{Cols: uint16(cols), Rows: uint16(rows)}, nil //nolint:gosec // both dimensions are at most 65535
}

// validateRequestFeatures checks the features of a Start in list order, and the first violation decides: a feature
// this build does not know (unspecified, or a number of a newer contract) is ErrorClassUnsupported; a feature that cannot
// be requested, or one listed twice, is ErrorClassInvalidRequest. Only marks and modes may be requested.
func validateRequestFeatures(features []execv2.Feature) *Error {
	for i, f := range features {
		switch {
		case !knownFeature(f):
			return unsupportedRequest("features[%d] is not a feature this build knows", i)
		case f != execv2.Feature_FEATURE_MARKS && f != execv2.Feature_FEATURE_MODES:
			return invalidRequest("%s cannot be requested", f)
		case slices.Contains(features[:i], f):
			return invalidRequest("%s is requested twice", f)
		}
	}
	return nil
}

// validateRequestIntegration decides on an integration request. A request for no mode is accepted whatever its
// consent flag says. Any mode without the host's consent is ErrorClassInvalidRequest with ErrorReasonConsentRequired, and
// consent is checked before anything else about the mode; a mode this build does not know is ErrorClassUnsupported.
func validateRequestIntegration(requested, known, consent bool) *Error {
	switch {
	case !requested:
		return nil
	case !consent:
		return NewError(ErrorClassInvalidRequest, ErrorReasonConsentRequired, errors.New("shell integration needs the host's consent"))
	case !known:
		return unsupportedRequest("the integration mode is not one this build knows")
	}
	return nil
}
