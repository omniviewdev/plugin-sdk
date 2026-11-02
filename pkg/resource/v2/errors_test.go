package resourcev2

import (
	"context"
	"fmt"
	"testing"
)

func TestErrorCodePreservesCancellationPrecedence(t *testing.T) {
	for _, wrapperCode := range []string{"INTERNAL", "PROVIDER_ERROR", "CONFLICT", "UNSUPPORTED"} {
		for _, cancellation := range []struct {
			cause error
			code  string
		}{{context.Canceled, "CANCELLED"}, {context.DeadlineExceeded, "TIMEOUT"}} {
			t.Run(wrapperCode+"/"+cancellation.code, func(t *testing.T) {
				err := fmt.Errorf("operation: %w", &ResourceOperationError{Code: wrapperCode, Err: cancellation.cause})
				if got := ErrorCode(err); got != cancellation.code {
					t.Fatalf("ErrorCode = %q; want %q", got, cancellation.code)
				}
			})
		}
	}
	for _, brokerCode := range []string{"GRANT_DENIED", "CONNECTION_NOT_STARTED", "OWNER_UNAVAILABLE", "DELEGATION_NOT_ACTIVE"} {
		t.Run(brokerCode, func(t *testing.T) {
			err := &ResourceOperationError{Code: brokerCode, Err: context.Canceled}
			if got := ErrorCode(err); got != brokerCode {
				t.Fatalf("ErrorCode = %q; want %q", got, brokerCode)
			}
		})
	}
}
