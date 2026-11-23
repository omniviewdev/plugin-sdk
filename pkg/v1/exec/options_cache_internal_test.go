package exec

import (
	"strconv"
	"sync"
	"testing"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

// The cache of Options callbacks is the engine's: the admission of an Open or a Run reuses the result of the callback for the same
// request for a short time, so that a start right after the picker's own call does not run the callback again. These tests hold
// its building block to what it promises: the whole identity of a request is its key, an entry lives for its TTL and no longer, the
// cache never holds more than its bound, and what it keeps is its own copy.

var cacheEpoch = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func cacheRef(mods ...func(*resourcev2.ResourceReference)) resourcev2.ResourceReference {
	ref := resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{
		Provider: "k8s", Authority: "cluster", Type: "pods", ID: "web-0",
		Scope: []resourcev2.ScopeEntry{{Dimension: "namespace", Value: "default"}, {Dimension: "zone", Value: "a"}},
	}}
	for _, mod := range mods {
		mod(&ref)
	}
	return ref
}

func cacheOptionsFor(value string) []Option {
	caps := Capabilities{Signals: []Signal{SignalInt}, Integration: []IntegrationMode{IntegrationArgvWrapper}}
	return []Option{{Name: "container", Required: true, Choices: []Choice{{Value: value, IsDefault: true, Capabilities: &caps}}}}
}

// Two requests that differ in anything the callback may read are two keys: the provider, the type, the authority, the scope, the
// resource id, the incarnation (a request with none claims none, which is not the empty one), and the connection. Two that differ in
// nothing but the order of the scope are one: the order does not change what a scope says.
func TestTheOptionsCacheKeyIsTheWholeIdentityOfTheRequest(t *testing.T) {
	route := resourcev2.AccessRoute{Connection: "conn-1"}
	base := optionsKeyOf(cacheRef(), route)
	for name, other := range map[string]optionsKey{
		"another provider":   optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.Provider = "aws" }), route),
		"another type":       optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.Type = "nodes" }), route),
		"another authority":  optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.Authority = "other" }), route),
		"another id":         optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.ID = "web-1" }), route),
		"another scope":      optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.Scope[0].Value = "kube-system" }), route),
		"another dimension":  optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.Scope[0].Dimension = "ns" }), route),
		"a scope entry less": optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.Scope = r.Address.Scope[:1] }), route),
		"an incarnation":     optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Incarnation = new("uid-1") }), route),
		"another connection": optionsKeyOf(cacheRef(), resourcev2.AccessRoute{Connection: "conn-2"}),
	} {
		if other == base {
			t.Errorf("%s has the key of the base request: the cache would answer one with the other's options", name)
		}
	}
	first := optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Incarnation = new("uid-1") }), route)
	for name, other := range map[string]optionsKey{
		"another incarnation":                           optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Incarnation = new("uid-2") }), route),
		"the empty incarnation (a claim, not no claim)": optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Incarnation = new("") }), route),
	} {
		if other == first {
			t.Errorf("%s has the key of incarnation uid-1", name)
		}
	}
	if optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Incarnation = new("") }), route) == base {
		t.Error("the empty incarnation has the key of a request that claims none")
	}
	reordered := optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) {
		r.Address.Scope[0], r.Address.Scope[1] = r.Address.Scope[1], r.Address.Scope[0]
	}), route)
	if reordered != base {
		t.Error("the same scope in another order is another key: the order of a scope does not change what it says")
	}
	if optionsKeyOf(cacheRef(), route) != base {
		t.Error("the same request has two keys")
	}
	// Fields that run together must not collide: "ab" and "c" against "a" and "bc".
	if optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.Provider, r.Address.Type = "ab", "c" }), route) ==
		optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.Provider, r.Address.Type = "a", "bc" }), route) {
		t.Error("two requests whose provider and type run together to the same text have one key")
	}
	// The key is a value that a map can hold, and the connection is what a retirement drops by.
	if base.connection != "conn-1" {
		t.Errorf("the key's connection is %q, want conn-1", base.connection)
	}
}

