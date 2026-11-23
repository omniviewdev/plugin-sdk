// Package session is the lifecycle primitive of a lane. It owns the whole life of the sessions
// of one lane (a log stream, an exec run, a terminal): they are admitted, they end, and the
// lane shuts down. Each of those is a state decided under one lock, and none is a count that
// the caller keeps in step with a flag.
//
// A [Registry] admits [Entry] values. An entry passes through four states:
//
//   - running: it was admitted and no one has called [Entry.End]. The goroutines that feed the
//     session, its producers, are started with [Entry.Go].
//   - ending: End was called. The cause is recorded, the entry's context is cancelled and
//     [Entry.Ended] is closed. The producers that are running go on until they return. A
//     producer that races End is refused or waited for, never lost; one that comes after End
//     has returned is refused.
//   - done: the last producer returned and [Entry.Done] is closed.
//   - removed: the lane's end function ran, the id was released, the registry no longer counts
//     the entry, and [Entry.Removed] is closed.
//
// The lane's end function, [Config.OnEnd], is the one place where an end is emitted. It runs
// exactly once for every admitted entry: after Done is closed, so never while a producer of the
// entry runs, and before the id is released, so that a session that takes the id over is
// admitted only once the first one has ended. [Registry.Close] returns only after the end
// function of every entry has returned.
//
// The registry never ends an entry by itself, with two exceptions. Close ends every entry with
// [CauseShutdown]. And the cancellation of the parent context an entry was admitted with ends
// it with [CauseCancelled], so that an entry whose context is done always has had End called.
// Every other end, a finished run, a requested close, a lease that ran out, is the lane's to
// make.
//
// An entry costs no goroutine while it runs: the goroutine that waits for its producers,
// publishes its end and removes it is started by the first End, and is a goroutine of the
// registry, which Close waits for. Every other goroutine of an entry is started with
// [Entry.Go], so that none can outlive its session.
//
// The package does not use a sync.WaitGroup, which cannot do this: an entry's producers are
// added from outside, by a caller or by a producer, while the goroutine that waits for them may
// be returning, and that is exactly the use a WaitGroup forbids (it is a data race, and in
// production the panic "sync: WaitGroup is reused before previous Wait has returned"). The
// registry and each entry are built on [producers.Group], which makes the end of the group a
// state decided under the lock that admits its members.
package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/omniviewdev/plugin-sdk/internal/producers"
)

var (
	// ErrClosed is returned by [Registry.Admit] once [Registry.Close] has begun.
	ErrClosed = errors.New("session: registry closed")
	// ErrExists is returned by [Registry.Admit] for an id that is live, which is until the
	// registry has removed its entry: after the end function ran, not when the session ended.
	ErrExists = errors.New("session: id already in use")
	// ErrLimit is returned by [Registry.Admit] when admitting the session would take its group
	// over [Spec.Limit] with the live entries alone.
	ErrLimit = errors.New("session: group limit reached")
	// ErrHeld is returned by [Registry.Admit] when the live entries of the group are below
	// [Spec.Limit] and the group is full only because of its holds (see [Registry.Hold]).
	ErrHeld = errors.New("session: group limit reached by holds")
)

// Spec describes a session to admit.
type Spec struct {
	// ID names the session. It is required and unique among the live entries of the
	// registry, and [Registry.Admit] panics when it is empty.
	ID string
	// Group is optional: the live entries with the same Group are counted together, for
	// Limit. The empty Group is a group like any other.
	Group string
	// Limit, when it is above zero, is the most live entries Group may have. An Admit that
	// would take the group over it is refused with [ErrLimit]. An entry is live until it is
	// removed, so it counts during its end function.
	Limit int
}

// Info describes a live entry, as [Registry.Snapshot] reports it.
type Info struct {
	ID    string
	Group string
	// Cause is [CauseNone] while the session runs, and why it ended once it has.
	Cause Cause
}

// Config configures a [Registry].
type Config[S any] struct {
	// OnEnd is the lane's single "emit the end" function. It is called exactly once for every
	// admitted entry, by a goroutine of the registry, after the entry's last producer has
	// returned (so after [Entry.Done] is closed), and before the entry's id is released and
	// before [Registry.Close] can return. Nothing else may emit an end. It may be nil, and then
	// the end of an entry is not announced.
	//
	// OnEnd must not call [Registry.Close]: the Close would wait for the OnEnd that called
	// it. It may call the registry's other methods and the entry's. If it panics, the entry
	// is still removed and the registry's accounting is still released, and the panic goes on
	// to end the process: it is never swallowed.
	OnEnd func(*Entry[S])
}

