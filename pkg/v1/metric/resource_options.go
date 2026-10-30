package metric

import (
	"bytes"
	"encoding/json"
	"io"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
)

const ResourceWireVersion uint32 = 2

func (r QueryRequest) HasResourceIntent() bool {
	return r.ResourceData != nil || r.Reference != nil || r.Route != nil
}

func (r StreamInput) HasResourceIntent() bool {
	return r.ResourceData != nil || r.Reference != nil || r.Route != nil
}
func metricError(code, text string) error { return resourcev2.NewError(code, text) }
func validateQuery(r QueryRequest, ctx *types.PluginContext) error {
	metadata := r
	metadata.ResourceData = nil
	if err := resourcev2.ValidateMetadata(metadata); err != nil {
		return err
	}
	if r.Reference != nil {
		if err := resourcev2.ValidateAddress(r.Reference.Address); err != nil {
			return err
		}
		if r.Reference.Incarnation != nil && *r.Reference.Incarnation == "" {
			return metricError("INVALID_REQUEST", "Empty incarnation.")
		}
		if r.ResourceKey != "" && r.ResourceKey != r.Reference.Address.Type {
			return metricError("INVALID_REQUEST", "Resource selector conflicts with reference.")
		}
	}
	if r.Route != nil {
		if r.Route.Connection == "" {
			return metricError("INVALID_REQUEST", "Empty route connection.")
		}
		// A nil context validates encoding only; dispatch must supply its selected connection.
		if ctx != nil && (ctx.Connection == nil || ctx.Connection.ID == "") {
			return metricError("INVALID_REQUEST", "Resource route requires an actual selected connection.")
		}
		if ctx != nil && ctx.Connection != nil && ctx.Connection.ID != r.Route.Connection {
			return metricError("INVALID_REQUEST", "Route conflicts with trusted connection.")
		}
	}
	if r.ResourceData != nil {
		return resourcev2.ValidateValue(*r.ResourceData)
	}
	return nil
}

func validateStream(r StreamInput) error {
	metadata := r
	metadata.ResourceData = nil
	if err := resourcev2.ValidateMetadata(metadata); err != nil {
		return err
	}
	if r.Reference != nil {
		if err := resourcev2.ValidateCatalogRevision(r.CatalogRevision); err != nil {
			return err
		}
	} else if r.CatalogRevision != "" {
		return metricError("INVALID_REQUEST", "A nonreference metric frame cannot carry a catalog pin.")
	}
	return validateQuery(
		QueryRequest{
			ResourceKey:       r.ResourceKey,
			ResourceID:        r.ResourceID,
			ResourceNamespace: r.ResourceNamespace,
			ResourceData:      r.ResourceData,
			Reference:         r.Reference,
			Route:             r.Route,
		},
		nil,
	)
}

// ValidateStreamInput checks a frame before a host enqueues it on a shared stream.
func ValidateStreamInput(input StreamInput) error { return validateStream(input) }

func strictJSON(data []byte, target any) error {
	if err := resourcev2.ValidateJSON(data); err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return metricError("INVALID_REQUEST", "Metric input must be an object.")
	}
	scanner := json.NewDecoder(bytes.NewReader(data))
	scanner.UseNumber()
	if err := scanMetricJSON(scanner, 0); err != nil {
		return err
	}
	if _, err := scanner.Token(); err != io.EOF {
		return metricError("INVALID_REQUEST", "Trailing metric JSON.")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return metricError("INVALID_REQUEST", err.Error())
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return metricError("INVALID_REQUEST", "Trailing metric JSON.")
	}
	return nil
}

// scanMetricJSON checks exact outer field spelling after shared JSON admission.
func scanMetricJSON(d *json.Decoder, depth int) error {
	token, err := d.Token()
	if err != nil {
		return metricError("INVALID_REQUEST", err.Error())
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return metricError("INVALID_REQUEST", err.Error())
			}
			name, ok := key.(string)
			if !ok {
				return metricError("INVALID_REQUEST", "Invalid metric JSON key.")
			}
			if depth == 0 {
				switch name {
				case "resource_key",
					"resource_id",
					"resource_namespace",
					"resource_data",
					"reference",
					"route",
					"catalog_revision",
					"metric_ids",
					"shape",
					"start_time",
					"end_time",
					"step",
					"params",
					"subscription_id",
					"command",
					"interval":
				default:
					return metricError("INVALID_REQUEST", "Unknown metric JSON field.")
				}
			}
			if err = scanMetricJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := scanMetricJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return metricError("INVALID_REQUEST", "Invalid metric JSON delimiter.")
	}
	if _, err := d.Token(); err != nil {
		return metricError("INVALID_REQUEST", err.Error())
	}
	return nil
}

