package exectestkit_test

import (
	"context"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
)

// The tests of Path run on the clock of a synctest bubble, as the tests that use a Path do: the goroutines of the path wait on
// in-memory buffers, so the bubble is idle when they wait, and a test decides when time passes.

// h2Frame is one HTTP/2 frame.
func h2Frame(typ, flags byte, stream uint32, payload []byte) []byte {
	n := len(payload)
	head := []byte{byte(n >> 16), byte(n >> 8), byte(n), typ, flags, byte(stream >> 24), byte(stream >> 16), byte(stream >> 8), byte(stream)}
	return append(head, payload...)
}

const h2Preface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// accepted is the plugin's end of the connection that a test's host dialed.
func accepted(t *testing.T, path *exectestkit.Path) net.Conn {
	t.Helper()
	conn, err := path.Listener().Accept()
	if err != nil {
		t.Fatalf("the plugin's accept: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// dialed is a host's end of a connection to the plugin.
func dialed(t *testing.T, path *exectestkit.Path) net.Conn {
	t.Helper()
	conn, err := path.DialContext(context.Background(), "")
	if err != nil {
		t.Fatalf("the host's dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readResult is what one Read returned.
type readResult struct {
	data string
	err  error
}

// readAsync reads once from conn in a goroutine, so that a test can look at whether the read has returned without waiting for it.
func readAsync(conn net.Conn) <-chan readResult {
	out := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 64<<10)
		n, err := conn.Read(buf)
		out <- readResult{string(buf[:n]), err}
	}()
	return out
}

// Until the path is cut it is a wire: bytes go both ways, in order, and a close at one end is the other end's EOF.
func TestPathForwardsBothWaysAndPassesTheClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := exectestkit.NewPath(t)
		host := dialed(t, path)
		plugin := accepted(t, path)

		if _, err := host.Write([]byte("to the plugin")); err != nil {
			t.Fatalf("host write: %v", err)
		}
		if got := <-readAsync(plugin); got.data != "to the plugin" || got.err != nil {
			t.Errorf("the plugin read %+v", got)
		}
		if _, err := plugin.Write([]byte("to the host")); err != nil {
			t.Fatalf("plugin write: %v", err)
		}
		if got := <-readAsync(host); got.data != "to the host" || got.err != nil {
			t.Errorf("the host read %+v", got)
		}

		_ = host.Close()
		if got := <-readAsync(plugin); got.err == nil {
			t.Errorf("the plugin read %+v after the host closed, want an error", got)
		}
		if n := path.Dials(); n != 1 {
			t.Errorf("Dials = %d, want 1", n)
		}
	})
}

// A cut path is the state of a peer that vanished with no FIN: each end's writes are accepted however much it writes, nothing
// arrives, and the close of an end is not seen by the other, until the path itself is closed.
func TestBlackholeAcceptsWritesDeliversNothingAndSwallowsTheClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := exectestkit.NewPath(t)
		host := dialed(t, path)
		plugin := accepted(t, path)

		pluginReads, hostReads := readAsync(plugin), readAsync(host)
		path.Blackhole()
		path.Blackhole() // cutting a cut path changes nothing

		// More than a connection's buffer: a cut path that stopped reading would block these writes.
		chunk := make([]byte, 64<<10)
		for range 64 {
			if _, err := host.Write(chunk); err != nil {
				t.Fatalf("host write on a cut path: %v", err)
			}
			if _, err := plugin.Write(chunk); err != nil {
				t.Fatalf("plugin write on a cut path: %v", err)
			}
		}
		_ = host.Close()
		time.Sleep(time.Hour)
		synctest.Wait()
		select {
		case got := <-pluginReads:
			t.Errorf("the plugin read %d bytes, err %v, on a cut path", len(got.data), got.err)
		default:
		}
		// The host's own read ends with its own close; the plugin, which cannot know, waits on.
		if got := <-hostReads; got.err == nil {
			t.Errorf("the host's read after its own close: %+v, want an error", got)
		}

		// The cut is not a close: a connection made after it is accepted, and carries nothing either.
		late := dialed(t, path)
		lateAccepted := accepted(t, path)
		if _, err := late.Write([]byte("late")); err != nil {
			t.Fatalf("write on a connection made after the cut: %v", err)
		}
		lateReads := readAsync(lateAccepted)
		synctest.Wait()
		select {
		case got := <-lateReads:
			t.Errorf("the plugin read %q on a connection made after the cut", got.data)
		default:
		}

		// Closing the path is what wakes the ends, and it returns, twice.
		path.Close()
		path.Close()
		if got := <-pluginReads; got.err == nil {
			t.Errorf("the plugin's read after the path was closed: %+v, want an error", got)
		}
		if got := <-lateReads; got.err == nil {
			t.Errorf("the late read after the path was closed: %+v, want an error", got)
		}
	})
}

