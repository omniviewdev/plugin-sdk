package profiling

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLifecycle(t *testing.T) {
	disabled := New(Config{Port: 6060}, "settings")
	if err := disabled.Start(); err != nil || disabled.GetStatus().Running || disabled.GetStatus().URL != "" {
		t.Fatalf("disabled status: %+v %v", disabled.GetStatus(), err)
	}
	service := New(Config{Enabled: true, Port: 0}, "settings")
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	status := service.GetStatus()
	if !status.Running || status.URL == "" || strings.Contains(status.URL, ":0/") || status.Source != "settings" {
		t.Fatalf("bad active status: %+v", status)
	}
	if err := service.Start(); err != nil || service.GetStatus().URL != status.URL {
		t.Fatal("Start was not idempotent")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if closedStatus := service.GetStatus(); closedStatus.Running || closedStatus.URL != "" {
		t.Fatalf("closed status: %+v", closedStatus)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Start(); err == nil {
		t.Fatal("closed service restarted")
	}
}

func TestOccupiedPortAndServeFailure(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	service := New(Config{Enabled: true, Port: port}, "settings")
	if startErr := service.Start(); startErr == nil {
		t.Fatal("occupied port accepted")
	}
	if status := service.GetStatus(); status.Running || status.URL != "" || status.Error == "" {
		t.Fatalf("bind error status: %+v", status)
	}
	service = New(Config{Enabled: true, Port: 0}, "environment")
	if startErr := service.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	defer service.Close()
	if closeErr := service.listener.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for service.GetStatus().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := service.GetStatus()
	if status.Running || status.URL != "" || status.Error == "" || status.Source != "environment" {
		t.Fatalf("serve error status: %+v", status)
	}
}

func TestInvalidConfigurationStatus(t *testing.T) {
	for _, config := range []Config{
		{Enabled: true, Port: -1},
		{Enabled: true, Port: 65536},
		{Enabled: true, Diagnostic: "invalid settings"},
	} {
		service := New(config, "environment")
		if err := service.Start(); err == nil {
			t.Fatalf("invalid configuration accepted: %+v", config)
		}
		status := service.GetStatus()
		if status.Running || status.URL != "" || status.Error == "" {
			t.Fatalf("invalid configuration status: %+v", status)
		}
		if err := service.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
