package resourcev2

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

func metadataTypesTime() time.Time {
	return time.Date(2026, time.October, 31, 10, 0, 0, 123, time.UTC)
}

func metadataTypesSnapshot() ConnectionMetadataSnapshot {
	observed := metadataTypesTime()
	return ConnectionMetadataSnapshot{
		ConnectionID: "connection",
		Generation:   "generation",
		Revision:     "1",
		Active:       true,
		Entries: map[string]ConnectionMetadataEntry{
			"test.fact": {State: "current", Value: "value", ObservedAt: &observed},
		},
	}
}

func metadataTypesUpdate(value any) ConnectionMetadataUpdate {
	return ConnectionMetadataUpdate{
		Set: map[string]ConnectionMetadataObservation{
			"test.fact": {State: "success", Value: value, ObservedAt: metadataTypesTime()},
		},
	}
}

func metadataTypesError() *ConnectionMetadataError {
	return &ConnectionMetadataError{Code: "OBSERVATION_FAILED", Message: "Fact is unavailable."}
}

func TestConnectionMetadataTypesPreserveFalsyValues(t *testing.T) {
	for index, value := range []any{nil, false, 0, "", []any{}, map[string]any{}} {
		t.Run(strconv.Itoa(index)+"/"+metadataTypesJSON(t, value), func(t *testing.T) {
			update := metadataTypesUpdate(value)
			if err := ValidateConnectionMetadataUpdate(update); err != nil {
				t.Fatal(err)
			}
			observation := update.Set["test.fact"]
			data, err := json.Marshal(observation)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if _, exists := fields["value"]; !exists {
				t.Fatalf("observation dropped value: %s", data)
			}
			entry := ConnectionMetadataEntry{State: "current", Value: value, ObservedAt: &observation.ObservedAt}
			data, err = json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			var decoded ConnectionMetadataEntry
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if metadataTypesJSON(t, decoded.Value) != metadataTypesJSON(t, value) {
				t.Fatalf("value changed: %s", data)
			}
		})
	}
	entry := ConnectionMetadataEntry{State: "unavailable", Error: metadataTypesError()}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"value"`)) || bytes.Contains(data, []byte(`"observedAt"`)) {
		t.Fatalf("unavailable entry leaked payload fields: %s", data)
	}
	observation := ConnectionMetadataObservation{State: "unavailable", Error: metadataTypesError()}
	data, err = json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"value"`)) || bytes.Contains(data, []byte(`"observedAt"`)) {
		t.Fatalf("unavailable observation leaked payload fields: %s", data)
	}
}

func metadataTypesJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type metadataTypesEncoded int

func (*metadataTypesEncoded) MarshalJSON() ([]byte, error) {
	return []byte(`"custom"`), nil
}

func TestConnectionMetadataTypesValidateJSONValues(t *testing.T) {
	valid := []any{
		nil, true, "value", int64(metadataMaxSafeInteger), uint64(metadataMaxSafeInteger),
		float32(0.1), 0.1, math.Copysign(0, -1), json.Number("0.1"), json.Number("1.0"),
		json.Number("1e-7"), json.Number("-0"), json.Number("0e99999999"),
		[]string{"a", "b"}, map[string]int{"zero": 0},
		map[string]any{"nested": []any{nil, false, json.Number("1.25")}},
	}
	for index, value := range valid {
		if err := ValidateConnectionMetadataUpdate(metadataTypesUpdate(value)); err != nil {
			t.Fatalf("valid value %d (%T): %v", index, value, err)
		}
	}
	var cycle = map[string]any{}
	cycle["cycle"] = cycle
	invalidValues := []any{
		int64(metadataMaxSafeInteger + 1), uint64(metadataMaxSafeInteger + 1),
		float64(metadataMaxSafeInteger + 1), math.NaN(), math.Inf(1), math.Inf(-1),
		json.Number("9007199254740993"), json.Number("0.10000000000000001"),
		json.Number("1e400"), json.Number("1e-400"), json.Number("1e-999999999"),
		json.Number("01"), json.Number("NaN"), json.Number(""),
		[]byte("bytes"), json.RawMessage(`true`), map[int]string{1: "one"},
		struct{ Value string }{Value: "struct"}, new(string), func() {},
		metadataTypesEncoded(1), []metadataTypesEncoded{1}, cycle,
		string([]byte{0xff}), map[string]any{string([]byte{0xff}): true},
	}
	for index, value := range invalidValues {
		if err := ValidateConnectionMetadataUpdate(metadataTypesUpdate(value)); err == nil {
			t.Fatalf("accepted invalid value %d (%T)", index, value)
		}
	}
}

