package service

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/eventstream"
	"time"
)

func TestPrivacyReaderEnabledIsIndependentOfCredentials(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, key := range []string{"", "private-key"} {
			cfg := appconfig.Config{Reader: appconfig.ReaderConfig{Enabled: enabled, APIKey: key, BaseURL: "https://reader.example/"}}
			report := buildPrivacyReport(cfg, time.Now())
			if report.Reader.Enabled != enabled || report.Reader.Configured != (key != "") {
				t.Fatalf("inaccurate reader surface: %+v", report.Reader)
			}
			for _, action := range report.Actions {
				if action.Action == "url_capture" || action.Action == "capture_commit" {
					hasReader := false
					for _, surface := range action.Surfaces {
						hasReader = hasReader || surface == "reader"
					}
					if hasReader != enabled {
						t.Fatalf("reader disclosure inaccurate: %+v", action)
					}
				}
			}
		}
	}
}

func TestConfiguredSSEHeartbeatIsUsed(t *testing.T) {
	service := &Service{stream: eventstream.New(8), config: appconfig.Config{Events: appconfig.EventsConfig{SSEHeartbeat: 10 * time.Millisecond}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { service.handleEvents(context.Background(), w, r) }))
	defer server.Close()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal("configured short heartbeat did not flush", err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != ": ping" {
		t.Fatalf("heartbeat = %q %v", line, err)
	}
}
