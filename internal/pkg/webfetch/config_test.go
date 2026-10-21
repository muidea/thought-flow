package webfetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/appconfig"
)

func TestReaderDisabledMakesNoThirdPartyRequests(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var readerRequests atomic.Int64
			reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				readerRequests.Add(1)
				fmt.Fprint(w, "reader content")
			}))
			defer reader.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("Reader credentials leaked to origin")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, "short origin content")
			}))
			defer origin.Close()
			f := NewFromConfig(appconfig.ReaderConfig{Enabled: false, BaseURL: reader.URL, APIKey: "private-key", Timeout: time.Second})
			result, err := f.Fetch(context.Background(), origin.URL+"/?secret=private")
			if status == http.StatusOK && (err != nil || result.Content != "short origin content") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if status != http.StatusOK && err == nil {
				t.Fatal("expected origin error")
			}
			if got := readerRequests.Load(); got != 0 {
				t.Fatalf("third-party requests=%d, want 0", got)
			}
		})
	}
}

func TestReaderConfigUsesPrivateEndpointCredentialsAndTimeout(t *testing.T) {
	var requests atomic.Int64
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer private-key" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		if !strings.HasPrefix(r.URL.Path, "/private/http://") {
			t.Errorf("reader path=%q", r.URL.Path)
		}
		fmt.Fprint(w, "Title: Private Reader\nreader content")
	}))
	defer reader.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("Reader credentials leaked to origin")
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer origin.Close()
	f := NewFromConfig(appconfig.ReaderConfig{Enabled: true, BaseURL: reader.URL + "/private/", APIKey: "private-key", Timeout: 120 * time.Millisecond})
	if f.client.Timeout != 120*time.Millisecond {
		t.Fatalf("timeout=%v", f.client.Timeout)
	}
	result, err := f.Fetch(context.Background(), origin.URL)
	if err != nil || result.Title != "Private Reader" || requests.Load() != 1 {
		t.Fatalf("result=%+v requests=%d err=%v", result, requests.Load(), err)
	}
}