// An entry is the callback's answer for a time and no longer: valid for strictly less than the TTL, gone at it.
func TestAnOptionsCacheEntryLivesForItsTTLAndNoLonger(t *testing.T) {
	const ttl = 5 * time.Second
	var c optionsCache
	key := optionsKeyOf(cacheRef(), resourcev2.AccessRoute{Connection: "conn-1"})
	if _, ok := c.get(key, cacheEpoch, ttl); ok {
		t.Fatal("an empty cache answered")
	}
	c.put(key, cacheOptionsFor("app"), cacheEpoch, 8)
	for _, tc := range []struct {
		after time.Duration
		hit   bool
	}{{0, true}, {ttl - time.Nanosecond, true}, {ttl, false}, {ttl + time.Hour, false}} {
		options, ok := c.get(key, cacheEpoch.Add(tc.after), ttl)
		if ok != tc.hit {
			t.Errorf("%v after the put: hit %v, want %v", tc.after, ok, tc.hit)
		}
		if ok && (len(options) != 1 || options[0].Choices[0].Value != "app") {
			t.Errorf("%v after the put: the options are %+v", tc.after, options)
		}
	}
}

// The cache never holds more than its bound: the oldest entry goes to make room, and it is the oldest that goes, because it is the
// one that expires first.
func TestTheOptionsCacheIsBoundedAndEvictsTheOldest(t *testing.T) {
	const bound, puts = 4, 40
	var c optionsCache
	route := resourcev2.AccessRoute{Connection: "conn-1"}
	keys := make([]optionsKey, puts)
	for i := range puts {
		keys[i] = optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.ID = "web-" + strconv.Itoa(i) }), route)
		c.put(keys[i], cacheOptionsFor("app"), cacheEpoch.Add(time.Duration(i)*time.Millisecond), bound)
		if got := c.len(); got > bound {
			t.Fatalf("after %d puts the cache holds %d entries, its bound is %d", i+1, got, bound)
		}
	}
	if got := c.len(); got != bound {
		t.Errorf("the cache holds %d entries after %d distinct puts, want its bound %d", got, puts, bound)
	}
	now := cacheEpoch.Add(time.Second)
	for i, key := range keys {
		_, ok := c.get(key, now, time.Hour)
		if want := i >= puts-bound; ok != want {
			t.Errorf("entry %d: present %v, want %v: the oldest go first", i, ok, want)
		}
	}
}

// What has expired is dropped when the engine keeps a new answer, whether or not the cache is full: an entry that nobody can use must
// not hold the memory of its options until it is evicted by a bound that may never be reached.
func TestTheEngineSweepsWhatHasExpiredWhenItKeepsANewAnswer(t *testing.T) {
	const ttl = 5 * time.Second
	now := cacheEpoch
	e := newEngine(engineConfig{optionsCacheTTL: ttl, optionsCacheMax: 100})
	e.now = func() time.Time { return now }
	route := resourcev2.AccessRoute{Connection: "conn-1"}
	key := func(id string) optionsKey {
		return optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.ID = id }), route)
	}
	for i, id := range []string{"a", "b", "c"} {
		now = cacheEpoch.Add(time.Duration(i) * time.Second)
		e.cacheOptions(key(id), cacheOptionsFor(id))
	}
	if got := e.optionsCache.len(); got != 3 {
		t.Fatalf("the cache holds %d entries, want 3", got)
	}
	// a has expired at 5 s and b at 6 s; c lives until 7 s. Keeping an answer at 6 s sweeps a and b.
	now = cacheEpoch.Add(6 * time.Second)
	e.cacheOptions(key("d"), cacheOptionsFor("d"))
	if got := e.optionsCache.len(); got != 2 {
		t.Errorf("the cache holds %d entries after an answer was kept at 6 s, want 2 (c and d): a and b expired and nobody swept them", got)
	}
	if _, ok := e.cachedOptions(key("c")); !ok {
		t.Error("c, which has not expired, was swept")
	}
	if _, ok := e.cachedOptions(key("a")); ok {
		t.Error("a, which has expired, still answers")
	}
}

