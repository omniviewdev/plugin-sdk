package resourcev2

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"
)

// Connection metadata limits bound both admission and complete published snapshots.
const (
	MaxConnectionMetadataKeyBytes          = 256
	MaxConnectionMetadataEntries           = 128
	MaxConnectionMetadataEntryBytes        = 16 << 10
	MaxConnectionMetadataDepth             = 16
	MaxConnectionMetadataSnapshotBytes     = 256 << 10
	MaxConnectionMetadataErrorCodeBytes    = 128
	MaxConnectionMetadataErrorMessageBytes = 1024
	// Successful snapshots reserve space for SDK lifecycle failure publication.
	ConnectionMetadataLifecycleReserveBytes = 512
	metadataMaxSafeInteger                  = 9007199254740991
)

// ConnectionMetadataProvider exposes the SDK's authoritative, in-memory facts.
// GetConnectionMetadata never acquires facts. RefreshConnectionMetadata acquires
// only for an already active connection and never implicitly connects. Concurrent
// refresh callers share acquisition; cancellation detaches only that caller.
// WatchConnectionMetadata emits complete ordered snapshots and removal tombstones.
// Subscribe before fetching, and compare decimal revisions numerically within one
// provider registration. A generation is opaque and must never be sorted.
// Calls may overlap. Honor cancellation, return stable detached containers, and
// stop a watch when its sink returns an error. Public facts and errors must not
// contain credentials, private configuration, or raw native diagnostics. Remote
// callers must negotiate provider support rather than inspect static methods.
type ConnectionMetadataProvider interface {
	GetConnectionMetadata(context.Context, string) (ConnectionMetadataSnapshot, error)
	RefreshConnectionMetadata(context.Context, string) (ConnectionMetadataSnapshot, error)
	WatchConnectionMetadata(context.Context, ConnectionMetadataSink) error
}

// ConnectionMetadataSink receives one ordered event. An error terminates its watch.
type ConnectionMetadataSink func(ConnectionMetadataEvent) error

// ConnectionMetadataEvent carries a complete snapshot, or an ordered tombstone.
// Removed events retain connection identity and revision but contain no facts.
type ConnectionMetadataEvent struct {
	Snapshot ConnectionMetadataSnapshot `json:"snapshot"`
	Removed  bool                       `json:"removed"`
}

// ConnectionMetadataSnapshot is the authoritative state for one connection.
// An inactive inventory entry may have an empty generation. Revisions are positive
// canonical unsigned decimal strings and continue across generation changes.
type ConnectionMetadataSnapshot struct {
	ConnectionID     string                             `json:"connectionID"`
	Generation       string                             `json:"generation"`
	Revision         string                             `json:"revision"`
	Active           bool                               `json:"active"`
	Refreshing       bool                               `json:"refreshing"`
	LastRefreshError *ConnectionMetadataError           `json:"lastRefreshError,omitempty"`
	Entries          map[string]ConnectionMetadataEntry `json:"entries"`
}

// ConnectionMetadataError contains only bounded, public-safe classification and text.
// Providers must sanitize these fields before returning observations.
type ConnectionMetadataError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ConnectionMetadataEntry is current, stale, or unavailable. Current and stale
// entries always serialize value, including null, false, zero, and empty strings.
// Unavailable entries omit value and observedAt and require a public-safe error.
type ConnectionMetadataEntry struct {
	State      string                   `json:"state"`
	Value      any                      `json:"value"`
	ObservedAt *time.Time               `json:"observedAt,omitempty"`
	Error      *ConnectionMetadataError `json:"error,omitempty"`
}

// ConnectionMetadataObservation reports success or unavailable, not lifecycle state.
// A success requires a nonzero UTC observation time and a JSON value. Unavailable
// requires an error and has neither a value nor an observation time.
type ConnectionMetadataObservation struct {
	State      string                   `json:"state"`
	Value      any                      `json:"value"`
	ObservedAt time.Time                `json:"observedAt"`
	Error      *ConnectionMetadataError `json:"error,omitempty"`
}

