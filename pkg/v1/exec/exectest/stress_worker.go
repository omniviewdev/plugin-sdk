package exectest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

const (
	// poolCap is how many commands a worker operates on at once; one that it started and has not ended counts until its
	// reader has seen it end.
	poolCap = 32
	// writeBound is how long a worker waits for the engine to take its input: a function that does not read holds the host's
	// write at the bound of the queue, and the worker goes on to something else. What was taken is counted exactly.
	writeBound = 20 * time.Millisecond
)

// readMode is how the reader of a command reads.
type readMode int

const (
	// readEager reads as fast as the engine gives.
	readEager readMode = iota
	// readSlow reads a few bytes at a time and lets the scheduler run between reads: a reader that does not keep up.
	readSlow
	// readLate reads nothing until the command has ended: a stalled reader.
	readLate
)

// stressCommand is a command that the engine admitted: what a worker needs to operate on it, and what its reader needs to read
// it. The fields a worker changes belong to the worker, and the reader reads only the ones that never change.
type stressCommand struct {
	id     string
	key    string // the recorder's key: the id and the number of the admission
	b      *stressBehaviour
	run    bool
	tty    bool
	cmd    exechook.Command
	cancel context.CancelFunc // the context of a run
	mode   readMode
	buf    int // the size of the reader's buffer

	// first is the result the reader saw once the command had ended: what every later look at the result is held to. It is set
	// once, when the reader is done.
	first atomic.Pointer[exechook.Result]

	// the worker's
	sent      uint64 // the bytes of input the command took: the offset of the next byte of the pattern
	inputDone bool   // the worker closed the input
	resizes   uint32
	idle      bool // the worker walked away
}

// outstanding is the set of commands whose reader is not done, to name the ones a round cannot finish.
type outstanding struct {
	mu sync.Mutex
	m  map[string]*stressCommand
}

func (o *outstanding) add(c *stressCommand) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.m == nil {
		o.m = map[string]*stressCommand{}
	}
	o.m[c.key] = c
}

func (o *outstanding) remove(c *stressCommand) {
	o.mu.Lock()
	delete(o.m, c.key)
	o.mu.Unlock()
}

// describe says, for at most limit of the commands whose reader is not done, whether the command has ended.
func (o *outstanding) describe(limit int) []string {
	o.mu.Lock()
	commands := make([]*stressCommand, 0, len(o.m))
	for _, c := range o.m {
		commands = append(commands, c)
	}
	o.mu.Unlock()
	slices.SortFunc(commands, func(a, b *stressCommand) int { return strings.Compare(a.key, b.key) })
	var lines []string
	for i, c := range commands {
		if i == limit {
			lines = append(lines, fmt.Sprintf("... and %d more commands whose reader is not done", len(commands)-limit))
			break
		}
		state := "has not ended: Done() is not closed"
		select {
		case <-c.cmd.Done():
			state = "has ended and its output never reached the end"
		default:
		}
		lines = append(lines, fmt.Sprintf("%s: its reader is not done (%s command, read %v); the command %s", c.id, c.b.name, c.mode, state))
	}
	return lines
}

func (m readMode) String() string {
	switch m {
	case readSlow:
		return "slow"
	case readLate:
		return "late"
	}
	return "eager"
}

// stressAttempt is what a start was, to tell the refusals it may get from the ones it may not.
type stressAttempt struct {
	run       bool
	duplicate bool // the id is the one of a command the worker holds
	ctxEnded  bool // the context of the call had ended
}

// refused takes a start the engine refused. A refusal is legal, and which one is the engine's to say by the situation: a full
// target, a closed engine, an id that is live, a call that was cancelled. Any other is a violation.
func (r *stressRound) refused(err error, id string, a stressAttempt) {
	name := refusalName(err)
	r.tally.add("refused/" + name)
	class, typed := refusalClass(err)
	allowed := false
	switch {
	case !typed:
	case class == exec.ClassLimit:
		allowed = (!a.run && name == "LIMIT/TOO_MANY_SESSIONS") || (a.run && name == "LIMIT/TOO_MANY_RUNS")
	case class == exec.ClassUnavailable:
		allowed = r.closing.Load()
	case class == exec.ClassConflict:
		allowed = a.duplicate && name == "CONFLICT/SESSION_EXISTS"
	case class == exec.ClassCancelled:
		allowed = a.ctxEnded
	}
	if !allowed {
		r.found.add("%s: a start was refused as %s (%v) where nothing explains it: closing %v, id in use %v, context ended %v",
			id, name, err, r.closing.Load(), a.duplicate, a.ctxEnded)
	}
}

