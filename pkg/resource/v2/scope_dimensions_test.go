package resourcev2

import (
	"context"
	"reflect"
	"testing"
)

func selectionScope(repository string) QueryScope {
	domain := []string{"native-domain"}
	scope := QueryScope{
		{Dimension: "domain", Match: ScopeOneOf, Values: &domain},
		{Dimension: "repository", Match: ScopeAny},
	}
	if repository != "" {
		values := []string{repository}
		scope[1].Match, scope[1].Values = ScopeOneOf, &values
	}
	return scope
}

func selectionDescriptor(repository string) ResourceType {
	return ResourceType{
		Type:               "opaque/type",
		IdentityDimensions: []Dimension{{Name: "domain"}},
		QueryDimensions:    []Dimension{{Name: "domain"}, {Name: "repository"}},
		Representations:    []Representation{controllerRepresentation()},
		Capabilities: []Capability{
			{Operation: OperationGet, Scope: selectionScope(repository)},
			{Operation: OperationEnumerate, Scope: selectionScope("")},
			{Operation: OperationNativeQuery, Scope: selectionScope("")},
		},
	}
}

func selectionRecord(repository string) Record {
	record := controllerRecord()
	record.Reference.Address.Scope = []ScopeEntry{{Dimension: "domain", Value: "native-domain"}}
	record.Body.Map["observed_repository"] = Value{Kind: "string", String: repository}
	return record
}

func selectionConfig(repository string) Config[string] {
	return Config[string]{Catalog: func(context.Context, CatalogRequest) (Catalog, error) {
		return Catalog{
			Provider: "test",
			Revision: "selection-1",
			Coverage: Coverage{Kind: "complete", Paths: []string{}},
			Types:    []ResourceType{selectionDescriptor(repository)},
		}, nil
	}}
}

func TestQueryOnlyDimensionsDoNotBecomeIdentityOrImplicitAcquisition(t *testing.T) {
	cfg := selectionConfig("")
	record := selectionRecord("repository-X")
	calls := 0
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Get: func(_ context.Context, _ *string, request GetRequest) (Record, error) {
			calls++
			if !sameAddress(request.Reference.Address, record.Reference.Address) {
				t.Fatal("identity changed before native Get")
			}
			return record, nil
		}},
	}
	controller := newTestController(t, cfg)
	request := GetRequest{
		Route:          AccessRoute{Connection: "explicit"},
		Reference:      record.Reference,
		Representation: "document",
	}
	actual, err := controller.Get(acceptedControllerContext(t, context.Background(), controller), request)
	if err != nil || calls != 1 || !reflect.DeepEqual(actual.Reference, record.Reference) {
		t.Fatalf("Get changed identity: %+v %v", actual, err)
	}
	scope, exact := queryScopeForIdentity(record.Reference.Address, selectionDescriptor("").QueryDimensions)
	if exact || !reflect.DeepEqual(scope, selectionScope("")) {
		t.Fatalf("selection-only scope was invented: %+v exact=%v", scope, exact)
	}
	// The false exact flag prevents the derived unknown selection from starting
	// a source; it qualifies support only, not enumeration or watch acquisition.
	if len(actual.Reference.Address.Scope) != 1 {
		t.Fatal("query-only repository leaked into identity")
	}
}

func TestQueryOnlyRestrictedGetDoesNotGuessOpaqueID(t *testing.T) {
	for _, restriction := range []string{"catalog", "registration"} {
		t.Run(restriction, func(t *testing.T) {
			cfg := selectionConfig("")
			if restriction == "catalog" {
				cfg = selectionConfig("repository-X")
			}
			calls := 0
			registration := Registration[string]{
				Type: "opaque/type",
				Get: func(context.Context, *string, GetRequest) (Record, error) {
					calls++
					return selectionRecord("repository-X"), nil
				},
			}
			if restriction == "registration" {
				registration.Scope = selectionScope("repository-X")
			}
			cfg.Registrations = []Registration[string]{registration}
			controller := newTestController(t, cfg)
			record := selectionRecord("repository-X")
			record.Reference.Address.ID = "repository-X/opaque-looking-ID"
			_, err := controller.Get(
				acceptedControllerContext(t, context.Background(), controller),
				GetRequest{
					Route:          AccessRoute{Connection: "explicit"},
					Reference:      record.Reference,
					Representation: "document",
				},
			)
			if ErrorCode(err) != "UNSUPPORTED" || calls != 0 {
				t.Fatalf("restricted Get broadened or parsed ID: %v calls=%d", err, calls)
			}
		})
	}
}

