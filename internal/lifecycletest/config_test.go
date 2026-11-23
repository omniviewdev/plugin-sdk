package lifecycletest_test

import (
	"strings"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
)

const envName = "OMNIVIEW_LIFECYCLE_HAMMER"

func TestDurationFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string // the variable is set to this, and an empty value is as good as unset
		want    time.Duration
		wantErr string
	}{
		{name: "unset or empty"},
		{name: "seconds", value: "30s", want: 30 * time.Second},
		{name: "compound", value: "1m30s", want: 90 * time.Second},
		{name: "not a duration", value: "soon", wantErr: envName},
		{name: "no unit", value: "30", wantErr: envName},
		{name: "zero", value: "0s", wantErr: "positive"},
		{name: "negative", value: "-5s", wantErr: "positive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envName, tc.value) // whatever the environment of the run has
			got, err := lifecycletest.DurationFromEnv()
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("DurationFromEnv() = (%v, %v), want an error that says %q", got, err, tc.wantErr)
				}
			case err != nil:
				t.Fatalf("DurationFromEnv() failed: %v", err)
			case got != tc.want:
				t.Fatalf("DurationFromEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultDuration(t *testing.T) {
	builtIn := 5 * time.Second
	if testing.Short() {
		builtIn = time.Second
	}
	t.Run("unset", func(t *testing.T) {
		t.Setenv(envName, "")
		if got := lifecycletest.DefaultDuration(); got != builtIn {
			t.Fatalf("DefaultDuration() = %v, want %v (go test -short is %v)", got, builtIn, testing.Short())
		}
	})
	t.Run("the environment wins", func(t *testing.T) {
		t.Setenv(envName, "30s")
		if got := lifecycletest.DefaultDuration(); got != 30*time.Second {
			t.Fatalf("DefaultDuration() = %v, want 30s", got)
		}
	})
	t.Run("an invalid value is not an error here", func(t *testing.T) {
		t.Setenv(envName, "banana")
		if got := lifecycletest.DefaultDuration(); got != builtIn {
			t.Fatalf("DefaultDuration() = %v, want the built-in %v for an invalid environment value", got, builtIn)
		}
	})
}

func TestConfigWithDefaults(t *testing.T) {
	t.Setenv(envName, "")
	got := lifecycletest.Config{}.WithDefaults()
	want := lifecycletest.Config{
		Workers:    8,
		Duration:   lifecycletest.DefaultDuration(),
		Seed:       1,
		CloseBound: 10 * time.Second,
		Settle:     2 * time.Second,
	}
	if got != want {
		t.Fatalf("Config{}.WithDefaults() = %+v, want %+v", got, want)
	}

	set := lifecycletest.Config{Workers: 3, Duration: time.Minute, Seed: 99, CloseBound: time.Second, Settle: time.Millisecond}
	if got := set.WithDefaults(); got != set {
		t.Fatalf("WithDefaults() changed a config that was complete: %+v, want %+v", got, set)
	}
	if got := (lifecycletest.Config{Workers: -1, Duration: -time.Second}).WithDefaults(); got.Workers != 8 || got.Duration != want.Duration {
		t.Fatalf("WithDefaults() kept negative values: %+v", got)
	}
}