// lateAdmit records a start that was admitted after a Close had returned.
func (r *stressRound) lateAdmit(id string) {
	if r.lateAdmits.Add(1) == 1 {
		r.firstLate.Store(id)
	}
}

// admit takes a command the engine admitted: the recorder is told, and a reader of its own reads it to its end.
func (r *stressRound) admit(c *stressCommand) {
	r.rec.Admitted(c.key)
	r.outstanding.add(c)
	kind := "session"
	if c.run {
		kind = "run"
	}
	r.tally.add("admitted/" + kind)
	r.tally.add("behaviour/" + c.b.name)
	if !r.readers.Start(1) {
		r.found.add("%s: the readers of the round had ended when a command was admitted", c.id)
		return
	}
	go r.read(c)
}

// read is the one reader of a command. It reads the output to its end, checking each read, waits for the end, holds the end to
// the invariants of the end state, and tells the recorder; then it reads once more, to see that nothing comes after the end.
func (r *stressRound) read(c *stressCommand) {
	defer r.readers.Done()
	defer r.outstanding.remove(c)
	if c.mode == readLate {
		select {
		case <-c.cmd.Done():
		case <-r.readCtx.Done():
			return
		}
	}
	audit := outputAudit{tty: c.tty, patterned: true}
	buf := make([]byte, c.buf)
	for {
		chunk, err := c.cmd.Read(r.readCtx, buf)
		eof := errors.Is(err, io.EOF)
		if err != nil && !eof {
			if r.readCtx.Err() == nil {
				r.found.add("%s: Read returned %v: the only errors of a Read are io.EOF and the caller's context", c.id, err)
			}
			return
		}
		for _, v := range audit.take(chunk, buf[:chunk.N], eof) {
			r.found.add("%s: %s", c.id, v)
		}
		if chunk.N > 0 || len(chunk.Dropped) > 0 {
			r.rec.Event(c.key)
		}
		if eof {
			break
		}
		if c.mode == readSlow {
			runtime.Gosched()
		}
	}
	select {
	case <-c.cmd.Done():
	case <-r.readCtx.Done():
		return
	}
	res := c.cmd.Result()
	c.first.Store(&res)
	r.rec.Ended(c.key)
	r.checkEnded(c, res, audit)

	chunk, err := c.cmd.Read(r.readCtx, buf)
	if !errors.Is(err, io.EOF) && r.readCtx.Err() != nil {
		return
	}
	if !errors.Is(err, io.EOF) || chunk.N != 0 || len(chunk.Dropped) != 0 {
		r.rec.Event(c.key)
		r.found.add("%s: a Read after the end returned %d bytes, %d dropped ranges and %v: nothing may come after the end", c.id, chunk.N, len(chunk.Dropped), err)
	}
}

// checkEnded holds the end of a command whose reader is done to what must be true of every command, and to what its behaviour
// says of it, and counts it.
func (r *stressRound) checkEnded(c *stressCommand, res exechook.Result, audit outputAudit) {
	for _, v := range checkResult(res, audit) {
		r.found.add("%s: %s", c.id, v)
	}
	for _, v := range c.b.check(res, audit, c.run, c.mode) {
		r.found.add("%s: %s", c.id, v)
	}
	arm, _ := endArm(res.End)
	r.tally.add("ended/" + arm)
	r.tally.add("cause/" + res.Cause)
	r.tally.add("output/" + strings.ToLower(strings.TrimPrefix(res.Output.String(), "OUTPUT_COMPLETENESS_")))
	r.tally.addN("bytes read", int(audit.read))
	r.tally.addN("bytes dropped", int(audit.dropped))
	if res.Abandoned {
		r.tally.add("abandoned")
	}
	for _, v := range res.Violations {
		kind, _, _ := strings.Cut(v, ":")
		r.tally.add("contract/" + kind)
	}
}

