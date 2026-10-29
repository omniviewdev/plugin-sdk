package resourcev2

import (
	"sort"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func validateGuarantee(v string) error {
	if v != BestEffortGuarantee {
		return invalid("unsupported update guarantee")
	}
	return nil
}
func validateWatchState(v WatchState) error {
	if v < WatchStateIdle || v > WatchStateSkipped {
		return invalid("unknown watch state")
	}
	return nil
}
func validateMetadata(v ResourceMetadata) error { return admission(v) }
func validateWatchAdd(v WatchAddPayload) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateRoute(v.Route); err != nil {
		return err
	}
	if err := validateGuarantee(v.Guarantee); err != nil {
		return err
	}
	if err := validateMetadata(v.Metadata); err != nil {
		return err
	}
	return ValidateRecord(v.Record)
}
func validateWatchUpdate(v WatchUpdatePayload) error {
	return validateWatchAdd(WatchAddPayload{Route: v.Route, Record: v.Record, Metadata: v.Metadata, Guarantee: v.Guarantee})
}
func validateWatchDelete(v WatchDeletePayload) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateRoute(v.Route); err != nil {
		return err
	}
	if err := validateGuarantee(v.Guarantee); err != nil {
		return err
	}
	if err := validateReference(v.Reference); err != nil {
		return err
	}
	if v.LastKnown != nil {
		if err := ValidateRecord(*v.LastKnown); err != nil {
			return err
		}
		if !sameAddress(v.Reference.Address, v.LastKnown.Reference.Address) {
			return invalid("last-known record address mismatch")
		}
		if v.Reference.Incarnation != nil && (v.LastKnown.Reference.Incarnation == nil || *v.Reference.Incarnation != *v.LastKnown.Reference.Incarnation) {
			return invalid("last-known record incarnation mismatch")
		}
	}
	return nil
}
func validateWatchStateEvent(v WatchStateEvent) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateRoute(v.Route); err != nil {
		return err
	}
	if err := text(v.Type, true); err != nil {
		return err
	}
	if err := validateWatchState(v.State); err != nil {
		return err
	}
	if err := validateCount(v.Count); err != nil {
		return err
	}
	if v.Error != nil && !IsErrorCode(v.Error.Code) {
		return invalid("unknown update error code")
	}
	return validateGuarantee(v.Guarantee)
}
func validateWatchSummary(v WatchConnectionSummary) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateRoute(v.Route); err != nil {
		return err
	}
	if err := validateGuarantee(v.Guarantee); err != nil {
		return err
	}
	if err := ValidateScope(v.Scope); err != nil {
		return err
	}
	for k, s := range v.Resources {
		if err := text(k, true); err != nil {
			return err
		}
		if err := validateWatchState(s); err != nil {
			return err
		}
	}
	for k, c := range v.ResourceCounts {
		if err := text(k, true); err != nil {
			return err
		}
		if err := validateCount(c); err != nil {
			return err
		}
	}
	return nil
}

