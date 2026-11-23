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

	// startLease is the lease the session was started asking for, zero for the target's own.
	startLease time.Duration
	// sess is what a session has beyond a command: the windows that read it and the calls made on it. A run has none: it has one reader.
	sess *sessionState
	// endRequests and cancels count the calls of a run that can end it, before they are made, so that a cause that needs one is
	// explained by one that was made. A session's are its sess's.
	endRequests, cancels atomic.Int32

	// first is the result seen once the command had ended: what every later look at the result is held to. It is set once: by the reader of a
	// run when it is done, and by the watcher of a session when the session has ended.
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
	case class == exec.ErrorClassLimit:
		allowed = (!a.run && name == "LIMIT/TOO_MANY_SESSIONS") || (a.run && name == "LIMIT/TOO_MANY_RUNS")
	case class == exec.ErrorClassUnavailable:
		allowed = r.closing.Load() || r.mayHold.Load() // a closed engine, or a target whose places abandoned functions hold
	case class == exec.ErrorClassConflict:
		allowed = a.duplicate && name == "CONFLICT/SESSION_EXISTS"
	case class == exec.ErrorClassCancelled:
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

// admit takes a command the engine admitted: the recorder is told, and a reader of its own reads it to its end: the one reader of a run, the
// windows of a session (admitSession). admitAt is the clock before the start that admitted it.
func (r *stressRound) admit(c *stressCommand, admitAt int64) {
	if c.b.name == "stuck" {
		r.mayHold.Store(true) // its function ignores its context: the engine gives up on it and holds its place
	}
	r.rec.Admitted(c.key)
	r.outstanding.add(c)
	kind := "session"
	if c.run {
		kind = "run"
	}
	r.tally.add("admitted/" + kind)
	r.tally.add("behaviour/" + c.b.name)
	if !c.run {
		r.admitSession(c, admitAt)
		return
	}
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
	r.checkEndedRun(c, res, audit)

	chunk, err := c.cmd.Read(r.readCtx, buf)
	if !errors.Is(err, io.EOF) && r.readCtx.Err() != nil {
		return
	}
	if !errors.Is(err, io.EOF) || chunk.N != 0 || len(chunk.Dropped) != 0 {
		r.rec.Event(c.key)
		r.found.add("%s: a Read after the end returned %d bytes, %d dropped ranges and %v: nothing may come after the end", c.id, chunk.N, len(chunk.Dropped), err)
	}
}

// checkEndedRun holds the end of a run whose reader is done to what must be true of every command, to what its behaviour says of it and to the calls
// that can explain its cause, and counts it. A session's end is held to the same when its streams have all returned (finishSession).
func (r *stressRound) checkEndedRun(c *stressCommand, res exechook.Result, audit outputAudit) {
	for _, v := range checkResult(res, audit) {
		r.found.add("%s: %s", c.id, v)
	}
	for _, v := range c.b.check(res, audit, c.run, c.mode, false) {
		r.found.add("%s: %s", c.id, v)
	}
	facts := causeFacts{endRequests: c.endRequests.Load(), cancels: c.cancels.Load(), closing: r.closing.Load(), run: true}
	if v := checkCause(res.Cause, facts); v != "" {
		r.found.add("%s: %s", c.id, v)
	}
	r.tallyEnd(res)
	r.tally.addN("bytes read", int(audit.read))
	r.tally.addN("bytes dropped", int(audit.dropped))
}

// tallyEnd counts the end of a command: by arm, by cause, by completeness, and what the engine says its function broke.
func (r *stressRound) tallyEnd(res exechook.Result) {
	arm, _ := endArm(res.End)
	r.tally.add("ended/" + arm)
	r.tally.add("cause/" + res.Cause)
	r.tally.add("output/" + strings.ToLower(strings.TrimPrefix(res.Output.String(), "OUTPUT_COMPLETENESS_")))
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
	// history is the last sessions the worker let go of: their ids are the ones a start asks for again, long after their command is gone.
	history []retiredSession
	// calm says that the next start is the canary's: no short lease, no small ring, no slow reader, so that what it expects of its session
	// cannot be a race with a lease that runs out.
	calm bool
}

// retiredSession is a session that a worker is done with: what is needed to ask for its id again.
type retiredSession struct {
	rec *sessionRec
	b   *stressBehaviour
}