// dispose is the last look at a command that a worker is done with: its result is what its reader saw, whatever was done to the
// command since (a close after the exit, a kill after a close). A result that changed is a second end.
func (r *stressRound) dispose(c *stressCommand) {
	if c.cancel != nil {
		c.cancel()
	}
	first := c.first.Load()
	if first == nil {
		return // its reader never finished: the round says so
	}
	if diff := sameResult(*first, c.cmd.Result()); diff != "" {
		r.found.add("%s: %s: the command ended twice", c.id, diff)
		r.rec.Ended(c.key)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the workers
// ---------------------------------------------------------------------------------------------------------------

// stressWeight is the sum of the weights of the behaviours.
var stressWeight = func() int {
	total := 0
	for _, b := range stressBehaviours {
		total += b.weight
	}
	return total
}()

// stressWorker is one goroutine that starts commands and does things to them at random, from a random source of its own.
type stressWorker struct {
	r    *stressRound
	id   int
	rng  *rand.Rand
	seq  int
	pool []*stressCommand
}

// work is one worker: it starts a command of each behaviour as a session and as a run, so that every one is met whatever the dice
// say, and then picks at random until stop is closed.
func (r *stressRound) work(id int, stop <-chan struct{}) {
	w := &stressWorker{r: r, id: id, rng: rand.New(rand.NewPCG(r.seed, uint64(id)+1))}
	defer w.handOver()
	for _, run := range []bool{false, true} {
		for i := range stressBehaviours {
			w.start(stressBehaviours[(i+id)%len(stressBehaviours)], run, "", false)
		}
	}
	for {
		select {
		case <-stop:
			return
		default:
		}
		w.step()
	}
}

// handOver gives the round what the worker still holds, to be disposed of once the commands have ended.
func (w *stressWorker) handOver() {
	w.r.left.Lock()
	w.r.left.commands = append(w.r.left.commands, w.pool...)
	w.r.left.Unlock()
}

// step does one thing, picked at random.
func (w *stressWorker) step() {
	always := func(*stressCommand) bool { return true }
	switch p := w.rng.IntN(100); {
	case p < 30:
		w.startRandom()
	case p < 52:
		w.with(func(c *stressCommand) bool { return !c.run && (c.b.input || w.rng.IntN(6) == 0) }, w.write)
	case p < 57:
		w.with(func(c *stressCommand) bool { return !c.run }, w.closeInput)
	case p < 66:
		w.with(always, w.resize)
	case p < 73:
		w.with(always, func(c *stressCommand) { w.end(c, false) })
	case p < 79:
		w.with(always, func(c *stressCommand) { w.end(c, true) })
	case p < 83:
		w.with(func(c *stressCommand) bool { return c.run }, w.cancelRun)
	case p < 86:
		w.with(always, w.walkAway)
	case p < 88:
		w.with(always, w.duplicate)
	default:
		w.sweep()
		runtime.Gosched()
	}
}

// with does op to a random command of the pool that the worker has not walked away from and that pred picks, if there is one.
func (w *stressWorker) with(pred func(*stressCommand) bool, op func(*stressCommand)) {
	var chosen *stressCommand
	seen := 0
	for _, c := range w.pool {
		if c.idle || !pred(c) {
			continue
		}
		if seen++; w.rng.IntN(seen) == 0 {
			chosen = c
		}
	}
	if chosen == nil {
		runtime.Gosched()
		return
	}
	op(chosen)
}

// sweep disposes of the commands whose reader has seen them end.
func (w *stressWorker) sweep() {
	kept := w.pool[:0]
	for _, c := range w.pool {
		if c.first.Load() != nil {
			w.r.dispose(c)
			continue
		}
		kept = append(kept, c)
	}
	clear(w.pool[len(kept):])
	w.pool = kept
}

// startRandom starts a command of a behaviour picked by weight, or makes room for it when the pool is full: the commands that
// ended are disposed of, and if none has, one is ended.
func (w *stressWorker) startRandom() {
	if len(w.pool) >= poolCap {
		w.sweep()
		if len(w.pool) >= poolCap {
			w.with(func(*stressCommand) bool { return true }, func(c *stressCommand) { w.end(c, w.rng.IntN(3) == 0) })
			return
		}
	}
	p := w.rng.IntN(stressWeight)
	b := stressBehaviours[0]
	for _, candidate := range stressBehaviours {
		if p < candidate.weight {
			b = candidate
			break
		}
		p -= candidate.weight
	}
	w.start(b, w.rng.IntN(100) < 30, "", false)
}

// start asks the engine for a command of the behaviour: a session, or a run. An empty id is a new one. With again set, id is one the
// worker used before: the engine refuses it while the command that holds it is live, and admits it once that command is gone.
func (w *stressWorker) start(b *stressBehaviour, run bool, id string, again bool) {
	r := w.r
	if left, capped := r.budgets[b.name]; capped && left.Load() <= 0 {
		return
	}
	if id == "" {
		w.seq++
		kind := "s"
		if run {
			kind = "r"
		}
		id = fmt.Sprintf("%s%d-w%d-%d", kind, r.n, w.id, w.seq)
	}
	target, err := wireTarget(r.conns, b.name, stressThing)
	if err != nil {
		r.found.add("%s: %v", id, err)
		return
	}
	attempt := stressAttempt{run: run, duplicate: again}
	afterClose := r.closed.Load()
	var (
		cmd    exechook.Command
		cancel context.CancelFunc
		tty    bool
	)
	if run {
		var runCtx context.Context
		runCtx, cancel = context.WithCancel(context.Background())
		cmd, err = r.eng.Run(r.conns.Context(runCtx), &execv2.RunRequest{RequestId: id, Target: target, Argv: []string{"stress"}, Stdin: w.stdin(b)})
	} else {
		tty = w.rng.IntN(2) == 0
		ctx := context.Background()
		if w.rng.IntN(50) == 0 { // a call whose context has ended: refused as cancelled, or admitted, as the engine finds it
			var stop context.CancelFunc
			ctx, stop = context.WithCancel(ctx)
			stop()
			attempt.ctxEnded = true
		}
		start := &execv2.Start{Target: target, Tty: tty}
		if tty && w.rng.IntN(3) == 0 {
			start.Size = &execv2.Size{Cols: uint32(20 + w.rng.IntN(200)), Rows: uint32(5 + w.rng.IntN(60))}
		}
		cmd, err = r.eng.Open(r.conns.Context(ctx), &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}})
	}
	if err != nil {
		if cancel != nil {
			cancel()
		}
		r.refused(err, id, attempt)
		runtime.Gosched()
		return
	}
	if afterClose {
		r.lateAdmit(id)
	}
	if left, capped := r.budgets[b.name]; capped {
		left.Add(-1)
	}
	mode, size := w.reader(b)
	c := &stressCommand{
		id: id, key: fmt.Sprintf("%s#%d", id, r.seq.Add(1)), b: b, run: run, tty: tty, cmd: cmd, cancel: cancel, mode: mode, buf: size,
	}
	if again {
		// The engine admits an id again only once the command that held it is gone, and a command is gone after it has ended.
		if prev := w.latest(id); prev != nil {
			select {
			case <-prev.cmd.Done():
			default:
				r.found.add("%s: the id was admitted again while the command that held it had not ended", id)
			}
		}
	}
	r.admit(c)
	w.pool = append(w.pool, c)
}