// ConnectionMetadataUpdate is an atomic patch. Omitted keys remain unchanged.
// Remove cannot contain duplicates or overlap Set. JSON values accept native
// finite scalars, json.Number, string-keyed maps, and arrays/slices (not []byte).
// Structs, pointers, custom JSON encoders, and non-JavaScript-safe numbers are
// rejected. Providers must keep returned data stable until the SDK detaches it.
type ConnectionMetadataUpdate struct {
	Set    map[string]ConnectionMetadataObservation `json:"set"`
	Remove []string                                 `json:"remove"`
}

// ValidateConnectionMetadataUpdate admits an entire bounded patch without mutation.
// The coordinator must also validate the resulting complete snapshot before commit.
func ValidateConnectionMetadataUpdate(update ConnectionMetadataUpdate) error {
	if len(update.Set)+len(update.Remove) > MaxConnectionMetadataEntries {
		return limited("connection metadata key count limit")
	}
	size := len(`{"set":{},"remove":[]}`)
	for key, observation := range update.Set {
		if err := validateMetadataKey(key); err != nil {
			return err
		}
		n, err := metadataObservationSize(observation)
		if err != nil {
			return err
		}
		size += quotedJSONSize(key) + 1 + n
	}
	if len(update.Set) > 0 {
		size += len(update.Set) - 1
	}
	seen := make(map[string]struct{}, len(update.Remove))
	for _, key := range update.Remove {
		if err := validateMetadataKey(key); err != nil {
			return err
		}
		if _, exists := update.Set[key]; exists {
			return invalid("metadata key is both set and removed")
		}
		if _, exists := seen[key]; exists {
			return invalid("duplicate metadata removal")
		}
		seen[key] = struct{}{}
		size += quotedJSONSize(key)
	}
	if len(update.Remove) > 0 {
		size += len(update.Remove) - 1
	}
	if size > MaxConnectionMetadataSnapshotBytes {
		return limited("connection metadata update byte limit")
	}
	return nil
}

// ValidateConnectionMetadataSnapshot validates complete state, including the final
// aggregate size after incremental updates. It never rewrites caller-owned data.
func ValidateConnectionMetadataSnapshot(snapshot ConnectionMetadataSnapshot) error {
	return validateMetadataSnapshotLimit(snapshot, MaxConnectionMetadataSnapshotBytes)
}

// Admission reserves lifecycle publication space; readers accept the full limit.
func validateMetadataSnapshotAdmission(snapshot ConnectionMetadataSnapshot) error {
	return validateMetadataSnapshotLimit(snapshot, MaxConnectionMetadataSnapshotBytes-ConnectionMetadataLifecycleReserveBytes)
}

func validateMetadataSnapshotLimit(snapshot ConnectionMetadataSnapshot, limit int) error {
	if len(snapshot.ConnectionID) > 4096 || len(snapshot.Generation) > 256 {
		return limited("connection metadata identity byte limit")
	}
	if err := text(snapshot.ConnectionID, true); err != nil {
		return err
	}
	if err := text(snapshot.Generation, snapshot.Active); err != nil {
		return err
	}
	if snapshot.Revision == "" || snapshot.Revision[0] == '0' {
		return invalid("invalid metadata revision")
	}
	if _, err := strconv.ParseUint(snapshot.Revision, 10, 64); err != nil {
		return invalid("invalid metadata revision")
	}
	for _, digit := range snapshot.Revision {
		if digit < '0' || digit > '9' {
			return invalid("invalid metadata revision")
		}
	}
	if snapshot.Refreshing && !snapshot.Active {
		return invalid("inactive metadata cannot refresh")
	}
	if len(snapshot.Entries) > MaxConnectionMetadataEntries {
		return limited("connection metadata key count limit")
	}
	if snapshot.Entries == nil {
		return invalid("metadata entries must be an object")
	}
	size := len(`{"connectionID":,"generation":,"revision":,"active":,"refreshing":,"entries":{}}`) +
		quotedJSONSize(snapshot.ConnectionID) + quotedJSONSize(snapshot.Generation) + quotedJSONSize(snapshot.Revision) + metadataBoolSize(snapshot.Active) + metadataBoolSize(snapshot.Refreshing)
	if snapshot.LastRefreshError != nil {
		n, err := metadataErrorSize(snapshot.LastRefreshError)
		if err != nil {
			return err
		}
		size += len(`,"lastRefreshError":`) + n
	}
	for key, entry := range snapshot.Entries {
		if err := validateMetadataKey(key); err != nil {
			return err
		}
		if entry.State == "current" && !snapshot.Active {
			return invalid("inactive metadata cannot contain current facts")
		}
		if snapshot.Generation == "" {
			return invalid("metadata facts require a generation")
		}
		n, err := metadataEntrySize(entry)
		if err != nil {
			return err
		}
		size += quotedJSONSize(key) + 1 + n
		if size > limit {
			return limited("connection metadata snapshot byte limit")
		}
	}
	if len(snapshot.Entries) > 0 {
		size += len(snapshot.Entries) - 1
	}
	if size > limit {
		return limited("connection metadata snapshot byte limit")
	}
	return nil
}

