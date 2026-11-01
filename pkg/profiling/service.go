// Package profiling owns an explicitly enabled, loopback-only process profiler.
package profiling

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"sync"
	"time"
)

const (
	maxCPUProfileSeconds = 120
	maxTraceSeconds      = 15
)

type Config struct {
	Enabled bool
	Port    int
	// Diagnostic prevents invalid configuration from enabling a listener.
	Diagnostic string
}

type Status struct {
	Running bool   `json:"running"`
	URL     string `json:"url"`
	Source  string `json:"source"`
	Error   string `json:"error"`
}

type Service struct {
	mu       sync.Mutex
	config   Config
	status   Status
	server   *http.Server
	listener net.Listener
	closed   bool
}

func New(config Config, source string) *Service {
	return &Service{config: config, status: Status{Source: source, Error: config.Diagnostic}}
}

// Start is idempotent and uses the configuration snapshot supplied to New.
func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("profiling service is closed")
	}
	if s.server != nil {
		return nil
	}
	if s.config.Diagnostic != "" {
		return errors.New(s.config.Diagnostic)
	}
	if s.config.Port < 0 || s.config.Port > 65535 {
		s.status.Error = "Profiling port must be from 0 to 65535."
		return errors.New(s.status.Error)
	}
	if !s.config.Enabled {
		return nil
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.config.Port)))
	if err != nil {
		s.status.Error = fmt.Sprintf("Profiling could not listen on 127.0.0.1:%d: %v", s.config.Port, err)
		return errors.New(s.status.Error)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", boundedPprofHandler(pprof.Profile, maxCPUProfileSeconds))
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", boundedPprofHandler(pprof.Trace, maxTraceSeconds))
	server := &http.Server{
		Addr:              listener.Addr().String(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       30 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != listener.Addr().String() || len(r.Header.Values("Origin")) != 0 {
				http.Error(w, "pprof requires a direct loopback request without Origin", http.StatusForbidden)
				return
			}
			mux.ServeHTTP(w, r)
		}),
	}
	s.server, s.listener = server, listener
	s.status.Running, s.status.URL, s.status.Error = true, "http://"+server.Addr+"/debug/pprof/", ""
	go func() {
		serveErr := server.Serve(listener)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.status.Running, s.status.URL = false, ""
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !s.closed {
			s.status.Error = fmt.Sprintf("Profiling server failed: %v", serveErr)
		}
	}()
	return nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	s.status.Running, s.status.URL = false, ""
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Close()
}

func (s *Service) GetStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// boundedPprofHandler prevents HEAD captures and caps explicit durations.
func boundedPprofHandler(handler http.HandlerFunc, maxSeconds int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "profiling captures require GET", http.StatusMethodNotAllowed)
			return
		}
		// Captures use query parameters only, never the request body.
		r.Form = r.URL.Query()
		seconds, err := strconv.ParseFloat(r.Form.Get("seconds"), 64)
		if err == nil && seconds > float64(maxSeconds) {
			r.Form.Set("seconds", strconv.Itoa(maxSeconds))
		}
		handler(w, r)
	}
}
