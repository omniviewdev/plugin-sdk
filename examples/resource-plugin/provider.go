package main

import (
	"context"
	"embed"
	"io/fs"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

const (
	pluginID         = "notes"
	resourceTypeID   = "note"
	representationID = "native"
)

//go:embed data/*.json
var notes embed.FS

func newProvider() (*resourcev2.Controller[struct{}], error) {
	return resourcev2.NewController(resourcev2.Config[struct{}]{
		Catalog: catalog,
		Registrations: []resourcev2.Registration[struct{}]{{
			Type: resourceTypeID,
			Get:  getNote,
		}},
	})
}

func nativeRepresentation() resourcev2.Representation {
	return resourcev2.Representation{
		ID:       representationID,
		Revision: "1",
		Schema:   resourcev2.SchemaBinding{State: "absent", Validation: "none"},
	}
}

func checkRoute(ctx context.Context, route resourcev2.AccessRoute) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if route.Connection != "local" {
		return resourcev2.NewError("INVALID_REQUEST", "Select the local connection.")
	}
	return nil
}

func catalog(ctx context.Context, request resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	if err := checkRoute(ctx, request.Route); err != nil {
		return resourcev2.Catalog{}, err
	}
	return resourcev2.Catalog{
		Provider: pluginID,
		Revision: "embedded-1",
		Coverage: resourcev2.Coverage{Kind: "complete"},
		Types: []resourcev2.ResourceType{{
			Type:                  resourceTypeID,
			DisplayName:           "Note",
			Representations:       []resourcev2.Representation{nativeRepresentation()},
			DefaultRepresentation: representationID,
			Capabilities: []resourcev2.Capability{{
				Operation: resourcev2.OperationGet,
				Scope:     resourcev2.QueryScope{},
			}},
		}},
	}, nil
}

func getNote(ctx context.Context, _ *struct{}, request resourcev2.GetRequest) (resourcev2.Record, error) {
	if err := checkRoute(ctx, request.Route); err != nil {
		return resourcev2.Record{}, err
	}
	// This fixed collection has one ID. Never turn arbitrary IDs into file paths.
	if request.Reference.Address.ID != "welcome" {
		return resourcev2.Record{}, resourcev2.NewError("NOT_FOUND", "The note does not exist.")
	}
	data, err := fs.ReadFile(notes, "data/welcome.json")
	if err != nil {
		return resourcev2.Record{}, &resourcev2.ResourceOperationError{
			Code: "INTERNAL", Message: "The embedded note could not be read.", Err: err,
		}
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return resourcev2.Record{}, contextErr
	}
	// Do not decode through map[string]any: float64 would lose integer fidelity.
	body, err := resourcev2.JSONToValue(data)
	if err != nil {
		return resourcev2.Record{}, &resourcev2.ResourceOperationError{
			Code: "INTERNAL", Message: "The embedded note could not be decoded.", Err: err,
		}
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return resourcev2.Record{}, contextErr
	}
	return resourcev2.Record{
		Reference:      request.Reference,
		Representation: nativeRepresentation(),
		Body:           body,
		Coverage:       resourcev2.Coverage{Kind: "complete"},
	}, nil
}