// work is one worker: it starts a command of each behaviour as a session and as a run, so that every one is met whatever the dice
// say, plays the canary, and then picks at random until stop is closed.
func (r *stressRound) work(id int, stop <-chan struct{}) {
	w := &stressWorker{r: r, id: id, rng: rand.New(rand.NewPCG(r.seed, uint64(id)+1))}
	defer w.handOver()
	for _, run := range []bool{false, true} {
		for i := range stressBehaviours {
			w.start(stressBehaviours[(i+id)%len(stressBehaviours)], run, "", false)
		}
	}
	w.canary()
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
	r := w.r
	always := func(*stressCommand) bool { return true }
	session := func(c *stressCommand) bool { return !c.run && c.sess != nil }
	switch p := w.rng.IntN(100); {
	case p < 20:
		w.startRandom()
	case p < 30:
		w.with(func(c *stressCommand) bool { return !c.run && (c.b.input || w.rng.IntN(6) == 0) }, w.write)
	case p < 32:
		w.with(func(c *stressCommand) bool { return !c.run }, w.closeInput)
	case p < 37:
		w.with(always, w.resize)
	case p < 41:
		w.with(session, func(c *stressCommand) { r.signalOp(c, w.rng) })
	case p < 44:
		w.with(always, func(c *stressCommand) { w.end(c, false) })
	case p < 46:
		w.with(always, func(c *stressCommand) { w.end(c, true) })
	case p < 49:
		w.with(func(c *stressCommand) bool { return c.run }, w.cancelRun)
	case p < 51:
		w.with(always, w.walkAway)
	case p < 54:
		w.duplicateAny()
	case p < 62:
		w.with(session, func(c *stressCommand) {
			w.either(c, func(rng *rand.Rand) { r.attachOp(c, rng, false, rng.IntN(12) == 0) })
		})
	case p < 66:
		w.with(session, func(c *stressCommand) { w.either(c, func(rng *rand.Rand) { r.attachOp(c, rng, true, false) }) })
	case p < 73:
		w.with(session, func(c *stressCommand) { w.either(c, func(rng *rand.Rand) { r.detachOp(c, rng, 0, false) }) })
	case p < 77:
		w.with(session, func(c *stressCommand) { w.either(c, func(rng *rand.Rand) { r.leaveOp(c, rng) }) })
	case p < 80:
		w.with(session, func(c *stressCommand) {
			kill := w.rng.IntN(3) == 0
			w.either(c, func(rng *rand.Rand) { r.closeStreamOp(c, rng, kill) })
		})
	case p < 84:
		w.with(session, func(c *stressCommand) {
			kill := w.rng.IntN(3) == 0
			w.either(c, func(*rand.Rand) { r.endSessionOp(c, kill, endSessionPatience) })
		})
	case p < 88:
		w.with(session, func(c *stressCommand) { r.staleOp(c, w.rng) }) // not on a goroutine of its own: it sends input, which only the worker does
	case p < 89:
		r.listOp()
	case p < 91:
		w.asyncAny(func(rng *rand.Rand) { r.readOutputOp(rng) })
	case p < 93 && r.regime.ticks:
		r.tickOp(w.rng)
	default:
		w.sweep()
		runtime.Gosched()
	}
}

// either makes the op now, in the worker, or, one time in three, on a goroutine of its own, so that the worker goes on to make another meanwhile
// and an op meets itself: two attaches, a detach and a close, an EndSession and an attach, at the same moment, on one session. Until the op has
// returned the id of the command is not asked for again (see duplicate): the call is addressed to the id, and is meant for the session that holds it.
func (w *stressWorker) either(c *stressCommand, op func(rng *rand.Rand)) {
	if w.rng.IntN(3) != 0 {
		op(w.rng)
		return
	}
	r := w.r
	if !r.ops.Start(1) {
		return
	}
	flight := r.ledger.flightOf(c.id)
	flight.Add(1)
	rng := rand.New(rand.NewPCG(w.rng.Uint64(), w.rng.Uint64()))
	go func() {
		defer r.ops.Done()
		defer flight.Add(-1)
		op(rng)
	}()
}