func metadataBoolSize(value bool) int {
	if value {
		return 4
	}
	return 5
}

func validateMetadataKey(key string) error {
	if len(key) > MaxConnectionMetadataKeyBytes {
		return limited("connection metadata key byte limit")
	}
	if err := text(key, true); err != nil {
		return err
	}
	return nil
}

func metadataErrorSize(value *ConnectionMetadataError) (int, error) {
	if value == nil {
		return 0, invalid("metadata error is required")
	}
	if len(value.Code) > MaxConnectionMetadataErrorCodeBytes || len(value.Message) > MaxConnectionMetadataErrorMessageBytes {
		return 0, limited("connection metadata error byte limit")
	}
	if err := text(value.Code, true); err != nil {
		return 0, err
	}
	if err := text(value.Message, true); err != nil {
		return 0, err
	}
	return len(`{"code":,"message":}`) + quotedJSONSize(value.Code) + quotedJSONSize(value.Message), nil
}

func metadataTimeSize(value time.Time) (int, error) {
	if value.IsZero() {
		return 0, invalid("metadata observation time is required")
	}
	if err := validateNativeTime(value); err != nil {
		return 0, err
	}
	if _, offset := value.Zone(); offset != 0 {
		return 0, invalid("metadata observation time must be UTC")
	}
	var buffer [64]byte
	return len(value.AppendFormat(buffer[:0], time.RFC3339Nano)) + 2, nil
}

func metadataEntrySize(entry ConnectionMetadataEntry) (int, error) {
	size := len(`{"state":}`) + quotedJSONSize(entry.State)
	switch entry.State {
	case "current", "stale":
		if entry.ObservedAt == nil {
			return 0, invalid("metadata observation time is required")
		}
		if entry.State == "current" && entry.Error != nil {
			return 0, invalid("current metadata cannot have an error")
		}
		n, err := metadataTimeSize(*entry.ObservedAt)
		if err != nil {
			return 0, err
		}
		size += len(`,"observedAt":`) + n + len(`,"value":`)
		budget := metadataValueBudget{}
		if err := budget.visit(reflect.ValueOf(entry.Value), 0); err != nil {
			return 0, err
		}
		size += budget.bytes
	case "unavailable":
		if entry.Value != nil || entry.ObservedAt != nil {
			return 0, invalid("unavailable metadata has a value or observation time")
		}
		if entry.Error == nil {
			return 0, invalid("unavailable metadata requires an error")
		}
	default:
		return 0, invalid("invalid metadata entry state")
	}
	if entry.Error != nil {
		n, err := metadataErrorSize(entry.Error)
		if err != nil {
			return 0, err
		}
		size += len(`,"error":`) + n
	}
	if size > MaxConnectionMetadataEntryBytes {
		return 0, limited("connection metadata entry byte limit")
	}
	return size, nil
}

