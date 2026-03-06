package logs_test

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/logs/logtest"
)

// ===========================================================================
// Benchmark infrastructure — high-throughput sources and sinks
// ===========================================================================

// countingSink is a minimal OutputSink that counts lines with atomics.
// Zero allocation overhead, no mutex, no slices — measures Manager hot path only.
type countingSink struct {
	lineCount  atomic.Int64
	eventCount atomic.Int64
}

func (s *countingSink) OnLine(_ logs.LogLine) { s.lineCount.Add(1) }
func (s *countingSink) OnEvent(_ string, _ logs.LogStreamEvent) {
	s.eventCount.Add(1)
}
func (s *countingSink) reset() {
	s.lineCount.Store(0)
	s.eventCount.Store(0)
}

// pipeSource writes N lines at maximum speed to an io.Pipe.
// Unlike channelReader, this has zero channel overhead — it pushes bytes
// directly through a synchronous pipe, which is what real log streams do.
type pipeSource struct {
	lineCount    int
	lineTemplate string // pre-built line with newline, ready to write
}

func newPipeSource(lineCount int, lineLen int) *pipeSource {
	// Build a realistic log line: RFC3339 timestamp + content
	ts := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC).Format(time.RFC3339Nano)
	contentLen := lineLen - len(ts) - 2 // -2 for space and newline
	if contentLen < 10 {
		contentLen = 10
	}
	content := strings.Repeat("x", contentLen)
	line := ts + " " + content + "\n"
	return &pipeSource{lineCount: lineCount, lineTemplate: line}
}

// handler returns a LogHandlerFunc that writes lineCount lines then returns EOF.
func (p *pipeSource) handler() logs.LogHandlerFunc {
	return func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		pr, pw := io.Pipe()
		go func() {
			lineBytes := []byte(p.lineTemplate)
			for i := 0; i < p.lineCount; i++ {
				if _, err := pw.Write(lineBytes); err != nil {
					pw.CloseWithError(err)
					return
				}
			}
			pw.Close()
		}()
		return pr, nil
	}
}

// infinitePipeHandler returns a handler that writes lines until context cancel.
func infinitePipeHandler(lineLen int) logs.LogHandlerFunc {
	ts := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC).Format(time.RFC3339Nano)
	contentLen := lineLen - len(ts) - 2
	if contentLen < 10 {
		contentLen = 10
	}
	content := strings.Repeat("x", contentLen)
	line := []byte(ts + " " + content + "\n")

	return func(pctx *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		pr, pw := io.Pipe()
		go func() {
			for {
				if pctx.Context.Err() != nil {
					pw.CloseWithError(pctx.Context.Err())
					return
				}
				if _, err := pw.Write(line); err != nil {
					pw.CloseWithError(err)
					return
				}
			}
		}()
		return pr, nil
	}
}

// ===========================================================================
// Benchmark: single-session line throughput (measures hot path per-line cost)
// ===========================================================================

// BenchmarkLineThroughput_1Source measures raw per-line throughput through
// the Manager's hot path: scanner → extractTimestamp → LogLine → sink.OnLine.
// Reports: ns/line, bytes/line, allocs/line.
func BenchmarkLineThroughput_1Source(b *testing.B) {
	for _, lineLen := range []int{100, 256, 512, 1024} {
		b.Run(fmt.Sprintf("lineLen=%d", lineLen), func(b *testing.B) {
			sink := &countingSink{}
			src := newPipeSource(b.N, lineLen)

			mgr := logs.NewManager(logs.ManagerConfig{
				Handlers: map[string]logs.Handler{
					"test/pod": {
						Plugin:   "test",
						Resource: "pod",
						Handler:  src.handler(),
						SourceBuilder: logtest.SingleSourceBuilder(
							"pod-1", map[string]string{"app": "nginx"},
						),
					},
				},
				Sink:  sink,
				Clock: timeutil.RealClock{},
			})

			pctx := types.NewPluginContext(context.Background(), "bench", nil, nil, nil)

			b.ResetTimer()
			b.ReportAllocs()

			session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
				ResourceKey: "pod",
				ResourceID:  "bench-pod",
				Options:     logs.LogSessionOptions{Follow: false},
			})
			if err != nil {
				b.Fatalf("CreateSession: %v", err)
			}

			mgr.Wait()
			b.StopTimer()

			got := sink.lineCount.Load()
			if got != int64(b.N) {
				b.Logf("line count mismatch: got %d, want %d (session %s)", got, b.N, session.ID)
			}

			mgr.Close()
		})
	}
}

