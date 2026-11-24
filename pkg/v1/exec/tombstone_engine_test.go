package exec_test

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The tombstone through the engine, in a synctest bubble: the clock moves only when every goroutine waits, so a time to live is exact to
// the nanosecond and a timer is the bubble's.

// startsAt makes a start of an id and says how it was answered: the refusal when the handler returned a status, and nil when the session
// was admitted (a session that ends at once has its handler returned too, with status OK). Its stream is returned to be left.
func startsAt(t *testing.T, rig *memoryRig, id string) (*memoryStream, error) {
	t.Helper()
	host := rig.streamOf(rig.startFrame(id), nil)
	synctest.Wait()
	if err, returned := host.handlerReturned(); returned && err != nil {
		return host, exec.StatusError(err)
	}
	return host, nil
}

// An id is held by its tombstone and free exactly when the tombstone is gone, however it goes: by its time to live (the default of 60 s
// and a seam), by the count when the store is full (the oldest first), and never by the loss of its output.
func TestAnIdIsReusableExactlyWhenItsTombstoneIsGone(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  exechook.EngineConfig
		ttl  time.Duration
	}{
		{"the default time to live, 60 s", exechook.EngineConfig{}, 60 * time.Second},
		{"a time to live of 7 s, the seam", exechook.EngineConfig{TombstoneTTL: 7 * time.Second}, 7 * time.Second},
	} {
		t.Run("expiry/"+tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rig := newMemoryRig(t, engTarget(writeThenExit("bye", 0)))
				exec.ConfigurePluginEngine(rig.plugin, tc.cfg)
				first := rig.streamOf(rig.startFrame("s1"), nil)
				first.opened(t)
				synctest.Wait()
				time.Sleep(tc.ttl - time.Nanosecond)
				synctest.Wait()
				if _, err := startsAt(t, rig, "s1"); !isRefusalOf(err, exec.ErrorClassConflict, exec.ErrorReasonSessionExists) {
					t.Fatalf("a start of s1 one nanosecond before its tombstone's time to live (%v): %v, want CONFLICT / SESSION_EXISTS", tc.ttl, err)
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				second, err := startsAt(t, rig, "s1")
				if err != nil {
					t.Fatalf("a start of s1 at its tombstone's time to live (%v): %v, want it admitted: the tombstone is gone", tc.ttl, err)
				}
				second.opened(t)
				synctest.Wait()
				first.hostLeaves()
				second.hostLeaves()
			})
		})
	}

	t.Run("the count: the oldest tombstone goes first and takes its id with it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			rig := newMemoryRig(t, engTarget(writeThenExit("x", 0)))
			exec.ConfigurePluginEngine(rig.plugin, exechook.EngineConfig{MaxTombstones: 2})
			var hosts []*memoryStream
			for i := 1; i <= 3; i++ {
				h := rig.streamOf(rig.startFrame(fmt.Sprintf("s%d", i)), nil)
				h.opened(t)
				synctest.Wait()
				time.Sleep(time.Second)
				hosts = append(hosts, h)
			}
			if ids, _ := exec.PluginTombstoneState(rig.plugin); fmt.Sprint(ids) != "[s2 s3]" {
				t.Fatalf("the store holds %v, want the two newest, [s2 s3]", ids)
			}
			for _, id := range []string{"s2", "s3"} {
				if _, err := startsAt(t, rig, id); !isRefusalOf(err, exec.ErrorClassConflict, exec.ErrorReasonSessionExists) {
					t.Errorf("a start of %s, whose tombstone is held: %v, want CONFLICT / SESSION_EXISTS", id, err)
				}
			}
			// (A refused start leaves nothing. The one that is admitted ends at once, and its tombstone takes the oldest's place.)
			again, err := startsAt(t, rig, "s1")
			if err != nil {
				t.Fatalf("a start of s1, whose tombstone the count took: %v, want it admitted", err)
			}
			again.opened(t)
			synctest.Wait()
			if ids, _ := exec.PluginTombstoneState(rig.plugin); fmt.Sprint(ids) != "[s3 s1]" {
				t.Errorf("the store holds %v, want [s3 s1]: the new tombstone is the newest and s2, now the oldest, went", ids)
			}
			for _, h := range append(hosts, again) {
				h.hostLeaves()
			}
		})
	})

	t.Run("the output: a tombstone that lost its bytes to the budget keeps its id", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			rig := newMemoryRig(t, engTarget(writeThenExit("0123456789", 0)))
			exec.ConfigurePluginEngine(rig.plugin, exechook.EngineConfig{TombstoneBytes: 4})
			first := rig.streamOf(rig.startFrame("s1"), nil)
			first.opened(t)
			synctest.Wait()
			if _, kept := exec.PluginTombstones(rig.plugin); kept != 0 {
				t.Fatalf("the store keeps %d bytes of a session that wrote 10 against a budget of 4", kept)
			}
			info := rig.sessionInfo(t, "s1")
			if info.GetEndedWith() == nil || info.GetReplayStart() != 10 || info.GetNextOffset() != 10 {
				t.Fatalf("the tombstone that lost its bytes is %v, want its end, with nothing to read: front 10, next 10", info)
			}
			if _, err := startsAt(t, rig, "s1"); !isRefusalOf(err, exec.ErrorClassConflict, exec.ErrorReasonSessionExists) {
				t.Fatalf("a start of the id of a tombstone that lost its output: %v, want CONFLICT / SESSION_EXISTS: it holds the id until it expires", err)
			}
			time.Sleep(time.Minute)
			synctest.Wait()
			again, err := startsAt(t, rig, "s1")
			if err != nil {
				t.Fatalf("a start of s1 after the tombstone expired: %v", err)
			}
			again.opened(t)
			synctest.Wait()
			first.hostLeaves()
			again.hostLeaves()
		})
	})
}

