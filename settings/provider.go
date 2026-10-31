// Package settings provides synchronized in-memory preferences and user-safe validation.
package settings

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"

	"go.uber.org/zap"
)

var (
	// ErrSettingNotFound reports an unknown setting within a category.
	ErrSettingNotFound = errors.New("setting not found")
	// ErrSettingTypeMismatch reports a value incompatible with the declared setting type.
	ErrSettingTypeMismatch = errors.New("setting type mismatch")
	// ErrSettingCategoryNotFound reports an unknown category.
	ErrSettingCategoryNotFound = errors.New("setting category not found")
	// ErrInvalidSettingID reports an ID without nonempty category and setting components.
	ErrInvalidSettingID = errors.New("invalid setting ID")
)

//nolint:gochecknoglobals // don't want to have to specify this in every function
var nilSetting = Setting{}

// Store groups settings by category ID, then by setting ID.
type Store map[string]Category

// Category is a group of settings. This is used to group settings together in the UI.
type Category struct {
	Settings    map[string]Setting `json:"settings"`
	ID          string             `json:"id"`
	Label       string             `json:"label"`
	Description string             `json:"description"`
	Icon        string             `json:"icon"`
}

// clone returns a deep copy of the Category with cloned Settings.
func (c Category) clone() Category {
	cp := c
	if c.Settings != nil {
		cp.Settings = make(map[string]Setting, len(c.Settings))
		for k := range c.Settings {
			v := c.Settings[k]
			cp.Settings[k] = v.clone()
		}
	}
	return cp
}

// CloneValue copies JSON maps/arrays and typed string, int, float64, and bool
// slices. Scalars are immutable. Unsupported arbitrary Go objects pass through.
func CloneValue(v any) any {
	switch val := v.(type) {
	case []string:
		cp := make([]string, len(val))
		copy(cp, val)
		return cp
	case []int:
		cp := make([]int, len(val))
		copy(cp, val)
		return cp
	case []float64:
		cp := make([]float64, len(val))
		copy(cp, val)
		return cp
	case []bool:
		cp := make([]bool, len(val))
		copy(cp, val)
		return cp
	case []any:
		cp := make([]any, len(val))
		for i, elem := range val {
			cp[i] = CloneValue(elem)
		}
		return cp
	case map[string]any:
		cp := make(map[string]any, len(val))
		for k, v := range val {
			cp[k] = CloneValue(v)
		}
		return cp
	default:
		return v // primitives are safe
	}
}

// ValidationError rejects a proposed transaction before persistence.
type ValidationError struct {
	Err error
}

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// PersistFunc durably saves normalized changes. The base Provider supplies resolved
// category.setting IDs, including plugin.name for an unscoped plugin setting name.
// Transport adapters may map these IDs to their public namespace.
// It may read the provider but must not mutate it.
type PersistFunc func(map[string]any) error

// CommitResult distinguishes durable storage from application.
type CommitResult struct {
	Committed bool `json:"committed"`
	Applied   bool `json:"applied"`
	Uncertain bool `json:"uncertain"`
}

