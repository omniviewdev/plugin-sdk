package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
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
	enabled, _ := strconv.ParseBool(os.Getenv("OMNIVIEW_TELEMETRY_ENABLED"))
	profiling, _ := strconv.ParseBool(os.Getenv("OMNIVIEW_TELEMETRY_PROFILING"))
	return Config{
		Enabled:           enabled,
		OTLPEndpoint:      os.Getenv("OMNIVIEW_TELEMETRY_OTLP_ENDPOINT"),
		AuthHeader:        os.Getenv("OMNIVIEW_TELEMETRY_AUTH_HEADER"),
		AuthValue:         os.Getenv("OMNIVIEW_TELEMETRY_AUTH_VALUE"),
		PluginID:          os.Getenv("OMNIVIEW_PLUGIN_ID"),
		Profiling:         profiling,
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
		otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint),
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
		profiler, profErr := pyroscope.Start(pyroscope.Config{
			ApplicationName: "omniview.plugin." + cfg.PluginID,
			ServerAddress:   cfg.PyroscopeEndpoint,
			ProfileTypes: []pyroscope.ProfileType{
				pyroscope.ProfileCPU,
				pyroscope.ProfileAllocSpace,
				pyroscope.ProfileGoroutines,
			},
		})
		if profErr != nil {
			// Profiling failure is not fatal — install tracing without the
			// profiling wrapper and surface the error for diagnostics.
			otel.SetTracerProvider(tp)
			return p, fmt.Errorf("pyroscope start failed (continuing without profiling): %w", profErr)
		}
		p.profiler = profiler
		otel.SetTracerProvider(otelpyroscope.NewTracerProvider(tp))
		return p, nil
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
			shutdownErr = errors.Join(shutdownErr, p.profiler.Stop())
		}
	})
	return shutdownErr
}