// Update errors use the same reviewed C02 vocabulary as the finite boundary.
func updateErrorCode(err *ResourceOperationError) string {
	if err == nil {
		return ""
	}
	return ErrorCode(err)
}
func toResourceMetadata(v ResourceMetadata) *resourcepb.ResourceMetadata {
	keys := make([]string, 0, len(v.Labels))
	for k := range v.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	p := &resourcepb.ResourceMetadata{Labels: make([]*resourcepb.LabelEntry, 0, len(keys)), CreatedAt: toOptionalTimestamp(v.CreatedAt)}
	for _, k := range keys {
		p.Labels = append(p.Labels, &resourcepb.LabelEntry{Key: k, Value: v.Labels[k]})
	}
	return p
}
func (d *adapterDecoder) metadata(p *resourcepb.ResourceMetadata) ResourceMetadata {
	if p == nil {
		return ResourceMetadata{Labels: map[string]string{}}
	}
	keys := make(map[string]struct{}, len(p.Labels))
	for _, entry := range p.Labels {
		if entry == nil {
			d.fail("missing label entry")
			return ResourceMetadata{}
		}
		if _, ok := keys[entry.Key]; ok {
			d.fail("duplicate metadata label")
			return ResourceMetadata{}
		}
		keys[entry.Key] = struct{}{}
	}
	v := ResourceMetadata{Labels: make(map[string]string, len(p.Labels)), CreatedAt: d.optionalTimestamp(p.CreatedAt)}
	for _, entry := range p.Labels {
		v.Labels[entry.Key] = entry.Value
	}
	return v
}
func toWatchAddPayload(v WatchAddPayload) *resourcepb.WatchAddPayload {
	return &resourcepb.WatchAddPayload{Route: toAccessRoute(v.Route), Record: toRecord(v.Record), Metadata: toResourceMetadata(v.Metadata), Guarantee: v.Guarantee}
}
func (d *adapterDecoder) watchAdd(p *resourcepb.WatchAddPayload) WatchAddPayload {
	if p == nil {
		d.fail("missing add payload")
		return WatchAddPayload{}
	}
	return WatchAddPayload{Route: d.route(p.Route), Record: d.record(p.Record), Metadata: d.metadata(p.Metadata), Guarantee: p.Guarantee}
}
func toWatchUpdatePayload(v WatchUpdatePayload) *resourcepb.WatchUpdatePayload {
	return &resourcepb.WatchUpdatePayload{Route: toAccessRoute(v.Route), Record: toRecord(v.Record), Metadata: toResourceMetadata(v.Metadata), Guarantee: v.Guarantee}
}
func (d *adapterDecoder) watchUpdate(p *resourcepb.WatchUpdatePayload) WatchUpdatePayload {
	if p == nil {
		d.fail("missing update payload")
		return WatchUpdatePayload{}
	}
	return WatchUpdatePayload{Route: d.route(p.Route), Record: d.record(p.Record), Metadata: d.metadata(p.Metadata), Guarantee: p.Guarantee}
}
func toWatchDeletePayload(v WatchDeletePayload) *resourcepb.WatchDeletePayload {
	p := &resourcepb.WatchDeletePayload{Route: toAccessRoute(v.Route), Reference: toResourceReference(v.Reference), Guarantee: v.Guarantee}
	if v.LastKnown != nil {
		p.LastKnown = toRecord(*v.LastKnown)
	}
	return p
}
func (d *adapterDecoder) watchDelete(p *resourcepb.WatchDeletePayload) WatchDeletePayload {
	if p == nil {
		d.fail("missing delete payload")
		return WatchDeletePayload{}
	}
	v := WatchDeletePayload{Route: d.route(p.Route), Reference: d.reference(p.Reference), Guarantee: p.Guarantee}
	if p.LastKnown != nil {
		r := d.record(p.LastKnown)
		v.LastKnown = &r
	}
	return v
}
func toWatchStateEvent(v WatchStateEvent) *resourcepb.WatchStateEvent {
	return &resourcepb.WatchStateEvent{Route: toAccessRoute(v.Route), Type: v.Type, State: int32(v.State), Count: toCount(v.Count), ErrorCode: updateErrorCode(v.Error), Message: v.Message, Guarantee: v.Guarantee}
}
func (d *adapterDecoder) watchState(p *resourcepb.WatchStateEvent) WatchStateEvent {
	if p == nil {
		d.fail("missing watch state")
		return WatchStateEvent{}
	}
	v := WatchStateEvent{Route: d.route(p.Route), Type: p.Type, State: WatchState(p.State), Count: d.count(p.Count), Message: p.Message, Guarantee: p.Guarantee}
	if p.ErrorCode != "" {
		if !IsErrorCode(p.ErrorCode) {
			d.fail("unknown update error code")
		} else {
			v.Error = &ResourceOperationError{Code: p.ErrorCode}
		}
	}
	return v
}
func toWatchConnectionSummary(v WatchConnectionSummary) *resourcepb.WatchConnectionSummary {
	p := &resourcepb.WatchConnectionSummary{Route: toAccessRoute(v.Route), Scope: toQueryScope(v.Scope), Guarantee: v.Guarantee, Resources: make([]*resourcepb.ResourceWatchState, 0, len(v.Resources)), ResourceCounts: make([]*resourcepb.ResourceWatchCount, 0, len(v.ResourceCounts))}
	keys := make([]string, 0, len(v.Resources))
	for k := range v.Resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p.Resources = append(p.Resources, &resourcepb.ResourceWatchState{Type: k, State: int32(v.Resources[k])})
	}
	keys = make([]string, 0, len(v.ResourceCounts))
	for k := range v.ResourceCounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p.ResourceCounts = append(p.ResourceCounts, &resourcepb.ResourceWatchCount{Type: k, Count: toCount(v.ResourceCounts[k])})
	}
	return p
}
func (d *adapterDecoder) watchSummary(p *resourcepb.WatchConnectionSummary) WatchConnectionSummary {
	if p == nil {
		d.fail("missing watch summary")
		return WatchConnectionSummary{}
	}
	states := map[string]struct{}{}
	for _, entry := range p.Resources {
		if entry == nil {
			d.fail("missing watch resource state")
			return WatchConnectionSummary{}
		}
		if _, ok := states[entry.Type]; ok {
			d.fail("duplicate watch resource state")
			return WatchConnectionSummary{}
		}
		states[entry.Type] = struct{}{}
	}
	counts := map[string]struct{}{}
	for _, entry := range p.ResourceCounts {
		if entry == nil {
			d.fail("missing watch resource count")
			return WatchConnectionSummary{}
		}
		if _, ok := counts[entry.Type]; ok {
			d.fail("duplicate watch resource count")
			return WatchConnectionSummary{}
		}
		counts[entry.Type] = struct{}{}
	}
	v := WatchConnectionSummary{Route: d.route(p.Route), Scope: d.scope(p.Scope), Guarantee: p.Guarantee, Resources: make(map[string]WatchState, len(p.Resources)), ResourceCounts: make(map[string]Count, len(p.ResourceCounts))}
	for _, entry := range p.Resources {
		v.Resources[entry.Type] = WatchState(entry.State)
	}
	for _, entry := range p.ResourceCounts {
		v.ResourceCounts[entry.Type] = d.count(entry.Count)
	}
	return v
}

