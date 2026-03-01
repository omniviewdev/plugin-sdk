package resource

import (
	"fmt"
	"strings"
	"sync"
)

// resourcerEntry holds a registered resource type and its associated data.
type resourcerEntry[ClientT any] struct {
	meta       ResourceMeta
	resourcer  Resourcer[ClientT]
	definition *ResourceDefinition
}

// resourcerRegistry manages registered Resourcer implementations and pattern fallbacks.
// Thread-safe for concurrent reads; writes happen only at registration time.
type resourcerRegistry[ClientT any] struct {
	mu         sync.RWMutex
	entries    map[string]*resourcerEntry[ClientT]
	patterns   map[string]Resourcer[ClientT]
	defaultDef ResourceDefinition
}

func newResourcerRegistry[ClientT any](defaultDef ResourceDefinition) *resourcerRegistry[ClientT] {
	return &resourcerRegistry[ClientT]{
		entries:    make(map[string]*resourcerEntry[ClientT]),
		patterns:   make(map[string]Resourcer[ClientT]),
		defaultDef: defaultDef,
	}
}

// Register adds a resource type to the registry. Duplicate keys overwrite.
func (r *resourcerRegistry[ClientT]) Register(reg ResourceRegistration[ClientT]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[reg.Meta.Key()] = &resourcerEntry[ClientT]{
		meta:       reg.Meta,
		resourcer:  reg.Resourcer,
		definition: reg.Definition,
	}
}

// RegisterPattern adds a pattern fallback resourcer.
func (r *resourcerRegistry[ClientT]) RegisterPattern(pattern string, res Resourcer[ClientT]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.patterns[pattern] = res
}

// Lookup returns the Resourcer for a given key.
// Checks exact matches first, then falls back to patterns (most specific wins).
func (r *resourcerRegistry[ClientT]) Lookup(key string) (Resourcer[ClientT], error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if entry, ok := r.entries[key]; ok {
		return entry.resourcer, nil
	}
	if res := r.matchPattern(key); res != nil {
		return res, nil
	}
	return nil, fmt.Errorf("resource %q not registered", key)
}

// LookupMeta returns the ResourceMeta for a given key.
func (r *resourcerRegistry[ClientT]) LookupMeta(key string) (ResourceMeta, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if entry, ok := r.entries[key]; ok {
		return entry.meta, true
	}
	return ResourceMeta{}, false
}

// IsWatcher returns true if the resourcer for the given key implements Watcher[ClientT].
func (r *resourcerRegistry[ClientT]) IsWatcher(key string) bool {
	_, ok := r.GetWatcher(key)
	return ok
}

// GetWatcher returns the Watcher interface if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetWatcher(key string) (Watcher[ClientT], bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	w, ok := res.(Watcher[ClientT])
	return w, ok
}

// GetSyncPolicy returns the sync policy for a resource type.
// If the Resourcer implements SyncPolicyDeclarer, returns its declared policy.
// If the Resourcer is a Watcher but not a SyncPolicyDeclarer, returns SyncOnConnect.
// If the Resourcer is not a Watcher, returns SyncNever.
func (r *resourcerRegistry[ClientT]) GetSyncPolicy(key string) SyncPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return SyncNever
	}
	// Must be a Watcher to have any sync policy.
	if _, ok := res.(Watcher[ClientT]); !ok {
		return SyncNever
	}
	if sp, ok := res.(SyncPolicyDeclarer); ok {
		return sp.SyncPolicy()
	}
	return SyncOnConnect
}

// GetDefinition returns the ResourceDefinition for a resource type.
// Precedence: DefinitionProvider on Resourcer > Registration.Definition > DefaultDefinition.
func (r *resourcerRegistry[ClientT]) GetDefinition(key string) ResourceDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.entries[key]
	if !ok {
		return r.defaultDef
	}
	// Check if resourcer implements DefinitionProvider.
	if dp, ok := entry.resourcer.(DefinitionProvider); ok {
		return dp.Definition()
	}
	// Fallback to registration-level definition.
	if entry.definition != nil {
		return *entry.definition
	}
	return r.defaultDef
}

// GetActionResourcer returns the ActionResourcer interface if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetActionResourcer(key string) (ActionResourcer[ClientT], bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	ar, ok := res.(ActionResourcer[ClientT])
	return ar, ok
}

// GetErrorClassifier returns the ErrorClassifier if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetErrorClassifier(key string) (ErrorClassifier, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	ec, ok := res.(ErrorClassifier)
	return ec, ok
}

// GetSchemaResourcer returns the ResourceSchemaProvider if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetSchemaResourcer(key string) (ResourceSchemaProvider[ClientT], bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	sp, ok := res.(ResourceSchemaProvider[ClientT])
	return sp, ok
}

// GetFilterableProvider returns the FilterableProvider if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetFilterableProvider(key string) (FilterableProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	fp, ok := res.(FilterableProvider)
	return fp, ok
}

