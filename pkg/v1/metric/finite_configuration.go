package metric

import (
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"time"
)

// MetricFieldTransform is a bounded, deterministic public identity transform.
type MetricFieldTransform string

const (
	MetricTransformIdentity  MetricFieldTransform = "identity"
	MetricTransformLowercase MetricFieldTransform = "lowercase"
	MetricTransformUppercase MetricFieldTransform = "uppercase"
)

// MetricFieldBinding binds an approved owner scalar or public scope to a source label.
// ResourcePath is an RFC6901 scalar pointer; only the host may populate PublicData.
type MetricFieldBinding struct {
	Field        string               `json:"field"`
	Label        string               `json:"label"`
	Source       string               `json:"source"`
	Required     bool                 `json:"required"`
	Transform    MetricFieldTransform `json:"transform,omitempty"`
	ResourcePath string               `json:"resource_path,omitempty"`
}

// MetricDefinition combines stable semantics with an explicitly selected source mapping.
// Units and shapes use the same physical vocabulary as FiniteMetricDescriptor.
type MetricDefinition struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	ResourceKeys    []string             `json:"resource_keys"`
	Unit            string               `json:"unit"`
	Semantics       string               `json:"semantics"`
	Dimensions      []string             `json:"dimensions"`
	SupportedShapes []string             `json:"supported_shapes"`
	QueryLanguage   string               `json:"query_language"`
	Query           string               `json:"query"`
	FieldBindings   []MetricFieldBinding `json:"field_bindings"`
	IdentityLabels  []string             `json:"identity_labels"`
	Reducer         string               `json:"reducer"`
	BatchStrategy   string               `json:"batch_strategy"`
	Revision        string               `json:"revision"`
	Native          bool                 `json:"native"`
	// RangeReuse is the owner's declaration that this authored definition meets
	// the FiniteMetricDescriptor.RangeReuse guarantee. It requires the range
	// shape. It is not an opt-in: the provider's descriptor decides and may
	// decline, and a host clears the declaration unless the effective definition
	// is exactly the owner-authored one.
	RangeReuse bool `json:"range_reuse,omitempty"`
}

// MetricDefinitionOverride is sparse. Nil inherits; removing the override resets it.
type MetricDefinitionOverride struct {
	MetricID       string                `json:"metric_id"`
	Source         *MetricSourceRef      `json:"source,omitempty"`
	Query          *string               `json:"query,omitempty"`
	FieldBindings  *[]MetricFieldBinding `json:"field_bindings,omitempty"`
	IdentityLabels *[]string             `json:"identity_labels,omitempty"`
	Reducer        *string               `json:"reducer,omitempty"`
	BatchStrategy  *string               `json:"batch_strategy,omitempty"`
	Revision       string                `json:"revision"`
}

// MetricBinding stores source configuration independently of presentation preferences.
// Resource owner authority never comes from the selected source connection.
type MetricBinding struct {
	ID                   string                     `json:"id"`
	Signal               string                     `json:"signal"`
	ResourcePluginID     string                     `json:"resource_plugin_id"`
	ResourceConnectionID string                     `json:"resource_connection_id"`
	Source               MetricSourceRef            `json:"source"`
	Policy               string                     `json:"policy"`
	PublicScope          map[string]string          `json:"public_scope"`
	Overrides            []MetricDefinitionOverride `json:"overrides"`
	Revision             string                     `json:"revision"`
}

// MetricSourceDefaults is a profile default. A nil Source resets to native inheritance.
type MetricSourceDefaults struct {
	Source   *MetricSourceRef `json:"source,omitempty"`
	Revision string           `json:"revision"`
}

// MetricViewSourceSelection is profile-durable source configuration for one
// owner connection and resource view, independent of presentation preferences.
// Mode is inherit, native, or external. Only external carries Source.
type MetricViewSourceSelection struct {
	OwnerPluginID     string           `json:"owner_plugin_id"`
	OwnerConnectionID string           `json:"owner_connection_id"`
	ResourceKey       string           `json:"resource_key"`
	Mode              string           `json:"mode"`
	Source            *MetricSourceRef `json:"source,omitempty"`
	Revision          string           `json:"revision"`
}

