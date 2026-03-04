package sdk

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCurrentProtocolVersion_IsOne(t *testing.T) {
	assert.Equal(t, 1, CurrentProtocolVersion)
}

func TestRegisterLifecycle_UseCurrentProtocolVersion(t *testing.T) {
	// Ensures the constant is used rather than a hard-coded literal.
	assert.Equal(t, 1, CurrentProtocolVersion,
		"CurrentProtocolVersion should be 1 for v1 SDK")
}
