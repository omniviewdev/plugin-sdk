// Package resourceconformance is verification-only data and finite oracles.
// It is internal to the SDK repository, is not registered by a product, and has
// no SDK transport dependency. Its types describe this corpus, not future APIs.
package resourceconformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
)

// Record preserves one native object's exact identity, tokens, and JSON body.
// Scope and Body remain caller-owned mutable containers.
type Record struct {
	Name        string
	Provider    string
	Authority   string
	Type        string
	Scope       map[string]string
	ID          string // canonical decimal text for native numeric IDs
	Incarnation string // absent means unknown, never a synthesized UID
	Revision    string // opaque; never sorted
	Body        json.RawMessage
}

// Page names finite corpus members in native cursor order; empty Next ends traversal.
type Page struct {
	Cursor  string
	Next    string
	Records []string // finite oracle names, never wire identity
}

// Coverage records native enumeration and consistency claims without upgrading them.
type Coverage struct {
	Enumeration string
	Consistency string
	Limitations []string
}

// Change is one ordered native update; Revision remains opaque, not sortable.
type Change struct {
	Record   string
	Revision string
	Body     json.RawMessage
}

// Scenario schema delivery modes.
const (
	SchemaDeliveryInline    = "inline"
	SchemaDeliveryReference = "reference"
)

// Scenario binds selected native scopes to finite pages and declared guarantees.
// A nil Schema means the source did not provide one.
type Scenario struct {
	Name      string
	Profile   string
	Type      string
	Selection map[string][]string // explicit finite selected native scope values
	Schema    json.RawMessage     // nil means not provided
	// SchemaDialect names the Schema dialect; empty means JSON Schema 2020-12.
	SchemaDialect string
	// SchemaRoot is the local RFC6901 URI-fragment root of Schema; empty
	// selects the document root. OpenAPI documents require a root.
	SchemaRoot string
	// SchemaDelivery is empty or SchemaDeliveryInline for an inline document,
	// or SchemaDeliveryReference for a document served by reference.
	SchemaDelivery string
	// SchemaUnavailable, when set, is the reviewed error code reported as the
	// unavailable representation schema reason; Schema must then be nil.
	SchemaUnavailable string
	Pages             []Page
	Coverage          Coverage
	Freshness         string
	Updates           []Change
	Operations        map[string]string // finite fixture expectations, not SDK capabilities
}

// IdentityCase supplies independent expected equality for two named records.
type IdentityCase struct {
	Name        string
	Left, Right string
	Equal       bool
}

// Link names a native relation whose endpoints must remain in the same node scope.
type Link struct {
	From, Relation, To string
}

// Corpus owns finite fixture expectations, not product registrations or wire types.
// Callers may mutate a corpus; each NativeCorpus call returns independent storage.
type Corpus struct {
	Records       []Record
	Scenarios     []Scenario
	IdentityCases []IdentityCase
	Links         []Link
}

// Observation pairs a native identity with the body actually seen by a consumer.
type Observation struct {
	Record Record
	Body   json.RawMessage
}

// SameAddress is only a test oracle, not a canonical encoder or host identity
// implementation. Access routes, bodies, incarnation and revisions are excluded.
func SameAddress(a, b Record) bool {
	return a.Provider == b.Provider && a.Authority == b.Authority &&
		a.Type == b.Type && a.ID == b.ID && reflect.DeepEqual(a.Scope, b.Scope)
}

// Record returns the named fixture, sharing its scope and body; unknown names panic.
func (c Corpus) Record(name string) Record {
	for i := range c.Records {
		r := &c.Records[i]
		if r.Name == name {
			return *r
		}
	}
	panic("unknown corpus record: " + name)
}

