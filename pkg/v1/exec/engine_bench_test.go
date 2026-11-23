package exec_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// BenchmarkEngineFlood is the flood of the design (section 3 of the exec-lane plan): a function writes as fast as it can
// in 32 KiB chunks, a reader takes every byte, and the numbers are what the engine's queue costs: MB/s, and bytes
// allocated per byte moved (B/op divided by the 32 KiB of an op). The measured baseline of the legacy lane is 10 to 35 MB/s
// on macOS and 23 to 27 MB/s on Linux; the bar is at least 300 MB/s and at most 4 bytes allocated per byte.
func BenchmarkEngineFlood(b *testing.B) {
	for name, stall := range map[string]exec.StallBehavior{"blocking": exec.StallPropagates, "lossy": exec.StallLossy} {
		b.Run(name, func(b *testing.B) {
			const chunk = 32 << 10
			payload := make([]byte, chunk)
			rig := newEngineRig(b, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
				for range b.N {
					if _, err := p.Stdout.Write(payload); err != nil {
						return exec.Exit{}, err
					}
				}
				return exec.ExitCode(0), nil
			}, func(i *exec.TargetInfo) { i.Capabilities.Stall = stall }))
			b.SetBytes(chunk)
			b.ReportAllocs()
			b.ResetTimer()
			cmd := rig.mustOpen("s1", withoutTTY)
			buf := make([]byte, 64<<10)
			var read, dropped uint64
			for {
				c, err := cmd.Read(context.Background(), buf)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					b.Fatalf("read: %v", err)
				}
				read += uint64(c.N)
				for _, d := range c.Dropped {
					dropped += d.Bytes
				}
			}
			b.StopTimer()
			if want := uint64(b.N) * chunk; read+dropped != want {
				b.Fatalf("read %d and dropped %d, want %d in all", read, dropped, want)
			}
			awaitDone(b, cmd)
		})
	}
}