// stdin is the input of a run: what a command that reads its input needs, and sometimes a little for one that does not.
func (w *stressWorker) stdin(b *stressBehaviour) []byte {
	switch {
	case b.input:
		return patternBytes(0, 1+w.rng.IntN(4096))
	case w.rng.IntN(4) == 0:
		return patternBytes(0, 1+w.rng.IntN(64))
	}
	return nil
}

// reader picks how a command is read: mostly as fast as it comes, sometimes a few bytes at a time, and, for a command that ends by
// itself whatever its reader does, sometimes not until it has ended.
func (w *stressWorker) reader(b *stressBehaviour) (readMode, int) {
	switch p := w.rng.IntN(100); {
	case p < 25:
		return readSlow, 8 << w.rng.IntN(5)
	case p < 40 && b.alone:
		return readLate, 4096
	}
	return readEager, 256 << w.rng.IntN(6)
}

// write sends input to a session: mostly a little, sometimes more than the queue holds. A function that does not read its input
// holds the write at the bound of the queue, and the write gives up at its own bound; what the engine took is exact, and the
// pattern goes on from there.
func (w *stressWorker) write(c *stressCommand) {
	size := 1 + w.rng.IntN(2048)
	if w.rng.IntN(25) == 0 {
		size = 64<<10 + w.rng.IntN(300<<10)
	}
	data := patternBytes(c.sent, size)
	ctx, cancel := context.WithTimeout(context.Background(), writeBound)
	n, err := c.cmd.Write(ctx, data)
	cancel()
	if n > 0 && n <= len(data) {
		c.sent += uint64(n)
	}
	r := w.r
	switch {
	case n < 0 || n > len(data):
		r.found.add("%s: Write of %d bytes returned %d", c.id, len(data), n)
	case err == nil && n != len(data):
		r.found.add("%s: Write of %d bytes took %d and returned no error: it takes all of its bytes or fails", c.id, len(data), n)
	case err == nil:
		r.tally.add("op/write")
	case errors.Is(err, io.ErrClosedPipe):
		r.tally.add("op/write refused")
	case errors.Is(err, context.DeadlineExceeded):
		r.tally.add("op/write held")
	default:
		r.found.add("%s: Write returned %v: the only errors of a Write are io.ErrClosedPipe and the caller's context", c.id, err)
	}
	if c.inputDone && (n != 0 || err == nil) {
		r.found.add("%s: a Write after CloseInput took %d bytes and returned %v: it must be refused", c.id, n, err)
	}
}

