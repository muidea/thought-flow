package webfetch

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Result struct {
	Title   string
	Content string
}

type Fetcher struct {
	client *http.Client
}

func New(timeout time.Duration) *Fetcher {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Fetcher{client: &http.Client{Timeout: timeout}}
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Result, error) {
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
	html := string(body)
	return Result{
		Title:   extractTitle(html),
		Content: htmlToText(html),
	}, nil
}

var (
	titleRe  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
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
	text := scriptRe.ReplaceAllString(html, " ")
	text = tagRe.ReplaceAllString(text, " ")
	return cleanText(text)
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
