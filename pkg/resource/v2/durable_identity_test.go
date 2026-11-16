package resourcev2

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const (
	testUIDScheme = "kubernetes.namespace-uid.v1"
	testCAScheme  = "kubernetes.root-ca.v1"
)

func envStrong(scheme, id string) EnvironmentIdentity {
	return EnvironmentIdentity{Scheme: scheme, ID: id, Strength: EnvironmentStrong}
}

func envExclusive(scheme, id string) EnvironmentIdentity {
	return EnvironmentIdentity{Scheme: scheme, ID: id, Strength: EnvironmentStrong, Exclusive: true}
}

func envWeak(scheme, id string) EnvironmentIdentity {
	return EnvironmentIdentity{Scheme: scheme, ID: id, Strength: EnvironmentWeak}
}

func envSet(fingerprints ...EnvironmentIdentity) []EnvironmentIdentity { return fingerprints }

// testEnvironment is what an administrator of one cluster can read.
func testEnvironment() []EnvironmentIdentity {
	return envSet(
		envExclusive(testUIDScheme, "uid-A"),
		envStrong(testCAScheme, "ca-1"),
		envWeak("aws.eks-cluster-arn.v1", "arn:aws:eks:us-east-1:111122223333:cluster/prod"),
	)
}

func durableTestReference(authority string, incarnation *string, scope ...ScopeEntry) ResourceReference {
	return ResourceReference{
		Address: ResourceAddress{Provider: "kubernetes", Authority: authority, Type: "k8s:/pods", Scope: scope, ID: "web-0"},
		// The incarnation is the native object UID.
		Incarnation: incarnation,
	}
}

func mustDurable(t *testing.T, environment []EnvironmentIdentity, reference ResourceReference) DurableReference {
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

func TestSameEnvironment(t *testing.T) {
	admin := envSet(envExclusive(testUIDScheme, "uid-A"), envStrong(testCAScheme, "ca-1"))
	cases := []struct {
		name string
		a, b []EnvironmentIdentity
		same bool
	}{
		{"shared root CA", admin, envSet(envStrong(testCAScheme, "ca-1")), true},
		{"shared namespace UID", admin, envSet(envExclusive(testUIDScheme, "uid-A")), true},
		{"partial declarations share nothing pairwise", envSet(envStrong(testCAScheme, "ca-1")), envSet(envExclusive(testUIDScheme, "uid-A")), false},
		{"two clusters", admin, envSet(envExclusive(testUIDScheme, "uid-B"), envStrong(testCAScheme, "ca-2")), false},
		{"rotation publishes old and new together", envSet(envStrong(testCAScheme, "old"), envStrong(testCAScheme, "new")), envSet(envStrong(testCAScheme, "new")), true},
		{"rotation seen before and after", envSet(envStrong(testCAScheme, "old"), envStrong(testCAScheme, "new")), envSet(envStrong(testCAScheme, "old")), true},
		{"rotated CA joined by the UID", envSet(envExclusive(testUIDScheme, "uid-A"), envStrong(testCAScheme, "old")), envSet(envExclusive(testUIDScheme, "uid-A"), envStrong(testCAScheme, "new")), true},
		{"shared CA, different exclusive UID", admin, envSet(envExclusive(testUIDScheme, "uid-B"), envStrong(testCAScheme, "ca-1")), false},
		{"shared CA, UID unreadable on one side", admin, envSet(envStrong(testCAScheme, "ca-1")), true},
		{"non-exclusive scheme with different values", envSet(envStrong(testCAScheme, "ca-1"), envStrong("other.v1", "x")), envSet(envStrong(testCAScheme, "ca-1"), envStrong("other.v1", "y")), true},
		{"weak never decides", envSet(envWeak(testCAScheme, "ca-1")), envSet(envWeak(testCAScheme, "ca-1")), false},
		{"weak against strong", envSet(envStrong(testCAScheme, "ca-1")), envSet(envWeak(testCAScheme, "ca-1")), false},
		{"same value under another scheme", envSet(envStrong("a.v1", "x")), envSet(envStrong("b.v1", "x")), false},
		{"nothing declared", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameEnvironment(tc.a, tc.b); got != tc.same {
				t.Fatalf("SameEnvironment = %v, want %v", got, tc.same)
			}
			if got := SameEnvironment(tc.b, tc.a); got != tc.same {
				t.Fatalf("SameEnvironment is not symmetric: %v, want %v", got, tc.same)
			}
		})
	}
}