// closeInput is the host's stdin EOF.
func (w *stressWorker) closeInput(c *stressCommand) {
	c.cmd.CloseInput()
	c.inputDone = true
	w.r.tally.add("op/close input")
}

// resize sends a size, valid or, now and then, with a zero dimension.
func (w *stressWorker) resize(c *stressCommand) {
	c.resizes++
	cols, rows := uint16(1+w.rng.IntN(300)), uint16(1+w.rng.IntN(100))
	if w.rng.IntN(10) == 0 {
		cols = 0
	}
	name := "closed"
	switch c.cmd.Resize(c.resizes, cols, rows) {
	case exechook.ResizeAccepted:
		name = "accepted"
	case exechook.ResizeIgnored:
		name = "ignored"
	case exechook.ResizeInvalid:
		name = "invalid"
	case exechook.ResizeClosed:
	}
	w.r.tally.add("op/resize " + name)
}

// end asks for the command to end, gracefully or with a kill.
func (w *stressWorker) end(c *stressCommand, kill bool) {
	c.cmd.Close(kill)
	if kill {
		w.r.tally.add("op/kill")
	} else {
		w.r.tally.add("op/close")
	}
}

// cancelRun ends the context of a run.
func (w *stressWorker) cancelRun(c *stressCommand) {
	c.cancel()
	w.r.tally.add("op/cancel run")
}

// walkAway leaves the command as it is: nothing more is done to it, and the end of the round ends it.
func (w *stressWorker) walkAway(c *stressCommand) {
	c.idle = true
	w.r.tally.add("op/walk away")
}

// duplicate asks for a command with the id of one the worker holds: refused while that command is live, a new command once it is
// gone.
func (w *stressWorker) duplicate(c *stressCommand) {
	w.r.tally.add("op/duplicate id")
	w.start(c.b, c.run, c.id, true)
}

// latest returns the command with the id that the worker started last, or nil.
func (w *stressWorker) latest(id string) *stressCommand {
	for i := len(w.pool) - 1; i >= 0; i-- {
		if w.pool[i].id == id {
			return w.pool[i]
		}
	}
	return nil
}