// CheckSnapshot compares a finite scenario, not query-language semantics. A
// consumer supplies observations and its declared guarantees independently.
func (c Corpus) CheckSnapshot(s Scenario, got []Observation, coverage Coverage, freshness string) error {
	if !reflect.DeepEqual(coverage, s.Coverage) || freshness != s.Freshness {
		return fmt.Errorf("%s: coverage/freshness differs from native guarantee", s.Name)
	}
	var want []Record
	for _, p := range s.Pages {
		for _, name := range p.Records {
			want = append(want, c.Record(name))
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("%s: got %d members, want %d", s.Name, len(got), len(want))
	}
	seen := make([]bool, len(want))
	for j := range got {
		o := &got[j]
		matched := false
		for i := range want {
			r := &want[i]
			if !seen[i] && SameAddress(o.Record, *r) {
				if !equalJSON(o.Body, r.Body) || o.Record.Incarnation != r.Incarnation ||
					o.Record.Revision != r.Revision {
					return fmt.Errorf("%s: native body/tokens changed for %s", s.Name, r.Name)
				}
				seen[i], matched = true, true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: duplicate or out-of-selection address", s.Name)
		}
	}
	return nil
}

func equalJSON(a, b json.RawMessage) bool {
	if !json.Valid(a) || !json.Valid(b) {
		return false
	}
	var x, y any
	da, db := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	da.UseNumber()
	db.UseNumber()
	return da.Decode(&x) == nil && db.Decode(&y) == nil && reflect.DeepEqual(x, y)
}

// Validate prevents malformed corpus edits from turning a finite oracle into a
// self-fulfilling expectation. Identity/link expectations remain hand-authored.
func (c Corpus) Validate() error {
	names, err := c.validateRecords()
	if err != nil {
		return err
	}
	for i := range c.Scenarios {
		if err := c.Scenarios[i].validate(names); err != nil {
			return err
		}
	}
	return c.validateReferences(names)
}

func (c Corpus) validateRecords() (map[string]*Record, error) {
	names := make(map[string]*Record, len(c.Records))
	for i := range c.Records {
		r := &c.Records[i]
		if r.Name == "" || r.Provider == "" || r.Authority == "" || r.Type == "" || r.ID == "" || !json.Valid(r.Body) {
			return nil, fmt.Errorf("invalid record %q", r.Name)
		}
		if _, exists := names[r.Name]; exists {
			return nil, fmt.Errorf("duplicate record %q", r.Name)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(r.Body, &body); err != nil {
			return nil, err
		}
		for _, forbidden := range []string{"metadata", "apiVersion", "kind", "namespace", "uid"} {
			if _, exists := body[forbidden]; exists {
				return nil, fmt.Errorf("%s: fabricated universal field %s", r.Name, forbidden)
			}
		}
		names[r.Name] = r
	}
	return names, nil
}

func (s *Scenario) validate(names map[string]*Record) error {
	if len(s.Pages) == 0 || s.Pages[0].Cursor != "" || (s.Schema != nil && !json.Valid(s.Schema)) {
		return fmt.Errorf("%s: invalid pages/schema", s.Name)
	}
	if err := s.validatePages(names); err != nil {
		return err
	}
	if err := s.validateSchemaSource(); err != nil {
		return err
	}
	if s.Freshness == "" || s.Coverage.Enumeration == "" || s.Coverage.Consistency == "" {
		return fmt.Errorf("%s: missing explicit guarantee", s.Name)
	}
	for _, u := range s.Updates {
		r, ok := names[u.Record]
		if !ok || r.Provider != s.Profile || r.Type != s.Type || !json.Valid(u.Body) || u.Revision == "" {
			return fmt.Errorf("%s: invalid native change", s.Name)
		}
	}
	return nil
}

func (s *Scenario) validateSchemaSource() error {
	switch s.SchemaDelivery {
	case "", SchemaDeliveryInline:
	case SchemaDeliveryReference:
		if s.Schema == nil {
			return fmt.Errorf("%s: reference schema delivery requires a schema", s.Name)
		}
	default:
		return fmt.Errorf("%s: unknown schema delivery %q", s.Name, s.SchemaDelivery)
	}
	if s.SchemaUnavailable != "" && (s.Schema != nil || s.SchemaDelivery != "" || s.SchemaDialect != "") {
		return fmt.Errorf("%s: unavailable schema carries a document binding", s.Name)
	}
	if s.Schema == nil && (s.SchemaDialect != "" || s.SchemaRoot != "") {
		return fmt.Errorf("%s: schema dialect or root without a schema", s.Name)
	}
	if s.SchemaDialect == schemaDialectOpenAPI30 && s.SchemaRoot == "" {
		return fmt.Errorf("%s: OpenAPI schema requires a root", s.Name)
	}
	return nil
}

func (s *Scenario) validatePages(names map[string]*Record) error {
	for i, p := range s.Pages {
		if i+1 < len(s.Pages) && p.Next != s.Pages[i+1].Cursor || i+1 == len(s.Pages) && p.Next != "" {
			return fmt.Errorf("%s: broken finite cursor chain", s.Name)
		}
		for _, name := range p.Records {
			r, ok := names[name]
			if !ok || r.Provider != s.Profile || r.Type != s.Type {
				return fmt.Errorf("%s: invalid page member %s", s.Name, name)
			}
			for dimension, value := range r.Scope {
				if !slices.Contains(s.Selection[dimension], value) {
					return fmt.Errorf("%s: scope escape %s", s.Name, name)
				}
			}
		}
	}
	return nil
}

func (c Corpus) validateReferences(names map[string]*Record) error {
	for _, tc := range c.IdentityCases {
		if _, ok := names[tc.Left]; !ok {
			return fmt.Errorf("unknown identity operand %s", tc.Left)
		}
		if _, ok := names[tc.Right]; !ok {
			return fmt.Errorf("unknown identity operand %s", tc.Right)
		}
	}
	for _, link := range c.Links {
		if _, ok := names[link.From]; !ok {
			return fmt.Errorf("unknown link source %s", link.From)
		}
		if _, ok := names[link.To]; !ok {
			return fmt.Errorf("unknown link target %s", link.To)
		}
	}
	return nil
}