// Provider manages the settings for the application as a pure in-memory store.
type Provider interface {
	// ListSettings returns the settings store
	ListSettings() Store

	// RegisterSetting registers a setting with the provider
	RegisterSetting(categoryID string, setting Setting) error

	// RegisterSettings registers a list of settings with the provider to a category
	RegisterSettings(categoryID string, settings ...Setting) error

	// GetSetting returns the setting by ID. This ID should be in the form of a dot separated string
	// that represents the path to the setting. For example, "appearance.theme"
	GetSetting(id string) (Setting, error)

	// GetSettingValue returns the value of the setting by ID
	GetSettingValue(id string) (any, error)

	// GetCategoryValues returns detached, unredacted values for trusted consumers.
	GetCategoryValues(category string) (map[string]any, error)

	// GetString returns the value of the setting by ID as a string.
	GetString(id string) (string, error)

	// GetStringSlice returns the value of the setting by ID as a string slice.
	GetStringSlice(id string) ([]string, error)

	// GetInt returns the value of the setting by ID as an int.
	GetInt(id string) (int, error)

	// GetIntSlice returns the value of the setting by ID as an int slice.
	GetIntSlice(id string) ([]int, error)

	// GetFloat returns the value of the setting by ID as a float64.
	GetFloat(id string) (float64, error)

	// GetFloatSlice returns the value of the setting by ID as a float64 slice.
	GetFloatSlice(id string) ([]float64, error)

	// GetBool returns the value of the setting by ID as a bool.
	GetBool(id string) (bool, error)

	// SetSetting sets the value of the setting by ID
	SetSetting(id string, value any) error

	// SetSettings sets multiple settings at once
	SetSettings(settings map[string]any) error

	// CommitSettings validates once, persists normalized changes, then publishes.
	// Validators and persist may read the provider but must not mutate it.
	CommitSettings(ctx context.Context, values map[string]any, persist PersistFunc) (CommitResult, error)

	// RegisterChangeHandler registers a callback that fires after settings in the
	// given category are saved. Only one handler per category.
	RegisterChangeHandler(categoryID string, fn CategoryChangeFunc)
}

// CategoryChangeFunc is called after settings in a category are saved.
// The map contains all current setting values for the category.
type CategoryChangeFunc func(values map[string]any)

// ProviderOpts are the options for creating a new settings provider.
type ProviderOpts struct {
	// Logger is the logger for the provider
	Logger *zap.SugaredLogger
	// PluginID is the ID of the plugin
	PluginID string
	// PluginSettings
	PluginSettings []Category
}

// NewProvider creates an independent in-memory store. A nil logger disables logging.
// Validators may read the provider. Change handlers may read and mutate it.
func NewProvider(opts ProviderOpts) Provider {
	p := &provider{
		logger:         opts.Logger,
		pluginID:       opts.PluginID,
		changeHandlers: make(map[string]CategoryChangeFunc),
	}
	if p.logger == nil {
		p.logger = zap.NewNop().Sugar()
	}
	if len(opts.PluginSettings) > 0 {
		p.mergeSettings(opts.PluginSettings...)
	}
	return p
}

type provider struct {
	pluginID         string
	logger           *zap.SugaredLogger
	writeMu          cancelableMutex
	storeMu          sync.RWMutex
	store            Store
	changeHandlersMu sync.RWMutex
	changeHandlers   map[string]CategoryChangeFunc
}

// cancelableMutex serializes mutations without retaining canceled queued callers.
// Its zero value is ready for use, including providers built directly in tests.
type cancelableMutex struct {
	once sync.Once
	gate chan struct{}
}

func (m *cancelableMutex) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.once.Do(func() { m.gate = make(chan struct{}, 1) })
	select {
	case m.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *cancelableMutex) Lock()   { _ = m.lock(context.Background()) }
func (m *cancelableMutex) Unlock() { <-m.gate }

// define custom merge behavior to make sure we don't overwrite any existing settings,
// but update the values of any settings that already exist
//
// TODO - right now, if the new setting has a different type than the existing setting,
// the new setting will overwrite the existing setting. We should probably throw an error
// if this happens.
func (p *provider) mergeSettings(categories ...Category) {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.storeMu.Lock()
	defer p.storeMu.Unlock()
	p.mergeSettingsLocked(categories...)
}

func (p *provider) mergeSettingsLocked(categories ...Category) {
	if p.store == nil {
		p.store = make(Store)
	}
	for _, category := range categories {
		currentCategory, ok := p.store[category.ID]
		if !ok {
			// we don't have this category, go ahead and full assign it and set the settings
			// to their defaults
			newSettings := make(map[string]Setting, len(category.Settings))
			for key := range category.Settings {
				setting := category.Settings[key]
				setting.Value = setting.Default
				newSettings[setting.ID] = setting.clone()
			}
			category.Settings = newSettings
			p.store[category.ID] = category
			continue
		}

		// if we've gotten here, the category exists
		// lets update the category info first
		currentCategory.Label = category.Label
		currentCategory.Description = category.Description
		currentCategory.Icon = category.Icon

		for id := range category.Settings {
			setting := category.Settings[id]
			current, ok := currentCategory.Settings[id]
			if !ok {
				// we don't have this setting, go ahead and full assign it with a default
				// and move on
				setting.Value = setting.Default
				currentCategory.Settings[id] = setting.clone()
				continue
			}

			p.mergeSettingMetadata(id, &current, &setting)
			currentCategory.Settings[id] = current
		}

		p.store[category.ID] = currentCategory
	}
}

