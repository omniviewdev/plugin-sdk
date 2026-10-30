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
		case ErrorUnavailable, ErrorPermissionDenied:
			code = result.ErrorCode
		}
	}
	text := "Tool execution failed."
	switch code {
	case ErrorUnavailable:
		text = "Tool is unavailable."
	case ErrorPermissionDenied:
		text = "Tool operation is not permitted."
	}
	return &Result{IsError: true, ErrorCode: code, Content: []Content{{Text: text}}}
}
