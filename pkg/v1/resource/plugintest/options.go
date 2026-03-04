package plugintest

import (
	"context"
	"time"
)

// harnessConfig holds configuration for the Harness.
type harnessConfig[ClientT any] struct {
	timeout time.Duration
	ctx     context.Context
}

// Option configures a Harness.
type Option[ClientT any] func(*harnessConfig[ClientT])

// WithTimeout sets the default timeout for wait operations.
func WithTimeout[ClientT any](d time.Duration) Option[ClientT] {
	return func(c *harnessConfig[ClientT]) {
		c.timeout = d
	}
}

// WithContext sets a parent context for the harness.
func WithContext[ClientT any](ctx context.Context) Option[ClientT] {
	return func(c *harnessConfig[ClientT]) {
		c.ctx = ctx
	}
}