func TestConnectionMetadataTypesValidateAtomicUpdateShape(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ConnectionMetadataUpdate)
	}{
		{"empty key", func(update *ConnectionMetadataUpdate) { update.Set[""] = update.Set["test.fact"] }},
		{"long key", func(update *ConnectionMetadataUpdate) {
			update.Remove = []string{strings.Repeat("x", MaxConnectionMetadataKeyBytes+1)}
		}},
		{"invalid UTF-8 key", func(update *ConnectionMetadataUpdate) { update.Remove = []string{string([]byte{0xff})} }},
		{"overlap", func(update *ConnectionMetadataUpdate) { update.Remove = []string{"test.fact"} }},
		{"duplicate removal", func(update *ConnectionMetadataUpdate) { update.Remove = []string{"test.other", "test.other"} }},
		{"bad state", func(update *ConnectionMetadataUpdate) {
			update.Set["test.fact"] = ConnectionMetadataObservation{State: "current"}
		}},
		{"success without time", func(update *ConnectionMetadataUpdate) {
			update.Set["test.fact"] = ConnectionMetadataObservation{State: "success", Value: false}
		}},
		{"success with error", func(update *ConnectionMetadataUpdate) {
			update.Set["test.fact"] = ConnectionMetadataObservation{State: "success", ObservedAt: metadataTypesTime(), Error: metadataTypesError()}
		}},
		{"unavailable without error", func(update *ConnectionMetadataUpdate) {
			update.Set["test.fact"] = ConnectionMetadataObservation{State: "unavailable"}
		}},
		{"unavailable with value", func(update *ConnectionMetadataUpdate) {
			update.Set["test.fact"] = ConnectionMetadataObservation{State: "unavailable", Value: false, Error: metadataTypesError()}
		}},
		{"unavailable with time", func(update *ConnectionMetadataUpdate) {
			update.Set["test.fact"] = ConnectionMetadataObservation{State: "unavailable", ObservedAt: metadataTypesTime(), Error: metadataTypesError()}
		}},
		{"non-UTC time", func(update *ConnectionMetadataUpdate) {
			observation := update.Set["test.fact"]
			observation.ObservedAt = observation.ObservedAt.In(time.FixedZone("offset", 3600))
			update.Set["test.fact"] = observation
		}},
		{"empty error", func(update *ConnectionMetadataUpdate) {
			update.Set["test.fact"] = ConnectionMetadataObservation{State: "unavailable", Error: &ConnectionMetadataError{}}
		}},
		{"oversized error", func(update *ConnectionMetadataUpdate) {
			update.Set["test.fact"] = ConnectionMetadataObservation{State: "unavailable", Error: &ConnectionMetadataError{Code: "FAILED", Message: strings.Repeat("x", MaxConnectionMetadataErrorMessageBytes+1)}}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			update := metadataTypesUpdate("value")
			test.mutate(&update)
			if err := ValidateConnectionMetadataUpdate(update); err == nil {
				t.Fatal("invalid update accepted")
			}
			if _, err := cloneMetadataUpdate(update); err == nil {
				t.Fatal("invalid update cloned")
			}
		})
	}
	if err := ValidateConnectionMetadataUpdate(ConnectionMetadataUpdate{}); err != nil {
		t.Fatalf("empty patch: %v", err)
	}
}

