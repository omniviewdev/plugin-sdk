package resourcev2

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Defect (plan item S5): StartConnection runs Lifecycle.CreateClient on the
// generation context with no deadline (controller_lifecycle.go startClient), so
// a native auth step that never returns holds the pending slot and every waiter
// until the creating caller cancels. ConnectionAcquisitionTimeoutSetting is
// opt-in and nothing in the controller applies ConnectionAcquisitionTimeout.
//
// Fixed looks like: with a caller context that has no deadline and no plugin
// configuration, the context handed to CreateClient carries a finite deadline
// (the documented acquisition default, at most the 300 s maximum), so a hung
// CreateClient that honors its context ends the start with DEADLINE_EXCEEDED
// and frees the slot.
func TestStartConnectionAppliesDefaultAcquisitionDeadlineToCreateClient(t *testing.T) {
	cfg := controllerConfig()
	lifecycle := controllerLifecycle(&atomic.Int32{})
	var (
		sawDeadline bool
		deadline    time.Time
	)
	lifecycle.CreateClient = func(ctx context.Context, connection types.Connection) (*string, error) {
		deadline, sawDeadline = ctx.Deadline()
		client := connection.ID
		return &client, nil
	}
	cfg.Lifecycle = lifecycle
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	if !sawDeadline {
		t.Fatal("S5: CreateClient context has no deadline; a hung CreateClient holds the pending slot until the creator cancels")
	}
	if remaining := deadline.Sub(began); remaining <= 0 || remaining > time.Duration(maxConnectionAcquisitionTimeoutSeconds)*time.Second+time.Second {
		t.Fatalf("S5: CreateClient deadline %v from start is outside (0, %ds]", remaining, maxConnectionAcquisitionTimeoutSeconds)
	}
}
