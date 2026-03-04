package networktest

import (
	"fmt"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
)

// FakePortChecker provides deterministic port assignment for tests.
type FakePortChecker struct {
	mu              sync.Mutex
	nextPort        int32
	unavailablePorts map[int32]bool
}

var _ networker.PortChecker = (*FakePortChecker)(nil)

// NewFakePortChecker creates a FakePortChecker that assigns ports starting at startPort.
func NewFakePortChecker(startPort int32) *FakePortChecker {
	return &FakePortChecker{
		nextPort:        startPort,
		unavailablePorts: make(map[int32]bool),
	}
}

func (f *FakePortChecker) FindFreePort() (int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const maxProbes = 10000
	for i := 0; i < maxProbes; i++ {
		port := f.nextPort
		f.nextPort++
		if !f.unavailablePorts[port] {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port found after %d probes", maxProbes)
}

func (f *FakePortChecker) IsPortUnavailable(port int32) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unavailablePorts[port]
}

// BlockPort marks a port as unavailable.
func (f *FakePortChecker) BlockPort(port int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unavailablePorts[port] = true
}

// UnblockPort marks a port as available.
func (f *FakePortChecker) UnblockPort(port int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.unavailablePorts, port)
}