// The path records the PINGs and GOAWAYs that cross it with who sent them and when, and goes on recording them once it is cut:
// what an end tried to say is evidence even when nobody heard it.
func TestPathRecordsPingsAndGoAwaysWithTheirSenderAndTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := exectestkit.NewPath(t)
		host := dialed(t, path)
		plugin := accepted(t, path)
		go func() { _, _ = io.Copy(io.Discard, plugin) }()
		go func() { _, _ = io.Copy(io.Discard, host) }()

		ping := []byte{1, 2, 3, 4, 5, 6, 7, 8}
		start := time.Now()
		write := func(c net.Conn, b ...[]byte) {
			t.Helper()
			for _, p := range b {
				if _, err := c.Write(p); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
		}
		write(host, []byte(h2Preface), h2Frame(0x4, 0, 0, nil), h2Frame(0x6, 0, 0, ping))
		time.Sleep(7 * time.Second)
		write(plugin, h2Frame(0x6, 0x1, 0, ping), h2Frame(0x0, 0, 1, make([]byte, 300)))
		time.Sleep(3 * time.Second)
		path.Blackhole()
		write(plugin, h2Frame(0x7, 0, 0, append([]byte{0, 0, 0, 3, 0, 0, 0, 0xb}, "too_many_pings"...)))
		synctest.Wait()

		frames := path.Frames()
		if len(frames) != 3 {
			t.Fatalf("recorded %d frames, want 3: %v", len(frames), frames)
		}
		if f := frames[0]; f.From != exectestkit.FromHost || f.Type != exectestkit.FramePing || f.Ack || f.At.Sub(start) != 0 {
			t.Errorf("first frame: %v at +%v, want the host's PING at +0", f, f.At.Sub(start))
		}
		if f := frames[1]; f.From != exectestkit.FromPlugin || f.Type != exectestkit.FramePing || !f.Ack || f.At.Sub(start) != 7*time.Second {
			t.Errorf("second frame: %v at +%v, want the plugin's PING ack at +7s", f, f.At.Sub(start))
		}
		if f := frames[2]; f.From != exectestkit.FromPlugin || f.Type != exectestkit.FrameGoAway || f.ErrCode != exectestkit.ErrCodeEnhanceYourCalm ||
			f.Debug != "too_many_pings" || f.At.Sub(start) != 10*time.Second {
			t.Errorf("third frame: %v at +%v, want the plugin's GOAWAY 0xb \"too_many_pings\" at +10s (after the cut)", f, f.At.Sub(start))
		}
	})
}

