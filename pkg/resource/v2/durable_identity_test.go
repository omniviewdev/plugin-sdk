package resourcev2

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

var testEnvironment = EnvironmentIdentity{Scheme: "kubernetes.namespace-uid.v1", ID: "0b0c8c6e-7d0f-4a4e-9a5b-1a2b3c4d5e6f"}

func durableTestReference(authority string, incarnation *string, scope ...ScopeEntry) ResourceReference {
	return ResourceReference{
		Address: ResourceAddress{Provider: "kubernetes", Authority: authority, Type: "k8s:/pods", Scope: scope, ID: "web-0"},
		// The incarnation is the native object UID.
		Incarnation: incarnation,
	}
}

func mustDurable(t *testing.T, environment EnvironmentIdentity, reference ResourceReference) DurableReference {
	t.Helper()
	durable, err := NewDurableReference(environment, reference)
	if err != nil {
		t.Fatal(err)
	}
	return durable
}

func mustKey(t *testing.T, address DurableAddress) string {
	t.Helper()
	key, err := address.Key()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestDurableReferenceIgnoresHostAuthorityAndScopeOrder(t *testing.T) {
	uid := "pod-uid-1"
	first := mustDurable(t, testEnvironment, durableTestReference("local:one", &uid,
		ScopeEntry{Dimension: "namespace", Value: "default"}, ScopeEntry{Dimension: "cluster-shard", Value: "a"}))
	// A retired authority, another route's authority and another scope order are
	// the cases a connection stop, a profile edit and a restart produce.
	for _, authority := range []string{"local:two", "local:three", "other-host-issued-authority"} {
		again := mustDurable(t, testEnvironment, durableTestReference(authority, &uid,
			ScopeEntry{Dimension: "cluster-shard", Value: "a"}, ScopeEntry{Dimension: "namespace", Value: "default"}))
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("authority %q changed the durable reference:\n%#v\n%#v", authority, first, again)
		}
		if mustKey(t, first.Address) != mustKey(t, again.Address) {
			t.Fatalf("authority %q changed the durable key", authority)
		}
	}
	if first.Address.Scope[0].Dimension != "cluster-shard" {
		t.Fatalf("scope is not ordered by dimension: %v", first.Address.Scope)
	}
}

func TestDurableAddressKeyDistinguishesTargetsAndObjects(t *testing.T) {
	base := mustDurable(t, testEnvironment, durableTestReference("a", nil, ScopeEntry{Dimension: "namespace", Value: "default"}))
	baseKey := mustKey(t, base.Address)
	mutations := map[string]func(*DurableAddress){
		"environment id":       func(a *DurableAddress) { a.Environment.ID = "another-cluster" },
		"environment scheme":   func(a *DurableAddress) { a.Environment.Scheme = "kubernetes.namespace-uid.v2" },
		"provider":             func(a *DurableAddress) { a.Provider = "other" },
		"type":                 func(a *DurableAddress) { a.Type = "k8s:/services" },
		"identifier":           func(a *DurableAddress) { a.ID = "web-1" },
		"scope value":          func(a *DurableAddress) { a.Scope[0].Value = "kube-system" },
		"scope dimension name": func(a *DurableAddress) { a.Scope[0].Dimension = "ns" },
		"scope presence":       func(a *DurableAddress) { a.Scope = []ScopeEntry{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base.Address
			changed.Scope = append([]ScopeEntry(nil), base.Address.Scope...)
			mutate(&changed)
			if mustKey(t, changed) == baseKey {
				t.Fatalf("changing %s kept the key %s", name, baseKey)
			}
		})
	}
}

func TestDurableAddressKeyIsNotAmbiguous(t *testing.T) {
	// Fields that merely look alike when concatenated must still differ.
	a := DurableAddress{Environment: EnvironmentIdentity{Scheme: "s.v1", ID: `a","b`}, Provider: "p", Type: "t", Scope: []ScopeEntry{}, ID: "i"}
	b := DurableAddress{Environment: EnvironmentIdentity{Scheme: "s.v1", ID: "a"}, Provider: `b","p`, Type: "t", Scope: []ScopeEntry{}, ID: "i"}
	if mustKey(t, a) == mustKey(t, b) {
		t.Fatal("delimiter characters moved bytes between key fields")
	}
	// Strings stay opaque: no normalization, case folding or path cleaning.
	for _, pair := range [][2]string{{"Pod", "pod"}, {"a/b", "a//b"}, {"caf\u00e9", "cafe\u0301"}} {
		left, right := a, a
		left.ID, right.ID = pair[0], pair[1]
		if mustKey(t, left) == mustKey(t, right) {
			t.Fatalf("%q and %q share a key", pair[0], pair[1])
		}
	}
	if !strings.HasPrefix(mustKey(t, a), `["omniview.durable-address.v1",`) {
		t.Fatalf("key is not version tagged: %s", mustKey(t, a))
	}
}