func metadataObservationSize(observation ConnectionMetadataObservation) (int, error) {
	entry := ConnectionMetadataEntry{State: observation.State, Value: observation.Value, Error: observation.Error}
	switch observation.State {
	case "success":
		entry.State = "current"
		entry.ObservedAt = &observation.ObservedAt
	case "unavailable":
		if !observation.ObservedAt.IsZero() {
			return 0, invalid("unavailable observation has a timestamp")
		}
	default:
		return 0, invalid("invalid metadata observation state")
	}
	return metadataEntrySize(entry)
}

type metadataValueBudget struct{ bytes int }

func (budget *metadataValueBudget) add(size int) error {
	budget.bytes += size
	if budget.bytes > MaxConnectionMetadataEntryBytes {
		return limited("connection metadata value byte limit")
	}
	return nil
}

func (budget *metadataValueBudget) visit(value reflect.Value, depth int) error {
	if depth > MaxConnectionMetadataDepth {
		return limited("connection metadata nesting limit")
	}
	if !value.IsValid() {
		return budget.add(4)
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return budget.add(4)
		}
		return budget.visit(value.Elem(), depth)
	}
	if number, ok := value.Interface().(json.Number); ok {
		if err := budget.add(len(number)); err != nil {
			return err
		}
		return validateMetadataNumber(number)
	}
	if value.Type().Implements(reflect.TypeFor[json.Marshaler]()) ||
		value.Type().Implements(reflect.TypeFor[encoding.TextMarshaler]()) ||
		reflect.PointerTo(value.Type()).Implements(reflect.TypeFor[json.Marshaler]()) ||
		reflect.PointerTo(value.Type()).Implements(reflect.TypeFor[encoding.TextMarshaler]()) {
		return invalid("custom metadata encoders are not JSON values")
	}
	switch value.Kind() {
	case reflect.Bool:
		return budget.add(metadataBoolSize(value.Bool()))
	case reflect.String:
		if value.Len() > MaxConnectionMetadataEntryBytes {
			return limited("connection metadata value byte limit")
		}
		if !utf8.ValidString(value.String()) {
			return invalid("invalid metadata UTF-8 string")
		}
		return budget.add(quotedJSONSize(value.String()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := value.Int()
		if n < -metadataMaxSafeInteger || n > metadataMaxSafeInteger {
			return invalid("metadata integer is not JavaScript-safe")
		}
		var buffer [32]byte
		return budget.add(len(strconv.AppendInt(buffer[:0], n, 10)))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n := value.Uint()
		if n > metadataMaxSafeInteger {
			return invalid("metadata integer is not JavaScript-safe")
		}
		var buffer [32]byte
		return budget.add(len(strconv.AppendUint(buffer[:0], n, 10)))
	case reflect.Float32, reflect.Float64:
		n := value.Float()
		if math.IsNaN(n) || math.IsInf(n, 0) || (math.Trunc(n) == n && math.Abs(n) > metadataMaxSafeInteger) {
			return invalid("metadata number is not JavaScript-safe")
		}
		format := byte('f')
		if absolute := math.Abs(n); absolute != 0 && (absolute < 1e-6 || absolute >= 1e21) {
			format = 'e'
		}
		var buffer [64]byte
		encoded := strconv.AppendFloat(buffer[:0], n, format, -1, value.Type().Bits())
		size := len(encoded)
		// encoding/json removes the leading zero from negative exponents.
		if format == 'e' && size >= 4 && encoded[size-4] == 'e' && encoded[size-3] == '-' && encoded[size-2] == '0' {
			size--
		}
		return budget.add(size)
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return invalid("metadata object keys must be strings")
		}
		if value.IsNil() {
			return budget.add(4)
		}
		if err := budget.add(2); err != nil {
			return err
		}
		iterator := value.MapRange()
		first := true
		for iterator.Next() {
			key := iterator.Key().String()
			if len(key) > MaxConnectionMetadataEntryBytes {
				return limited("connection metadata object key byte limit")
			}
			if !utf8.ValidString(key) {
				return invalid("invalid metadata object key")
			}
			n := quotedJSONSize(key) + 1
			if !first {
				n++
			}
			first = false
			if err := budget.add(n); err != nil {
				return err
			}
			if err := budget.visit(iterator.Value(), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			return invalid("byte slices are not metadata JSON arrays")
		}
		if value.Kind() == reflect.Slice && value.IsNil() {
			return budget.add(4)
		}
		if err := budget.add(2); err != nil {
			return err
		}
		for i := range value.Len() {
			if i > 0 {
				if err := budget.add(1); err != nil {
					return err
				}
			}
			if err := budget.visit(value.Index(i), depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return invalid("metadata value must be plain JSON")
	}
}

func validateMetadataNumber(number json.Number) error {
	if !json.Valid([]byte(number)) {
		return invalid("invalid metadata JSON number")
	}
	value, err := strconv.ParseFloat(string(number), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || (math.Trunc(value) == value && math.Abs(value) > metadataMaxSafeInteger) {
		return invalid("metadata number is not JavaScript-safe")
	}
	if value == 0 {
		for _, digit := range number {
			if digit == 'e' || digit == 'E' {
				break
			}
			if digit >= '1' && digit <= '9' {
				return invalid("metadata number loses JavaScript precision")
			}
		}
		return nil
	}
	// Decimal spelling may vary (1.0 and 1e0), but its numeric value must survive
	// JavaScript's shortest decimal serialization without rounding or underflow.
	original, ok := new(big.Rat).SetString(string(number))
	if !ok {
		return invalid("invalid metadata JSON number")
	}
	roundtrip, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	if !ok || original.Cmp(roundtrip) != 0 {
		return invalid("metadata number loses JavaScript precision")
	}
	return nil
}

// CloneConnectionMetadataSnapshot detaches all mutable maps, slices, timestamps,
// and errors without JSON roundtripping or scalar coercion. Input must already
// pass ValidateConnectionMetadataSnapshot and remain stable during copying.
func CloneConnectionMetadataSnapshot(snapshot ConnectionMetadataSnapshot) ConnectionMetadataSnapshot {
	var copier metadataCopy
	return copyMetadata(&copier, snapshot)
}

func cloneMetadataUpdate(update ConnectionMetadataUpdate) (ConnectionMetadataUpdate, error) {
	if err := ValidateConnectionMetadataUpdate(update); err != nil {
		return ConnectionMetadataUpdate{}, err
	}
	var copier metadataCopy
	return copyMetadata(&copier, update), nil
}

// MarshalJSON preserves every successful value, including explicit JSON null.
func (entry ConnectionMetadataEntry) MarshalJSON() ([]byte, error) {
	if _, err := metadataEntrySize(entry); err != nil {
		return nil, err
	}
	if entry.State == "unavailable" {
		return json.Marshal(struct {
			State string                   `json:"state"`
			Error *ConnectionMetadataError `json:"error"`
		}{entry.State, entry.Error})
	}
	type plain ConnectionMetadataEntry
	return json.Marshal(plain(entry))
}

// MarshalJSON emits the observation union without unavailable payload fields.
func (observation ConnectionMetadataObservation) MarshalJSON() ([]byte, error) {
	if _, err := metadataObservationSize(observation); err != nil {
		return nil, err
	}
	if observation.State == "unavailable" {
		return json.Marshal(struct {
			State string                   `json:"state"`
			Error *ConnectionMetadataError `json:"error"`
		}{observation.State, observation.Error})
	}
	type plain ConnectionMetadataObservation
	return json.Marshal(plain(observation))
}

func decodeMetadataObject(data []byte, out any, required ...string) (map[string]any, error) {
	return decodeMetadataObjectLimit(data, out, MaxConnectionMetadataSnapshotBytes, required...)
}

func decodeMetadataObjectLimit(data []byte, out any, limit int, required ...string) (map[string]any, error) {
	raw, err := parseJSONWithLimit(data, limit)
	if err != nil {
		return nil, err
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, invalid("metadata DTO must be an object")
	}
	for name, value := range object {
		if _, found := domainFieldType(reflect.TypeOf(out).Elem(), name); !found {
			return nil, invalid("unknown metadata field: " + name)
		}
		if value == nil && name != "value" {
			return nil, invalid("null metadata field: " + name)
		}
	}
	for _, name := range required {
		if _, found := object[name]; !found {
			return nil, invalid("missing metadata field: " + name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		if ErrorCode(err) != errorCodeInternal {
			return nil, err
		}
		return nil, invalid("invalid metadata JSON")
	}
	return object, nil
}

func validateMetadataUnionFields(object map[string]any, state string) error {
	_, value := object["value"]
	_, observed := object["observedAt"]
	if state == "unavailable" {
		if value || observed {
			return invalid("unavailable metadata has payload fields")
		}
	} else if !value || !observed {
		return invalid("successful metadata requires value and observedAt")
	}
	return nil
}

// UnmarshalJSON rejects unknown, duplicate, missing, and invalid union fields.
func (entry *ConnectionMetadataEntry) UnmarshalJSON(data []byte) error {
	type plain ConnectionMetadataEntry
	var out plain
	object, err := decodeMetadataObject(data, &out, "state")
	if err != nil {
		return err
	}
	if err := validateMetadataUnionFields(object, out.State); err != nil {
		return err
	}
	if _, err := metadataEntrySize(ConnectionMetadataEntry(out)); err != nil {
		return err
	}
	*entry = ConnectionMetadataEntry(out)
	return nil
}

// UnmarshalJSON rejects invalid observation states and payload shapes.
func (observation *ConnectionMetadataObservation) UnmarshalJSON(data []byte) error {
	type plain ConnectionMetadataObservation
	var out plain
	object, err := decodeMetadataObject(data, &out, "state")
	if err != nil {
		return err
	}
	if err := validateMetadataUnionFields(object, out.State); err != nil {
		return err
	}
	if _, err := metadataObservationSize(ConnectionMetadataObservation(out)); err != nil {
		return err
	}
	*observation = ConnectionMetadataObservation(out)
	return nil
}

// UnmarshalJSON validates public-safe error shape and bounded text.
func (value *ConnectionMetadataError) UnmarshalJSON(data []byte) error {
	type plain ConnectionMetadataError
	var out plain
	if _, err := decodeMetadataObject(data, &out, "code", "message"); err != nil {
		return err
	}
	result := ConnectionMetadataError(out)
	if _, err := metadataErrorSize(&result); err != nil {
		return err
	}
	*value = result
	return nil
}

// UnmarshalJSON validates complete bounded state before assigning it.
func (snapshot *ConnectionMetadataSnapshot) UnmarshalJSON(data []byte) error {
	type plain ConnectionMetadataSnapshot
	var out plain
	if _, err := decodeMetadataObject(data, &out, "connectionID", "generation", "revision", "active", "refreshing", "entries"); err != nil {
		return err
	}
	result := ConnectionMetadataSnapshot(out)
	if err := ValidateConnectionMetadataSnapshot(result); err != nil {
		return err
	}
	*snapshot = result
	return nil
}

// UnmarshalJSON rejects malformed patches before exposing them to a coordinator.
func (update *ConnectionMetadataUpdate) UnmarshalJSON(data []byte) error {
	type plain ConnectionMetadataUpdate
	var out plain
	if _, err := decodeMetadataObject(data, &out, "set", "remove"); err != nil {
		return err
	}
	result := ConnectionMetadataUpdate(out)
	if err := ValidateConnectionMetadataUpdate(result); err != nil {
		return err
	}
	*update = result
	return nil
}

// UnmarshalJSON validates event shape and ordered removal tombstones.
func (event *ConnectionMetadataEvent) UnmarshalJSON(data []byte) error {
	type plain ConnectionMetadataEvent
	var out plain
	// The nested snapshot retains its own bound; reserve only event framing.
	if _, err := decodeMetadataObjectLimit(data, &out, MaxConnectionMetadataSnapshotBytes+64, "snapshot", "removed"); err != nil {
		return err
	}
	if out.Removed && (len(out.Snapshot.Entries) != 0 || out.Snapshot.Active || out.Snapshot.Refreshing || out.Snapshot.LastRefreshError != nil) {
		return invalid("metadata removal tombstone contains live state")
	}
	*event = ConnectionMetadataEvent(out)
	return nil
}
