package exectestkit

import (
	"context"
	"fmt"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// Path is the network between a host and a plugin, in memory and in the open: the plugin's server serves its Listener, the host
// dials through DialContext, every PING and GOAWAY that crosses is recorded with the time it was sent, Blackhole cuts the path the
// way a peer that vanished cuts it, and Stall holds it back the way a peer that is frozen for a while does.
//
// It exists for the tests that need a transport the clock of a testing/synctest bubble can drive. A goroutine that waits on a
// socket is not durably blocked, so a bubble whose goroutines read a unix or a loopback socket never lets its clock advance; the
// goroutines of a Path wait on in-memory buffers, and the keepalive of a gRPC connection over it runs on fake time, with the
// production parameters, in no time at all. Create a Path inside the bubble that uses it.
type Path struct {
	front *bufconn.Listener // the host dials it and the path accepts
	back  *bufconn.Listener // the path dials it and the plugin's server accepts
	dead  atomic.Bool
	dials atomic.Int64

	mu     sync.Mutex
	frames []Frame
	conns  []net.Conn
	done   []chan struct{} // one per goroutine the path runs
	gate   chan struct{}   // closed by Resume; non-nil while the path is stalled
	closed bool
}

// pathBuffer is the capacity of each direction of a connection over a Path. It is large enough for the exchanges of a test and
// small enough that a reader that stops reading is felt by its writer, as it is on a socket.
const pathBuffer = 1 << 20

// NewPath returns a path with no connection. It is closed at the end of the test, after what was registered after it (the
// client and the server of the test), so that the connections it carries are closed by their owners first.
func NewPath(t TB) *Path {
	t.Helper()
	p := &Path{front: bufconn.Listen(pathBuffer), back: bufconn.Listen(pathBuffer)}
	p.run(p.accept)
	t.Cleanup(p.Close)
	return p
}

// Listener is what the plugin's server serves.
func (p *Path) Listener() net.Listener { return p.back }

// Target is the address a client dials through the path. The address is not looked up: the dialer ignores it.
func (p *Path) Target() string { return "passthrough:///plugin" }

// DialContext connects a host to the plugin. It is the context dialer of the host's connection, and what a test that speaks
// HTTP/2 itself calls.
func (p *Path) DialContext(ctx context.Context, _ string) (net.Conn, error) {
	conn, err := p.front.DialContext(ctx)
	if err == nil {
		p.dials.Add(1)
	}
	return conn, err
}

// DialOptions are the options that make a gRPC client dial through the path.
func (p *Path) DialOptions() []grpc.DialOption {
	return []grpc.DialOption{grpc.WithContextDialer(p.DialContext)}
}

// Dials is how many connections hosts have made through the path: a connection that is made again after the transport was
// closed counts again.
func (p *Path) Dials() int { return int(p.dials.Load()) }

// Blackhole cuts the path, for good and in both directions, without closing anything. From now on what each end writes is accepted
// and lost, nothing reaches the other end, and a close by either end is not passed on: the sockets of both ends stay open and each
// end waits for an answer that never comes, as it does when its peer vanished without a FIN (a frozen or a crashed host behind a
// middlebox, a path that dropped its packets). What each end tries to send is still recorded.
func (p *Path) Blackhole() { p.dead.Store(true) }

// Stall holds back everything the path forwards, in both directions, until Resume: what each end sends waits in the path (the
// frames that were sent are recorded as they are sent, and each end's writes go on being accepted until the buffer between the
// ends is full) and arrives, in order and complete, when the path resumes. It is the state of a peer that is frozen for a while and
// thawed, a connection that stalled and recovered. Stalling a stalled path changes nothing.
func (p *Path) Stall() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate == nil {
		p.gate = make(chan struct{})
	}
}

// Resume lets a stalled path forward what it held, and what follows. Resuming a path that is not stalled changes nothing.
func (p *Path) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate != nil {
		close(p.gate)
		p.gate = nil
	}
}

// hold waits while the path is stalled.
func (p *Path) hold() {
	p.mu.Lock()
	gate := p.gate
	p.mu.Unlock()
	if gate != nil {
		<-gate
	}
}

// Frames returns the PING and GOAWAY frames that the ends have sent, in the order the path read them, whether or not they were
// delivered.
func (p *Path) Frames() []Frame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.frames)
}

// Close closes the listeners and every connection, and returns when the goroutines of the path have returned. What a stalled path
// held is dropped: the connections are closed before the path lets go of it. It may be called more than once.
func (p *Path) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	conns, done := p.conns, p.done
	p.mu.Unlock()
	_ = p.front.Close()
	_ = p.back.Close()
	for _, conn := range conns {
		_ = conn.Close()
	}
	p.Resume()
	for _, d := range done {
		<-d
	}
}

// run starts a goroutine of the path that Close waits for.
func (p *Path) run(fn func()) {
	done := make(chan struct{})
	p.mu.Lock()
	p.done = append(p.done, done)
	p.mu.Unlock()
	go func() {
		defer close(done)
		fn()
	}()
}

// accept connects each host that dials to the plugin, until the path is closed.
func (p *Path) accept() {
	for {
		host, err := p.front.Accept()
		if err != nil {
			return
		}
		plugin, err := p.back.DialContext(context.Background())
		if err != nil {
			_ = host.Close()
			return
		}
		p.attach(host, plugin)
	}
}