func TestDurableReferenceCarriesIncarnationOutsideTheKey(t *testing.T) {
	first, second := "uid-before-delete", "uid-after-recreate"
	before := mustDurable(t, testEnvironment, durableTestReference("a", &first))
	after := mustDurable(t, testEnvironment, durableTestReference("a", &second))
	unknown := mustDurable(t, testEnvironment, durableTestReference("a", nil))
	if mustKey(t, before.Address) != mustKey(t, after.Address) || mustKey(t, before.Address) != mustKey(t, unknown.Address) {
		t.Fatal("incarnation changed the identity key")
	}
	if reflect.DeepEqual(before, after) {
		t.Fatal("delete and recreate is indistinguishable from an update")
	}
	if *before.Incarnation != first || *after.Incarnation != second || unknown.Incarnation != nil {
		t.Fatal("incarnation was not carried through")
	}
}

func TestNewDurableReferenceOwnsItsStorage(t *testing.T) {
	uid := "uid"
	reference := durableTestReference("a", &uid, ScopeEntry{Dimension: "namespace", Value: "default"})
	durable := mustDurable(t, testEnvironment, reference)
	reference.Address.Scope[0].Value = "changed"
	uid = "changed"
	if durable.Address.Scope[0].Value != "default" || *durable.Incarnation != "uid" {
		t.Fatal("durable reference aliases the caller's storage")
	}
	empty := mustDurable(t, testEnvironment, durableTestReference("a", nil))
	if empty.Address.Scope == nil {
		t.Fatal("an address without dimensions must carry an empty, non-nil scope")
	}
	if !reflect.DeepEqual(empty, mustDurable(t, testEnvironment, durableTestReference("b", nil, []ScopeEntry{}...))) {
		t.Fatal("nil and empty scope produced different durable references")
	}
}

func TestNewDurableReferenceRejectsInvalidInput(t *testing.T) {
	good := durableTestReference("a", nil)
	cases := map[string]struct {
		environment EnvironmentIdentity
		reference   ResourceReference
	}{
		"empty scheme":      {EnvironmentIdentity{Scheme: "", ID: "x"}, good},
		"empty id":          {EnvironmentIdentity{Scheme: "s.v1", ID: ""}, good},
		"invalid utf-8":     {EnvironmentIdentity{Scheme: "s.v1", ID: "\xff"}, good},
		"control character": {EnvironmentIdentity{Scheme: "s.v1", ID: "a\nb"}, good},
		"delete character":  {EnvironmentIdentity{Scheme: "s.v1", ID: "a\x7fb"}, good},
		"oversized scheme":  {EnvironmentIdentity{Scheme: strings.Repeat("s", MaxEnvironmentSchemeBytes+1), ID: "x"}, good},
		"oversized id":      {EnvironmentIdentity{Scheme: "s.v1", ID: strings.Repeat("x", MaxEnvironmentIDBytes+1)}, good},
		"missing authority": {testEnvironment, durableTestReference("", nil)},
		"duplicate dimension": {testEnvironment, durableTestReference("a", nil,
			ScopeEntry{Dimension: "namespace", Value: "a"}, ScopeEntry{Dimension: "namespace", Value: "b"})},
		"empty incarnation": {testEnvironment, durableTestReference("a", new(string))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewDurableReference(tc.environment, tc.reference); err == nil {
				t.Fatal("invalid durable reference was accepted")
			}
		})
	}
	limit := EnvironmentIdentity{Scheme: strings.Repeat("s", MaxEnvironmentSchemeBytes), ID: strings.Repeat("x", MaxEnvironmentIDBytes)}
	if _, err := NewDurableReference(limit, good); err != nil {
		t.Fatalf("values at the limit were rejected: %v", err)
	}
}

