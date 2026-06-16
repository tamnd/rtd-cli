// Package rtd is the library behind the rtd command line: the HTTP client,
// request shaping, and typed data models for the ReadTheDocs API v3.
//
// The ReadTheDocs API v3 is documented at https://docs.readthedocs.io/en/stable/api/v3.html.
// Project listing and details require an API token (RTD_TOKEN env var).
// The search endpoint also requires a token in practice.
package rtd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultUserAgent identifies the client to ReadTheDocs.
const DefaultUserAgent = "rtd/dev (+https://github.com/tamnd/rtd-cli)"

// Host is the ReadTheDocs hostname.
const Host = "readthedocs.org"

// BaseURL is the root every request is built from.
const BaseURL = "https://" + Host

// apiBase is the API v3 base URL.
const apiBase = BaseURL + "/api/v3"

// ErrNotFound is returned when the API returns 404.
var ErrNotFound = errors.New("not found")

// ErrUnauthorized is returned when the API returns 401 or 403.
var ErrUnauthorized = errors.New("unauthorized: set RTD_TOKEN env var or pass --token")

// Project represents a ReadTheDocs project.
type Project struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Language string `json:"language"`
	URL      string `json:"url"`
	Homepage string `json:"homepage"`
}

// Page is the record type for raw HTML page navigation (scaffold fallback).
// The real domain objects are Project and SearchResult.
type Page struct {
	ID    string `json:"id" kit:"id"`
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty" kit:"body"`
}

// SearchResult represents one hit from the RTD search API.
type SearchResult struct {
	Project string `json:"project"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// rawProject decodes the nested RTD API shape.
type rawProject struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Language struct {
		Code string `json:"code"`
	} `json:"language"`
	URLs struct {
		Documentation string `json:"documentation"`
		Home          string `json:"home"`
	} `json:"urls"`
}

func (r rawProject) toProject() Project {
	return Project{
		Slug:     r.Slug,
		Name:     r.Name,
		Language: r.Language.Code,
		URL:      r.URLs.Documentation,
		Homepage: r.URLs.Home,
	}
}

type projectListResp struct {
	Count   int          `json:"count"`
	Results []rawProject `json:"results"`
}

type searchResp struct {
	Count   int `json:"count"`
	Results []struct {
		Project struct {
			Slug string `json:"slug"`
		} `json:"project"`
		Title  string `json:"title"`
		Domain string `json:"domain"`
		Blocks []struct {
			Content string `json:"content"`
		} `json:"blocks"`
	} `json:"results"`
}

// Client talks to the ReadTheDocs API v3.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	Token     string
	// Rate is the minimum gap between requests. Zero means no pacing.
	Rate    time.Duration
	Retries int

	last time.Time
}

// NewClient returns a Client with sensible defaults.
func NewClient() *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		UserAgent: DefaultUserAgent,
		Rate:      200 * time.Millisecond,
		Retries:   3,
	}
}

// GetProject fetches a single project by slug.
func (c *Client) GetProject(ctx context.Context, slug string) (*Project, error) {
	slug = strings.Trim(slug, "/")
	u := fmt.Sprintf("%s/projects/%s/", apiBase, url.PathEscape(slug))
	var raw rawProject
	if err := c.getJSON(ctx, u, &raw); err != nil {
		return nil, fmt.Errorf("project %q: %w", slug, err)
	}
	p := raw.toProject()
	return &p, nil
}

// ListProjects returns up to limit projects. Requires a token.
func (c *Client) ListProjects(ctx context.Context, limit int) ([]Project, error) {
	if limit <= 0 {
		limit = 25
	}
	u := fmt.Sprintf("%s/projects/?limit=%d", apiBase, limit)
	var resp projectListResp
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]Project, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, r.toProject())
	}
	return out, nil
}

// Search searches RTD for query.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 25
	}
	u := fmt.Sprintf("%s/search/?q=%s&page_size=%d", apiBase, url.QueryEscape(query), limit)
	var resp searchResp
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("search %q: %w", query, err)
	}
	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		sr := SearchResult{
			Project: r.Project.Slug,
			Title:   r.Title,
			URL:     r.Domain,
		}
		if len(r.Blocks) > 0 {
			s := r.Blocks[0].Content
			if len(s) > 200 {
				s = s[:200]
			}
			sr.Snippet = s
		}
		out = append(out, sr)
	}
	return out, nil
}

// GetPage fetches one page by its path (raw HTML navigation, scaffold fallback).
func (c *Client) GetPage(ctx context.Context, path string) (*Page, error) {
	path = strings.Trim(path, "/")
	u := BaseURL + "/" + path
	body, err := c.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	return &Page{ID: path, URL: u, Title: path, Body: pageText(body)}, nil
}

// PageLinks fetches a page and returns linked pages as stubs.
func (c *Client) PageLinks(ctx context.Context, path string, limit int) ([]*Page, error) {
	path = strings.Trim(path, "/")
	body, err := c.Get(ctx, BaseURL+"/"+path)
	if err != nil {
		return nil, err
	}
	var out []*Page
	seen := map[string]bool{}
	for _, p := range linkPaths(body) {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, &Page{ID: p, URL: BaseURL + "/" + p})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Get fetches url and returns the body.
func (c *Client) Get(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		body, retry, err := c.do(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("get %s: %w", rawURL, lastErr)
}

func (c *Client) getJSON(ctx context.Context, rawURL string, v any) error {
	body, err := c.Get(ctx, rawURL)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s: %w", rawURL, err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, rawURL string) ([]byte, bool, error) {
	c.pace()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Token "+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, false, ErrUnauthorized
	case http.StatusNotFound:
		return nil, false, ErrNotFound
	case http.StatusTooManyRequests:
		return nil, true, fmt.Errorf("http 429")
	}
	if resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, true, err
	}
	return b, false, nil
}

func (c *Client) pace() {
	if c.Rate <= 0 {
		return
	}
	if wait := c.Rate - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
}

func backoff(attempt int) time.Duration {
	d := time.Duration(attempt) * 500 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

var (
	hrefRE = regexp.MustCompile(`href="(/[^":#?]+)"`)
	tagRE  = regexp.MustCompile(`<[^>]+>`)
)

func linkPaths(body []byte) []string {
	var out []string
	for _, m := range hrefRE.FindAllSubmatch(body, -1) {
		if p := strings.Trim(string(m[1]), "/"); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func pageText(body []byte) string {
	s := strings.Join(strings.Fields(tagRE.ReplaceAllString(string(body), " ")), " ")
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}
