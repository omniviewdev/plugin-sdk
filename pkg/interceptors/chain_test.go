package interceptors

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultUnaryInterceptors_ReturnsThree(t *testing.T) {
	interceptors := DefaultUnaryInterceptors()
	assert.Len(t, interceptors, 3, "should return exactly 3 unary interceptors")

	// Each element should be non-nil (a valid interceptor function).
	for i, ic := range interceptors {
		assert.NotNil(t, ic, "unary interceptor at index %d should not be nil", i)
	}
}

func TestDefaultStreamInterceptors_ReturnsThree(t *testing.T) {
	interceptors := DefaultStreamInterceptors()
	assert.Len(t, interceptors, 3, "should return exactly 3 stream interceptors")

	for i, ic := range interceptors {
		assert.NotNil(t, ic, "stream interceptor at index %d should not be nil", i)
	}
}
