package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/protobuf/proto"
)

func TestActionActivitySurvivesProtobufWire(t *testing.T) {
	presentation := resourcev2.ActivityPresentation{Title: "Expand volume", Progress: &resourcev2.ActivityProgress{Kind: "units", Current: "9007199254740993", Total: "18446744073709551615", Unit: "bytes"}}
	payload, err := json.Marshal(presentation)
	if err != nil {
		t.Fatal(err)
	}
	original := &resourcepb.ActionStreamEvent{Version: resourcev2.WireVersion, Type: "progress", ActivityJson: payload}
	wire, err := proto.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var received resourcepb.ActionStreamEvent
	if err := proto.Unmarshal(wire, &received); err != nil {
		t.Fatal(err)
	}
	ready := true
	event, err := decodeActionEvent(&received, &ready)
	if err != nil {
		t.Fatal(err)
	}
	if event.Data != nil || event.Activity == nil || event.Activity.Progress.Current != "9007199254740993" {
		t.Fatalf("lost typed activity: %#v", event)
	}
	received.Type = "output"
	if _, err := decodeActionEvent(&received, &ready); err == nil {
		t.Fatal("presentation replaced required business output")
	}
	ready = false
	received.Type = "progress"
	if _, err := decodeActionEvent(&received, &ready); err == nil {
		t.Fatal("presentation bypassed native readiness")
	}
}

func TestActivityPresentationEncodedBoundary(t *testing.T) {
	base := resourcev2.ActivityPresentation{Title: "Copy", Details: "x"}
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	remaining := resourcev2.MaxActivityPresentationBytes - len(encoded) + 1
	base.Details = strings.Repeat("<", remaining/6) + strings.Repeat("x", remaining%6)
	for _, extra := range []string{"", "x"} {
		presentation := base
		presentation.Details += extra
		payload, err := json.Marshal(presentation)
		if err != nil {
			t.Fatal(err)
		}
		validationErr := resourcev2.ValidateActivityPresentation(presentation)
		ready := true
		_, receiveErr := decodeActionEvent(&resourcepb.ActionStreamEvent{
			Version: resourcev2.WireVersion, Type: "progress", ActivityJson: payload,
		}, &ready)
		if extra == "" {
			if len(payload) != resourcev2.MaxActivityPresentationBytes || validationErr != nil || receiveErr != nil {
				t.Fatalf("boundary rejected: size=%d validation=%v receive=%v", len(payload), validationErr, receiveErr)
			}
		} else if validationErr == nil || receiveErr == nil {
			t.Fatalf("oversize admitted: validation=%v receive=%v", validationErr, receiveErr)
		}
	}
	if err := resourcev2.ValidateActivityPresentation(resourcev2.ActivityPresentation{
		Title: "Copy", Details: strings.Repeat("<", 30000),
	}); err == nil {
		t.Fatal("HTML-escaped presentation exceeded receiver budget")
	}
}