// A writer that the path holds back is let go when the path is closed, and nothing the path started outlives it: the bubble would
// not return from a goroutine that waits for ever.
func TestPathCloseReleasesWhatIsBlockedOnIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := exectestkit.NewPath(t)
		host := dialed(t, path)
		_ = accepted(t, path) // the plugin never reads

		// The path forwards to a plugin that does not read, so the host's writes end up blocked behind full buffers.
		blocked := make(chan error, 1)
		go func() {
			chunk := make([]byte, 64<<10)
			for {
				if _, err := host.Write(chunk); err != nil {
					blocked <- err
					return
				}
			}
		}()
		synctest.Wait()
		select {
		case err := <-blocked:
			t.Fatalf("the host's writes failed before the path was closed: %v", err)
		default:
		}
		path.Close()
		if err := <-blocked; err == nil {
			t.Errorf("the blocked write returned no error after the path was closed")
		}
		// A dial after the close is refused, not hung.
		if _, err := path.DialContext(context.Background(), ""); err == nil {
			t.Errorf("a dial after Close succeeded")
		}
	})
}

// readAll reads exactly n bytes from conn, in a goroutine, and returns them with the error that ended the read, so that a test
// can look at whether the read has finished.
func readAll(conn net.Conn, n int) <-chan readResult {
	out := make(chan readResult, 1)
	go func() {
		buf := make([]byte, n)
		got, err := io.ReadFull(conn, buf)
		out <- readResult{string(buf[:got]), err}
	}()
	return out
}

// A stalled path holds what the ends send, in both directions, and delivers all of it, in order, when it resumes: nothing is lost,
// and a close that came while it was stalled is seen after the bytes that preceded it. Stall and Resume may each be repeated.
func TestStallHoldsBothDirectionsAndResumeDeliversEverythingInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := exectestkit.NewPath(t)
		host := dialed(t, path)
		plugin := accepted(t, path)

		path.Stall()
		path.Stall()
		pluginReads, hostReads := readAll(plugin, len("onetwo")), readAll(host, len("alpha"))
		for _, w := range []string{"one", "two"} {
			if _, err := host.Write([]byte(w)); err != nil {
				t.Fatalf("host write: %v", err)
			}
		}
		if _, err := plugin.Write([]byte("alpha")); err != nil {
			t.Fatalf("plugin write: %v", err)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		select {
		case got := <-pluginReads:
			t.Errorf("the plugin read %+v while the path was stalled", got)
		case got := <-hostReads:
			t.Errorf("the host read %+v while the path was stalled", got)
		default:
		}

		path.Resume()
		path.Resume()
		if got := <-pluginReads; got.data != "onetwo" || got.err != nil {
			t.Errorf("after the path resumed the plugin read %+v, want \"onetwo\"", got)
		}
		if got := <-hostReads; got.data != "alpha" || got.err != nil {
			t.Errorf("after the path resumed the host read %+v, want \"alpha\"", got)
		}

		// A second stall, on a connection that closes while it lasts: the plugin sees the bytes, and then the close.
		host2, plugin2 := dialed(t, path), accepted(t, path)
		path.Stall()
		if _, err := host2.Write([]byte("last")); err != nil {
			t.Fatalf("host write: %v", err)
		}
		_ = host2.Close()
		held := readAll(plugin2, len("last"))
		synctest.Wait()
		select {
		case got := <-held:
			t.Errorf("the plugin read %+v of a connection whose path was stalled", got)
		default:
		}
		path.Resume()
		if got := <-held; got.data != "last" || got.err != nil {
			t.Errorf("after the second resume the plugin read %+v, want \"last\"", got)
		}
		if got := <-readAsync(plugin2); got.err == nil {
			t.Errorf("the plugin did not see the host's close behind its bytes: %+v", got)
		}
	})
}

// A path that is closed while it is stalled lets go of everything it holds.
func TestPathCloseReleasesAStalledPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := exectestkit.NewPath(t)
		host := dialed(t, path)
		plugin := accepted(t, path)
		path.Stall()
		if _, err := host.Write([]byte("held")); err != nil {
			t.Fatalf("host write: %v", err)
		}
		pluginRead := readAsync(plugin)
		synctest.Wait()
		path.Close()
		if got := <-pluginRead; got.err == nil {
			t.Errorf("the plugin's read after the stalled path was closed: %+v, want an error", got)
		}
	})
}
