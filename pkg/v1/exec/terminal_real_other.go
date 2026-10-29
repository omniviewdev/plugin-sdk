//go:build !unix

package exec

import (
	"fmt"

	"github.com/creack/pty"
)

// NewRealTerminalFactory returns a TerminalFactory that always fails:
// creack/pty has no PTYs on this platform.
func NewRealTerminalFactory() TerminalFactory {
	return func() (Terminal, error) {
		return nil, fmt.Errorf("failed to open pty: %w", pty.ErrUnsupported)
	}
}