// attach starts forwarding between the two ends of a connection, unless the path has been closed.
func (p *Path) attach(host, plugin net.Conn) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = host.Close()
		_ = plugin.Close()
		return
	}
	p.conns = append(p.conns, host, plugin)
	toPlugin, toHost := make(chan struct{}), make(chan struct{})
	p.done = append(p.done, toPlugin, toHost)
	p.mu.Unlock()
	go func() {
		defer close(toPlugin)
		p.pump(FromHost, host, plugin)
	}()
	go func() {
		defer close(toHost)
		p.pump(FromPlugin, plugin, host)
	}()
}

// pump copies what src sends to dst, looking at the frames on the way. A cut path still reads what src sends, so that src never
// blocks on a full buffer, and forwards none of it; a stalled path holds what it has read, and reads nothing more, until it
// resumes.
func (p *Path) pump(from Side, src, dst net.Conn) {
	scan := frameScanner{}
	if from == FromHost {
		scan.preface = len(clientPreface)
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			scan.feed(buf[:n], func(f Frame) {
				f.From, f.At = from, time.Now()
				p.mu.Lock()
				p.frames = append(p.frames, f)
				p.mu.Unlock()
			})
			p.hold()
			if !p.dead.Load() {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
		}
		if err != nil {
			p.hold()
			if !p.dead.Load() {
				_ = dst.Close()
			}
			return
		}
	}
}

// Side is an end of a Path.
type Side int

const (
	// FromHost marks what the host sent: it dialed, and the plugin serves.
	FromHost Side = iota
	// FromPlugin marks what the plugin sent.
	FromPlugin
)

func (s Side) String() string {
	if s == FromHost {
		return "host"
	}
	return "plugin"
}

// FrameType is the type of an HTTP/2 frame that a Path records.
type FrameType uint8

const (
	// FramePing is a PING: a keepalive probe, or its acknowledgement.
	FramePing FrameType = 0x6
	// FrameGoAway is a GOAWAY: the end of a connection, with an error code and the debug data that says why.
	FrameGoAway FrameType = 0x7
)

// ErrCodeEnhanceYourCalm is the HTTP/2 error code of a GOAWAY that refuses a peer that pings too often.
const ErrCodeEnhanceYourCalm uint32 = 0xb

// Frame is one frame that crossed a Path.
type Frame struct {
	// From says which end sent it.
	From Side
	// At is when it crossed, on the clock of the goroutine that saw it: a bubble's, when the path was made in one.
	At time.Time
	// Type is FramePing or FrameGoAway.
	Type FrameType
	// Ack is set on a PING that answers another.
	Ack bool
	// ErrCode and Debug are the error code and the debug data of a GOAWAY.
	ErrCode uint32
	Debug   string
}

func (f Frame) String() string {
	switch f.Type {
	case FramePing:
		if f.Ack {
			return fmt.Sprintf("%s PING ack at %s", f.From, f.At.Format("15:04:05.000"))
		}
		return fmt.Sprintf("%s PING at %s", f.From, f.At.Format("15:04:05.000"))
	case FrameGoAway:
		return fmt.Sprintf("%s GOAWAY code %#x %q at %s", f.From, f.ErrCode, f.Debug, f.At.Format("15:04:05.000"))
	}
	return fmt.Sprintf("%s frame %#x", f.From, uint8(f.Type))
}

// clientPreface is what an HTTP/2 client writes before its first frame.
const clientPreface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

const (
	frameHeaderLen = 9
	// maxKept bounds the payload a scanner keeps of a PING or a GOAWAY: the debug data of a GOAWAY is as long as its sender made it.
	maxKept = 1 << 10
)

// frameScanner picks the PING and GOAWAY frames out of a stream of HTTP/2, which it is given in pieces of any size. It keeps
// nothing of the other frames.
type frameScanner struct {
	preface int // bytes of the client preface that are still to skip

	header [frameHeaderLen]byte
	have   int // bytes of the header read so far

	typ     byte
	flags   byte
	left    int  // bytes of the current frame's payload still to come
	keep    bool // the payload of the current frame is the scanner's to report
	payload []byte
}

// feed reads b, and calls emit with the frame it has completed, for each one of the kinds the path records.
func (s *frameScanner) feed(b []byte, emit func(Frame)) {
	for len(b) > 0 {
		switch {
		case s.preface > 0:
			n := min(s.preface, len(b))
			s.preface -= n
			b = b[n:]
		case s.have < frameHeaderLen:
			n := copy(s.header[s.have:], b)
			s.have += n
			b = b[n:]
			if s.have == frameHeaderLen {
				s.start(emit)
			}
		default:
			n := min(s.left, len(b))
			if room := maxKept - len(s.payload); s.keep && room > 0 {
				s.payload = append(s.payload, b[:min(n, room)]...)
			}
			s.left -= n
			b = b[n:]
			if s.left == 0 {
				s.finish(emit)
			}
		}
	}
}

func (s *frameScanner) start(emit func(Frame)) {
	s.left = int(s.header[0])<<16 | int(s.header[1])<<8 | int(s.header[2])
	s.typ, s.flags = s.header[3], s.header[4]
	s.keep = FrameType(s.typ) == FramePing || FrameType(s.typ) == FrameGoAway
	s.payload = s.payload[:0]
	if s.left == 0 {
		s.finish(emit)
	}
}

func (s *frameScanner) finish(emit func(Frame)) {
	s.have = 0
	if !s.keep {
		return
	}
	frame := Frame{Type: FrameType(s.typ)}
	switch frame.Type {
	case FramePing:
		frame.Ack = s.flags&0x1 != 0
	case FrameGoAway:
		if len(s.payload) >= 8 {
			frame.ErrCode = uint32(s.payload[4])<<24 | uint32(s.payload[5])<<16 | uint32(s.payload[6])<<8 | uint32(s.payload[7])
			frame.Debug = string(s.payload[8:])
		}
	}
	emit(frame)
}
