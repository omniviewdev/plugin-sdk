package logs

import (
	"log"
	"maps"
	"slices"
	"strings"
)

// handlerRegistry provides O(1) handler and resolver lookup.
// Built once at Manager construction from the handlers map.
type handlerRegistry struct {
	// handlers maps full key "plugin/resource" → Handler
	handlers map[string]Handler

	// index maps resource key → full key for O(1) lookup
	index map[string]string

	// resolvers maps resource key → SourceResolver
	resolvers map[string]SourceResolver
}

func newHandlerRegistry(handlers map[string]Handler, resolvers map[string]SourceResolver) *handlerRegistry {
	r := &handlerRegistry{
		handlers:  make(map[string]Handler, len(handlers)),
		index:     make(map[string]string, len(handlers)),
		resolvers: make(map[string]SourceResolver, len(resolvers)),
	}

	for fullKey, h := range handlers {
		r.handlers[fullKey] = h
		// Extract resource key from "plugin/resource" format.
		// When multiple plugins register the same resource suffix,
		// choose the lexicographically smallest fullKey for determinism.
		if parts := strings.SplitN(fullKey, "/", 2); len(parts) == 2 {
			existing, ok := r.index[parts[1]]
			if !ok || fullKey < existing {
				if ok {
					log.Printf("[handler-registry] resource %q: %q shadows %q", parts[1], fullKey, existing)
				}
				r.index[parts[1]] = fullKey
			}
		}
	}

	for key, resolver := range resolvers {
		r.resolvers[key] = resolver
	}

	return r
}

// FindHandler returns the handler for the given resource key, if any.
func (r *handlerRegistry) FindHandler(resourceKey string) (Handler, bool) {
	fullKey, ok := r.index[resourceKey]
	if !ok {
		return Handler{}, false
	}
	h, ok := r.handlers[fullKey]
	return h, ok
}

// FindResolver returns the source resolver for the given resource key, if any.
func (r *handlerRegistry) FindResolver(resourceKey string) (SourceResolver, bool) {
	resolver, ok := r.resolvers[resourceKey]
	return resolver, ok
}

// AllHandlers returns all registered handlers in deterministic (sorted key) order.
func (r *handlerRegistry) AllHandlers() []Handler {
	keys := slices.Sorted(maps.Keys(r.handlers))
	result := make([]Handler, 0, len(keys))
	for _, k := range keys {
		result = append(result, r.handlers[k])
	}
	return result
}

// AnyHandler returns the handler with the lexicographically smallest key,
// or false if the registry is empty.
// Used when a resolver's sources need a handler but no direct mapping exists.
func (r *handlerRegistry) AnyHandler() (Handler, bool) {
	keys := slices.Sorted(maps.Keys(r.handlers))
	if len(keys) == 0 {
		return Handler{}, false
	}
	return r.handlers[keys[0]], true
}