// mergeSettingMetadata preserves existing values and rejects incompatible metadata.
func (p *provider) mergeSettingMetadata(id string, current, incoming *Setting) {
	if incoming.Type != current.Type {
		p.logger.Errorf("setting type mismatch: %s. currently has %q, tried to assign %q",
			id, current.Type, incoming.Type)
		return
	}
	toCheck := incoming.Value
	if toCheck == nil {
		toCheck = incoming.Default
	}
	if reflect.TypeOf(current.Value) != reflect.TypeOf(toCheck) {
		p.logger.Errorf("setting value mismatch: %s. currently has %s, tried to assign %s",
			id, reflect.TypeOf(current.Value), reflect.TypeOf(toCheck))
		return
	}
	current.Label = incoming.Label
	current.Description = incoming.Description
	current.Default = CloneValue(incoming.Default)
	current.Validator = incoming.Validator
	current.Options = incoming.clone().Options
	current.RequiresRestart = incoming.RequiresRestart
}

func (p *provider) ListSettings() Store {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	clone := make(Store, len(p.store))
	for id, cat := range p.store {
		clone[id] = cat.clone()
	}
	return clone
}

// Values returns all of the values in the store as a map.
// Available on the concrete *provider via type assertion.
func (p *provider) Values() map[string]any {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	return p.valuesLocked()
}

func (p *provider) valuesLocked() map[string]any {
	m := make(map[string]any, len(p.store))
	for categoryID, category := range p.store {
		for settingID := range category.Settings {
			m[fmt.Sprintf("%s.%s", categoryID, settingID)] = CloneValue(category.Settings[settingID].Value)
		}
	}

	return m
}

func (p *provider) GetSetting(id string) (Setting, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	return p.getSettingLocked(id)
}

func (p *provider) getSettingLocked(id string) (Setting, error) {
	category, id, err := p.parseSettingIDLocked(id)
	if err != nil {
		return nilSetting, err
	}
	setting, ok := p.store[category].Settings[id]
	if !ok {
		return nilSetting, ErrSettingNotFound
	}
	return setting.clone(), nil
}

func (p *provider) GetSettingValue(id string) (any, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	setting, err := p.getSettingLocked(id)
	if err != nil {
		return nil, err
	}
	return setting.Value, nil
}

func (p *provider) SetSettings(values map[string]any) error {
	_, err := p.CommitSettings(context.Background(), values, nil)
	return err
}

func (p *provider) SetSetting(id string, value any) error {
	return p.SetSettings(map[string]any{id: value})
}

func (p *provider) CommitSettings(
	ctx context.Context, values map[string]any, persist PersistFunc,
) (CommitResult, error) {
	if err := p.writeMu.lock(ctx); err != nil {
		return CommitResult{}, err
	}
	result, changed, err := p.commitSettings(ctx, values, persist)
	p.writeMu.Unlock()
	if err == nil {
		p.notifyChangeHandlers(changed)
	}
	return result, err
}