// The store of an engine has a timer that releases what expired when nobody asks, and stops with the plugin: what a plugin that has
// shut down holds stays readable until it expires, and nothing arms a timer after.
func TestTheEnginesTombstoneTimerReleasesWhatExpiredAndStopsWhenThePluginShutsDown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(writeThenExit("bye", 0)))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		synctest.Wait()
		if ids, armed := exec.PluginTombstoneState(rig.plugin); fmt.Sprint(ids) != "[s1]" || !armed {
			t.Fatalf("after a session ended the store holds %v with its timer armed %v, want [s1] and a timer", ids, armed)
		}
		time.Sleep(60 * time.Second)
		synctest.Wait()
		if ids, armed := exec.PluginTombstoneState(rig.plugin); len(ids) != 0 || armed {
			t.Fatalf("a minute later, with nobody asking, the store holds %v with its timer armed %v, want it empty and at rest", ids, armed)
		}

		second := rig.streamOf(rig.startFrame("s2"), nil)
		second.opened(t)
		synctest.Wait()
		if err := rig.plugin.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		if ids, armed := exec.PluginTombstoneState(rig.plugin); fmt.Sprint(ids) != "[s2]" || armed {
			t.Fatalf("after Shutdown the store holds %v with its timer armed %v, want [s2] still readable and no timer", ids, armed)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if ids, armed := exec.PluginTombstoneState(rig.plugin); fmt.Sprint(ids) != "[s2]" || armed {
			t.Fatalf("a minute after Shutdown, with nobody asking, the store holds %v with its timer armed %v: nothing may arm a timer of a plugin that has shut down", ids, armed)
		}
		if rig.hasSession(t, "s2") {
			t.Fatal("a tombstone was listed past its time to live")
		}
		host.hostLeaves()
		second.hostLeaves()
	})
}

// Plugin.Shutdown ends the sessions that no stream is attached to, and does it within the shutdown grace of a function that ignores its
// context, not the grace of an ordinary close: the host stops the plugin two seconds after it asks. Each of those sessions has its tombstone,
// and the place of the function that was given up on stays held until it returns.
func TestShutdownReturnsWithinTheShutdownGraceWhenADetachedSessionsFunctionIgnoresItsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := exectest.NewGate()
		t.Cleanup(gate.Open)
		rig := newMemoryRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
			<-gate.Opened() // ignores its context
			return exec.ExitCode(0), nil
		}, func(i *exec.TargetInfo) { i.Session.MaxSessions = 4 }))
		exec.ConfigurePluginEngine(rig.plugin, exechook.EngineConfig{CloseGrace: time.Hour, ShutdownGrace: time.Second})
		var hosts []*memoryStream
		for _, id := range []string{"detached-1", "detached-2", "attached"} {
			h := rig.streamOf(rig.startFrame(id), nil)
			h.opened(t)
			if id != "attached" {
				h.sendDetachFrame(0)
			}
			hosts = append(hosts, h)
		}
		synctest.Wait()

		begin := time.Now()
		if err := rig.plugin.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		if elapsed := time.Since(begin); elapsed < time.Second || elapsed > 1500*time.Millisecond {
			t.Fatalf("Shutdown took %v with three sessions whose functions ignore their context, want the shutdown grace of 1 s (the close grace is an hour) and at most the 1.5 s the host can spare", elapsed)
		}
		for _, id := range []string{"detached-1", "detached-2", "attached"} {
			info := rig.sessionInfo(t, id)
			if closed := info.GetEndedWith().GetEnd().GetClosed(); closed.GetBy() != execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN {
				t.Errorf("%s: the tombstone says %v, want Closed by PLUGIN_SHUTDOWN", id, info.GetEndedWith().GetEnd())
			}
		}
		// The functions were given up on: the places are held until they return.
		if live := exec.PluginLive(rig.plugin); len(live) != 0 {
			t.Errorf("the plugin holds %v after Shutdown", live)
		}
		gate.Open()
		synctest.Wait()
		for _, h := range hosts {
			h.hostLeaves()
		}
	})
}
