package sdk

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/lifecycle"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestPluginProfilingOptIn(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE", "true"} {
		t.Run("enabled="+value, func(t *testing.T) {
			t.Setenv("OMNIVIEW_PLUGIN_PPROF_ENABLED", value)
			t.Setenv("OMNIVIEW_PPROF_PORT", "6060")
			p := &Plugin{pluginMap: make(map[string]plugin.Plugin)}
			p.profiler = p.startProfiling()
			defer p.profiler.Close()
			p.registerLifecycle()
			server := p.pluginMap["lifecycle"].(*lifecycle.Plugin).Impl
			status, err := server.GetProfilingStatus(context.Background(), &emptypb.Empty{})
			if err != nil || status.Pid != int64(os.Getpid()) || status.Running != (value == "true") || status.Error != "" {
				t.Fatalf("status=%+v err=%v", status, err)
			}
			if value != "true" {
				if status.Url != "" {
					t.Fatalf("disabled profiler advertised %q", status.Url)
				}
				return
			}
			client := &http.Client{Timeout: 5 * time.Second}
			res, getErr := client.Get(status.Url + "goroutine?debug=1")
			if getErr != nil {
				t.Fatal(getErr)
			}
			_, readErr := io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if readErr != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("goroutine capture: status=%d err=%v", res.StatusCode, readErr)
			}
			if err := p.profiler.Close(); err != nil {
				t.Fatal(err)
			}
			closed, err := server.GetProfilingStatus(context.Background(), &emptypb.Empty{})
			if err != nil || closed.Running || closed.Url != "" {
				t.Fatalf("stale closed endpoint: %+v %v", closed, err)
			}
		})
	}
}
