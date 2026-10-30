package resourceconformance

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// CheckPages checks native source-page traversal, not final query pagination.
func CheckPages(s Scenario, got []Page) error {
	if !reflect.DeepEqual(s.Pages, got) {
		return fmt.Errorf("%s: native source pages/cursors changed", s.Name)
	}
	return nil
}

// CheckChanges preserves finite delivery order without interpreting revisions.
// The deliberate event:z9 -> event:a1 transition forbids lexical recency tests.
func CheckChanges(s Scenario, got []Change) error {
	if len(s.Updates) != len(got) {
		return fmt.Errorf("%s: missing or extra native updates", s.Name)
	}
	for i, want := range s.Updates {
		if got[i].Record != want.Record || got[i].Revision != want.Revision || !equalJSON(got[i].Body, want.Body) {
			return fmt.Errorf("%s: native update %d changed", s.Name, i)
		}
	}
	return nil
}

// CheckLinks resolves only the six hand-authored native hypervisor edges. It is
// not a graph engine or proposed relationship DTO. Bodies can come from a real
// transport; identical node-local numeric IDs on another node never resolve.
func (c Corpus) CheckLinks(bodies map[string]json.RawMessage) error {
	for _, edge := range c.Links {
		from, to := c.Record(edge.From), c.Record(edge.To)
		if from.Provider != to.Provider || from.Authority != to.Authority || !reflect.DeepEqual(from.Scope, to.Scope) {
			return fmt.Errorf("%s: link target escaped native node", edge.From)
		}
		var source, target map[string]json.RawMessage
		if err := json.Unmarshal(bodies[edge.From], &source); err != nil {
			return fmt.Errorf("%s: %w", edge.From, err)
		}
		if err := json.Unmarshal(bodies[edge.To], &target); err != nil {
			return fmt.Errorf("%s: %w", edge.To, err)
		}
		node, _ := json.Marshal(from.Scope["node"])
		if !equalJSON(source["node"], node) || !equalJSON(target["node"], node) {
			return fmt.Errorf("%s: native body points at another node", edge.From)
		}
		if err := checkNativeRelation(edge, source, target); err != nil {
			return err
		}
	}
	return nil
}

func checkNativeRelation(edge Link, source, target map[string]json.RawMessage) error {
	switch edge.Relation {
	case relationHostedBy:
		if !equalJSON(source["host"], target["node"]) {
			return fmt.Errorf("%s: native host link changed", edge.From)
		}
	case relationHasDisk:
		var disks []json.RawMessage
		if err := json.Unmarshal(source["disks"], &disks); err != nil {
			return err
		}
		for _, id := range disks {
			if equalJSON(id, target["diskid"]) {
				return nil
			}
		}
		return fmt.Errorf("%s: native disk link changed", edge.From)
	case relationAttachedTo:
		if !equalJSON(source["vmid"], target["vmid"]) {
			return fmt.Errorf("%s: native VM link changed", edge.From)
		}
	default:
		return fmt.Errorf("unknown finite relation %q", edge.Relation)
	}
	return nil
}
