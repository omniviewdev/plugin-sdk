package config

import "testing"

func TestConnectionGrants(t *testing.T) {
	owner := PluginMeta{ID: "owner", Exports: &ExportsSpec{Connections: &ConnectionExport{Operations: []string{"catalog", "get"}, ResourceTypes: []string{"x/group/*", "literal"}}}}
	consumer := PluginMeta{ID: "consumer", Requires: &RequiresSpec{Connections: []ConnectionRequirement{{Plugin: "owner", Operations: []string{"catalog", "get"}, ResourceTypes: []string{"x/group/narrow/*", "literal"}}}}}
	grant, err := GrantConnections(consumer, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		op, typ string
		want    bool
	}{{"get", "literal", true}, {"get", "x/group/narrow/object", true}, {"get", "x/group/narrow/", false}, {"get", "x/group/other", false}, {"update", "literal", false}, {"get", "Literal", false}} {
		if got := grant.Allows(tc.op, tc.typ); got != tc.want {
			t.Errorf("Allows(%q,%q)=%v", tc.op, tc.typ, got)
		}
	}
	consumer.Requires.Connections[0].ResourceTypes = []string{"x/*"}
	if _, err = GrantConnections(consumer, owner); err == nil {
		t.Fatal("overbroad requirement granted")
	}
}
func TestConnectionValidation(t *testing.T) {
	for _, pattern := range []string{"", "*", "/*", "a*", "a/*/b", "a/**"} {
		m := PluginMeta{ID: "p", Exports: &ExportsSpec{Connections: &ConnectionExport{Operations: []string{"get"}, ResourceTypes: []string{pattern}}}}
		if m.Validate() == nil {
			t.Errorf("accepted %q", pattern)
		}
	}
	for _, ops := range [][]string{nil, {"get", "get"}, {"write"}} {
		m := PluginMeta{Exports: &ExportsSpec{Connections: &ConnectionExport{Operations: ops, ResourceTypes: []string{"a"}}}}
		if m.Validate() == nil {
			t.Errorf("accepted operations %v", ops)
		}
	}
	self := PluginMeta{ID: "p", Requires: &RequiresSpec{Connections: []ConnectionRequirement{{Plugin: "p", Operations: []string{"get"}, ResourceTypes: []string{"a"}}}}}
	if self.Validate() == nil {
		t.Fatal("accepted self dependency")
	}
}
