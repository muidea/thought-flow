package webfetch

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
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
	if github, err := f.fetchGitHubReadme(ctx, rawURL); err == nil && usefulContent(github.Content) {
		return github, nil
	}
	local, err := f.fetchLocal(ctx, rawURL)
	if err == nil && usefulContent(local.Content) && !noisyShellContent(local.Content) {
		return local, nil
	}
	reader, readerErr := f.fetchReader(ctx, rawURL)
	if readerErr == nil && strings.TrimSpace(reader.Content) != "" {
		return reader, nil
	}
	if err == nil && strings.TrimSpace(local.Content) != "" {
		return local, nil
	}
	if err != nil {
		return Result{}, err
	}
	if readerErr != nil {
		return Result{}, readerErr
	}
	return Result{}, errors.New("fetched content is empty")
}

func (f *Fetcher) fetchGitHubReadme(ctx context.Context, rawURL string) (Result, error) {
	candidates, title := githubReadmeCandidates(rawURL)
	if len(candidates) == 0 {
		return Result{}, errors.New("not a github repository url")
	}
	var lastErr error
	for _, candidate := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate, nil)
		if err != nil {
			return Result{}, err
		}
		req.Header.Set("User-Agent", "ThoughtFlow/0.1")
		res, err := f.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(res.Body, 2<<20))
		_ = res.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			lastErr = fmt.Errorf("github readme returned status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
			continue
		}
		content := cleanReaderText(string(body))
		if strings.TrimSpace(content) != "" {
			return Result{Title: title, Content: content}, nil
		}
	}
	if lastErr != nil {
		return Result{}, lastErr
	}
	return Result{}, errors.New("github readme content is empty")
}

func githubReadmeCandidates(rawURL string) ([]string, string) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, ""
	}
	if !strings.EqualFold(parsed.Hostname(), "github.com") {
		return nil, ""
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nil, ""
	}
	owner, repo := parts[0], parts[1]
	if len(parts) > 2 && parts[2] != "" && parts[2] != "tree" && parts[2] != "blob" {
		return nil, ""
	}
	base := "https://raw.githubusercontent.com/" + owner + "/" + repo
	return []string{
		base + "/main/README.md",
		base + "/master/README.md",
		base + "/main/readme.md",
		base + "/master/readme.md",
	}, owner + "/" + repo
}

func usefulContent(content string) bool {
	content = strings.TrimSpace(content)
	if len([]rune(content)) < 400 {
		return false
	}
	words := strings.Fields(content)
	return len(words) >= 40
}

func noisyShellContent(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, "skip to content") &&
		strings.Contains(lower, "navigation menu") &&
		strings.Contains(lower, "sign in")
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
	scriptRe = regexp.MustCompile(`(?is)<(script|style|svg|noscript)[^>]*>.*?</(script|style|svg|noscript)>`)
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
	text = blockTagsToMarkdownBreaks(text)
	text = tagRe.ReplaceAllString(text, " ")
	return cleanStructuredText(text)
}

func blockTagsToMarkdownBreaks(text string) string {
	replacements := []struct {
		pattern string
		value   string
	}{
		{`(?is)<br\s*/?>`, "\n"},
		{`(?is)<hr\s*/?>`, "\n\n---\n\n"},
		{`(?is)<li[^>]*>`, "\n- "},
		{`(?is)</li>`, ""},
		{`(?is)<h1[^>]*>`, "\n\n# "},
		{`(?is)<h2[^>]*>`, "\n\n## "},
		{`(?is)<h3[^>]*>`, "\n\n### "},
		{`(?is)<h4[^>]*>`, "\n\n#### "},
		{`(?is)<h5[^>]*>`, "\n\n##### "},
		{`(?is)<h6[^>]*>`, "\n\n###### "},
		{`(?is)</h[1-6]>`, "\n\n"},
		{`(?is)</p>`, "\n\n"},
		{`(?is)</div>`, "\n"},
		{`(?is)</section>`, "\n\n"},
		{`(?is)</article>`, "\n\n"},
		{`(?is)</main>`, "\n\n"},
		{`(?is)</header>`, "\n\n"},
		{`(?is)</footer>`, "\n\n"},
		{`(?is)</blockquote>`, "\n\n"},
		{`(?is)</pre>`, "\n\n"},
		{`(?is)</tr>`, "\n"},
		{`(?is)</table>`, "\n\n"},
	}
	for _, replacement := range replacements {
		text = regexp.MustCompile(replacement.pattern).ReplaceAllString(text, replacement.value)
	}
	return text
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
	text = html.UnescapeString(text)
	return strings.TrimSpace(spaceRe.ReplaceAllString(text, " "))
}

func cleanStructuredText(text string) string {
	text = html.UnescapeString(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := make([]string, 0, len(strings.Split(text, "\n")))
	previousBlank := true
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(spaceRe.ReplaceAllString(line, " "))
		if line == "" {
			if !previousBlank {
				lines = append(lines, "")
			}
			previousBlank = true
			continue
		}
		lines = append(lines, line)
		previousBlank = false
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