// ===========================================================================
// Benchmark: multi-source fan-out (concurrent sources within one session)
// ===========================================================================

// BenchmarkMultiSourceFanOut measures throughput with N sources pushing lines
// concurrently through one session. Tests the semaphore, per-source goroutines,
// and sink contention.
func BenchmarkMultiSourceFanOut(b *testing.B) {
	for _, numSources := range []int{1, 5, 10, 20, 50} {
		b.Run(fmt.Sprintf("sources=%d", numSources), func(b *testing.B) {
			sink := &countingSink{}
			linesPerSource := b.N / numSources
			if linesPerSource < 1 {
				linesPerSource = 1
			}

			sourceIDs := make([]string, numSources)
			for i := range sourceIDs {
				sourceIDs[i] = fmt.Sprintf("src-%d", i)
			}

			src := newPipeSource(linesPerSource, 200)
			mgr := logs.NewManager(logs.ManagerConfig{
				Handlers: map[string]logs.Handler{
					"test/pod": {
						Plugin:        "test",
						Resource:      "pod",
						Handler:       src.handler(),
						SourceBuilder: logtest.MultiSourceBuilder(sourceIDs...),
					},
				},
				Sink:  sink,
				Clock: timeutil.RealClock{},
			})

			pctx := types.NewPluginContext(context.Background(), "bench", nil, nil, nil)

			b.ResetTimer()
			b.ReportAllocs()

			_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
				ResourceKey: "pod",
				ResourceID:  "bench-pod",
				Options:     logs.LogSessionOptions{Follow: false},
			})
			if err != nil {
				b.Fatalf("CreateSession: %v", err)
			}

			mgr.Wait()
			b.StopTimer()

			totalExpected := int64(linesPerSource) * int64(numSources)
			got := sink.lineCount.Load()
			b.ReportMetric(float64(got)/b.Elapsed().Seconds(), "lines/sec")
			b.ReportMetric(float64(numSources), "sources")
			if got != totalExpected {
				b.Logf("line count: got %d, expected %d", got, totalExpected)
			}

			mgr.Close()
		})
	}
}

// ===========================================================================
// Benchmark: multi-session (many independent sessions)
// ===========================================================================

// BenchmarkMultiSession measures throughput with N independent sessions,
// each with a single source. Tests Manager.mu contention and session map
// operations under concurrent load.
func BenchmarkMultiSession(b *testing.B) {
	for _, numSessions := range []int{1, 5, 10, 25, 50} {
		b.Run(fmt.Sprintf("sessions=%d", numSessions), func(b *testing.B) {
			sink := &countingSink{}
			linesPerSession := b.N / numSessions
			if linesPerSession < 1 {
				linesPerSession = 1
			}

			src := newPipeSource(linesPerSession, 200)
			mgr := logs.NewManager(logs.ManagerConfig{
				Handlers: map[string]logs.Handler{
					"test/pod": {
						Plugin:   "test",
						Resource: "pod",
						Handler:  src.handler(),
						SourceBuilder: logtest.SingleSourceBuilder(
							"pod-1", map[string]string{"app": "nginx"},
						),
					},
				},
				Sink:  sink,
				Clock: timeutil.RealClock{},
			})

			pctx := types.NewPluginContext(context.Background(), "bench", nil, nil, nil)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < numSessions; i++ {
				_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
					ResourceKey: "pod",
					ResourceID:  fmt.Sprintf("pod-%d", i),
					Options:     logs.LogSessionOptions{Follow: false},
				})
				if err != nil {
					b.Fatalf("CreateSession[%d]: %v", i, err)
				}
			}

			mgr.Wait()
			b.StopTimer()

			totalExpected := int64(linesPerSession) * int64(numSessions)
			got := sink.lineCount.Load()
			b.ReportMetric(float64(got)/b.Elapsed().Seconds(), "lines/sec")
			b.ReportMetric(float64(numSessions), "sessions")
			if got != totalExpected {
				b.Logf("line count: got %d, expected %d", got, totalExpected)
			}

			mgr.Close()
		})
	}
}

// ===========================================================================
// Benchmark: combined stress (many sessions × many sources × sustained load)
// ===========================================================================