func TestDurableAddressKeyRejectsHandBuiltInvalidAddress(t *testing.T) {
	for name, address := range map[string]DurableAddress{
		"zero":                 {},
		"missing environment":  {Provider: "p", Type: "t", ID: "i"},
		"duplicate dimensions": {Environment: testEnvironment, Provider: "p", Type: "t", ID: "i", Scope: []ScopeEntry{{Dimension: "d", Value: "1"}, {Dimension: "d", Value: "2"}}},
		"invalid scope value":  {Environment: testEnvironment, Provider: "p", Type: "t", ID: "i", Scope: []ScopeEntry{{Dimension: "d", Value: "\xff"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := address.Key(); err == nil {
				t.Fatal("invalid address produced a key")
			}
		})
	}
}

func TestDurableIdentityJSONIsStrict(t *testing.T) {
	uid := "pod-uid"
	original := mustDurable(t, testEnvironment, durableTestReference("a", &uid, ScopeEntry{Dimension: "namespace", Value: "default"}))
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DurableReference
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip changed the reference:\n%#v\n%#v", original, decoded)
	}
	env := `{"scheme":"s.v1","id":"x"}`
	address := `{"environment":` + env + `,"provider":"p","type":"t","scope":[],"id":"i"}`
	for name, source := range map[string]string{
		"environment unknown member":     `{"scheme":"s.v1","id":"x","extra":true}`,
		"environment duplicate member":   `{"scheme":"s.v1","scheme":"s.v2","id":"x"}`,
		"environment missing id":         `{"scheme":"s.v1"}`,
		"environment null":               `null`,
		"address missing scope":          `{"environment":` + env + `,"provider":"p","type":"t","id":"i"}`,
		"address null scope":             `{"environment":` + env + `,"provider":"p","type":"t","scope":null,"id":"i"}`,
		"address authority":              `{"environment":` + env + `,"provider":"p","authority":"a","type":"t","scope":[],"id":"i"}`,
		"reference null incarnation":     `{"address":` + address + `,"incarnation":null}`,
		"reference empty incarnation":    `{"address":` + address + `,"incarnation":""}`,
		"reference missing address":      `{"incarnation":"x"}`,
		"reference invalid environment":  `{"address":{"environment":{"scheme":"","id":"x"},"provider":"p","type":"t","scope":[],"id":"i"}}`,
		"reference duplicate dimensions": `{"address":{"environment":` + env + `,"provider":"p","type":"t","scope":[{"dimension":"d","value":"1"},{"dimension":"d","value":"2"}],"id":"i"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var target any = &DurableReference{}
			if strings.HasPrefix(name, "environment") {
				target = &EnvironmentIdentity{}
			} else if strings.HasPrefix(name, "address") {
				target = &DurableAddress{}
			}
			if err := json.Unmarshal([]byte(source), target); ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("class %s: %v", ErrorCode(err), err)
			}
		})
	}
	var unordered DurableAddress
	source := `{"environment":` + env + `,"provider":"p","type":"t","scope":[{"dimension":"b","value":"2"},{"dimension":"a","value":"1"}],"id":"i"}`
	if err = json.Unmarshal([]byte(source), &unordered); err != nil {
		t.Fatal(err)
	}
	if unordered.Scope[0].Dimension != "a" {
		t.Fatalf("decoded scope is not ordered: %v", unordered.Scope)
	}
}

func durableTestCatalog(environments ...*EnvironmentIdentity) Catalog {
	catalog := Catalog{Provider: "p", Revision: "r", Coverage: Coverage{Kind: "complete", Paths: []string{}}, Types: []ResourceType{}}
	for i, environment := range environments {
		catalog.Types = append(catalog.Types, ResourceType{
			Type: "type/" + string(rune('a'+i)), IdentityDimensions: []Dimension{}, QueryDimensions: []Dimension{},
			Representations: []Representation{}, Capabilities: []Capability{}, Environment: environment,
		})
	}
	return catalog
}

func TestCatalogEnvironmentValidationAndTransport(t *testing.T) {
	cluster := &EnvironmentIdentity{Scheme: "kubernetes.namespace-uid.v1", ID: "cluster-a"}
	other := &EnvironmentIdentity{Scheme: "helm.local.v1", ID: "machine"}
	catalog := durableTestCatalog(cluster, nil, other, cluster)
	if err := ValidateCatalog(catalog); err != nil {
		t.Fatal(err)
	}

	// Protobuf: present on exactly the declaring types, with independent storage.
	wire, err := ToProtoCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if wire.Types[0].Environment == nil || wire.Types[1].Environment != nil || wire.Types[2].Environment == nil {
		t.Fatal("environment was not declared per type")
	}
	decoded, err := FromProtoCatalog(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(catalog, decoded) {
		t.Fatalf("protobuf round trip changed the catalog:\n%#v\n%#v", catalog, decoded)
	}
	decoded.Types[0].Environment.ID = "changed"
	if decoded.Types[3].Environment.ID != "cluster-a" || cluster.ID != "cluster-a" {
		t.Fatal("decoded environments share storage")
	}

	// JSON: omitted when absent, strict when present.
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"environment"`) != 3 {
		t.Fatalf("environment must be omitted on types that declare none: %s", data)
	}
	var fromJSON Catalog
	if err = json.Unmarshal(data, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(catalog, fromJSON) {
		t.Fatal("JSON round trip changed the catalog")
	}
	nullEnvironment := `{"type":"vm","identityDimensions":[],"queryDimensions":[],"representations":[],"capabilities":[],"environment":null}`
	if err = json.Unmarshal([]byte(nullEnvironment), &ResourceType{}); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("null environment accepted: %v", err)
	}

	for name, bad := range map[string]EnvironmentIdentity{
		"empty":        {},
		"control":      {Scheme: "s.v1", ID: "a\tb"},
		"oversized id": {Scheme: "s.v1", ID: strings.Repeat("x", MaxEnvironmentIDBytes+1)},
	} {
		invalid := durableTestCatalog(&bad)
		if err := ValidateCatalog(invalid); err == nil {
			t.Fatalf("%s environment passed catalog validation", name)
		}
		if _, err := ToProtoCatalog(invalid); err == nil {
			t.Fatalf("%s environment crossed the transport adapter", name)
		}
	}
}

