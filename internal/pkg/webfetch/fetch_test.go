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

func TestFetcherLocalExtractsLinks(t *testing.T) {
	body := strings.Repeat("Local body &amp; text with enough readable words for direct extraction. ", 8)
	html := `<html><head><title>Local</title></head><body><main>
		<h1>Project</h1>
		<p>` + body + `</p>
		<ul>
			<li><a href="https://example.com/a">Alpha</a></li>
			<li><a href="https://example.com/b">Beta</a></li>
			<li><a href="#section">Skip</a></li>
			<li><a href="mailto:x@y">Mail</a></li>
			<li><a href="/relative">Relative</a></li>
			<li><a href="javascript:void(0)">JS</a></li>
			<li><a href="https://example.com/a">Duplicate</a></li>
		</ul>
	</main></body></html>`
	local := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/html")
		_, _ = res.Write([]byte(html))
	}))
	defer local.Close()

	fetcher := New(time.Second)
	result, err := fetcher.Fetch(context.Background(), local.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(result.Links) != 2 {
		t.Fatalf("links length = %d, want 2 (got %#v)", len(result.Links), result.Links)
	}
	if result.Links[0].URL != "https://example.com/a" || result.Links[0].Title != "Alpha" {
		t.Fatalf("link[0] = %#v", result.Links[0])
	}
	if result.Links[1].URL != "https://example.com/b" || result.Links[1].Title != "Beta" {
		t.Fatalf("link[1] = %#v", result.Links[1])
	}
}

func TestFetcherReaderExtractsMarkdownLinks(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/html")
		_, _ = res.Write([]byte(`<html><head><title>Shell</title></head><body><main>Skip to content</main></body></html>`))
	}))
	defer local.Close()
	readerBody := "Title: Reader title\n\n# Heading\n\nSee [Alpha](https://example.com/a) and [Beta](https://example.com/b).\n\n[skip](#anchor) and [dup](https://example.com/a)."
	reader := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/plain")
		_, _ = res.Write([]byte(readerBody))
	}))
	defer reader.Close()

	fetcher := New(time.Second, WithReaderBaseURL(reader.URL))
	result, err := fetcher.Fetch(context.Background(), local.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(result.Links) != 2 {
		t.Fatalf("links length = %d, want 2 (got %#v)", len(result.Links), result.Links)
	}
	if result.Links[0].URL != "https://example.com/a" || result.Links[0].Title != "Alpha" {
		t.Fatalf("link[0] = %#v", result.Links[0])
	}
	if result.Links[1].URL != "https://example.com/b" || result.Links[1].Title != "Beta" {
		t.Fatalf("link[1] = %#v", result.Links[1])
	}
}

func TestFetcherLocalCapsFollowupLinksAtFive(t *testing.T) {
	body := strings.Repeat("Local body &amp; text with enough readable words for direct extraction. ", 8)
	var items strings.Builder
	for i := 0; i < 12; i++ {
		items.WriteString(`<li><a href="https://example.com/l` + intToA(i) + `">L` + intToA(i) + `</a></li>`)
	}
	local := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/html")
		_, _ = res.Write([]byte(`<html><head><title>Local</title></head><body><main>` + body + `<ul>` + items.String() + `</ul></main></body></html>`))
	}))
	defer local.Close()

	fetcher := New(time.Second)
	result, err := fetcher.Fetch(context.Background(), local.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(result.Links) != 5 {
		t.Fatalf("links length = %d, want 5 (got %#v)", len(result.Links), result.Links)
	}
}

func intToA(n int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	var out []byte
	for n > 0 {
		out = append([]byte{digits[n%10]}, out...)
		n /= 10
	}
	return string(out)
}