// commitSettings runs under writeMu, leaving storeMu free during callbacks.
func (p *provider) commitSettings(
	ctx context.Context, values map[string]any, persist PersistFunc,
) (CommitResult, map[string]struct{}, error) {
	type preparedSetting struct {
		id       string
		category string
		key      string
		setting  Setting
	}
	entries := make([]preparedSetting, 0, len(values))
	p.storeMu.RLock()
	for id := range values {
		setting, err := p.getSettingLocked(id)
		if err != nil {
			p.storeMu.RUnlock()
			return CommitResult{}, nil, &ValidationError{Err: err}
		}
		category, key, err := p.parseSettingIDLocked(id)
		if err != nil {
			p.storeMu.RUnlock()
			return CommitResult{}, nil, &ValidationError{Err: err}
		}
		entries = append(entries, preparedSetting{id, category, key, setting})
	}
	p.storeMu.RUnlock()
	normalized := make(map[string]any, len(entries))
	for i := range entries {
		entry := &entries[i]
		if err := entry.setting.SetValue(values[entry.id]); err != nil {
			return CommitResult{}, nil, &ValidationError{Err: err}
		}
		normalized[entry.category+"."+entry.key] = CloneValue(entry.setting.Value)
	}
	if err := ctx.Err(); err != nil {
		return CommitResult{}, nil, err
	}
	if persist != nil {
		if err := persist(normalized); err != nil {
			return CommitResult{}, nil, err
		}
	}
	changed := make(map[string]struct{}, len(entries))
	p.storeMu.Lock()
	for i := range entries {
		entry := &entries[i]
		p.store[entry.category].Settings[entry.key] = entry.setting
		changed[entry.category] = struct{}{}
	}
	p.storeMu.Unlock()
	return CommitResult{Committed: true, Applied: true}, changed, nil
}

// ResetSetting resets the value of the setting by ID to the default value.
// Available on the concrete *provider via type assertion.
func (p *provider) ResetSetting(id string) error {
	p.writeMu.Lock()
	p.storeMu.Lock()
	category, settingKey, err := p.parseSettingIDLocked(id)
	if err != nil {
		p.storeMu.Unlock()
		p.writeMu.Unlock()
		return err
	}

	setting, ok := p.store[category].Settings[settingKey]
	if !ok {
		p.storeMu.Unlock()
		p.writeMu.Unlock()
		return ErrSettingNotFound
	}
	setting.ResetValue()
	p.store[category].Settings[settingKey] = setting
	p.storeMu.Unlock()
	p.writeMu.Unlock()

	p.notifyChangeHandlers(map[string]struct{}{category: {}})
	return nil
}

func (p *provider) HasSetting(id string) bool {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	_, err := p.getSettingLocked(id)
	return err == nil
}

func (p *provider) RegisterSetting(category string, setting Setting) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.storeMu.Lock()
	defer p.storeMu.Unlock()
	if p.store == nil {
		p.store = make(Store)
	}
	found, ok := p.store[category]
	if !ok {
		found = Category{ID: category, Settings: make(map[string]Setting)}
	}
	if found.Settings == nil {
		found.Settings = make(map[string]Setting)
	}
	if existing, exists := found.Settings[setting.ID]; !exists {
		setting.Value = setting.Default
	} else if existing.Type != setting.Type {
		p.logger.Warnw("setting type changed on re-register, resetting to new default",
			"id", setting.ID, "oldType", existing.Type, "newType", setting.Type)
		setting.Value = setting.Default
	} else {
		setting.Value = existing.Value
	}
	found.Settings[setting.ID] = setting.clone()
	p.store[category] = found
	return nil
}

func (p *provider) RegisterSettings(category string, settings ...Setting) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.storeMu.Lock()
	defer p.storeMu.Unlock()
	if p.store == nil {
		p.store = make(Store)
	}
	found, ok := p.store[category]
	if !ok {
		found = Category{ID: category, Settings: make(map[string]Setting)}
	}
	if found.Settings == nil {
		found.Settings = make(map[string]Setting)
	}
	for i := range settings {
		setting := settings[i]
		if existing, exists := found.Settings[setting.ID]; !exists {
			setting.Value = setting.Default
		} else if existing.Type != setting.Type {
			p.logger.Warnw("setting type changed on re-register, resetting to new default",
				"id", setting.ID, "oldType", existing.Type, "newType", setting.Type)
			setting.Value = setting.Default
		} else {
			setting.Value = existing.Value
		}
		found.Settings[setting.ID] = setting.clone()
	}
	p.store[category] = found
	return nil
}