func TestSourceContractFencesEnvironmentChange(t *testing.T) {
	request := controllerSourceOpen(SourceModeSnapshotAndUpdates)
	catalog, err := controllerConfig().Catalog(t.Context(), CatalogRequest{Route: request.Target.Route})
	if err != nil {
		t.Fatal(err)
	}
	catalog.Revision = boundCatalogRevision([32]byte{1}, "support-1")
	ctx := WithCatalogRevisionForRoute(t.Context(), catalog.Provider, request.Target.Route, catalog.Revision)
	admitted := catalog.Types[0]
	admitted.Environment = &EnvironmentIdentity{Scheme: "fixture.v1", ID: "cluster-a"}

	same := catalog
	same.Types = []ResourceType{admitted}
	same.Types[0].Environment = &EnvironmentIdentity{Scheme: "fixture.v1", ID: "cluster-a"}
	if !SourceOperationalContractEqual(admitted, same.Types[0], request) {
		t.Fatal("an equal environment declaration retired the source")
	}
	if err = CheckSourceCatalog(ctx, same, request, admitted); err != nil {
		t.Fatalf("equal environment: %v", err)
	}

	for name, environment := range map[string]*EnvironmentIdentity{
		"another target":  {Scheme: "fixture.v1", ID: "cluster-b"},
		"another meaning": {Scheme: "fixture.v2", ID: "cluster-a"},
		"newly withdrawn": nil,
	} {
		t.Run(name, func(t *testing.T) {
			changed := catalog
			changed.Types = []ResourceType{admitted}
			changed.Types[0].Environment = environment
			if SourceOperationalContractEqual(admitted, changed.Types[0], request) {
				t.Fatal("changed environment kept the source contract")
			}
			if err := CheckSourceCatalog(ctx, changed, request, admitted); ErrorCode(err) != "RESET_REQUIRED" {
				t.Fatalf("changed environment class %s: %v", ErrorCode(err), err)
			}
		})
	}

	// Learning an environment the admitted descriptor lacked is also a change:
	// an open source keeps one environment for its whole life.
	unknown := admitted
	unknown.Environment = nil
	learned := catalog
	learned.Types = []ResourceType{admitted}
	if err = CheckSourceCatalog(ctx, learned, request, unknown); ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("newly known environment class %s: %v", ErrorCode(err), err)
	}
	// Partial discovery cannot prove that an environment disappeared.
	partial := catalog
	partial.Coverage = Coverage{Kind: "partial", Reason: "Discovery incomplete."}
	partial.Types = []ResourceType{admitted}
	partial.Types[0].Environment = nil
	if err = CheckSourceCatalog(ctx, partial, request, admitted); ErrorCode(err) != "UNAVAILABLE" {
		t.Fatalf("partial discovery class %s: %v", ErrorCode(err), err)
	}
}