func TestConnectionMetadataTypesBounds(t *testing.T) {
	update := metadataTypesUpdate(strings.Repeat("x", MaxConnectionMetadataEntryBytes))
	if err := ValidateConnectionMetadataUpdate(update); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("entry byte limit: %v", err)
	}
	update = metadataTypesUpdate(strings.Repeat("<", MaxConnectionMetadataEntryBytes/6))
	if err := ValidateConnectionMetadataUpdate(update); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("escaped entry byte limit: %v", err)
	}
	var nested any = false
	for range MaxConnectionMetadataDepth {
		nested = []any{nested}
	}
	if err := ValidateConnectionMetadataUpdate(metadataTypesUpdate(nested)); err != nil {
		t.Fatalf("allowed depth: %v", err)
	}
	if err := ValidateConnectionMetadataUpdate(metadataTypesUpdate([]any{nested})); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("depth limit: %v", err)
	}
	update = ConnectionMetadataUpdate{Set: map[string]ConnectionMetadataObservation{}}
	for index := range MaxConnectionMetadataEntries + 1 {
		update.Set["test."+strconv.Itoa(index)] = ConnectionMetadataObservation{State: "success", Value: nil, ObservedAt: metadataTypesTime()}
	}
	if err := ValidateConnectionMetadataUpdate(update); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("key count limit: %v", err)
	}
	delete(update.Set, "test."+strconv.Itoa(MaxConnectionMetadataEntries))
	if err := ValidateConnectionMetadataUpdate(update); err != nil {
		t.Fatalf("allowed key count: %v", err)
	}
	snapshot := metadataTypesSnapshot()
	for index := range 18 {
		entry := snapshot.Entries["test.fact"]
		entry.Value = strings.Repeat("x", 15000)
		snapshot.Entries["test."+strconv.Itoa(index)] = entry
	}
	if err := ValidateConnectionMetadataSnapshot(snapshot); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("final aggregate limit: %v", err)
	}
	update = ConnectionMetadataUpdate{Set: map[string]ConnectionMetadataObservation{}}
	for index := range 18 {
		update.Set["test."+strconv.Itoa(index)] = ConnectionMetadataObservation{State: "success", Value: strings.Repeat("x", 15000), ObservedAt: metadataTypesTime()}
	}
	if err := ValidateConnectionMetadataUpdate(update); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("update aggregate limit: %v", err)
	}
}

