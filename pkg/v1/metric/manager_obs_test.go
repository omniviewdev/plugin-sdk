package metric

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
)

func TestPollOnceSendsClassificationNotErrorText(t *testing.T) {
	sink := &logtest.RecordingSink{}
	log := logging.New(logging.Config{
		Name:    "test",
		Level:   logging.NewLevelController(logging.LevelTrace),
		Backend: logging.NewSinkBackend(sink),
	})
	manager := NewManager(log, nil, ProviderInfo{ID: "p"}, nil,
		func(*QueryContext, QueryRequest) (*QueryResponse, error) {
			return nil, errors.New("upstream said token=abc123")
		}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan StreamOutput, 1)
	manager.pollOnce(&managedSubscription{id: "sub-1", ctx: ctx, cancel: cancel, out: out, interval: time.Second})

	got := <-out
	if strings.Contains(got.Error, "abc123") || got.Error == "" {
		t.Fatalf("subscriber error must be a classification, got %q", got.Error)
	}
	var logged bool
	for _, record := range sink.Records() {
		for _, field := range record.Fields {
			if s, ok := field.Value.(string); ok && strings.Contains(s, "abc123") {
				t.Fatalf("log leaks provider text: %+v", record)
			}
		}
		if record.Message == "metric poll failed" {
			logged = true
		}
	}
	if !logged {
		t.Fatal("poll failure was not logged")
	}
}
