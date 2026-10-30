package resourcev2

import "context"

// Feature reports availability of an ancillary transport trait, not support for
// a finite operation. Finite support always comes from the qualified catalog.
type Feature string

const (
	FeatureConnections         Feature = "connections"
	FeatureConnectionReadiness Feature = "connectionReadiness"
	FeatureConnectionBrowser   Feature = "connectionBrowser"
	FeatureConnectionOverlay   Feature = "connectionOverlay"
	FeatureScopeValues         Feature = "scopeValues"
	FeaturePresentation        Feature = "presentation"
	FeatureEditorSchemas       Feature = "editorSchemas"
	FeatureSchemaDocuments     Feature = "schemaDocuments"
	FeatureRelationships       Feature = "relationships"
	FeatureHealth              Feature = "health"
	FeatureActionCatalog       Feature = "actionCatalog"
	FeatureStreamingActions    Feature = "streamingActions"
	FeatureBestEffortUpdates   Feature = "bestEffortUpdates"
)

// FeatureSet lists ancillary traits available on this provider or transport.
// Nil or empty Traits advertises none; duplicates and unknown traits are invalid.
// Callers must not mutate returned Traits concurrently with SDK use.
type FeatureSet struct {
	Traits []Feature `json:"traits"`
}

// Has tests only the advertised trait list. It does not authorize an operation
// or replace scope-qualified catalog admission.
func (f FeatureSet) Has(trait Feature) bool {
	for _, candidate := range f.Traits {
		if candidate == trait {
			return true
		}
	}
	return false
}

// FeatureNegotiator is implemented by the transport client and configurable
// controllers. Hosts call it before optional lifecycle/stream startup. A remote
// client's Go method set is never evidence that its provider supports a trait.
type FeatureNegotiator interface {
	Features(context.Context) (FeatureSet, error)
}

// ValidateFeatures rejects duplicate or unknown trait names without changing f.
// Unknown traits return UNSUPPORTED; malformed duplicates return INVALID_REQUEST.
func ValidateFeatures(f FeatureSet) error {
	seen := map[Feature]bool{}
	for _, feature := range f.Traits {
		if seen[feature] {
			return NewError("INVALID_REQUEST", "duplicate ancillary feature")
		}
		seen[feature] = true
		switch feature {
		case FeatureConnections,
			FeatureConnectionReadiness,
			FeatureConnectionBrowser,
			FeatureConnectionOverlay,
			FeatureScopeValues,
			FeaturePresentation,
			FeatureEditorSchemas,
			FeatureSchemaDocuments,
			FeatureRelationships,
			FeatureHealth,
			FeatureActionCatalog,
			FeatureStreamingActions,
			FeatureBestEffortUpdates:
		default:
			return NewError("UNSUPPORTED", "unknown ancillary feature")
		}
	}
	return nil
}

// DetectFeatures derives real optional interfaces unless a configurable provider
// explicitly reports which of its transport traits are installed.
func DetectFeatures(ctx context.Context, p Provider) (FeatureSet, error) {
	if explicit, ok := p.(FeatureNegotiator); ok {
		f, err := explicit.Features(ctx)
		if err != nil {
			return FeatureSet{}, err
		}
		return f, ValidateFeatures(f)
	}
	f := FeatureSet{Traits: []Feature{}}
	if _, ok := p.(ConnectionLifecycle); ok {
		f.Traits = append(f.Traits, FeatureConnections)
	}
	if _, ok := p.(ConnectionReadinessProvider); ok {
		f.Traits = append(f.Traits, FeatureConnectionReadiness)
	}
	if _, ok := p.(ConnectionBrowser); ok {
		f.Traits = append(f.Traits, FeatureConnectionBrowser)
	}
	if _, ok := p.(ConnectionOverlayProvider); ok {
		f.Traits = append(f.Traits, FeatureConnectionOverlay)
	}
	if _, ok := p.(ScopeValuesProvider); ok {
		f.Traits = append(f.Traits, FeatureScopeValues)
	}
	if _, ok := p.(PresentationProvider); ok {
		f.Traits = append(f.Traits, FeaturePresentation)
	}
	if _, ok := p.(EditorSchemaProvider); ok {
		f.Traits = append(f.Traits, FeatureEditorSchemas)
	}
	if _, ok := p.(SchemaDocumentProvider); ok {
		f.Traits = append(f.Traits, FeatureSchemaDocuments)
	}
	if _, ok := p.(RelationshipProvider); ok {
		f.Traits = append(f.Traits, FeatureRelationships)
	}
	if _, ok := p.(HealthProvider); ok {
		f.Traits = append(f.Traits, FeatureHealth)
	}
	if _, ok := p.(ActionCatalog); ok {
		f.Traits = append(f.Traits, FeatureActionCatalog)
	}
	if _, ok := p.(StreamingActioner); ok {
		f.Traits = append(f.Traits, FeatureStreamingActions)
	}
	if _, ok := p.(BestEffortUpdateProvider); ok {
		f.Traits = append(f.Traits, FeatureBestEffortUpdates)
	}
	return f, nil
}
