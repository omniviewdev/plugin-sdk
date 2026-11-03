package resourcev2

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActivityProgressWireKinds(t *testing.T) {
	for _, test := range []struct {
		progress ActivityProgress
		fragment string
	}{
		{
			ActivityProgress{Kind: "steps", Current: "1", Total: "3", Label: "Expand"},
			`"current":"1"`,
		},
		{
			ActivityProgress{Kind: "units", Current: "9007199254740993", Total: "18446744073709551615", Unit: "bytes"},
			`"current":"9007199254740993"`,
		},
	} {
		if err := ValidateActivityPresentation(ActivityPresentation{Title: "Resize", Progress: &test.progress}); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(test.progress)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), test.fragment) {
			t.Fatalf("progress count is not a decimal string: %s", data)
		}
		var result ActivityProgress
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result != test.progress {
			t.Fatalf("progress changed: %#v", result)
		}
	}
}

func TestActivityPresentationRejectsInvalidUnion(t *testing.T) {
	for _, p := range []ActivityPresentation{
		{Title: "Bad", Progress: &ActivityProgress{Kind: "steps", Current: "1", Total: "1000001", Label: "phase"}},
		{Title: "Bad", Progress: &ActivityProgress{Kind: "units", Current: "01", Total: "2", Unit: "bytes"}},
		{Title: "Bad", Progress: &ActivityProgress{Kind: "indeterminate", Current: "1"}},
		{
			Title: "Bad",
			Actions: []ActivityAction{{
				ID:     "a",
				Kind:   "cancel-task",
				Label:  "Stop",
				TaskID: "id",
				Target: &ResourceNavigationIntent{},
			}},
		},
		{
			Title: "Bad",
			Metrics: []ActivityMetric{
				{ID: "x", Label: "a", Value: "1"},
				{ID: "x", Label: "b", Value: "2"},
			},
		},
	} {
		if err := ValidateActivityPresentation(p); err == nil {
			t.Fatalf("admitted invalid presentation: %#v", p)
		}
	}
}
