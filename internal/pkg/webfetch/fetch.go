package webfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	mnet "github.com/muidea/magicCommon/foundation/net"
)

type Result struct {
	Title   string
	Content string
}

type Fetcher struct {
	client        *http.Client
	readerBaseURL string
}

type Option func(*Fetcher)

func WithReaderBaseURL(baseURL string) Option {
	return func(f *Fetcher) {
		if strings.TrimSpace(baseURL) != "" {
			f.readerBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/"
		}
	}
}

func New(timeout time.Duration, options ...Option) *Fetcher {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := mnet.NewDNSCacheHttpClient()
	client.Timeout = timeout
	fetcher := &Fetcher{
		client:        client,
		readerBaseURL: "https://r.jina.ai/http://",
	}
	for _, option := range options {
		option(fetcher)
	}
	return fetcher
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Result, error) {
	local, err := f.fetchLocal(ctx, rawURL)
	if err == nil && strings.TrimSpace(local.Content) != "" {
		return local, nil
	}
	reader, readerErr := f.fetchReader(ctx, rawURL)
	if readerErr == nil && strings.TrimSpace(reader.Content) != "" {
		return reader, nil
	}
	if err != nil {
		return Result{}, err
	}
	if readerErr != nil {
		return Result{}, readerErr
	}
	return Result{}, errors.New("fetched content is empty")
}

func (f *Fetcher) fetchLocal(ctx context.Context, rawURL string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "ThoughtFlow/0.1")
	res, err := f.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return Result{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Result{}, fmt.Errorf("local fetch returned status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	html := string(body)
	return Result{
		Title:   extractTitle(html),
		Content: htmlToText(html),
	}, nil
}

func (f *Fetcher) fetchReader(ctx context.Context, rawURL string) (Result, error) {
	readerURL := f.readerBaseURL + strings.TrimSpace(rawURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readerURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "ThoughtFlow/0.1")
	res, err := f.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return Result{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Result{}, fmt.Errorf("jina reader returned status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	rawText := string(body)
	content := cleanReaderText(rawText)
	return Result{
		Title:   extractReaderTitle(rawText),
		Content: content,
	}, nil
}

var (
	titleRe  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	headRe   = regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`)
	scriptRe = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	tagRe    = regexp.MustCompile(`(?is)<[^>]+>`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

func extractTitle(html string) string {
	match := titleRe.FindStringSubmatch(html)
	if len(match) < 2 {
		return ""
	}
	return cleanText(match[1])
}

func htmlToText(html string) string {
	text := headRe.ReplaceAllString(html, " ")
	text = scriptRe.ReplaceAllString(text, " ")
	text = tagRe.ReplaceAllString(text, " ")
	return cleanText(text)
}

func extractReaderTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "title:") {
			return strings.TrimSpace(line[len("Title:"):])
		}
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func cleanReaderText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	replacements := map[string]string{
		"&nbsp;": " ",
		"&amp;":  "&",
		"&lt;":   "<",
		"&gt;":   ">",
		"&quot;": `"`,
		"&#39;":  "'",
	}
	for old, replacement := range replacements {
		text = strings.ReplaceAll(text, old, replacement)
	}
	lines := []string{}
	for _, line := range strings.Split(text, "\n") {
		lines = append(lines, strings.TrimSpace(spaceRe.ReplaceAllString(line, " ")))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func cleanText(text string) string {
	replacements := map[string]string{
		"&nbsp;": " ",
		"&amp;":  "&",
		"&lt;":   "<",
		"&gt;":   ">",
		"&quot;": `"`,
		"&#39;":  "'",
	}
	for old, replacement := range replacements {
		text = strings.ReplaceAll(text, old, replacement)
	}
	return strings.TrimSpace(spaceRe.ReplaceAllString(text, " "))
}