// ToProtoWatchAddPayload encodes advisory ADD data without establishing listener readiness or continuity.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoWatchAddPayload(v WatchAddPayload) (*resourcepb.WatchAddPayload, error) {
	return toProto(v, validateWatchAdd, toWatchAddPayload)
}

// FromProtoWatchAddPayload decodes advisory ADD data without establishing listener readiness or continuity.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoWatchAddPayload(p *resourcepb.WatchAddPayload) (WatchAddPayload, error) {
	return fromProto(p, (*adapterDecoder).watchAdd, validateWatchAdd)
}

// ToProtoWatchUpdatePayload encodes advisory UPDATE data without establishing listener readiness or continuity.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoWatchUpdatePayload(v WatchUpdatePayload) (*resourcepb.WatchUpdatePayload, error) {
	return toProto(v, validateWatchUpdate, toWatchUpdatePayload)
}

// FromProtoWatchUpdatePayload decodes advisory UPDATE data without establishing listener readiness or continuity.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoWatchUpdatePayload(p *resourcepb.WatchUpdatePayload) (WatchUpdatePayload, error) {
	return fromProto(p, (*adapterDecoder).watchUpdate, validateWatchUpdate)
}

// ToProtoWatchDeletePayload encodes advisory DELETE identity and optional last-known record without replay guarantees.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoWatchDeletePayload(v WatchDeletePayload) (*resourcepb.WatchDeletePayload, error) {
	return toProto(v, validateWatchDelete, toWatchDeletePayload)
}

// FromProtoWatchDeletePayload decodes advisory DELETE identity and optional last-known record without replay guarantees.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoWatchDeletePayload(p *resourcepb.WatchDeletePayload) (WatchDeletePayload, error) {
	return fromProto(p, (*adapterDecoder).watchDelete, validateWatchDelete)
}

// ToProtoWatchStateEvent encodes advisory source state without changing source lifecycle.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoWatchStateEvent(v WatchStateEvent) (*resourcepb.WatchStateEvent, error) {
	return toProto(v, validateWatchStateEvent, toWatchStateEvent)
}

// FromProtoWatchStateEvent decodes advisory source state without changing source lifecycle.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoWatchStateEvent(p *resourcepb.WatchStateEvent) (WatchStateEvent, error) {
	return fromProto(p, (*adapterDecoder).watchState, validateWatchStateEvent)
}

// ToProtoWatchConnectionSummary encodes per-type advisory states and counts without starting watches.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoWatchConnectionSummary(v WatchConnectionSummary) (*resourcepb.WatchConnectionSummary, error) {
	return toProto(v, validateWatchSummary, toWatchConnectionSummary)
}

// FromProtoWatchConnectionSummary decodes per-type advisory states and counts without starting watches.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoWatchConnectionSummary(p *resourcepb.WatchConnectionSummary) (WatchConnectionSummary, error) {
	return fromProto(p, (*adapterDecoder).watchSummary, validateWatchSummary)
}