func TestEnvironmentContinuous(t *testing.T) {
	admin := envSet(envExclusive(testUIDScheme, "uid-A"), envStrong(testCAScheme, "ca-1"))
	cases := []struct {
		name              string
		previous, current []EnvironmentIdentity
		continuous        bool
	}{
		{"nothing declared before or after", nil, nil, true},
		{"weak hints change", envSet(envWeak("a.v1", "1")), envSet(envWeak("a.v1", "2")), true},
		{"gains a fingerprint", envSet(envStrong(testCAScheme, "ca-1")), admin, true},
		{"loses a fingerprint but keeps one", admin, envSet(envStrong(testCAScheme, "ca-1")), true},
		{"loses every strong fingerprint", admin, envSet(envWeak("a.v1", "1")), false},
		{"learns its first strong fingerprint", nil, admin, false},
		{"another cluster", admin, envSet(envExclusive(testUIDScheme, "uid-B"), envStrong(testCAScheme, "ca-2")), false},
		{"shared CA, different exclusive UID", admin, envSet(envExclusive(testUIDScheme, "uid-B"), envStrong(testCAScheme, "ca-1")), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EnvironmentContinuous(tc.previous, tc.current); got != tc.continuous {
				t.Fatalf("EnvironmentContinuous = %v, want %v", got, tc.continuous)
			}
		})
	}
	if strong := StrongEnvironment(testEnvironment()); len(strong) != 2 || strong[0].Scheme != testUIDScheme || strong[1].Scheme != testCAScheme {
		t.Fatalf("StrongEnvironment = %#v", strong)
	}
}

