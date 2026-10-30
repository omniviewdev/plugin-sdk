package networker

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// PortChecker abstracts port availability operations so tests can inject fakes.
type PortChecker interface {
	// FindFreePort returns a free TCP port.
	FindFreePort() (int32, error)
	// IsPortUnavailable returns true if the port is already in use.
	IsPortUnavailable(port int32) bool
}

// RealPortChecker performs actual network checks.
type RealPortChecker struct{}

var _ PortChecker = RealPortChecker{}

// FindFreePort returns an available TCP port selected by the operating system.
func (RealPortChecker) FindFreePort() (int32, error) {
	return FindFreeTCPPort()
}

// IsPortUnavailable reports whether a TCP connection to the local port succeeds.
func (RealPortChecker) IsPortUnavailable(port int32) bool {
	return IsPortUnavailable(port)
}

const tcpPortBits = 16

// FindFreeTCPPort finds an available port by letting the OS pick a free port.
// The port is not reserved after this function returns.
func FindFreeTCPPort() (int32, error) {
	var config net.ListenConfig
	lis, err := config.Listen(context.Background(), "tcp", "localhost:0")
	if err != nil {
		return 0, fmt.Errorf("error listening on a port: %w", err)
	}
	addr := lis.Addr().String()
	if err = lis.Close(); err != nil {
		return 0, fmt.Errorf("error releasing selected port: %w", err)
	}

	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("error splitting host and port: %w", err)
	}

	port, err := strconv.ParseUint(portStr, 10, tcpPortBits)
	if err != nil {
		return 0, fmt.Errorf("error converting port string to int: %w", err)
	}

	return int32(port), nil
}

// IsPortUnavailable reports whether a TCP connection to the local port succeeds.
// A failed probe returns false but does not guarantee that a listener can bind.
func IsPortUnavailable(port int32) bool {
	var dialer net.Dialer
	conn, err := dialer.DialContext(context.Background(), "tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return false
	}
	// A successful connection proves the port is in use regardless of cleanup errors.
	_ = conn.Close()
	return true
}
