package resourceconformance

import (
	"encoding/json"
	"reflect"
	"testing"
)

// These cases catch scope/ID conflation, implicit UID requirements, accidental
// completeness upgrades and transport-dependent expectations.
func TestNativeCorpusOracles(t *testing.T) {
	c := NativeCorpus()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range c.IdentityCases {
		t.Run(tc.Name, func(t *testing.T) {
			if got := SameAddress(c.Record(tc.Left), c.Record(tc.Right)); got != tc.Equal {
				t.Fatalf("identity equality = %v, want %v", got, tc.Equal)
			}
		})
	}
	for _, s := range c.Scenarios {
		t.Run(s.Name, func(t *testing.T) {
			var observed []Observation
			for _, p := range s.Pages {
				for _, name := range p.Records {
					r := c.Record(name)
					observed = append(observed, Observation{Record: r, Body: r.Body})
				}
			}
			if err := c.CheckSnapshot(s, observed, s.Coverage, s.Freshness); err != nil {
				t.Fatal(err)
			}
			if len(observed) > 0 {
				bad := append([]Observation(nil), observed...)
				bad[0].Body = json.RawMessage(`{"wrong":"payload"}`)
				if c.CheckSnapshot(s, bad, s.Coverage, s.Freshness) == nil {
					t.Fatal("oracle accepted altered native payload")
				}
				if c.CheckSnapshot(s, observed[1:], s.Coverage, s.Freshness) == nil {
					t.Fatal("oracle accepted missing member")
				}
				if c.CheckSnapshot(s, append(observed, observed[0]), s.Coverage, s.Freshness) == nil {
					t.Fatal("oracle accepted duplicate member")
				}
			}
			badCoverage := s.Coverage
			badCoverage.Consistency = "pointInTime"
			if s.Coverage.Consistency != "pointInTime" && c.CheckSnapshot(s, observed, badCoverage, s.Freshness) == nil {
				t.Fatal("oracle accepted invented point-in-time consistency")
			}
			badCoverage = s.Coverage
			badCoverage.Enumeration = "exhausted"
			if s.Coverage.Enumeration == "partial" && c.CheckSnapshot(s, observed, badCoverage, s.Freshness) == nil {
				t.Fatal("oracle accepted partial enumeration as exhausted")
			}
			if c.CheckSnapshot(s, observed, s.Coverage, "live") == nil {
				t.Fatal("oracle accepted invented freshness")
			}
		})
	}
}

func TestCorpusIsIndependentAndDeterministic(t *testing.T) {
	a, b := NativeCorpus(), NativeCorpus()
	if !reflect.DeepEqual(a, b) {
		t.Fatal("corpus changed between constructions")
	}
	a.Records[0].Scope["account"] = "changed"
	a.Records[0].Body[0] = ' '
	a.Scenarios[0].Pages[0].Records[0] = "changed"
	if !reflect.DeepEqual(b, NativeCorpus()) {
		t.Fatal("caller's mutation leaked into a fresh corpus")
	}
}

func TestOpaqueIdentityAndScopeAreNotDelimitedKeys(t *testing.T) {
	a := Record{Provider: "p", Authority: "a", Type: "t", Scope: map[string]string{"node": "a::b"}, ID: "c"}
	b := Record{Provider: "p", Authority: "a", Type: "t", Scope: map[string]string{"node": "a"}, ID: "b::c"}
	if SameAddress(a, b) {
		t.Fatal("delimiter collision merged distinct addresses")
	}
	b = a
	b.Scope = map[string]string{"node": "A::b"}
	if SameAddress(a, b) {
		t.Fatal("case-sensitive native scope was normalized")
	}
}

func TestSnapshotOracleRejectsScopeAndIdentityEscape(t *testing.T) {
	c := NativeCorpus()
	s := c.Scenarios[0]
	var observed []Observation
	for _, p := range s.Pages {
		for _, name := range p.Records {
			r := c.Record(name)
			observed = append(observed, Observation{Record: r, Body: r.Body})
		}
	}
	r := observed[0].Record
	r.Scope = map[string]string{"account": "unselected", "region": "elsewhere"}
	observed[0].Record = r
	if c.CheckSnapshot(s, observed, s.Coverage, s.Freshness) == nil {
		t.Fatal("oracle accepted scope escape")
	}
}

func TestNativeLinksResolveWithinExactNode(t *testing.T) {
	c := NativeCorpus()
	bodies := make(map[string]json.RawMessage)
	for _, r := range c.Records {
		bodies[r.Name] = r.Body
	}
	if err := c.CheckLinks(bodies); err != nil {
		t.Fatal(err)
	}
	bodies["disk-a"] = c.Record("disk-b").Body
	if c.CheckLinks(bodies) == nil {
		t.Fatal("node-local disk ID collision resolved to another node")
	}
}

func TestFinitePageAndChangeOraclesRejectReordering(t *testing.T) {
	c := NativeCorpus()
	s := c.Scenarios[0]
	if CheckPages(s, s.Pages) != nil {
		t.Fatal("valid finite source pages rejected")
	}
	bad := append([]Page(nil), s.Pages...)
	bad[0], bad[1] = bad[1], bad[0]
	if CheckPages(s, bad) == nil {
		t.Fatal("source pages silently reordered")
	}
	for _, s := range c.Scenarios {
		if len(s.Updates) == 0 {
			continue
		}
		if CheckChanges(s, s.Updates) != nil {
			t.Fatal("native update rejected")
		}
		bad := append([]Change(nil), s.Updates...)
		bad[0].Revision = "event:z9"
		if CheckChanges(s, bad) == nil {
			t.Fatal("opaque revision changed without detection")
		}
		if CheckChanges(s, nil) == nil {
			t.Fatal("optional-but-requested native update silently omitted")
		}
	}
}

func TestAddressSeparatesProviderAuthorityAndType(t *testing.T) {
	r := NativeCorpus().Record("vm-a")
	for _, field := range []string{"provider", "authority", "type", "id"} {
		other := r
		switch field {
		case "provider":
			other.Provider = "another-provider"
		case "authority":
			other.Authority = "another-authority"
		case "type":
			other.Type = "disk"
		case "id":
			other.ID = "102"
		}
		if SameAddress(r, other) {
			t.Fatalf("identity omitted %s", field)
		}
	}
}