// BenchmarkStressCombined measures peak throughput with multiple sessions
// each having multiple sources. This is the realistic worst-case scenario
// for a user with many connections/pods open simultaneously.
func BenchmarkStressCombined(b *testing.B) {
	type scenario struct {
		sessions, sourcesPerSession int
	}
	scenarios := []scenario{
		{2, 5},   // light: 10 total streams
		{5, 10},  // moderate: 50 total streams
		{10, 10}, // heavy: 100 total streams
		{10, 20}, // extreme: 200 total streams
	}

	for _, sc := range scenarios {
		name := fmt.Sprintf("%dsess_%dsrc", sc.sessions, sc.sourcesPerSession)
		b.Run(name, func(b *testing.B) {
			sink := &countingSink{}
			totalStreams := sc.sessions * sc.sourcesPerSession
			linesPerStream := b.N / totalStreams
			if linesPerStream < 1 {
				linesPerStream = 1
			}

			sourceIDs := make([]string, sc.sourcesPerSession)
			for i := range sourceIDs {
				sourceIDs[i] = fmt.Sprintf("src-%d", i)
			}

			src := newPipeSource(linesPerStream, 200)
			mgr := logs.NewManager(logs.ManagerConfig{
				Handlers: map[string]logs.Handler{
					"test/pod": {
						Plugin:        "test",
						Resource:      "pod",
						Handler:       src.handler(),
						SourceBuilder: logtest.MultiSourceBuilder(sourceIDs...),
					},
				},
				Sink:  sink,
				Clock: timeutil.RealClock{},
			})

			pctx := types.NewPluginContext(context.Background(), "bench", nil, nil, nil)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < sc.sessions; i++ {
				_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
					ResourceKey: "pod",
					ResourceID:  fmt.Sprintf("pod-%d", i),
					Options:     logs.LogSessionOptions{Follow: false},
				})
				if err != nil {
					b.Fatalf("CreateSession[%d]: %v", i, err)
				}
			}

			mgr.Wait()
			b.StopTimer()

			got := sink.lineCount.Load()
			b.ReportMetric(float64(got)/b.Elapsed().Seconds(), "lines/sec")
			b.ReportMetric(float64(totalStreams), "total_streams")

			mgr.Close()
		})
	}
}

// ===========================================================================
// Benchmark: sustained throughput with GC pressure measurement
// ===========================================================================

// BenchmarkSustainedThroughputGC runs a fixed-duration stress test and
// reports GC stats. This is not a standard b.N benchmark — it runs for a
// fixed period and reports throughput + GC metrics.
func BenchmarkSustainedThroughputGC(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping sustained throughput test in short mode")
	}

	const (
		numSessions       = 5
		sourcesPerSession = 10
		duration          = 3 * time.Second
		lineLen           = 256
	)

	sink := &countingSink{}

	sourceIDs := make([]string, sourcesPerSession)
	for i := range sourceIDs {
		sourceIDs[i] = fmt.Sprintf("src-%d", i)
	}

	handler := infinitePipeHandler(lineLen)
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:        "test",
				Resource:      "pod",
				Handler:       handler,
				SourceBuilder: logtest.MultiSourceBuilder(sourceIDs...),
			},
		},
		Sink:  sink,
		Clock: timeutil.RealClock{},
	})

	pctx := types.NewPluginContext(context.Background(), "bench", nil, nil, nil)

	// Capture GC stats before
	var gcBefore runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&gcBefore)

	sessionIDs := make([]string, numSessions)
	for i := 0; i < numSessions; i++ {
		session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
			ResourceKey: "pod",
			ResourceID:  fmt.Sprintf("pod-%d", i),
			Options:     logs.LogSessionOptions{Follow: true},
		})
		if err != nil {
			b.Fatalf("CreateSession[%d]: %v", i, err)
		}
		sessionIDs[i] = session.ID
	}

	// Let it run for the specified duration
	time.Sleep(duration)

	// Close all sessions
	for _, id := range sessionIDs {
		_ = mgr.CloseSession(pctx, id)
	}
	mgr.Wait()

	// Capture GC stats after
	var gcAfter runtime.MemStats
	runtime.ReadMemStats(&gcAfter)

	totalLines := sink.lineCount.Load()
	elapsed := duration.Seconds()
	linesPerSec := float64(totalLines) / elapsed
	totalStreams := numSessions * sourcesPerSession

	gcCycles := gcAfter.NumGC - gcBefore.NumGC
	gcPauseTotal := gcAfter.PauseTotalNs - gcBefore.PauseTotalNs
	totalAlloc := gcAfter.TotalAlloc - gcBefore.TotalAlloc
	heapInUse := gcAfter.HeapInuse

	b.ReportMetric(linesPerSec, "lines/sec")
	b.ReportMetric(float64(totalStreams), "total_streams")
	b.ReportMetric(float64(totalLines), "total_lines")
	b.ReportMetric(float64(gcCycles), "gc_cycles")
	b.ReportMetric(float64(gcPauseTotal)/1e6, "gc_pause_ms")
	b.ReportMetric(float64(totalAlloc)/1e6, "total_alloc_MB")
	b.ReportMetric(float64(heapInUse)/1e6, "heap_inuse_MB")

	if totalLines > 0 {
		b.ReportMetric(float64(totalAlloc)/float64(totalLines), "bytes/line")
	}

	mgr.Close()
}

