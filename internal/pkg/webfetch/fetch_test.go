package webfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetcherUsesLocalHTMLWhenAvailable(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		if req.Header.Get("User-Agent") != "ThoughtFlow/0.1" {
			t.Fatalf("user agent = %q", req.Header.Get("User-Agent"))
		}
		res.Header().Set("Content-Type", "text/html")
		_, _ = res.Write([]byte(`<html><head><title>Local title</title></head><body><script>ignored()</script><main>Local body &amp; text.</main></body></html>`))
	}))
	defer local.Close()

	fetcher := New(time.Second)
	result, err := fetcher.Fetch(context.Background(), local.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if result.Title != "Local title" {
		t.Fatalf("title = %q", result.Title)
	}
	if result.Content != "Local body & text." {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestFetcherFallsBackToJinaReader(t *testing.T) {
	localCalls := 0
	readerCalls := 0
	local := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		localCalls++
		http.Error(res, "origin blocked", http.StatusForbidden)
	}))
	defer local.Close()
	reader := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		readerCalls++
		if !strings.Contains(req.URL.Path, local.URL) {
			t.Fatalf("reader path = %q, want target URL %q", req.URL.Path, local.URL)
		}
		res.Header().Set("Content-Type", "text/plain")
		_, _ = res.Write([]byte("Title: Reader title\n\nReader body &amp; extracted text."))
	}))
	defer reader.Close()

	fetcher := New(time.Second, WithReaderBaseURL(reader.URL))
	result, err := fetcher.Fetch(context.Background(), local.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if localCalls != 1 || readerCalls != 1 {
		t.Fatalf("calls local=%d reader=%d", localCalls, readerCalls)
	}
	if result.Title != "Reader title" {
		t.Fatalf("title = %q", result.Title)
	}
	if !strings.Contains(result.Content, "Reader body & extracted text.") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestFetcherReturnsLocalErrorWhenReaderAlsoFails(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		http.Error(res, "origin failed", http.StatusBadGateway)
	}))
	defer local.Close()
	reader := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		http.Error(res, "reader failed", http.StatusBadGateway)
	}))
	defer reader.Close()

	fetcher := New(time.Second, WithReaderBaseURL(reader.URL))
	_, err := fetcher.Fetch(context.Background(), local.URL)
	if err == nil {
		t.Fatalf("expected fetch error")
	}
	if !strings.Contains(err.Error(), "local fetch returned status 502") {
		t.Fatalf("error = %v", err)
	}
}
