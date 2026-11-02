package tools

// ContractVersion identifies exclusive tools.v2 explicit-route/native-admission semantics.
const ContractVersion uint32 = 2

// Safe error codes are a closed vocabulary. Diagnostic strings never cross this boundary.
const (
	ErrorExecutionFailed  = "EXECUTION_FAILED"
	ErrorUnavailable      = "UNAVAILABLE"
	ErrorPermissionDenied = "PERMISSION_DENIED"
)

func safeFailure(result *Result) *Result {
	code := ErrorExecutionFailed
	if result != nil {
		switch result.ErrorCode {
		case ErrorUnavailable, ErrorPermissionDenied, "GRANT_DENIED", "CONNECTION_NOT_STARTED", "OWNER_UNAVAILABLE", "DELEGATION_NOT_ACTIVE", "CONFLICT", "TIMEOUT", "UNSUPPORTED":
			code = result.ErrorCode
		}
	}
	text := "Tool execution failed."
	switch code {
	case ErrorUnavailable:
		text = "Tool is unavailable."
	case ErrorPermissionDenied:
		text = "Tool operation is not permitted."
	case "GRANT_DENIED":
		text = "Cross-plugin connection access is not permitted."
	case "CONNECTION_NOT_STARTED":
		text = "The owner connection is not started."
	case "OWNER_UNAVAILABLE":
		text = "The connection owner is unavailable."
	case "DELEGATION_NOT_ACTIVE":
		text = "The delegated tool call is not active."
	case "CONFLICT":
		text = "The object changed during the update."
	case "TIMEOUT":
		text = "The operation timed out."
	case "UNSUPPORTED":
		text = "The operation is not supported."
	}
	return &Result{IsError: true, ErrorCode: code, Content: []Content{{Text: text}}}
}