func TestConnectionMetadataTypesFloat32WireLimits(t *testing.T) {
	for _, test := range []struct {
		name     string
		value    float32
		count    int
		wantCode string
	}{
		{"positive within limit", 1e-6, 1800, ""},
		{"positive above limit", 1e-6, 2000, "RESOURCE_LIMIT"},
		{"negative within limit", -1e-6, 1600, ""},
		{"negative above limit", -1e-6, 2000, "RESOURCE_LIMIT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := make([]float32, test.count)
			for index := range values {
				values[index] = test.value
			}
			update := metadataTypesUpdate(values)
			if err := ValidateConnectionMetadataUpdate(update); ErrorCode(err) != test.wantCode {
				t.Fatalf("admission: got %v, want code %q", err, test.wantCode)
			}
			snapshot := metadataTypesSnapshot()
			entry := snapshot.Entries["test.fact"]
			entry.Value = values
			snapshot.Entries["test.fact"] = entry
			if err := ValidateConnectionMetadataSnapshot(snapshot); ErrorCode(err) != test.wantCode {
				t.Fatalf("snapshot: got %v, want code %q", err, test.wantCode)
			}
			data, err := json.Marshal(snapshot)
			if test.wantCode != "" {
				if err == nil {
					t.Fatal("serialized an oversized metadata snapshot")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var decoded ConnectionMetadataSnapshot
			if err := json.Unmarshal(data, &decoded); ErrorCode(err) != test.wantCode {
				t.Fatalf("wire decoding: got %v, want code %q", err, test.wantCode)
			}
		})
	}
}

func TestConnectionMetadataTypesSnapshotShape(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ConnectionMetadataSnapshot)
	}{
		{"missing connection", func(snapshot *ConnectionMetadataSnapshot) { snapshot.ConnectionID = "" }},
		{"missing active generation", func(snapshot *ConnectionMetadataSnapshot) { snapshot.Generation = "" }},
		{"missing revision", func(snapshot *ConnectionMetadataSnapshot) { snapshot.Revision = "" }},
		{"zero revision", func(snapshot *ConnectionMetadataSnapshot) { snapshot.Revision = "0" }},
		{"leading zero revision", func(snapshot *ConnectionMetadataSnapshot) { snapshot.Revision = "01" }},
		{"signed revision", func(snapshot *ConnectionMetadataSnapshot) { snapshot.Revision = "+1" }},
		{"overflow revision", func(snapshot *ConnectionMetadataSnapshot) { snapshot.Revision = "18446744073709551616" }},
		{"null entries", func(snapshot *ConnectionMetadataSnapshot) { snapshot.Entries = nil }},
		{"inactive current", func(snapshot *ConnectionMetadataSnapshot) { snapshot.Active = false }},
		{"inactive refreshing", func(snapshot *ConnectionMetadataSnapshot) {
			snapshot.Active = false
			snapshot.Refreshing = true
			snapshot.Entries = map[string]ConnectionMetadataEntry{}
		}},
		{"unknown state", func(snapshot *ConnectionMetadataSnapshot) {
			entry := snapshot.Entries["test.fact"]
			entry.State = "success"
			snapshot.Entries["test.fact"] = entry
		}},
		{"missing observation time", func(snapshot *ConnectionMetadataSnapshot) {
			entry := snapshot.Entries["test.fact"]
			entry.ObservedAt = nil
			snapshot.Entries["test.fact"] = entry
		}},
		{"bad overall error", func(snapshot *ConnectionMetadataSnapshot) { snapshot.LastRefreshError = &ConnectionMetadataError{} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			snapshot := metadataTypesSnapshot()
			test.mutate(&snapshot)
			if err := ValidateConnectionMetadataSnapshot(snapshot); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	snapshot := metadataTypesSnapshot()
	snapshot.Active = false
	snapshot.Generation = ""
	snapshot.Entries = map[string]ConnectionMetadataEntry{}
	if err := ValidateConnectionMetadataSnapshot(snapshot); err != nil {
		t.Fatalf("inactive inventory snapshot: %v", err)
	}
	snapshot = metadataTypesSnapshot()
	snapshot.Active = false
	entry := snapshot.Entries["test.fact"]
	entry.State = "stale"
	snapshot.Entries["test.fact"] = entry
	if err := ValidateConnectionMetadataSnapshot(snapshot); err != nil {
		t.Fatalf("stopped retained fact: %v", err)
	}
}

func TestConnectionMetadataTypesStrictJSON(t *testing.T) {
	entryJSON := []string{
		`null`, `[]`, `{}`, `{"state":"current","observedAt":"2026-10-31T10:00:00Z"}`,
		`{"state":"current","value":false,"observedAt":null}`,
		`{"state":"current","value":false,"observedAt":"2026-10-31T10:00:00Z","extra":true}`,
		`{"state":"current","value":false,"value":true,"observedAt":"2026-10-31T10:00:00Z"}`,
		`{"State":"current","value":false,"observedAt":"2026-10-31T10:00:00Z"}`,
		`{"state":"unavailable","value":null,"error":{"code":"FAILED","message":"Unavailable"}}`,
		`{"state":"unavailable","error":{"code":"FAILED","message":"Unavailable","private":"secret"}}`,
		`{"state":"unavailable","error":null}`,
		`{"state":"current","value":{"a":1,"a":2},"observedAt":"2026-10-31T10:00:00Z"}`,
		`{"state":"current","value":"\ud800","observedAt":"2026-10-31T10:00:00Z"}`,
	}
	for _, input := range entryJSON {
		var entry ConnectionMetadataEntry
		if err := json.Unmarshal([]byte(input), &entry); err == nil {
			t.Fatalf("accepted invalid entry: %s", input)
		}
	}
	var snapshot ConnectionMetadataSnapshot
	for _, input := range []string{
		`{"connectionID":"c","generation":"","revision":"1","active":false,"entries":{}}`,
		`{"connectionID":"c","generation":"","revision":"1","active":false,"refreshing":false,"entries":null}`,
		`{"connectionID":"c","generation":"","revision":"1","active":false,"refreshing":false,"entries":{},"unknown":true}`,
	} {
		if err := json.Unmarshal([]byte(input), &snapshot); err == nil {
			t.Fatalf("accepted invalid snapshot: %s", input)
		}
	}
	var update ConnectionMetadataUpdate
	for _, input := range []string{
		`{"set":{},"remove":["test.fact","test.fact"]}`,
		`{"set":{"test.fact":{"state":"success","observedAt":"2026-10-31T10:00:00Z"}},"remove":[]}`,
		`{"set":null,"remove":[]}`, `{"set":{},"remove":null}`,
	} {
		if err := json.Unmarshal([]byte(input), &update); err == nil {
			t.Fatalf("accepted invalid update: %s", input)
		}
	}
	valid := metadataTypesSnapshot()
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("valid roundtrip: %v", err)
	}
	event := ConnectionMetadataEvent{Snapshot: valid, Removed: true}
	data, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &event); err == nil {
		t.Fatal("accepted tombstone containing live facts")
	}
	valid.Active = false
	valid.Entries = map[string]ConnectionMetadataEntry{}
	data, err = json.Marshal(ConnectionMetadataEvent{Snapshot: valid, Removed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("valid tombstone: %v", err)
	}
}

func TestConnectionMetadataTypesCloneIsolation(t *testing.T) {
	values := map[string]any{"list": []any{false, map[string]any{"integer": int64(0)}}}
	update := metadataTypesUpdate(values)
	update.Remove = []string{"test.old"}
	update.Set["test.unavailable"] = ConnectionMetadataObservation{State: "unavailable", Error: metadataTypesError()}
	cloned, err := cloneMetadataUpdate(update)
	if err != nil {
		t.Fatal(err)
	}
	values["list"].([]any)[1].(map[string]any)["integer"] = int64(99)
	update.Remove[0] = "test.changed"
	update.Set["test.unavailable"].Error.Message = "mutated"
	delete(update.Set, "test.fact")
	if cloned.Remove[0] != "test.old" || cloned.Set["test.unavailable"].Error.Message == "mutated" {
		t.Fatal("update retained provider-owned containers")
	}
	copiedInteger := cloned.Set["test.fact"].Value.(map[string]any)["list"].([]any)[1].(map[string]any)["integer"]
	if copiedInteger != int64(0) {
		t.Fatalf("nested integer changed type or value: %#v", copiedInteger)
	}
	snapshot := metadataTypesSnapshot()
	entry := snapshot.Entries["test.fact"]
	entry.State = "stale"
	entry.Value = cloned.Set["test.fact"].Value
	entry.Error = metadataTypesError()
	snapshot.Entries["test.fact"] = entry
	snapshot.LastRefreshError = metadataTypesError()
	if err := ValidateConnectionMetadataSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	copy := CloneConnectionMetadataSnapshot(snapshot)
	*copy.Entries["test.fact"].ObservedAt = metadataTypesTime().Add(time.Hour)
	copy.Entries["test.fact"].Error.Message = "changed entry error"
	copy.LastRefreshError.Message = "changed overall error"
	copy.Entries["test.fact"].Value.(map[string]any)["list"].([]any)[0] = true
	delete(copy.Entries, "test.fact")
	original := snapshot.Entries["test.fact"]
	if !original.ObservedAt.Equal(metadataTypesTime()) || original.Error.Message == "changed entry error" || snapshot.LastRefreshError.Message == "changed overall error" {
		t.Fatal("snapshot copy retained pointers")
	}
	if original.Value.(map[string]any)["list"].([]any)[0] != false {
		t.Fatal("snapshot copy retained nested slice")
	}
}

func TestConnectionMetadataTypesLifecycleReserve(t *testing.T) {
	snapshot := metadataTypesSnapshot()
	delete(snapshot.Entries, "test.fact")
	observed := metadataTypesTime()
	for index := range 16 {
		snapshot.Entries["test."+strconv.Itoa(index)] = ConnectionMetadataEntry{
			State: "current", Value: strings.Repeat("x", 16000), ObservedAt: &observed,
		}
	}
	// Fill the remaining space with one small entry while accounting for exactly
	// the same JSON envelope a consumer receives.
	baseline := len(metadataTypesJSON(t, snapshot))
	limit := MaxConnectionMetadataSnapshotBytes - ConnectionMetadataLifecycleReserveBytes
	padding := limit - baseline - len(`,"test.padding":{"state":"current","value":"","observedAt":`) - len(metadataTypesJSON(t, observed)) - 1
	if padding < 0 || padding >= MaxConnectionMetadataEntryBytes-128 {
		t.Fatalf("unexpected padding size %d", padding)
	}
	snapshot.Entries["test.padding"] = ConnectionMetadataEntry{State: "current", Value: strings.Repeat("x", padding), ObservedAt: &observed}
	if size := len(metadataTypesJSON(t, snapshot)); size != limit {
		t.Fatalf("fixture size %d, want %d", size, limit)
	}
	if err := validateMetadataSnapshotAdmission(snapshot); err != nil {
		t.Fatalf("reserved boundary rejected: %v", err)
	}
	entry := snapshot.Entries["test.padding"]
	entry.Value = entry.Value.(string) + "x"
	snapshot.Entries["test.padding"] = entry
	if err := validateMetadataSnapshotAdmission(snapshot); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("missing lifecycle reserve: %v", err)
	}
	if err := ValidateConnectionMetadataSnapshot(snapshot); err != nil {
		t.Fatalf("public validator incorrectly requires admission reserve: %v", err)
	}
	entry.Value = strings.TrimSuffix(entry.Value.(string), "x")
	snapshot.Entries["test.padding"] = entry
	for key, entry := range snapshot.Entries {
		entry.State = "stale"
		snapshot.Entries[key] = entry
	}
	snapshot.LastRefreshError = metadataTypesError()
	snapshot.Refreshing = false
	if err := ValidateConnectionMetadataSnapshot(snapshot); err != nil {
		t.Fatalf("failure envelope exceeded reserved space: %v", err)
	}
}