func (p *provider) RegisterChangeHandler(categoryID string, fn CategoryChangeFunc) {
	p.changeHandlersMu.Lock()
	defer p.changeHandlersMu.Unlock()
	p.changeHandlers[categoryID] = fn
}

func (p *provider) notifyChangeHandlers(changedCategories map[string]struct{}) {
	p.changeHandlersMu.RLock()
	// Snapshot the handlers to release the lock before calling them.
	handlers := make(map[string]CategoryChangeFunc, len(p.changeHandlers))
	maps.Copy(handlers, p.changeHandlers)
	p.changeHandlersMu.RUnlock()

	for cat := range changedCategories {
		fn, ok := handlers[cat]
		if !ok {
			continue
		}
		p.storeMu.RLock()
		vals, err := p.getCategoryValuesLocked(cat)
		p.storeMu.RUnlock()
		if err != nil {
			p.logger.Warnw("failed to get category values for change handler", "category", cat, "error", err)
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					p.logger.Warnw("change handler panicked", "category", cat, "recovered", r)
				}
			}()
			fn(vals)
		}()
	}
}

// GetCategories returns a list of all categories, with the settings removed.
// Available on the concrete *provider via type assertion.
func (p *provider) GetCategories() []Category {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	categories := make([]Category, 0, len(p.store))
	for category := range p.store {
		// copy and remove the settings so we don't expose them
		copied := p.store[category]
		copied.Settings = nil
		categories = append(categories, copied)
	}
	return categories
}

// GetCategory returns the category by ID.
// Available on the concrete *provider via type assertion.
func (p *provider) GetCategory(category string) (Category, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	return p.getCategoryLocked(category)
}

func (p *provider) getCategoryLocked(category string) (Category, error) {
	cat, ok := p.store[category]
	if !ok {
		return Category{}, ErrSettingCategoryNotFound
	}
	return cat.clone(), nil
}

// GetCategoryValues returns a map of the values of the settings by category.
// Available on the concrete *provider via type assertion.
func (p *provider) GetCategoryValues(category string) (map[string]any, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	return p.getCategoryValuesLocked(category)
}

// getCategoryValuesLocked is the private implementation. Caller must hold storeMu (read or write).
func (p *provider) getCategoryValuesLocked(category string) (map[string]any, error) {
	cat, err := p.getCategoryLocked(category)
	if err != nil {
		return nil, err
	}

	values := make(map[string]any, len(cat.Settings))
	for id := range cat.Settings {
		values[id] = cat.Settings[id].Value
	}
	return values, nil
}

// parseSettingIDLocked does not access the store, but is marked Locked for consistency
// since callers that hold the lock need a non-locking variant.
func (p *provider) parseSettingIDLocked(id string) (string, string, error) {
	// if we have a pluginID on the provider, the category will always be "plugin"
	if p.pluginID != "" {
		return "plugin", id, nil
	}

	parts := strings.Split(id, ".")
	const settingIDComponents = 2 // category.setting; additional dots remain invalid.
	if len(parts) != settingIDComponents {
		return "", "", ErrInvalidSettingID
	}
	if parts[0] == "" || parts[1] == "" {
		return "", "", ErrInvalidSettingID
	}
	return parts[0], parts[1], nil
}

// ============================================= UTILS ============================================= //

// GetString returns the value of the setting by ID as a string.
func (p *provider) GetString(id string) (string, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	setting, err := p.getSettingLocked(id)
	if err != nil {
		return "", err
	}
	if setting.Type != Text && setting.Type != Select {
		return "", ErrSettingTypeMismatch
	}
	val, ok := setting.Value.(string)
	if !ok {
		return "", ErrSettingTypeMismatch
	}
	return val, nil
}