// Registry holds the live sessions of one lane, and decides under one lock who may be
// admitted and when the lane is shut down. Make one with [New]. It is safe for concurrent
// use.
type Registry[S any] struct {
	onEnd func(*Entry[S])
	seams seams

	// mu is the admission lock. The closed flag, the map of live entries and the count of each
	// group change under it, and an entry is registered only while closed is false, so no
	// admission can come after the one that finds the registry closed.
	mu      sync.Mutex
	closed  bool                 // Close has begun: nothing more is admitted
	entries map[string]*Entry[S] // the live entries, by id
	groups  map[string]int       // the live entries of each group
	held    map[string]int       // the holds of each group: places kept after the work that owned them ended

	// live holds one producer for each entry: started at Admit, under mu, and done after the
	// entry is removed. Close waits for it.
	live producers.Group
}

// seams are the test hooks of a Registry. Each is nil in production and is called, at the
// crossing it names, when a test has set it, to hold the code there and so to force an order of
// events that the scheduler would otherwise choose. A test sets them before it uses the
// registry.
type seams struct {
	// admitDecided runs in Admit, under the registry's lock, once its checks have passed and
	// before the entry is registered.
	admitDecided func(id string)
	// endRecorded runs in the first End, once the cause is recorded and before the context is
	// cancelled and the finisher is started.
	endRecorded func(id string)
	// goChecked runs in Go, once it found the entry not ending and before it registers the
	// producer.
	goChecked func(id string)
	// spawn starts the finisher goroutine in the place of a go statement.
	spawn func(run func())
}

// New returns an empty registry. cfg.OnEnd may be nil.
func New[S any](cfg Config[S]) *Registry[S] {
	return &Registry[S]{
		onEnd:   cfg.OnEnd,
		entries: make(map[string]*Entry[S]),
		groups:  make(map[string]int),
		held:    make(map[string]int),
	}
}

// Admit registers a session, atomically under the registry's lock. It is refused with
// [ErrClosed] once Close has begun, [ErrExists] for an id that is live, [ErrLimit] when
// spec.Limit is reached, and with init's error when init fails; a refused Admit registers
// nothing and starts nothing.
//
// init runs under the lock. It builds the session's state from the entry's context, and it
// must not block and must not call the registry. A nil init admits a session without state.
//
// The entry's context carries the values of parent, and is cancelled by [Entry.End], which
// Close and the cancellation of parent both call; it is not cancelled by anything else, so
// that a context that is done always means that End was called. When parent can be cancelled,
// its cancellation ends the entry with [CauseCancelled]: a watcher that is itself a producer of
// the entry does it, and an entry whose parent can never be cancelled costs no goroutine.
// parent must not be nil. Admit panics when spec.ID is empty.
func (r *Registry[S]) Admit(parent context.Context, spec Spec, init func(ctx context.Context) (S, error)) (*Entry[S], error) {
	if spec.ID == "" {
		panic("session: Admit with an empty Spec.ID")
	}
	e, err := r.admit(parent, spec, init)
	if err != nil {
		return nil, err
	}
	e.Watch(parent, CauseCancelled)
	return e, nil
}

// admit decides the admission under the lock, and registers the entry.
func (r *Registry[S]) admit(parent context.Context, spec Spec, init func(ctx context.Context) (S, error)) (*Entry[S], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.closed:
		return nil, ErrClosed
	case r.entries[spec.ID] != nil:
		return nil, ErrExists
	case spec.Limit > 0 && r.groups[spec.Group] >= spec.Limit:
		return nil, ErrLimit
	case spec.Limit > 0 && r.groups[spec.Group]+r.held[spec.Group] >= spec.Limit:
		return nil, ErrHeld
	}
	if h := r.seams.admitDecided; h != nil {
		h(spec.ID)
	}

	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	registered := false
	defer func() {
		if !registered {
			cancel() // init failed or panicked: nothing owns the context
		}
	}()
	var value S
	if init != nil {
		var err error
		if value, err = init(ctx); err != nil {
			return nil, err
		}
	}
	if !r.live.Start(1) {
		return nil, ErrClosed // not reachable: the group ends only after closed is set, under mu
	}
	e := &Entry[S]{
		reg:    r,
		id:     spec.ID,
		group:  spec.Group,
		value:  value,
		ctx:    ctx,
		cancel: cancel,
		ended:  make(chan struct{}),
		done:   make(chan struct{}),
		gone:   make(chan struct{}),
	}
	r.entries[spec.ID] = e
	r.groups[spec.Group]++
	registered = true
	return e, nil
}