// asyncAny is either for an op that is not about one command.
func (w *stressWorker) asyncAny(op func(rng *rand.Rand)) {
	if w.rng.IntN(3) != 0 {
		op(w.rng)
		return
	}
	r := w.r
	if !r.ops.Start(1) {
		return
	}
	rng := rand.New(rand.NewPCG(w.rng.Uint64(), w.rng.Uint64()))
	go func() {
		defer r.ops.Done()
		op(rng)
	}()
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

// sweep disposes of the commands whose reader has seen them end, and remembers the sessions among them, whose ids may be asked for again.
func (w *stressWorker) sweep() {
	kept := w.pool[:0]
	for _, c := range w.pool {
		if c.first.Load() != nil {
			w.r.dispose(c)
			if c.sess != nil {
				w.retire(c)
			}
			continue
		}
		kept = append(kept, c)
	}
	clear(w.pool[len(kept):])
	w.pool = kept
}

// retire remembers a session the worker is done with, the last 64 of them.
func (w *stressWorker) retire(c *stressCommand) {
	if len(w.history) >= 64 {
		w.history = append(w.history[:0], w.history[1:]...)
	}
	w.history = append(w.history, retiredSession{rec: c.sess.rec, b: c.b})
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
// worker used before: the engine refuses it while the command that holds it is live, and, for a session, while its tombstone holds it, and
// admits it once the command, or the tombstone, is gone.
func (w *stressWorker) start(b *stressBehaviour, run bool, id string, again bool) {
	r := w.r
	if left, capped := r.budgets[b.name]; capped && left.Load() <= 0 {
		return
	}
	if !run && r.regime.sessions > 0 && r.ledger.total() >= r.regime.sessions {
		return // a round that keeps every tombstone starts no more sessions than its store holds
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
	// What is known of the session that held the id, before the call: the readings that say when its tombstone was made.
	var prev *sessionRec
	var prevFacts reuseFacts
	if again && !run {
		if prev = w.latestSession(id); prev != nil {
			prev.look(r.clock)
			prevFacts = prev.reuseFacts()
		}
	}
	var (
		cmd    exechook.Command
		cancel context.CancelFunc
		tty    bool
		lease  time.Duration
	)
	if !run {
		// Until the answer is in the ledger the engine may have admitted the session, and a call addressed to the id that is made meanwhile, by any
		// worker, may meet it: the ledger knows the start is under way.
		r.ledger.beginStart(id)
		defer r.ledger.endStart(id)
	}
	asked := r.clock.Now()
	if run {
		var runCtx context.Context
		runCtx, cancel = context.WithCancel(context.Background())
		cmd, err = r.eng.Run(r.conns.Context(runCtx), &execv2.RunRequest{RequestId: id, Target: target, Argv: []string{"stress"}, Stdin: w.stdin(b)})
	} else {
		tty = w.rng.IntN(2) == 0 || w.calm
		ctx := context.Background()
		if w.rng.IntN(50) == 0 && !w.calm { // a call whose context has ended: refused as cancelled, or admitted, as the engine finds it
			var stop context.CancelFunc
			ctx, stop = context.WithCancel(ctx)
			stop()
			attempt.ctxEnded = true
		}
		start := &execv2.Start{Target: target, Tty: tty}
		if tty && w.rng.IntN(3) == 0 && !w.calm {
			start.Size = &execv2.Size{Cols: uint32(20 + w.rng.IntN(200)), Rows: uint32(5 + w.rng.IntN(60))}
		}
		if !w.calm {
			// The ring a host asks for, and the lease a stream that leaves gets, shorter than the target's: small rings make a detached session
			// lose its output, and short leases end it while the host is away.
			if b.session.ReplayBytes == 0 {
				start.ReplayBytes = startRings[w.rng.IntN(len(startRings))]
			}
			start.DetachTimeoutMs = startLeases[w.rng.IntN(len(startLeases))]
			lease = time.Duration(start.DetachTimeoutMs) * time.Millisecond
		}
		cmd, err = r.eng.Open(r.conns.Context(ctx), &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}})
	}
	answered := r.clock.Now()
	if prev != nil {
		held := err != nil && refusalName(err) == "CONFLICT/SESSION_EXISTS"
		if v := checkReuse(err == nil, held, asked, answered, prevFacts, r.regime.ttl, r.ledger.total() > r.regime.count); v != "" {
			r.found.add("%s: %s", id, v)
		}
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
		startLease: lease,
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
	r.admit(c, asked.UnixNano())
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
	if w.calm {
		return readEager, 4096
	}
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
// pattern goes on from there. A session's input is sent by its consumer or on one of its streams, which the engine refuses once the
// stream has left.
func (w *stressWorker) write(c *stressCommand) {
	size := 1 + w.rng.IntN(2048)
	if w.rng.IntN(25) == 0 {
		size = 64<<10 + w.rng.IntN(300<<10)
	}
	data := patternBytes(c.sent, size)
	ctx, cancel := context.WithTimeout(context.Background(), writeBound)
	send := c.cmd.Write
	var via *stressStream
	if c.sess != nil && w.rng.IntN(2) == 0 {
		if via = c.sess.liveStream(w.rng); via != nil {
			send = via.s.Write
		}
	}
	n, err := send(ctx, data)
	cancel()
	if n > 0 && n <= len(data) {
		c.sent += uint64(n)
	}
	r := w.r
	switch {
	case n < 0 || n > len(data):
		r.found.add("%s: Write of %d bytes returned %d", c.id, len(data), n)
	case via != nil && errors.Is(err, exechook.ErrStreamLeft):
		via.refusedLeft.Store(true)
		r.tally.add("op/write refused (stream left)")
		if n != 0 {
			r.found.add("%s: an input frame of a stream that left took %d bytes and was refused", c.id, n)
		}
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

// closeInput is the host's stdin EOF, from the session's consumer or on one of its streams.
func (w *stressWorker) closeInput(c *stressCommand) {
	if c.sess != nil {
		c.sess.inputEnds.Add(1)
		if w.rng.IntN(2) == 0 {
			if via := c.sess.liveStream(w.rng); via != nil {
				if err := via.s.CloseInput(); errors.Is(err, exechook.ErrStreamLeft) {
					via.refusedLeft.Store(true)
					w.r.tally.add("op/close input refused (stream left)")
					return
				}
				c.inputDone = true
				w.r.tally.add("op/close input")
				return
			}
		}
	}
	c.cmd.CloseInput()
	c.inputDone = true
	w.r.tally.add("op/close input")
}

// resize sends a size, valid or, now and then, with a zero dimension, from the session's consumer or on one of its streams.
func (w *stressWorker) resize(c *stressCommand) {
	c.resizes++
	cols, rows := uint16(1+w.rng.IntN(300)), uint16(1+w.rng.IntN(100))
	if w.rng.IntN(10) == 0 {
		cols = 0
	}
	disposition := func() exechook.ResizeDisposition { return c.cmd.Resize(c.resizes, cols, rows) }
	var via *stressStream
	if c.sess != nil && w.rng.IntN(2) == 0 {
		if via = c.sess.liveStream(w.rng); via != nil {
			disposition = func() exechook.ResizeDisposition {
				d, err := via.s.Resize(c.resizes, cols, rows)
				if errors.Is(err, exechook.ErrStreamLeft) {
					via.refusedLeft.Store(true)
					return -1
				}
				return d
			}
		}
	}
	name := "closed"
	switch disposition() {
	case exechook.ResizeAccepted:
		name = "accepted"
	case exechook.ResizeIgnored:
		name = "ignored"
	case exechook.ResizeInvalid:
		name = "invalid"
	case exechook.ResizeClosed:
	default:
		name = "refused (stream left)"
	}
	w.r.tally.add("op/resize " + name)
}

// end asks for the command to end, gracefully or with a kill.
func (w *stressWorker) end(c *stressCommand, kill bool) {
	if kill {
		w.r.mayHold.Store(true) // a function given up on holds its place until it returns
	}
	if c.sess != nil {
		c.sess.endRequests.Add(1)
	} else {
		c.endRequests.Add(1)
	}
	c.cmd.Close(kill)
	if kill {
		w.r.tally.add("op/kill")
	} else {
		w.r.tally.add("op/close")
	}
}

// cancelRun ends the context of a run.
func (w *stressWorker) cancelRun(c *stressCommand) {
	c.cancels.Add(1)
	c.cancel()
	w.r.tally.add("op/cancel run")
}

// walkAway leaves the command as it is: nothing more is done to it, and the end of the round ends it.
func (w *stressWorker) walkAway(c *stressCommand) {
	c.idle = true
	w.r.tally.add("op/walk away")
}

// duplicate asks for a command with the id of one the worker holds: refused while that command is live, a new command once it is
// gone, and, for a session, once its tombstone is. An id that a call on a goroutine of its own is addressed to is left alone, whichever of the id's
// sessions the call was made for, so that the id is not taken while the call is meant for the session that held it.
func (w *stressWorker) duplicate(c *stressCommand) {
	if c.sess != nil && w.r.ledger.flightOf(c.id).Load() > 0 {
		return
	}
	w.r.tally.add("op/duplicate id")
	w.start(c.b, c.run, c.id, true)
}

// duplicateAny asks for the id of a command in the pool, or of a session that was let go of long ago, whose tombstone may be gone.
func (w *stressWorker) duplicateAny() {
	if len(w.history) > 0 && w.rng.IntN(2) == 0 {
		h := w.history[w.rng.IntN(len(w.history))]
		if w.r.ledger.flightOf(h.rec.id).Load() == 0 {
			w.r.tally.add("op/duplicate id")
			w.start(h.b, false, h.rec.id, true)
		}
		return
	}
	w.with(func(*stressCommand) bool { return true }, w.duplicate)
}

// latestSession returns the session with the id that the worker started last: the one in its pool, or, once that was let go of, in its history.
func (w *stressWorker) latestSession(id string) *sessionRec {
	if c := w.latest(id); c != nil {
		if c.sess != nil {
			return c.sess.rec
		}
		return nil
	}
	for i := len(w.history) - 1; i >= 0; i-- {
		if w.history[i].rec.id == id {
			return w.history[i].rec
		}
	}
	return nil
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
