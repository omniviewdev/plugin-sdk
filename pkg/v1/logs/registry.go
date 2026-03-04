package logs

import "strings"

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
		// Extract resource key from "plugin/resource" format
		if parts := strings.SplitN(fullKey, "/", 2); len(parts) == 2 {
			r.index[parts[1]] = fullKey
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

// AllHandlers returns all registered handlers.
func (r *handlerRegistry) AllHandlers() []Handler {
	result := make([]Handler, 0, len(r.handlers))
	for _, h := range r.handlers {
		result = append(result, h)
	}
	return result
}

// AnyHandler returns an arbitrary handler from the registry, or false if empty.
// Used when a resolver's sources need a handler but no direct mapping exists.
func (r *handlerRegistry) AnyHandler() (Handler, bool) {
	for _, h := range r.handlers {
		return h, true
	}
	return Handler{}, false
}