// MetricQueryContext is host-resolved mapping state, not caller resource authority.
// MappingRevision fences source configuration; ProjectionRevision fences approved fields.
type MetricQueryContext struct {
	Definitions        []MetricDefinition `json:"definitions"`
	PublicScope        map[string]string  `json:"public_scope"`
	MappingRevision    string             `json:"mapping_revision"`
	ProjectionRevision string             `json:"projection_revision,omitempty"`
}

// ResourceMetricsCatalogRequest names the exact source whose mappings are requested.
type ResourceMetricsCatalogRequest struct {
	Source      MetricSourceRef    `json:"source"`
	ResourceKey string             `json:"resource_key"`
	Context     MetricQueryContext `json:"context"`
}

// ProviderConnection contains public configuration only; no operation returns secrets.
type ProviderConnection struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	PluginID       string            `json:"plugin_id"`
	Kind           string            `json:"kind"`
	URL            string            `json:"url"`
	HasCredentials bool              `json:"has_credentials"`
	PublicConfig   map[string]string `json:"public_config"`
}

// ProviderConnectionInput accepts a write-only secret: nil preserves, empty clears.
type ProviderConnectionInput struct {
	Connection ProviderConnection `json:"connection"`
	Credential *string            `json:"credential,omitempty"`
}
type MetricDataSource struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type MetricValidationIssue struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
type MetricValidationResult struct {
	Valid  bool                    `json:"valid"`
	Issues []MetricValidationIssue `json:"issues"`
}

// ProviderConfiguration runs only inside the trusted provider boundary.
type ProviderConfiguration interface {
	GetProviderConnections(*types.PluginContext) ([]ProviderConnection, error)
	SaveProviderConnection(*types.PluginContext, ProviderConnectionInput) (*ProviderConnection, error)
	DeleteProviderConnection(*types.PluginContext, string) error
	GetDataSources(*types.PluginContext, MetricSourceRef) ([]MetricDataSource, error)
	TestProviderConnection(*types.PluginContext, string) (*MetricValidationResult, error)
	ValidateMetricDefinition(*types.PluginContext, MetricSourceRef, MetricDefinition) (*MetricValidationResult, error)
}

// MetricOwnerCatalog exposes owner-authored definitions and public scope metadata.
type MetricOwnerCatalog struct {
	Definitions []MetricDefinition `json:"definitions"`
	ScopeFields []MetricScopeField `json:"scope_fields"`
}

// MetricScopeField describes one owner-approved public scope entry.
type MetricScopeField struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// MetricOwnerCatalogProvider is independent of finite execution and legacy metrics.
type MetricOwnerCatalogProvider interface {
	GetMetricOwnerCatalog(*types.PluginContext) (*MetricOwnerCatalog, error)
}

type GetMetricOwnerCatalogFunc func(*types.PluginContext) (*MetricOwnerCatalog, error)

// MetricDiscoveryRequest permits bounded exact-match discovery, not arbitrary selectors.
type MetricDiscoveryRequest struct {
	Source     MetricSourceRef   `json:"source"`
	MetricName string            `json:"metric_name"`
	LabelName  string            `json:"label_name"`
	Matchers   map[string]string `json:"matchers"`
	StartTime  time.Time         `json:"start_time"`
	EndTime    time.Time         `json:"end_time"`
	Limit      int               `json:"limit"`
}
type MetricDiscoveryResult struct {
	Values    []string `json:"values"`
	Truncated bool     `json:"truncated"`
	Warnings  []string `json:"warnings"`
}
type MetricDiscoveryProvider interface {
	DiscoverMetricLabels(*types.PluginContext, MetricDiscoveryRequest) (*MetricDiscoveryResult, error)
}

func ValidateMetricDiscovery(r MetricDiscoveryRequest) error {
	if err := validateFiniteSource(r.Source); err != nil {
		return err
	}
	if r.Limit < 1 || r.Limit > 100 || len(r.Matchers) > 32 || r.EndTime.Before(r.StartTime) {
		return metricError("INVALID_REQUEST", "Invalid bounded metric discovery request.")
	}
	return resourcev2.ValidateMetadata(r)
}