func (r *QueryRequest) UnmarshalJSON(data []byte) error {
	type plain QueryRequest
	var p plain
	if err := strictJSON(data, &p); err != nil {
		return err
	}
	if err := validateQuery(QueryRequest(p), nil); err != nil {
		return err
	}
	*r = QueryRequest(p)
	return nil
}

func (r *StreamInput) UnmarshalJSON(data []byte) error {
	type plain StreamInput
	var p plain
	if err := strictJSON(data, &p); err != nil {
		return err
	}
	if err := validateStream(StreamInput(p)); err != nil {
		return err
	}
	*r = StreamInput(p)
	return nil
}

func queryToProto(r QueryRequest, resource bool) (*metricpb.QueryMetricsRequest, error) {
	if err := validateQuery(r, nil); err != nil {
		return nil, err
	}
	p := &metricpb.QueryMetricsRequest{
		ResourceKey:       r.ResourceKey,
		ResourceId:        r.ResourceID,
		ResourceNamespace: r.ResourceNamespace,
		MetricIds:         r.MetricIDs,
		Shape:             r.Shape.ToProto(),
		Params:            r.Params,
	}
	if resource {
		p.ResourceWireVersion = 2
	}
	if !r.StartTime.IsZero() {
		p.StartTime = timestamppb.New(r.StartTime)
	}
	if !r.EndTime.IsZero() {
		p.EndTime = timestamppb.New(r.EndTime)
	}
	if r.Step != 0 {
		p.Step = durationpb.New(r.Step)
	}
	var err error
	if r.Reference != nil {
		p.Reference, err = resourcev2.ToProtoResourceReference(*r.Reference)
		if err != nil {
			return nil, err
		}
	}
	if r.Route != nil {
		p.Route, err = resourcev2.ToProtoAccessRoute(*r.Route)
		if err != nil {
			return nil, err
		}
	}
	if err = preflight(p, r.ResourceData, 11); err != nil {
		return nil, err
	}
	if r.ResourceData != nil {
		p.ResourceData, err = resourcev2.ToProtoValue(*r.ResourceData)
	}
	return p, err
}

func preflight(p proto.Message, v *resourcev2.Value, field protowire.Number) error {
	n := proto.Size(p)
	if v != nil {
		size, err := resourcev2.ValueWireSize(*v)
		if err != nil {
			return err
		}
		n += protowire.SizeTag(field) + protowire.SizeBytes(size)
	}
	if n > resourcev2.MaxMessageBytes {
		return metricError("RESOURCE_LIMIT", "Metric frame exceeds wire limit.")
	}
	return nil
}

func queryFromProto(p *metricpb.QueryMetricsRequest, resource bool) (QueryRequest, error) {
	if p == nil {
		return QueryRequest{}, metricError("INVALID_REQUEST", "Missing metric request.")
	}
	if err := preflight(p, nil, 11); err != nil {
		return QueryRequest{}, err
	}
	if resource && p.ResourceWireVersion != 2 {
		return QueryRequest{}, metricError("UNSUPPORTED", "Unsupported metric resource wire version.")
	}
	if !resource && (p.ResourceWireVersion != 0 || p.ResourceData != nil || p.Reference != nil || p.Route != nil) {
		return QueryRequest{}, metricError("UNSUPPORTED", "Legacy metric query cannot carry resource input.")
	}
	if unknown := p.ProtoReflect().GetUnknown(); len(unknown) != 0 {
		if resource {
			return QueryRequest{}, metricError("UNSUPPORTED", "Unknown metric request fields.")
		}
		if err := resourceplugin.ValidateLegacyUtilityWire(
			unknown,
			[]protowire.Number{4, 11, 12, 13, 14},
			nil,
		); err != nil {
			return QueryRequest{}, err
		}
	}
	r := QueryRequest{
		ResourceKey:       p.ResourceKey,
		ResourceID:        p.ResourceId,
		ResourceNamespace: p.ResourceNamespace,
		MetricIDs:         p.MetricIds,
		Shape:             MetricShapeFromProto(p.Shape),
		Params:            p.Params,
	}
	if p.StartTime != nil {
		r.StartTime = p.StartTime.AsTime()
	}
	if p.EndTime != nil {
		r.EndTime = p.EndTime.AsTime()
	}
	if p.Step != nil {
		r.Step = p.Step.AsDuration()
	}
	if err := validateQuery(r, nil); err != nil {
		return r, err
	}
	if p.Reference != nil {
		v, e := resourcev2.FromProtoResourceReference(p.Reference)
		if e != nil {
			return r, e
		}
		r.Reference = &v
	}
	if p.Route != nil {
		v, e := resourcev2.FromProtoAccessRoute(p.Route)
		if e != nil {
			return r, e
		}
		r.Route = &v
	}
	if p.ResourceData != nil {
		v, e := resourcev2.FromProtoValue(p.ResourceData)
		if e != nil {
			return r, e
		}
		r.ResourceData = &v
	}
	return r, validateQuery(r, nil)
}