func TestQueryOnlyNativeSelectionAndCoverageRemainExplicit(t *testing.T) {
	cfg := selectionConfig("")
	native := func(requestTarget ReadTarget) (SourcePage, error) {
		if len(requestTarget.Scope) != 2 || requestTarget.Scope[1].Values == nil ||
			len(*requestTarget.Scope[1].Values) != 1 {
			t.Fatal("native query lost explicit repository")
		}
		repository := (*requestTarget.Scope[1].Values)[0]
		record := selectionRecord(repository)
		// Exact repository membership is established here by the native adapter,
		// never inferred by the generic boundary from an address or opaque ID.
		return SourcePage{
			Records: []Record{record},
			Count:   Count{Kind: "unknown"},
			Coverage: Coverage{
				Kind:   "partial",
				Paths:  []string{},
				Reason: "native repository acquisition is incomplete",
			},
			Consistency: "bestEffort",
		}, nil
	}
	cfg.Registrations = []Registration[string]{
		{
			Type: "opaque/type",
			Enumerate: func(_ context.Context, _ *string, request EnumerateRequest) (SourcePage, error) {
				return native(request.Target)
			},
			NativeQuery: func(_ context.Context, _ *string, request NativeQueryRequest) (SourcePage, error) {
				return native(request.Target)
			},
		},
	}
	controller := newTestController(t, cfg)
	for _, repository := range []string{"repository-X", "repository-Y"} {
		target := controllerTarget()
		target.Scope = selectionScope(repository)
		for _, query := range []bool{false, true} {
			var page SourcePage
			var err error
			if query {
				page, err = controller.NativeQuery(
					acceptedControllerContext(t, context.Background(), controller),
					NativeQueryRequest{
						Target:   target,
						PageSize: 1,
						Language: "native",
						Query:    Value{Kind: "map", Map: map[string]Value{}},
					},
				)
			} else {
				page, err = controller.Enumerate(
					acceptedControllerContext(t, context.Background(), controller),
					EnumerateRequest{Target: target, PageSize: 1},
				)
			}
			if err != nil {
				t.Fatal(err)
			}
			if page.Coverage.Kind != "partial" || page.Count.Kind != "unknown" ||
				page.Records[0].Body.Map["observed_repository"].String != repository {
				t.Fatalf("native selection/coverage changed: %+v", page)
			}
			if len(page.Records[0].Reference.Address.Scope) != 1 {
				t.Fatal("selection-only dimension inserted into identity")
			}
		}
	}
	escaped := selectionRecord("repository-X")
	escaped.Reference.Address.Scope[0].Value = "other-domain"
	if err := checkRecord(
		escaped,
		selectionDescriptor(""),
		"test",
		"authority",
		"document",
		nil,
		selectionScope("repository-X"),
	); ErrorCode(
		err,
	) != "INTERNAL" {
		t.Fatalf("shared identity scope escape admitted: %v", err)
	}
}

func TestScopeForAddressUsesValidatedIdentityOnly(t *testing.T) {
	record := selectionRecord("repository-X")
	scope, err := ScopeForAddress(record.Reference.Address, selectionDescriptor(""))
	if err != nil || !reflect.DeepEqual(scope, selectionScope("")) {
		t.Fatalf("public projection changed unknown selection: %+v %v", scope, err)
	}
	record.Reference.Address.Scope = append(
		record.Reference.Address.Scope,
		ScopeEntry{Dimension: "repository", Value: "repository-X"},
	)
	if _, err := ScopeForAddress(
		record.Reference.Address,
		selectionDescriptor(""),
	); ErrorCode(
		err,
	) != "INVALID_REQUEST" {
		t.Fatalf("query-only identity entry admitted: %v", err)
	}
}
