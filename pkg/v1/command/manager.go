// Package command dispatches plugin-specific opaque command payloads through optional RPC transport.
package command

import (
	"context"
	"errors"
	"fmt"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/settings"
)

var (
	// ErrHandlerNotFound is returned when a handler is not found.
	ErrHandlerNotFound = errors.New("handler not found")
	// ErrHandlerTimeout is returned when a handler times out.
	ErrHandlerTimeout = errors.New("handler timeout")
)

// Manager dispatches commands through a handler map shared with its constructor.
type Manager struct {
	log              logging.Logger
	settingsProvider settings.Provider
	handlers         map[string]Handler
}

var _ Provider = (*Manager)(nil)

// NewManager retains handlers without copying. Do not mutate the map during calls.
// A nil logger disables logging; handlers own any synchronization they need.
func NewManager(
	log logging.Logger,
	sp settings.Provider,
	handlers map[string]Handler,
) *Manager {
	if log == nil {
		log = logging.NewNop()
	}
	return &Manager{
		log:              log.Named("manager"),
		settingsProvider: sp,
		handlers:         handlers,
	}
}

// Call invokes the exact target handler or returns ErrHandlerNotFound.
// Payload and result ownership follow the selected handler's contract.
func (m *Manager) Call(
	ctx *types.PluginContext,
	target string,
	payload []byte,
) ([]byte, error) {
	callCtx := commandLogContext(ctx)
	handler, ok := m.handlers[target]
	if !ok {
		m.log.Debugw(callCtx, "command handler not found", "path", target)
		return nil, ErrHandlerNotFound
	}
	result, err := handler(ctx, payload)
	if err != nil {
		m.log.Debugw(callCtx, "command handler failed",
			"path", target, "error_type", fmt.Sprintf("%T", err), "payload_bytes", len(payload))
	}
	return result, err
}

func commandLogContext(pc *types.PluginContext) context.Context {
	if pc != nil && pc.Context != nil {
		return pc.Context
	}
	return context.Background()
}