// ===========================================================================
// Benchmark: extractTimestamp (isolated hot-path component)
// ===========================================================================

// BenchmarkExtractTimestamp isolates the timestamp parsing cost from the
// rest of the pipeline. This function is called once per line.
func BenchmarkExtractTimestamp(b *testing.B) {
	lines := map[string]string{
		"RFC3339Nano": "2024-01-15T10:30:00.123456789Z application log message here with some content",
		"RFC3339":     "2024-01-15T10:30:00Z application log message here with some content",
		"NoTimestamp": "application log message here with some content that has no timestamp prefix",
	}

	for name, line := range lines {
		b.Run(name, func(b *testing.B) {
			lineBytes := []byte(line)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = logs.ExtractTimestampForBench(lineBytes)
			}
		})
	}
}

// ===========================================================================
// Benchmark: ChannelSink throughput (production sink path)
// ===========================================================================

// BenchmarkChannelSinkThroughput measures the ChannelSink → channel consumer
// path that production uses (Stream() API). This tests channel contention
// and the select{} overhead under load.
func BenchmarkChannelSinkThroughput(b *testing.B) {
	for _, bufSize := range []int{256, 1024, 4096} {
		b.Run(fmt.Sprintf("bufSize=%d", bufSize), func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			out := make(chan logs.StreamOutput, bufSize)
			sink := logs.NewChannelSink(ctx, out)

			// Consumer goroutine — drain as fast as possible
			consumed := atomic.Int64{}
			done := make(chan struct{})
			go func() {
				for range out {
					consumed.Add(1)
				}
				close(done)
			}()

			line := logs.LogLine{
				SessionID: "sess-1",
				SourceID:  "src-1",
				Labels:    map[string]string{"app": "nginx"},
				Content:   "test log line content",
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				sink.OnLine(line)
			}

			b.StopTimer()
			close(out)
			<-done

			if consumed.Load() != int64(b.N) {
				b.Logf("consumed %d of %d", consumed.Load(), b.N)
			}
		})
	}
}

// ===========================================================================
// Benchmark: scanner buffer allocation impact
// ===========================================================================

// BenchmarkScannerBufferAlloc measures the per-source scanner.Buffer allocation.
// Each source stream allocates a 1MB buffer. With many sources, this dominates
// initial memory usage.
func BenchmarkScannerBufferAlloc(b *testing.B) {
	for _, numSources := range []int{1, 10, 50, 100} {
		b.Run(fmt.Sprintf("sources=%d", numSources), func(b *testing.B) {
			var gcBefore, gcAfter runtime.MemStats

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runtime.GC()
				runtime.ReadMemStats(&gcBefore)

				sink := &countingSink{}
				sourceIDs := make([]string, numSources)
				for j := range sourceIDs {
					sourceIDs[j] = fmt.Sprintf("src-%d", j)
				}

				// Handler returns an empty reader (instant EOF)
				handler := logs.LogHandlerFunc(func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader("line\n")), nil
				})

				mgr := logs.NewManager(logs.ManagerConfig{
					Handlers: map[string]logs.Handler{
						"test/pod": {
							Plugin:        "test",
							Resource:      "pod",
							Handler:       handler,
							SourceBuilder: logtest.MultiSourceBuilder(sourceIDs...),
						},
					},
					Sink:  sink,
					Clock: timeutil.RealClock{},
				})

				pctx := types.NewPluginContext(context.Background(), "bench", nil, nil, nil)
				_, _ = mgr.CreateSession(pctx, logs.CreateSessionOptions{
					ResourceKey: "pod",
					ResourceID:  "bench-pod",
					Options:     logs.LogSessionOptions{Follow: false},
				})
				mgr.Wait()
				mgr.Close()

				runtime.ReadMemStats(&gcAfter)
				if i == 0 {
					allocMB := float64(gcAfter.TotalAlloc-gcBefore.TotalAlloc) / 1e6
					b.ReportMetric(allocMB, "alloc_MB")
					b.ReportMetric(allocMB/float64(numSources), "alloc_MB/source")
				}
			}
		})
	}
}
