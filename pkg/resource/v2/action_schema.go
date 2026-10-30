package resourcev2

import (
	"bytes"
	"context"
	"encoding/json"
)

// ValidateActionDescriptorRequest checks the advertised target, lane, and input.
func ValidateActionDescriptorRequest(request ActionRequest, descriptor ActionDescriptor, streaming bool) error {
	if err := ValidateActionDescriptors([]ActionDescriptor{descriptor}); err != nil {
		return err
	}
	if request.ExpectedDescriptor != nil && !equalActionDescriptors(*request.ExpectedDescriptor, descriptor) {
		return NewError("RESET_REQUIRED", "Action descriptor changed.")
	}
	if descriptor.ID != request.Action {
		return invalid("action descriptor does not match request")
	}
	if (request.TypeTarget != nil) != (descriptor.Scope == ActionScopeType) {
		return invalid("action scope does not match target")
	}
	if descriptor.Streaming != streaming {
		return NewError(errorCodeUnsupported, "Action execution lane is unsupported.")
	}
	return ValidateActionInput(descriptor, request.Input)
}

func equalActionDescriptors(a, b ActionDescriptor) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

// ResolveActionDescriptor retains an optional discovered declaration for validation.
// Nil means discovery is unavailable, not that any schema or execution hint was asserted.
func ResolveActionDescriptor(
	ctx context.Context,
	provider any,
	request ActionRequest,
	streaming bool,
) (*ActionDescriptor, error) {
	if err := ValidateActionRequest(request); err != nil {
		return nil, err
	}
	if resolver, ok := provider.(interface {
		resolveActionDescriptor(context.Context, ActionRequest, bool) (*ActionDescriptor, error)
	}); ok {
		return resolver.resolveActionDescriptor(ctx, request, streaming)
	}
	catalog, ok := provider.(ActionCatalog)
	if !ok {
		return unavailableActionDescriptor(request)
	}
	target := actionOperationTarget(request)
	descriptors, err := catalog.GetActions(ctx, request.Route, target.typ)
	if err != nil {
		if ErrorCode(err) == errorCodeUnsupported {
			return unavailableActionDescriptor(request)
		}
		return nil, err
	}
	return selectActionDescriptor(request, descriptors, streaming)
}

func unavailableActionDescriptor(request ActionRequest) (*ActionDescriptor, error) {
	if request.ExpectedDescriptor != nil {
		return nil, NewError("RESET_REQUIRED", "Expected action descriptor is unavailable.")
	}
	return nil, nil //nolint:nilnil // Nil is valid absent optional discovery metadata, not a missing required result.
}

func selectActionDescriptor(
	request ActionRequest,
	descriptors []ActionDescriptor,
	streaming bool,
) (*ActionDescriptor, error) {
	if descriptors == nil {
		return unavailableActionDescriptor(request)
	}
	if err := ValidateActionDescriptors(descriptors); err != nil {
		return nil, err
	}
	for _, descriptor := range descriptors {
		if descriptor.ID == request.Action {
			descriptor = cloneActionDescriptor(descriptor)
			return &descriptor, ValidateActionDescriptorRequest(request, descriptor, streaming)
		}
	}
	if request.ExpectedDescriptor != nil {
		return nil, NewError("RESET_REQUIRED", "Expected action descriptor is unavailable.")
	}
	return nil, NewError(errorCodeUnsupported, "Action is not advertised.")
}

// ValidateResolvedActionOutput validates only constraints actually discovered.
func ValidateResolvedActionOutput(descriptor *ActionDescriptor, value Value) error {
	if descriptor == nil {
		return ValidateValue(value)
	}
	return ValidateActionOutput(*descriptor, value)
}

// GetActions returns route/type descriptors whose declarations agree across scopes.
// Conflicting IDs are omitted because this discovery API cannot express their scope.
func (c *Controller[T]) GetActions(ctx context.Context, route AccessRoute, typ string) ([]ActionDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRoute(route); err != nil {
		return nil, err
	}
	registrations, err := c.registrations(ctx, route, typ)
	if err != nil {
		return nil, err
	}
	return mergeScopedActionDescriptors(registrations)
}

// mergeScopedActionDescriptors preserves unknown versus explicitly empty discovery.
// A conflicting ID cannot describe all scopes, so it is omitted from the catalog.
func mergeScopedActionDescriptors[T any](registrations []Registration[T]) ([]ActionDescriptor, error) {
	var out []ActionDescriptor
	positions := make(map[string]int)
	conflicts := make(map[string]bool)
	for _, registration := range registrations {
		if err := ValidateActionDescriptors(registration.Actions); err != nil {
			return nil, err
		}
		if registration.Actions != nil && out == nil {
			out = make([]ActionDescriptor, 0)
		}
		for _, descriptor := range registration.Actions {
			if index, exists := positions[descriptor.ID]; exists {
				if !equalActionDescriptors(out[index], descriptor) {
					conflicts[descriptor.ID] = true
				}
			} else {
				positions[descriptor.ID] = len(out)
				out = append(out, descriptor)
			}
		}
	}
	filtered := out[:0]
	for _, descriptor := range out {
		if !conflicts[descriptor.ID] {
			filtered = append(filtered, descriptor)
		}
	}
	return cloneActionDescriptors(filtered), nil
}

// resolveActionDescriptor selects the same scoped registration as finite dispatch.
func (c *Controller[T]) resolveActionDescriptor(
	ctx context.Context,
	request ActionRequest,
	streaming bool,
) (descriptor *ActionDescriptor, err error) {
	var registration *Registration[T]
	defer c.guard(&registration, &err)
	if streaming {
		return descriptor, NewError(errorCodeUnsupported, "Streaming action handler is not implemented.")
	}
	target := actionOperationTarget(request)
	registration, _, err = c.resolve(
		ctx,
		target.route,
		target.provider,
		target.typ,
		"",
		target.scope,
		target.address,
		OperationAction,
	)
	if err != nil {
		return descriptor, err
	}
	if registration.Actions == nil {
		return unavailableActionDescriptor(request)
	}
	return selectActionDescriptor(request, registration.Actions, false)
}
