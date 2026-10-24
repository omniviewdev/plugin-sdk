// Package resourceconformance is verification-only data and finite oracles.
// It is internal to the SDK repository, is not registered by a product, and has
// no SDK transport dependency. Its types describe this corpus, not future APIs.
package resourceconformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

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

type Page struct {
	Cursor  string
	Next    string
	Records []string // finite oracle names, never wire identity
}

type Coverage struct {
	Enumeration string
	Consistency string
	Limitations []string
}

type Change struct {
	Record   string
	Revision string
	Body     json.RawMessage
}

type Scenario struct {
	Name       string
	Profile    string
	Type       string
	Selection  map[string][]string // explicit finite selected native scope values
	Schema     json.RawMessage     // nil means not provided
	Pages      []Page
	Coverage   Coverage
	Freshness  string
	Updates    []Change
	Operations map[string]string // finite fixture expectations, not SDK capabilities
}

type IdentityCase struct {
	Name        string
	Left, Right string
	Equal       bool
}

type Link struct {
	From, Relation, To string
}

type Corpus struct {
	Records       []Record
	Scenarios     []Scenario
	IdentityCases []IdentityCase
	Links         []Link
}

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

func (c Corpus) Record(name string) Record {
	for _, r := range c.Records {
		if r.Name == name {
			return r
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
	for _, o := range got {
		matched := false
		for i, r := range want {
			if !seen[i] && SameAddress(o.Record, r) {
				if !equalJSON(o.Body, r.Body) || o.Record.Incarnation != r.Incarnation || o.Record.Revision != r.Revision {
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
	names := make(map[string]Record, len(c.Records))
	for _, r := range c.Records {
		if r.Name == "" || r.Provider == "" || r.Authority == "" || r.Type == "" || r.ID == "" || !json.Valid(r.Body) {
			return fmt.Errorf("invalid record %q", r.Name)
		}
		if _, exists := names[r.Name]; exists {
			return fmt.Errorf("duplicate record %q", r.Name)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(r.Body, &body); err != nil {
			return err
		}
		for _, forbidden := range []string{"metadata", "apiVersion", "kind", "namespace", "uid"} {
			if _, exists := body[forbidden]; exists {
				return fmt.Errorf("%s: fabricated universal field %s", r.Name, forbidden)
			}
		}
		names[r.Name] = r
	}
	for _, s := range c.Scenarios {
		if len(s.Pages) == 0 || s.Pages[0].Cursor != "" || (s.Schema != nil && !json.Valid(s.Schema)) {
			return fmt.Errorf("%s: invalid pages/schema", s.Name)
		}
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
					selected := false
					for _, v := range s.Selection[dimension] {
						selected = selected || value == v
					}
					if !selected {
						return fmt.Errorf("%s: scope escape %s", s.Name, name)
					}
				}
			}
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
	}
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