// Close refuses new admissions, ends every live entry with [CauseShutdown], and waits until
// the end function of every entry has returned and the entry is removed, or until ctx ends, and
// then returns ctx.Err(). Close is idempotent, safe to call from many goroutines at once, and
// safe concurrently with Admit, End and Go: an Admit that returned before Close began is ended
// by it, with CauseShutdown or an earlier cause, and one that comes after is refused with
// [ErrClosed]. A Close that gave up leaves the registry closed, and a later Close waits again.
//
// Close must not be called from the end function or from a producer of an entry: it would
// wait for itself.
func (r *Registry[S]) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	live := make([]*Entry[S], 0, len(r.entries))
	for _, e := range r.entries {
		live = append(live, e)
	}
	r.mu.Unlock()

	for _, e := range live {
		e.End(CauseShutdown)
	}
	return r.live.WaitContext(ctx)
}

// Snapshot returns the live entries, sorted by id. It is a copy: it does not change as the
// entries do. An entry is live until it is removed, so one that has ended is in it, with its
// cause, until its end function has returned.
func (r *Registry[S]) Snapshot() []Info {
	r.mu.Lock()
	infos := make([]Info, 0, len(r.entries))
	for _, e := range r.entries {
		infos = append(infos, Info{ID: e.id, Group: e.group, Cause: e.Cause()})
	}
	r.mu.Unlock()
	slices.SortFunc(infos, func(a, b Info) int { return strings.Compare(a.ID, b.ID) })
	return infos
}

// Len returns the number of live entries.
func (r *Registry[S]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Get returns the live entry with the id. An entry is live until it is removed, so one that has ended is found, with its cause, until
// its end function has returned. A lane that has to reach one of its sessions by the id a client names (an attach, an end) uses it.
func (r *Registry[S]) Get(id string) (*Entry[S], bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	return e, ok
}

// Entries returns the live entries, sorted by id. It is a copy of the list: it does not change as the entries do.
func (r *Registry[S]) Entries() []*Entry[S] {
	r.mu.Lock()
	entries := make([]*Entry[S], 0, len(r.entries))
	for _, e := range r.entries {
		entries = append(entries, e)
	}
	r.mu.Unlock()
	slices.SortFunc(entries, func(a, b *Entry[S]) int { return strings.Compare(a.id, b.id) })
	return entries
}

// startFinisher starts the goroutine that finishes e: the first End calls it.
func (r *Registry[S]) startFinisher(e *Entry[S]) {
	if spawn := r.seams.spawn; spawn != nil {
		spawn(func() { r.finish(e) })
		return
	}
	go r.finish(e)
}

// finish is the end of an entry, from the last producer to the release of its id: it waits for
// the producers, closes Done, runs the end function and removes the entry. It is deferred so
// that a panic in the end function still removes the entry and releases the registry's
// accounting.
func (r *Registry[S]) finish(e *Entry[S]) {
	defer r.remove(e)
	e.prod.Wait()
	close(e.done)
	if r.onEnd != nil {
		r.onEnd(e)
	}
}

// remove releases e's id and its place in its group, and then the registry's count of live
// entries, which is what Close waits for. A retained entry's place already became a hold when it was retained, and the hold's
// release is what frees it.
func (r *Registry[S]) remove(e *Entry[S]) {
	r.mu.Lock()
	delete(r.entries, e.id)
	if !e.retained {
		r.dropPlace(e.group)
	}
	r.mu.Unlock()
	close(e.gone) // the id and the place in the group are free: a start that was refused can be tried again
	r.live.Done()
}

// dropPlace takes one live entry out of the count of group. The caller holds mu.
func (r *Registry[S]) dropPlace(group string) {
	if r.groups[group] <= 1 {
		delete(r.groups, group)
	} else {
		r.groups[group]--
	}
}

// retain moves e's place in its group to a hold: the group's count does not change at that moment, and it does not change
// when the entry is removed, only when the hold is released. See [Entry.Retain].
func (r *Registry[S]) retain(e *Entry[S]) (release func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.retained {
		return e.releaseRetain
	}
	e.retained = true
	if r.entries[e.id] == e {
		// The entry still has its place: it is the hold's now. A place that was released already (the entry was removed)
		// is not given back, and the hold is a new one: a fact that is true whatever the entry did.
		r.dropPlace(e.group)
	}
	r.held[e.group]++
	var once sync.Once
	e.releaseRetain = func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.held[e.group] <= 1 {
				delete(r.held, e.group)
			} else {
				r.held[e.group]--
			}
		})
	}
	return e.releaseRetain
}

// Held returns the number of holds of group.
func (r *Registry[S]) Held(group string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.held[group]
}
