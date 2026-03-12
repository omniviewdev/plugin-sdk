package telemetry

import (
	"context"
	"os"
	"sync"

	otelpyroscope "github.com/grafana/otel-profiling-go"
	"github.com/grafana/pyroscope-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

// Config holds telemetry settings read from environment variables.
type Config struct {
	Enabled           bool
	OTLPEndpoint      string
	AuthHeader        string
	AuthValue         string
	PluginID          string
	Profiling         bool
	PyroscopeEndpoint string
}

// Provider holds the initialised telemetry subsystems for a plugin process.
type Provider struct {
	tracerProvider *sdktrace.TracerProvider
	profiler       *pyroscope.Profiler

	shutdownOnce sync.Once
}

// configFromEnv reads telemetry config from environment variables.
func configFromEnv() Config {
	return Config{
		Enabled:           os.Getenv("OMNIVIEW_TELEMETRY_ENABLED") == "true",
		OTLPEndpoint:      os.Getenv("OMNIVIEW_TELEMETRY_OTLP_ENDPOINT"),
		AuthHeader:        os.Getenv("OMNIVIEW_TELEMETRY_AUTH_HEADER"),
		AuthValue:         os.Getenv("OMNIVIEW_TELEMETRY_AUTH_VALUE"),
		PluginID:          os.Getenv("OMNIVIEW_PLUGIN_ID"),
		Profiling:         os.Getenv("OMNIVIEW_TELEMETRY_PROFILING") == "true",
		PyroscopeEndpoint: os.Getenv("OMNIVIEW_TELEMETRY_PYROSCOPE_ENDPOINT"),
	}
}

// InitFromEnv reads telemetry config from environment variables and initialises
// a TracerProvider (and optionally Pyroscope) for the plugin process.
// Returns nil, nil when telemetry is disabled.
func InitFromEnv() (*Provider, error) {
	cfg := configFromEnv()
	if !cfg.Enabled {
		return nil, nil
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("omniview.plugin."+cfg.PluginID),
			attribute.String("omniview.plugin.id", cfg.PluginID),
		),
	)
	if err != nil {
		return nil, err
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(cfg.OTLPEndpoint),
		otlptracehttp.WithInsecure(),
	}
	if cfg.AuthHeader != "" && cfg.AuthValue != "" {
		opts = append(opts, otlptracehttp.WithHeaders(map[string]string{cfg.AuthHeader: cfg.AuthValue}))
	}
	exp, err := otlptracehttp.New(context.Background(), opts...)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exp),
	)

	otel.SetTextMapPropagator(propagation.TraceContext{})

	p := &Provider{tracerProvider: tp}

	if cfg.Profiling && cfg.PyroscopeEndpoint != "" {
		profiler, err := pyroscope.Start(pyroscope.Config{
			ApplicationName: "omniview.plugin." + cfg.PluginID,
			ServerAddress:   "http://" + cfg.PyroscopeEndpoint,
			ProfileTypes: []pyroscope.ProfileType{
				pyroscope.ProfileCPU,
				pyroscope.ProfileAllocSpace,
				pyroscope.ProfileGoroutines,
			},
		})
		if err != nil {
			// Profiling failure is not fatal — continue without it.
			_ = err
		} else {
			p.profiler = profiler
			otel.SetTracerProvider(otelpyroscope.NewTracerProvider(tp))
			return p, nil
		}
	}

	otel.SetTracerProvider(tp)
	return p, nil
}

// Shutdown gracefully stops all telemetry subsystems. Safe to call multiple times.
func (p *Provider) Shutdown(ctx context.Context) error {
	var shutdownErr error
	p.shutdownOnce.Do(func() {
		if p.tracerProvider != nil {
			shutdownErr = p.tracerProvider.Shutdown(ctx)
		}
		if p.profiler != nil {
			_ = p.profiler.Stop()
		}
	})
	return shutdownErr
}
