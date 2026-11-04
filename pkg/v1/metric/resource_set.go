package metric

import (
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"time"
)

// MetricSourceReference identifies a datasource within one provider connection.
type MetricSourceReference struct {
	PluginID       string `json:"plugin_id"`
	ConnectionID   string `json:"connection_id"`
	DatasourceUID  string `json:"datasource_uid"`
	DatasourceKind string `json:"datasource_kind"`
}

// MetricResource carries only the host-approved public resource projection.
// Reference and Route belong to the resource owner, never the provider catalog.
type MetricResource struct {
	Token             string                        `json:"token"`
	ResourceKey       string                        `json:"resource_key"`
	ResourceID        string                        `json:"resource_id"`
	ResourceNamespace string                        `json:"resource_namespace"`
	Reference         *resourcev2.ResourceReference `json:"reference,omitempty"`
	Route             *resourcev2.AccessRoute       `json:"route,omitempty"`
	PublicData        *resourcev2.Value             `json:"public_data,omitempty"`
}

type MetricFieldBinding struct {
	Field     string               `json:"field"`
	Label     string               `json:"label"`
	Source    string               `json:"source"`
	Required  bool                 `json:"required"`
	Transform MetricFieldTransform `json:"transform,omitempty"`
	// ResourcePath selects one scalar leaf from the owner-admitted resource.
	// The host places the value under Field in the public projection.
	ResourcePath string `json:"resource_path,omitempty"`
}

// MetricDefinition gives stable semantics independently of a provider expression.
type MetricDefinition struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	ResourceKeys    []string             `json:"resource_keys"`
	Unit            MetricUnit           `json:"unit"`
	Semantics       string               `json:"semantics"`
	Dimensions      []string             `json:"dimensions"`
	SupportedShapes []MetricShape        `json:"supported_shapes"`
	QueryLanguage   string               `json:"query_language"`
	Query           string               `json:"query"`
	FieldBindings   []MetricFieldBinding `json:"field_bindings"`
	IdentityLabels  []string             `json:"identity_labels"`
	Reducer         string               `json:"reducer"`
	BatchStrategy   string               `json:"batch_strategy"`
	Revision        string               `json:"revision"`
	Native          bool                 `json:"native"`
}

// MetricDefinitionOverride is sparse: nil inherits the current global value.
type MetricDefinitionOverride struct {
	MetricID       string                `json:"metric_id"`
	Query          *string               `json:"query,omitempty"`
	FieldBindings  *[]MetricFieldBinding `json:"field_bindings,omitempty"`
	IdentityLabels *[]string             `json:"identity_labels,omitempty"`
	Reducer        *string               `json:"reducer,omitempty"`
	BatchStrategy  *string               `json:"batch_strategy,omitempty"`
	Revision       string                `json:"revision"`
}

type MetricBinding struct {
	ID                   string                     `json:"id"`
	Signal               string                     `json:"signal"`
	ResourcePluginID     string                     `json:"resource_plugin_id"`
	ResourceConnectionID string                     `json:"resource_connection_id"`
	Source               MetricSourceReference      `json:"source"`
	Policy               string                     `json:"policy"`
	PublicScope          map[string]string          `json:"public_scope"`
	Overrides            []MetricDefinitionOverride `json:"overrides"`
	Revision             string                     `json:"revision"`
}

type ResourceSetQueryRequest struct {
	ResourcePluginID     string                `json:"resource_plugin_id"`
	ResourceConnectionID string                `json:"resource_connection_id"`
	BindingID            string                `json:"binding_id"`
	Source               MetricSourceReference `json:"source"`
	Resources            []MetricResource      `json:"resources"`
	MetricIDs            []string              `json:"metric_ids"`
	Definitions          []MetricDefinition    `json:"definitions"`
	PublicScope          map[string]string     `json:"public_scope"`
	Shape                MetricShape           `json:"shape"`
	StartTime            time.Time             `json:"start_time"`
	EndTime              time.Time             `json:"end_time"`
	Step                 time.Duration         `json:"step"`
	MappingRevision      string                `json:"mapping_revision"`
}

type MetricResultStatus string

const (
	MetricStatusOK             MetricResultStatus = "ok"
	MetricStatusMissing        MetricResultStatus = "missing"
	MetricStatusDenied         MetricResultStatus = "denied"
	MetricStatusFailed         MetricResultStatus = "failed"
	MetricStatusPartial        MetricResultStatus = "partial"
	MetricStatusStale          MetricResultStatus = "stale"
	MetricStatusUnsupported    MetricResultStatus = "unsupported"
	MetricStatusInvalidContext MetricResultStatus = "invalid_context"
	MetricStatusAmbiguous      MetricResultStatus = "ambiguous"
)

type MetricProvenance struct {
	Source    MetricSourceReference `json:"source"`
	Native    bool                  `json:"native"`
	BindingID string                `json:"binding_id"`
}

type ResourceMetricResult struct {
	ResourceToken   string             `json:"resource_token"`
	MetricID        string             `json:"metric_id"`
	Status          MetricResultStatus `json:"status"`
	Value           *MetricResult      `json:"value,omitempty"`
	Dimensions      map[string]string  `json:"dimensions"`
	Provenance      MetricProvenance   `json:"provenance"`
	ObservedAt      time.Time          `json:"observed_at"`
	ExpiresAt       time.Time          `json:"expires_at"`
	MappingRevision string             `json:"mapping_revision"`
	Warnings        []string           `json:"warnings"`
}

type ResourceSetQueryResponse struct {
	Results  []ResourceMetricResult `json:"results"`
	Warnings []string               `json:"warnings"`
}

// ResourceSetProvider executes a finite explicit set using provider connection context.
// Hosts must admit each resource under ResourcePluginID before invoking this capability.
type ResourceSetProvider interface {
	QueryResources(*QueryContext, ResourceSetQueryRequest) (*ResourceSetQueryResponse, error)
}

// ProviderConnection contains public configuration only. Credentials remain backend-owned.
type ProviderConnection struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	PluginID       string            `json:"plugin_id"`
	Kind           string            `json:"kind"`
	URL            string            `json:"url"`
	CredentialRef  string            `json:"credential_ref,omitempty"`
	HasCredentials bool              `json:"has_credentials"`
	PublicConfig   map[string]string `json:"public_config"`
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

// ProviderConnectionInput accepts write-only credentials. An omitted credential
// preserves the existing secret. Public responses never include this field.
type ProviderConnectionInput struct {
	Connection ProviderConnection `json:"connection"`
	Credential *string            `json:"credential,omitempty"`
}

// ProviderConfiguration exposes only safe DTOs. Credential writes use an opaque
// backend reference; no read operation returns credential material.
type ProviderConfiguration interface {
	GetProviderConnections(*QueryContext) ([]ProviderConnection, error)
	SaveProviderConnection(*QueryContext, ProviderConnectionInput) (*ProviderConnection, error)
	DeleteProviderConnection(*QueryContext, string) error
	GetDataSources(*QueryContext, MetricSourceReference) ([]MetricDataSource, error)
	TestProviderConnection(*QueryContext, string) (*MetricValidationResult, error)
	ValidateMetricDefinition(*QueryContext, MetricSourceReference, MetricDefinition) (*MetricValidationResult, error)
}

// MetricCatalogProvider supplies reusable semantic definitions.
type MetricCatalogProvider interface {
	GetMetricDefinitions(*QueryContext) ([]MetricDefinition, error)
}