func TestDurableReferenceIgnoresHostAuthorityAndScopeOrder(t *testing.T) {
	uid := "pod-uid-1"
	first := mustDurable(t, testEnvironment(), durableTestReference("local:one", &uid,
		ScopeEntry{Dimension: "namespace", Value: "default"}, ScopeEntry{Dimension: "cluster-shard", Value: "a"}))
	// A retired authority, another route's authority and another scope order are
	// the cases a connection stop, a profile edit and a restart produce.
	for _, authority := range []string{"local:two", "local:three", "other-host-issued-authority"} {
		again := mustDurable(t, testEnvironment(), durableTestReference(authority, &uid,
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
	if first.Environment[0].Scheme != "aws.eks-cluster-arn.v1" || first.Environment[2].Scheme != testCAScheme {
		t.Fatalf("environment is not ordered by scheme: %v", first.Environment)
	}
}

func TestDurableAddressKeyDistinguishesObjects(t *testing.T) {
	base := mustDurable(t, testEnvironment(), durableTestReference("a", nil, ScopeEntry{Dimension: "namespace", Value: "default"})).Address
	baseKey := mustKey(t, base)
	mutations := map[string]func(*DurableAddress){
		"provider":             func(a *DurableAddress) { a.Provider = "other" },
		"type":                 func(a *DurableAddress) { a.Type = "k8s:/services" },
		"identifier":           func(a *DurableAddress) { a.ID = "web-1" },
		"scope value":          func(a *DurableAddress) { a.Scope[0].Value = "kube-system" },
		"scope dimension name": func(a *DurableAddress) { a.Scope[0].Dimension = "ns" },
		"scope presence":       func(a *DurableAddress) { a.Scope = []ScopeEntry{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Scope = append([]ScopeEntry(nil), base.Scope...)
			mutate(&changed)
			if mustKey(t, changed) == baseKey {
				t.Fatalf("changing %s kept the key %s", name, baseKey)
			}
		})
	}
}

func TestDurableAddressKeyIsNotAmbiguous(t *testing.T) {
	// Fields that merely look alike when concatenated must still differ.
	a := DurableAddress{Provider: `a","b`, Type: "t", Scope: []ScopeEntry{}, ID: "i"}
	b := DurableAddress{Provider: "a", Type: `b","t`, Scope: []ScopeEntry{}, ID: "i"}
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
	before := mustDurable(t, testEnvironment(), durableTestReference("a", &first))
	after := mustDurable(t, testEnvironment(), durableTestReference("a", &second))
	unknown := mustDurable(t, testEnvironment(), durableTestReference("a", nil))
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
	environment := testEnvironment()
	reference := durableTestReference("a", &uid, ScopeEntry{Dimension: "namespace", Value: "default"})
	durable := mustDurable(t, environment, reference)
	reference.Address.Scope[0].Value = "changed"
	environment[0].ID = "changed"
	uid = "changed"
	if durable.Address.Scope[0].Value != "default" || *durable.Incarnation != "uid" || durable.Environment[0].ID == "changed" {
		t.Fatal("durable reference aliases the caller's storage")
	}
	empty := mustDurable(t, testEnvironment(), durableTestReference("a", nil))
	if empty.Address.Scope == nil {
		t.Fatal("an address without dimensions must carry an empty, non-nil scope")
	}
	if !reflect.DeepEqual(empty, mustDurable(t, testEnvironment(), durableTestReference("b", nil, []ScopeEntry{}...))) {
		t.Fatal("nil and empty scope produced different durable references")
	}
}

func TestNewDurableReferenceRejectsInvalidInput(t *testing.T) {
	good := durableTestReference("a", nil)
	tooMany := make([]EnvironmentIdentity, MaxEnvironmentIdentities+1)
	for i := range tooMany {
		tooMany[i] = envStrong("s.v1", string(rune('a'+i)))
	}
	cases := map[string]struct {
		environment []EnvironmentIdentity
		reference   ResourceReference
	}{
		"no environment":                    {nil, good},
		"only weak fingerprints":            {envSet(envWeak("s.v1", "x")), good},
		"empty scheme":                      {envSet(envStrong("", "x")), good},
		"empty id":                          {envSet(envStrong("s.v1", "")), good},
		"invalid utf-8":                     {envSet(envStrong("s.v1", "\xff")), good},
		"control character":                 {envSet(envStrong("s.v1", "a\nb")), good},
		"delete character":                  {envSet(envStrong("s.v1", "a\x7fb")), good},
		"oversized scheme":                  {envSet(envStrong(strings.Repeat("s", MaxEnvironmentSchemeBytes+1), "x")), good},
		"oversized id":                      {envSet(envStrong("s.v1", strings.Repeat("x", MaxEnvironmentIDBytes+1))), good},
		"unknown strength":                  {envSet(EnvironmentIdentity{Scheme: "s.v1", ID: "x", Strength: "medium"}), good},
		"missing strength":                  {envSet(EnvironmentIdentity{Scheme: "s.v1", ID: "x"}), good},
		"weak and exclusive":                {envSet(EnvironmentIdentity{Scheme: "s.v1", ID: "x", Strength: EnvironmentWeak, Exclusive: true}, envStrong("t.v1", "y")), good},
		"repeated fingerprint":              {envSet(envStrong("s.v1", "x"), envStrong("s.v1", "x")), good},
		"same pair strong and weak":         {envSet(envStrong("s.v1", "x"), envWeak("s.v1", "x")), good},
		"two values of an exclusive scheme": {envSet(envExclusive("s.v1", "a"), envExclusive("s.v1", "b")), good},
		"too many fingerprints":             {tooMany, good},
		"missing authority":                 {testEnvironment(), durableTestReference("", nil)},
		"duplicate dimension": {testEnvironment(), durableTestReference("a", nil,
			ScopeEntry{Dimension: "namespace", Value: "a"}, ScopeEntry{Dimension: "namespace", Value: "b"})},
		"empty incarnation": {testEnvironment(), durableTestReference("a", new(string))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewDurableReference(tc.environment, tc.reference); err == nil {
				t.Fatal("invalid durable reference was accepted")
			}
		})
	}
	limit := envSet(envStrong(strings.Repeat("s", MaxEnvironmentSchemeBytes), strings.Repeat("x", MaxEnvironmentIDBytes)))
	if _, err := NewDurableReference(limit, good); err != nil {
		t.Fatalf("values at the limit were rejected: %v", err)
	}
	if _, err := NewDurableReference(tooMany[:MaxEnvironmentIdentities], good); err != nil {
		t.Fatalf("a declaration at the limit was rejected: %v", err)
	}
}

func TestDurableAddressKeyRejectsHandBuiltInvalidAddress(t *testing.T) {
	for name, address := range map[string]DurableAddress{
		"zero":                 {},
		"duplicate dimensions": {Provider: "p", Type: "t", ID: "i", Scope: []ScopeEntry{{Dimension: "d", Value: "1"}, {Dimension: "d", Value: "2"}}},
		"invalid scope value":  {Provider: "p", Type: "t", ID: "i", Scope: []ScopeEntry{{Dimension: "d", Value: "\xff"}}},
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
	original := mustDurable(t, testEnvironment(), durableTestReference("a", &uid, ScopeEntry{Dimension: "namespace", Value: "default"}))
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
	fingerprint := `{"scheme":"s.v1","id":"x","strength":"strong"}`
	address := `{"provider":"p","type":"t","scope":[],"id":"i"}`
	for name, source := range map[string]string{
		"fingerprint unknown member":       `{"scheme":"s.v1","id":"x","strength":"strong","extra":true}`,
		"fingerprint duplicate member":     `{"scheme":"s.v1","scheme":"s.v2","id":"x","strength":"strong"}`,
		"fingerprint missing strength":     `{"scheme":"s.v1","id":"x"}`,
		"fingerprint unknown strength":     `{"scheme":"s.v1","id":"x","strength":"medium"}`,
		"fingerprint weak and exclusive":   `{"scheme":"s.v1","id":"x","strength":"weak","exclusive":true}`,
		"fingerprint null":                 `null`,
		"address missing scope":            `{"provider":"p","type":"t","id":"i"}`,
		"address null scope":               `{"provider":"p","type":"t","scope":null,"id":"i"}`,
		"address authority":                `{"provider":"p","authority":"a","type":"t","scope":[],"id":"i"}`,
		"address environment":              `{"environment":[` + fingerprint + `],"provider":"p","type":"t","scope":[],"id":"i"}`,
		"reference missing environment":    `{"address":` + address + `}`,
		"reference null environment":       `{"environment":null,"address":` + address + `}`,
		"reference empty environment":      `{"environment":[],"address":` + address + `}`,
		"reference weak-only environment":  `{"environment":[{"scheme":"s.v1","id":"x","strength":"weak"}],"address":` + address + `}`,
		"reference null incarnation":       `{"environment":[` + fingerprint + `],"address":` + address + `,"incarnation":null}`,
		"reference empty incarnation":      `{"environment":[` + fingerprint + `],"address":` + address + `,"incarnation":""}`,
		"reference missing address":        `{"environment":[` + fingerprint + `]}`,
		"reference repeated fingerprint":   `{"environment":[` + fingerprint + `,` + fingerprint + `],"address":` + address + `}`,
		"reference duplicate dimensions":   `{"environment":[` + fingerprint + `],"address":{"provider":"p","type":"t","scope":[{"dimension":"d","value":"1"},{"dimension":"d","value":"2"}],"id":"i"}}`,
		"reference invalid fingerprint":    `{"environment":[{"scheme":"","id":"x","strength":"strong"}],"address":` + address + `}`,
		"reference unordered fingerprints": ``,
	} {
		if source == "" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			var target any = &DurableReference{}
			if strings.HasPrefix(name, "fingerprint") {
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
	if err = json.Unmarshal([]byte(`{"provider":"p","type":"t","scope":[{"dimension":"b","value":"2"},{"dimension":"a","value":"1"}],"id":"i"}`), &unordered); err != nil {
		t.Fatal(err)
	}
	if unordered.Scope[0].Dimension != "a" {
		t.Fatalf("decoded scope is not ordered: %v", unordered.Scope)
	}
}

func durableTestCatalog(environments ...[]EnvironmentIdentity) Catalog {
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
	cluster := testEnvironment()
	local := envSet(envStrong("helm.local.v1", "machine"))
	catalog := durableTestCatalog(cluster, nil, local, cluster)
	if err := ValidateCatalog(catalog); err != nil {
		t.Fatal(err)
	}

	// Protobuf: present on exactly the declaring types, with independent storage.
	wire, err := ToProtoCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Types[0].Environment) != 3 || len(wire.Types[1].Environment) != 0 || len(wire.Types[2].Environment) != 1 {
		t.Fatal("environment was not declared per type")
	}
	decoded, err := FromProtoCatalog(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(catalog, decoded) {
		t.Fatalf("protobuf round trip changed the catalog:\n%#v\n%#v", catalog, decoded)
	}
	decoded.Types[0].Environment[0].ID = "changed"
	if decoded.Types[3].Environment[0].ID != "uid-A" || cluster[0].ID != "uid-A" {
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

	tooMany := make([]EnvironmentIdentity, MaxEnvironmentIdentities+1)
	for i := range tooMany {
		tooMany[i] = envStrong("s.v1", string(rune('a'+i)))
	}
	for name, bad := range map[string][]EnvironmentIdentity{
		"empty fingerprint":     {{}},
		"unknown strength":      {{Scheme: "s.v1", ID: "x", Strength: "medium"}},
		"weak and exclusive":    {{Scheme: "s.v1", ID: "x", Strength: EnvironmentWeak, Exclusive: true}},
		"repeated fingerprint":  envSet(envStrong("s.v1", "x"), envWeak("s.v1", "x")),
		"two exclusive values":  envSet(envExclusive("s.v1", "a"), envExclusive("s.v1", "b")),
		"control character":     envSet(envStrong("s.v1", "a\tb")),
		"oversized id":          envSet(envStrong("s.v1", strings.Repeat("x", MaxEnvironmentIDBytes+1))),
		"too many fingerprints": tooMany,
	} {
		invalid := durableTestCatalog(bad)
		if err := ValidateCatalog(invalid); err == nil {
			t.Fatalf("%s passed catalog validation", name)
		}
		if _, err := ToProtoCatalog(invalid); err == nil {
			t.Fatalf("%s crossed the transport adapter", name)
		}
	}
}

func TestSourceContractFollowsEnvironmentContinuity(t *testing.T) {
	request := controllerSourceOpen(SourceModeSnapshotAndUpdates)
	catalog, err := controllerConfig().Catalog(t.Context(), CatalogRequest{Route: request.Target.Route})
	if err != nil {
		t.Fatal(err)
	}
	catalog.Revision = boundCatalogRevision([32]byte{1}, "support-1")
	ctx := WithCatalogRevisionForRoute(t.Context(), catalog.Provider, request.Target.Route, catalog.Revision)
	admitted := catalog.Types[0]
	admitted.Environment = envSet(envExclusive(testUIDScheme, "uid-A"), envStrong(testCAScheme, "ca-1"))
	partial := Coverage{Kind: "partial", Reason: "Discovery incomplete."}

	cases := []struct {
		name        string
		environment []EnvironmentIdentity
		coverage    *Coverage
		want        string
	}{
		{"unchanged", admitted.Environment, nil, ""},
		{"gains a fingerprint and a hint", append(envSet(envWeak("aws.eks-cluster-arn.v1", "arn")), admitted.Environment...), nil, ""},
		{"UID becomes unreadable but the CA remains", envSet(envStrong(testCAScheme, "ca-1")), nil, ""},
		{"CA rotates while the UID remains", envSet(envExclusive(testUIDScheme, "uid-A"), envStrong(testCAScheme, "ca-2")), nil, ""},
		{"another cluster", envSet(envExclusive(testUIDScheme, "uid-B"), envStrong(testCAScheme, "ca-2")), nil, "RESET_REQUIRED"},
		{"shared CA, different UID", envSet(envExclusive(testUIDScheme, "uid-B"), envStrong(testCAScheme, "ca-1")), nil, "RESET_REQUIRED"},
		{"another cluster under partial discovery", envSet(envExclusive(testUIDScheme, "uid-B")), &partial, "RESET_REQUIRED"},
		{"every strong fingerprint withdrawn", nil, nil, "RESET_REQUIRED"},
		{"withdrawal cannot be proven by partial discovery", nil, &partial, "UNAVAILABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := catalog
			current.Types = []ResourceType{admitted}
			current.Types[0].Environment = tc.environment
			if tc.coverage != nil {
				current.Coverage = *tc.coverage
			}
			if err := CheckSourceCatalog(ctx, current, request, admitted); ErrorCode(err) != tc.want {
				t.Fatalf("class %q, want %q: %v", ErrorCode(err), tc.want, err)
			}
		})
	}

	// Learning an environment the admitted descriptor lacked is a change: an open
	// source keeps one environment for its whole life.
	unknown := admitted
	unknown.Environment = nil
	learned := catalog
	learned.Types = []ResourceType{admitted}
	if err = CheckSourceCatalog(ctx, learned, request, unknown); ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("newly known environment class %s: %v", ErrorCode(err), err)
	}
}