// GetStringSlice returns the value of the setting by ID as a string slice.
func (p *provider) GetStringSlice(id string) ([]string, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	setting, err := p.getSettingLocked(id)
	if err != nil {
		return nil, err
	}
	if setting.Type != Text && setting.Type != MultiSelect {
		return nil, ErrSettingTypeMismatch
	}

	var strs []string
	switch v := setting.Value.(type) {
	case []string:
		return v, nil
	case []any:
		for _, item := range v {
			if str, valOk := item.(string); valOk {
				strs = append(strs, str)
			} else {
				return nil, errors.New("expected []string, but item is not a string")
			}
		}
		return strs, nil
	default:
		return nil, fmt.Errorf("expected []string or []interface{}, got %T", setting.Value)
	}
}

// GetInt returns the value of the setting by ID as an int.
func (p *provider) GetInt(id string) (int, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	setting, err := p.getSettingLocked(id)
	if err != nil {
		return 0, err
	}
	if setting.Type != Integer {
		return 0, ErrSettingTypeMismatch
	}
	val, ok := setting.Value.(int)
	if !ok {
		return 0, ErrSettingTypeMismatch
	}
	return val, nil
}

// GetIntSlice returns the value of the setting by ID as an int slice.
func (p *provider) GetIntSlice(id string) ([]int, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	setting, err := p.getSettingLocked(id)
	if err != nil {
		return nil, err
	}
	if setting.Type != Integer {
		return nil, ErrSettingTypeMismatch
	}

	if values, ok := setting.Value.([]int); ok {
		return values, nil
	}
	var vals []int
	if slice, ok := setting.Value.([]any); ok {
		for _, item := range slice {
			switch item := item.(type) {
			case int:
				vals = append(vals, item)
			case int32:
				vals = append(vals, int(item))
			case int64:
				vals = append(vals, int(item))
			case uint:
				vals = append(vals, int(item))
			case uint32:
				vals = append(vals, int(item))
			case uint64:
				const maxInt = int(^uint(0) >> 1)
				if item > uint64(maxInt) {
					return nil, ErrSettingTypeMismatch
				}
				vals = append(vals, int(item))
			default:
				return nil, errors.New("expected []int, but item is not an int")
			}
		}
	} else {
		return nil, fmt.Errorf("expected []int, got %T", setting.Value)
	}

	return vals, nil
}

// GetFloat returns the value of the setting by ID as a float64.
func (p *provider) GetFloat(id string) (float64, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	setting, err := p.getSettingLocked(id)
	if err != nil {
		return 0, err
	}
	if setting.Type != Float {
		return 0, ErrSettingTypeMismatch
	}
	val, ok := setting.Value.(float64)
	if !ok {
		return 0, ErrSettingTypeMismatch
	}
	return val, nil
}

// GetFloatSlice returns the value of the setting by ID as a float64 slice.
func (p *provider) GetFloatSlice(id string) ([]float64, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	setting, err := p.getSettingLocked(id)
	if err != nil {
		return nil, err
	}
	if setting.Type != Float {
		return nil, ErrSettingTypeMismatch
	}

	if values, ok := setting.Value.([]float64); ok {
		return values, nil
	}
	var vals []float64
	if slice, ok := setting.Value.([]any); ok {
		for _, item := range slice {
			switch item := item.(type) {
			case float64:
				vals = append(vals, item)
			case float32:
				vals = append(vals, float64(item))
			default:
				return nil, errors.New("expected []float64, but item is not a float")
			}
		}
	} else {
		return nil, fmt.Errorf("expected []float64, got %T", setting.Value)
	}

	return vals, nil
}

// GetBool returns the value of the setting by ID as a bool.
func (p *provider) GetBool(id string) (bool, error) {
	p.storeMu.RLock()
	defer p.storeMu.RUnlock()
	setting, err := p.getSettingLocked(id)
	if err != nil {
		return false, err
	}
	if setting.Type != Toggle {
		return false, ErrSettingTypeMismatch
	}
	val, ok := setting.Value.(bool)
	if !ok {
		return false, ErrSettingTypeMismatch
	}
	return val, nil
}