func TestMetadataEventAllowsFramingBeyondSnapshotLimit(t *testing.T) {
	snapshot := metadataTypesSnapshot()
	observed := metadataTypesTime()
	snapshot.Entries = make(map[string]ConnectionMetadataEntry)
	for i := range 16 {
		snapshot.Entries["test.fact_"+strconv.Itoa(i)] = ConnectionMetadataEntry{State: "current", Value: strings.Repeat("x", 16000), ObservedAt: &observed}
	}
	snapshot.Entries["test.padding"] = ConnectionMetadataEntry{State: "current", Value: "", ObservedAt: &observed}
	initial, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	padding := strings.Repeat("x", MaxConnectionMetadataSnapshotBytes-len(initial)-1)
	snapshot.Entries["test.padding"] = ConnectionMetadataEntry{State: "current", Value: padding, ObservedAt: &observed}
	if err := ValidateConnectionMetadataSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(ConnectionMetadataEvent{Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) <= MaxConnectionMetadataSnapshotBytes {
		t.Fatal("fixture does not cross snapshot framing boundary")
	}
	var event ConnectionMetadataEvent
	if err := json.Unmarshal(wire, &event); err != nil {
		t.Fatalf("valid snapshot rejected inside event envelope: %v", err)
	}
	snapshot.Entries["test.padding"] = ConnectionMetadataEntry{State: "current", Value: padding + "xx", ObservedAt: &observed}
	wire, err = json.Marshal(ConnectionMetadataEvent{Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &event); err == nil {
		t.Fatal("event envelope bypassed nested snapshot bound")
	}
}