// Putting the result of a key again replaces it and makes it the newest: a refresh is a new answer, and the entry that was
// refreshed is not the next to go.
func TestRefreshingAnOptionsCacheEntryReplacesItAndMakesItTheNewest(t *testing.T) {
	var c optionsCache
	route := resourcev2.AccessRoute{Connection: "conn-1"}
	key := func(id string) optionsKey {
		return optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.ID = id }), route)
	}
	c.put(key("a"), cacheOptionsFor("old"), cacheEpoch, 3)
	c.put(key("b"), cacheOptionsFor("b"), cacheEpoch.Add(time.Millisecond), 3)
	c.put(key("c"), cacheOptionsFor("c"), cacheEpoch.Add(2*time.Millisecond), 3)
	c.put(key("a"), cacheOptionsFor("new"), cacheEpoch.Add(3*time.Millisecond), 3) // a refresh, not a fourth entry
	if got := c.len(); got != 3 {
		t.Fatalf("a refresh made the cache hold %d entries, want 3", got)
	}
	c.put(key("d"), cacheOptionsFor("d"), cacheEpoch.Add(4*time.Millisecond), 3) // evicts the oldest: b, and not the refreshed a
	now := cacheEpoch.Add(10 * time.Millisecond)
	if options, ok := c.get(key("a"), now, time.Hour); !ok || options[0].Choices[0].Value != "new" {
		t.Errorf("the refreshed entry: %+v (present %v), want the new options", options, ok)
	}
	if _, ok := c.get(key("b"), now, time.Hour); ok {
		t.Error("b survived: the refreshed entry was evicted in its place")
	}
}

// The cache keeps its own copy of what it is given, and hands out its own: an author's callback often returns one slice again and
// again, and may change it between calls, and a caller of the cache may change what it got.
func TestTheOptionsCacheKeepsItsOwnCopy(t *testing.T) {
	var c optionsCache
	key := optionsKeyOf(cacheRef(), resourcev2.AccessRoute{Connection: "conn-1"})
	given := cacheOptionsFor("app")
	c.put(key, given, cacheEpoch, 8)
	given[0].Name = "changed"
	given[0].Choices[0].Value = "changed"
	given[0].Choices[0].Capabilities.Signals[0] = SignalKill
	given[0].Choices = append(given[0].Choices, Choice{Value: "added"})

	got, ok := c.get(key, cacheEpoch, time.Hour)
	if !ok || got[0].Name != "container" || len(got[0].Choices) != 1 || got[0].Choices[0].Value != "app" ||
		got[0].Choices[0].Capabilities.Signals[0] != SignalInt {
		t.Fatalf("the cache returned %+v after the author changed what it gave: the cache holds the author's own slice", got)
	}
	got[0].Choices[0].Value = "mutated"
	got[0].Choices[0].Capabilities.Signals[0] = SignalTerm
	again, _ := c.get(key, cacheEpoch, time.Hour)
	if again[0].Choices[0].Value != "app" || again[0].Choices[0].Capabilities.Signals[0] != SignalInt {
		t.Errorf("the cache returned %+v after a caller changed what it got: two callers share one copy", again)
	}
}

// A connection that is retired takes its entries with it, and no other connection's.
func TestTheOptionsCacheDropsTheEntriesOfOneConnection(t *testing.T) {
	var c optionsCache
	a, b := resourcev2.AccessRoute{Connection: "conn-a"}, resourcev2.AccessRoute{Connection: "conn-b"}
	for i := range 3 {
		id := func(r *resourcev2.ResourceReference) { r.Address.ID = "web-" + strconv.Itoa(i) }
		c.put(optionsKeyOf(cacheRef(id), a), cacheOptionsFor("a"), cacheEpoch, 16)
		c.put(optionsKeyOf(cacheRef(id), b), cacheOptionsFor("b"), cacheEpoch, 16)
	}
	c.dropConnection("conn-a")
	if got := c.len(); got != 3 {
		t.Errorf("the cache holds %d entries after conn-a was dropped, want conn-b's 3", got)
	}
	for i := range 3 {
		id := func(r *resourcev2.ResourceReference) { r.Address.ID = "web-" + strconv.Itoa(i) }
		if _, ok := c.get(optionsKeyOf(cacheRef(id), a), cacheEpoch, time.Hour); ok {
			t.Errorf("an entry of the dropped connection answered for web-%d", i)
		}
		if _, ok := c.get(optionsKeyOf(cacheRef(id), b), cacheEpoch, time.Hour); !ok {
			t.Errorf("an entry of another connection was dropped for web-%d", i)
		}
	}
	c.dropConnection("conn-unknown") // no entry, no harm
}

