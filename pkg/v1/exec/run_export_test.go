package exec

import (
	"context"
	"time"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// RunSeams are the timings and the moments of a served run that a test takes over.
type RunSeams struct {
	// ProgressTicks makes the ticks that pace RunProgress, and the function that stops them. The SDK's own pace is a ticker.
	ProgressTicks func() (<-chan time.Time, func())
	// BeforeRead runs on the goroutine that reads the output of a run, before each read; ctx ends when the reader is to stop.
	BeforeRead func(ctx context.Context)
}

// SetRunSeams sets the seams of the runs a plugin serves. A test calls it before it starts a run.
func SetRunSeams(p *Plugin, s RunSeams) {
	p.v2.server.run = runConfig{progressTicks: s.ProgressTicks, beforeRead: s.BeforeRead}
}

// RunEngineLive returns the ids of the commands the engine of a plugin holds, sorted.
func RunEngineLive(p *Plugin) []string { return p.v2.server.engine.live() }

// RunEngineAbandoned returns how many functions the engine of a plugin gave up on that have not returned.
func RunEngineAbandoned(p *Plugin) int64 { return p.v2.server.engine.tel.abandonedCount() }

// RunInflight returns how many calls of the server hold a command that Plugin.Shutdown waits for.
func RunInflight(p *Plugin) int {
	f := &p.v2.server.inflight
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// RunStreamOn serves one RunStream on the server of a plugin, over a stream that the test implements.
func RunStreamOn(p *Plugin, req *execv2.RunRequest, stream execv2.Exec_RunStreamServer) error {
	return p.v2.server.RunStream(req, stream)
}