func streamToProto(r StreamInput, resource bool) (*metricpb.MetricStreamInput, error) {
	if err := validateStream(r); err != nil {
		return nil, err
	}
	if !resource && r.HasResourceIntent() {
		return nil, metricError("UNSUPPORTED", "Legacy metric stream cannot carry resource input.")
	}
	p := &metricpb.MetricStreamInput{
		SubscriptionId:    r.SubscriptionID,
		Command:           r.Command.ToProto(),
		ResourceKey:       r.ResourceKey,
		ResourceId:        r.ResourceID,
		ResourceNamespace: r.ResourceNamespace,
		MetricIds:         r.MetricIDs,
		Interval:          durationpb.New(r.Interval),
	}
	p.Params = r.Params
	p.CatalogRevision = r.CatalogRevision
	if resource {
		p.ResourceWireVersion = 2
	}
	var err error
	if r.Reference != nil {
		p.Reference, err = resourcev2.ToProtoResourceReference(*r.Reference)
		if err != nil {
			return nil, err
		}
	}
	if r.Route != nil {
		p.Route, err = resourcev2.ToProtoAccessRoute(*r.Route)
		if err != nil {
			return nil, err
		}
	}
	if err = preflight(p, r.ResourceData, 9); err != nil {
		return nil, err
	}
	if r.ResourceData != nil {
		p.ResourceData, err = resourcev2.ToProtoValue(*r.ResourceData)
	}
	return p, err
}

func streamFromProto(p *metricpb.MetricStreamInput, resource bool) (StreamInput, error) {
	if p == nil {
		return StreamInput{}, metricError("INVALID_REQUEST", "Missing stream frame.")
	}
	if err := preflight(p, nil, 9); err != nil {
		return StreamInput{}, err
	}
	if unknown := p.ProtoReflect().GetUnknown(); len(unknown) != 0 {
		if resource {
			return StreamInput{}, metricError("UNSUPPORTED", "Unknown metric stream fields.")
		}
		if err := resourceplugin.ValidateLegacyUtilityWire(
			unknown,
			[]protowire.Number{6, 9, 10, 11, 12, 13, 15},
			nil,
		); err != nil {
			return StreamInput{}, err
		}
	}
	if p.ResourceOpen {
		return StreamInput{}, metricError("INVALID_REQUEST", "Unexpected resource stream handshake.")
	}
	q := &metricpb.QueryMetricsRequest{
		ResourceKey:         p.ResourceKey,
		ResourceId:          p.ResourceId,
		ResourceNamespace:   p.ResourceNamespace,
		ResourceData:        p.ResourceData,
		ResourceWireVersion: p.ResourceWireVersion,
		Reference:           p.Reference,
		Route:               p.Route,
	}
	q.Params = p.Params
	r, err := queryFromProto(q, resource)
	if err != nil {
		return StreamInput{}, err
	}
	out := StreamInput{
		SubscriptionID:    p.SubscriptionId,
		Command:           MetricStreamCommandFromProto(p.Command),
		ResourceKey:       r.ResourceKey,
		ResourceID:        r.ResourceID,
		ResourceNamespace: r.ResourceNamespace,
		ResourceData:      r.ResourceData,
		Reference:         r.Reference,
		Route:             r.Route,
		MetricIDs:         p.MetricIds,
	}
	out.Params = p.Params
	out.CatalogRevision = p.CatalogRevision
	if p.Interval != nil {
		out.Interval = p.Interval.AsDuration()
	}
	return out, validateStream(out)
}