// A cache of an engine that is configured with a TTL of zero or less is off: nothing is kept and nothing is read. The server
// builds its engine with the default of 5 s, and an in-process test turns it off with a negative TTL.
func TestAnEnginesOptionsCacheIsOffForATTLOfZeroOrLess(t *testing.T) {
	key := optionsKeyOf(cacheRef(), resourcev2.AccessRoute{Connection: "conn-1"})
	for _, ttl := range []time.Duration{0, -time.Second} {
		e := newEngine(engineConfig{optionsCacheTTL: ttl})
		e.cacheOptions(key, cacheOptionsFor("app"))
		if _, ok := e.cachedOptions(key); ok || e.optionsCache.len() != 0 {
			t.Errorf("ttl %v: the cache answered or kept a result, want it off", ttl)
		}
	}
	e := newEngine(engineConfig{optionsCacheTTL: time.Second})
	e.cacheOptions(key, cacheOptionsFor("app"))
	if _, ok := e.cachedOptions(key); !ok {
		t.Error("a cache with a TTL of one second kept nothing")
	}
}

// The engine of a plugin's server caches for 5 s by default and at most 64 results; an in-process test changes the TTL with
// exechook's config, where zero leaves the default and a negative value turns the cache off.
func TestTheServersEngineCachesOptionsFor5SecondsByDefault(t *testing.T) {
	if defaultOptionsCacheTTL != 5*time.Second || defaultOptionsCacheEntries != 64 {
		t.Errorf("the defaults are %v and %d entries, the contract says 5s and 64", defaultOptionsCacheTTL, defaultOptionsCacheEntries)
	}
	s := newServerV2(nil, nil, func() resourcev2.ConnectionContextProvider { return nil })
	if got := s.engine.cfg.optionsCacheTTL; got != defaultOptionsCacheTTL {
		t.Errorf("the server's engine caches for %v, want %v", got, defaultOptionsCacheTTL)
	}
	if got := s.engine.cfg.optionsCacheMax; got != defaultOptionsCacheEntries {
		t.Errorf("the server's engine keeps %d results, want %d", got, defaultOptionsCacheEntries)
	}
}

// Many goroutines read, fill, refresh and drop the same few keys: the cache holds its bound, what a reader gets is whole, and
// the race detector sees nothing.
func TestTheOptionsCacheIsSafeForConcurrentUse(t *testing.T) {
	const workers, rounds, bound = 16, 400, 5
	var c optionsCache
	routes := []resourcev2.AccessRoute{{Connection: "conn-a"}, {Connection: "conn-b"}}
	start := make(chan struct{})
	done := make(chan struct{}, workers)
	var mu sync.Mutex
	broken := ""
	for w := range workers {
		go func() {
			defer func() { done <- struct{}{} }()
			<-start
			for i := range rounds {
				key := optionsKeyOf(cacheRef(func(r *resourcev2.ResourceReference) { r.Address.ID = "web-" + strconv.Itoa((w+i)%9) }), routes[i%2])
				now := cacheEpoch.Add(time.Duration(i) * time.Millisecond)
				switch i % 4 {
				case 0:
					c.put(key, cacheOptionsFor("app"), now, bound)
				case 1, 2:
					if options, ok := c.get(key, now, 50*time.Millisecond); ok && (len(options) != 1 || options[0].Choices[0].Value != "app") {
						mu.Lock()
						broken = "a reader got a torn result"
						mu.Unlock()
					}
				default:
					if i%16 == 3 {
						c.dropConnection(routes[w%2].Connection)
					}
				}
				if got := c.len(); got > bound {
					mu.Lock()
					broken = "the cache held " + strconv.Itoa(got) + " entries, its bound is " + strconv.Itoa(bound)
					mu.Unlock()
				}
			}
		}()
	}
	close(start)
	for range workers {
		<-done
	}
	if broken != "" {
		t.Error(broken)
	}
	if got := c.len(); got > bound {
		t.Errorf("the cache holds %d entries, its bound is %d", got, bound)
	}
}