// GetTextSearchProvider returns the TextSearchProvider if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetTextSearchProvider(key string) (TextSearchProvider[ClientT], bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	sp, ok := res.(TextSearchProvider[ClientT])
	return sp, ok
}

// GetScaleHintProvider returns the ScaleHintProvider if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetScaleHintProvider(key string) (ScaleHintProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	shp, ok := res.(ScaleHintProvider)
	return shp, ok
}

// ListAll returns all registered ResourceMeta.
func (r *resourcerRegistry[ClientT]) ListAll() []ResourceMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()

	metas := make([]ResourceMeta, 0, len(r.entries))
	for _, entry := range r.entries {
		metas = append(metas, entry.meta)
	}
	return metas
}

// ListWatchable returns ResourceMeta only for Watcher-capable resources.
func (r *resourcerRegistry[ClientT]) ListWatchable() []ResourceMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var metas []ResourceMeta
	for _, entry := range r.entries {
		if _, ok := entry.resourcer.(Watcher[ClientT]); ok {
			metas = append(metas, entry.meta)
		}
	}
	return metas
}

// DeriveCapabilities auto-derives ResourceCapabilities from type assertions.
func (r *resourcerRegistry[ClientT]) DeriveCapabilities(key string) *ResourceCapabilities {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil
	}

	caps := &ResourceCapabilities{
		// All Resourcers implement CRUD by default.
		CanGet:    true,
		CanList:   true,
		CanFind:   true,
		CanCreate: true,
		CanUpdate: true,
		CanDelete: true,
	}

	if _, ok := res.(Watcher[ClientT]); ok {
		caps.Watchable = true
	}
	if _, ok := res.(FilterableProvider); ok {
		caps.Filterable = true
	}
	if _, ok := res.(TextSearchProvider[ClientT]); ok {
		caps.Searchable = true
	}
	if _, ok := res.(ActionResourcer[ClientT]); ok {
		caps.HasActions = true
	}
	if _, ok := res.(ResourceSchemaProvider[ClientT]); ok {
		caps.HasSchema = true
	}
	if shp, ok := res.(ScaleHintProvider); ok {
		caps.Scale = shp.ScaleHint()
	}
	if _, ok := res.(RelationshipDeclarer); ok {
		caps.HasRelationships = true
	}
	if _, ok := res.(HealthAssessor[ClientT]); ok {
		caps.HasHealth = true
	}
	if _, ok := res.(EventRetriever[ClientT]); ok {
		caps.HasEvents = true
	}
	return caps
}

// GetRelationshipDeclarer returns the RelationshipDeclarer if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetRelationshipDeclarer(key string) (RelationshipDeclarer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	rd, ok := res.(RelationshipDeclarer)
	return rd, ok
}

// GetRelationshipResolver returns the RelationshipResolver if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetRelationshipResolver(key string) (RelationshipResolver[ClientT], bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	rr, ok := res.(RelationshipResolver[ClientT])
	return rr, ok
}

// GetHealthAssessor returns the HealthAssessor if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetHealthAssessor(key string) (HealthAssessor[ClientT], bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	ha, ok := res.(HealthAssessor[ClientT])
	return ha, ok
}

// GetEventRetriever returns the EventRetriever if the resourcer implements it.
func (r *resourcerRegistry[ClientT]) GetEventRetriever(key string) (EventRetriever[ClientT], bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := r.lookupResourcer(key)
	if res == nil {
		return nil, false
	}
	er, ok := res.(EventRetriever[ClientT])
	return er, ok
}

// lookupResourcer returns the raw Resourcer for a key (including pattern fallback).
// Must be called with r.mu held.
func (r *resourcerRegistry[ClientT]) lookupResourcer(key string) Resourcer[ClientT] {
	if entry, ok := r.entries[key]; ok {
		return entry.resourcer
	}
	return r.matchPattern(key)
}

// matchPattern finds the most specific pattern match for a key.
// Must be called with r.mu held.
func (r *resourcerRegistry[ClientT]) matchPattern(key string) Resourcer[ClientT] {
	var (
		bestRes   Resourcer[ClientT]
		bestScore = -1
	)
	for pattern, res := range r.patterns {
		if score := patternMatchScore(pattern, key); score > bestScore {
			bestScore = score
			bestRes = res
		}
	}
	return bestRes
}

// patternMatchScore returns the specificity score of a pattern match.
// Returns -1 if the pattern does not match the key.
// Higher score = more specific match (more non-wildcard segments).
func patternMatchScore(pattern, key string) int {
	if pattern == "*" {
		return 0
	}
	patternParts := strings.Split(pattern, "::")
	keyParts := strings.Split(key, "::")
	if len(patternParts) != len(keyParts) {
		return -1
	}
	score := 0
	for i, pp := range patternParts {
		if pp == "*" {
			continue
		}
		if pp != keyParts[i] {
			return -1
		}
		score++
	}
	return score
}
