package exec

import (
	"container/list"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

// The cache of Options callbacks. The admission of an Open or a Run asks the target's Options callback for the resource, to hold the
// selection to its choices and to narrow the capabilities by the choice's mode, and a host that opens a session has usually just
// called the Options RPC for the same resource: without a cache the callback (an API call of the author's) runs twice for one picker
// and one start, and once more for every start after it. The engine keeps the result of a callback for a short time and the admission
// of a start reuses it. Only the admission reuses it: the unary Options RPC always runs the callback, and refreshes what is kept.
//
// What is kept is advisory state. A stale answer (a container that stopped since) only moves a refusal later: the target refuses the
// exec and the author's Classify types the refusal. The key is the whole identity of the request (the provider and type, the resource
// with its authority, scope and id, its incarnation, and the connection that reaches it), so two requests never share an answer; the
// entries of a connection are dropped when the engine sees the connection retired (the resource lane has no hook for it, so a command
// or an Options call that the retirement ended is where it is seen); an entry lives for the TTL and no longer; and the cache never
// holds more than its bound, the oldest entry going first, which is also the one that expires first. A callback that failed, panicked
// or broke its contract leaves nothing behind. Concurrent starts of a request at a cold cache may each run the callback once: the
// cache does not make them wait for one another.

const (
	// defaultOptionsCacheTTL is how long the admission of a start may reuse the result of an Options callback.
	defaultOptionsCacheTTL = 5 * time.Second
	// defaultOptionsCacheEntries is the most results the cache keeps.
	defaultOptionsCacheEntries = 64
)

// optionsKey is the whole identity of one request for options: the connection that reaches the resource, and the resource's own
// identity as text in which no two identities run together (every field is written with its length).
type optionsKey struct{ connection, identity string }

// optionsKeyOf returns the key of a request for the options of ref through route. The scope is sorted, because its order does not
// change what it says; a reference with no incarnation claims none, which is not the claim of the empty one.
func optionsKeyOf(ref resourcev2.ResourceReference, route resourcev2.AccessRoute) optionsKey {
	a := ref.Address
	scope := slices.Clone(a.Scope)
	slices.SortFunc(scope, func(x, y resourcev2.ScopeEntry) int {
		if c := strings.Compare(x.Dimension, y.Dimension); c != 0 {
			return c
		}
		return strings.Compare(x.Value, y.Value)
	})
	var b strings.Builder
	field := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	field(a.Provider)
	field(a.Authority)
	field(a.Type)
	field(a.ID)
	b.WriteString(strconv.Itoa(len(scope)))
	b.WriteByte(';')
	for _, entry := range scope {
		field(entry.Dimension)
		field(entry.Value)
	}
	if ref.Incarnation == nil {
		b.WriteByte('-')
	} else {
		b.WriteByte('+')
		field(*ref.Incarnation)
	}
	return optionsKey{connection: route.Connection, identity: b.String()}
}

// optionsCache holds the results of Options callbacks, oldest first. Its zero value is an empty cache, and it is safe for concurrent use.
type optionsCache struct {
	mu      sync.Mutex
	entries map[optionsKey]*list.Element
	order   list.List // of *optionsEntry, the oldest at the front
}

// optionsEntry is one kept result and when it was kept.
type optionsEntry struct {
	key     optionsKey
	options []Option
	at      time.Time
}

// get returns a copy of the result kept for key, when it is younger than ttl: an entry is valid for strictly less than its TTL.
func (c *optionsCache) get(key optionsKey, now time.Time, ttl time.Duration) ([]Option, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	entry := el.Value.(*optionsEntry)
	if now.Sub(entry.at) >= ttl {
		return nil, false
	}
	return cloneOptions(entry.options), true
}

// put keeps a copy of options for key as of now, as the newest entry (a key that is kept already is replaced), and evicts the oldest
// entries while the cache holds more than max.
func (c *optionsCache) put(key optionsKey, options []Option, now time.Time, max int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[optionsKey]*list.Element{}
	}
	if old, ok := c.entries[key]; ok {
		c.order.Remove(old)
		delete(c.entries, key)
	}
	c.entries[key] = c.order.PushBack(&optionsEntry{key: key, options: cloneOptions(options), at: now})
	for c.order.Len() > max && c.order.Len() > 0 {
		c.removeLocked(c.order.Front())
	}
}

// sweep drops what has expired as of now: the entries are in the order they were kept, so what expired is at the front.
func (c *optionsCache) sweep(now time.Time, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for el := c.order.Front(); el != nil && now.Sub(el.Value.(*optionsEntry).at) >= ttl; el = c.order.Front() {
		c.removeLocked(el)
	}
}

// dropConnection drops every entry kept for requests through the connection.
func (c *optionsCache) dropConnection(connection string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for el := c.order.Front(); el != nil; {
		next := el.Next()
		if el.Value.(*optionsEntry).key.connection == connection {
			c.removeLocked(el)
		}
		el = next
	}
}

// len is the number of entries kept, the expired ones that nothing has swept yet included.
func (c *optionsCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// removeLocked drops one entry. The lock is held.
func (c *optionsCache) removeLocked(el *list.Element) {
	delete(c.entries, el.Value.(*optionsEntry).key)
	c.order.Remove(el)
}

// cloneOptions returns a deep copy of options: the choices and the capabilities of each choice are the copy's own, so that neither the
// author who keeps changing the slice its callback returns, nor a caller of the cache, reaches what another reads.
func cloneOptions(options []Option) []Option {
	if options == nil {
		return nil
	}
	out := make([]Option, len(options))
	for i := range options {
		out[i] = options[i]
		out[i].Choices = slices.Clone(options[i].Choices)
		for j := range out[i].Choices {
			if caps := out[i].Choices[j].Capabilities; caps != nil {
				out[i].Choices[j].Capabilities = new(cloneCapabilities(*caps))
			}
		}
	}
	return out
}

// cachedOptions returns a copy of the answer that a callback gave for the request, when the engine has one that is still young: it is
// what the admission of a start reuses. A cache with no TTL (zero or less) is off and answers nothing.
func (e *engine) cachedOptions(key optionsKey) ([]Option, bool) {
	ttl := e.cfg.optionsCacheTTL
	if ttl <= 0 {
		return nil, false
	}
	return e.optionsCache.get(key, e.clock(), ttl)
}

// cacheOptions keeps what a callback answered for the request, as the newest entry, after it has dropped what expired.
func (e *engine) cacheOptions(key optionsKey, options []Option) {
	ttl := e.cfg.optionsCacheTTL
	if ttl <= 0 {
		return
	}
	now := e.clock()
	e.optionsCache.sweep(now, ttl)
	e.optionsCache.put(key, options, now, e.cfg.optionsCacheMax)
}
