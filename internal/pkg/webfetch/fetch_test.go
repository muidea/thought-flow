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
	body := strings.Repeat("Local body &amp; text with enough readable words for direct extraction. ", 8)
	local := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		if req.Header.Get("User-Agent") != "ThoughtFlow/0.1" {
			t.Fatalf("user agent = %q", req.Header.Get("User-Agent"))
		}
		res.Header().Set("Content-Type", "text/html")
		_, _ = res.Write([]byte(`<html><head><title>Local title</title></head><body><script>ignored()</script><main>` + body + `</main></body></html>`))
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
	if !strings.Contains(result.Content, "Local body & text with enough readable words") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestFetcherFormatsLocalHTMLForReadableMarkdown(t *testing.T) {
	paragraph := strings.Repeat("First paragraph &amp; details with enough words for local readable extraction. ", 8)
	local := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/html")
		_, _ = res.Write([]byte(`<html><head><title>Readable</title><style>.x{}</style></head><body><main><h1>Project</h1><p>` + paragraph + `</p><h2>Features</h2><ul><li>Embedding API</li><li>Keyword extraction</li></ul></main></body></html>`))
	}))
	defer local.Close()

	fetcher := New(time.Second)
	result, err := fetcher.Fetch(context.Background(), local.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if !strings.HasPrefix(result.Content, "# Project\n\nFirst paragraph & details with enough words") ||
		!strings.Contains(result.Content, "\n\n## Features\n\n- Embedding API\n- Keyword extraction") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestFetcherFallsBackToReaderWhenLocalContentIsTooShort(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/html")
		_, _ = res.Write([]byte(`<html><head><title>Short shell</title></head><body><main>Skip to content</main></body></html>`))
	}))
	defer local.Close()
	reader := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/plain")
		_, _ = res.Write([]byte("Title: Reader title\n\n# Reader heading\n\nReader body with useful extracted Markdown."))
	}))
	defer reader.Close()

	fetcher := New(time.Second, WithReaderBaseURL(reader.URL))
	result, err := fetcher.Fetch(context.Background(), local.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if result.Title != "Reader title" || !strings.Contains(result.Content, "Reader body with useful extracted Markdown.") {
		t.Fatalf("result = %#v", result)
	}
}

func TestFetcherFallsBackToReaderWhenLocalContentIsNavigationShell(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/html")
		nav := strings.Repeat("Navigation Menu Sign in Platform Actions Issues Pull requests Marketplace ", 12)
		_, _ = res.Write([]byte(`<html><head><title>Shell</title></head><body><main>Skip to content ` + nav + `</main></body></html>`))
	}))
	defer local.Close()
	reader := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/plain")
		_, _ = res.Write([]byte("Title: Clean reader\n\nMarkdown Content:\n# Clean project\n\nUseful README body."))
	}))
	defer reader.Close()

	fetcher := New(time.Second, WithReaderBaseURL(reader.URL))
	result, err := fetcher.Fetch(context.Background(), local.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if result.Title != "Clean reader" || !strings.Contains(result.Content, "Useful README body.") {
		t.Fatalf("result = %#v", result)
	}
}

func TestGitHubReadmeCandidates(t *testing.T) {
	candidates, title := githubReadmeCandidates("https://github.com/muidea/magicNLP")
	if title != "muidea/magicNLP" {
		t.Fatalf("title = %q", title)
	}
	expected := "https://raw.githubusercontent.com/muidea/magicNLP/master/README.md"
	if !contains(candidates, expected) {
		t.Fatalf("candidates = %#v, want %q", candidates, expected)
	}
	if candidates, _ := githubReadmeCandidates("https://example.com/muidea/magicNLP"); len(candidates) != 0 {
		t.Fatalf("non-github candidates = %#v", candidates)
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

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
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
