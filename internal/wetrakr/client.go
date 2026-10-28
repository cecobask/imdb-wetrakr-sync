// Package wetrakr is a client for the WeTrakr API (https://api.wetrakr.com).
package wetrakr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	apiVersion       = "1"
	defaultChunkSize = 1000
	compactPageSize  = 5000
	defaultAttempts  = 5
	maxBackoff       = 30 * time.Second
	maxRateLimitWait = 90 * time.Second
)

// Options configures a Client.
type Options struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	Store        *TokenStore
	HTTPClient   *http.Client
	Logger       *slog.Logger
}

// Client talks to the WeTrakr API. It is safe for concurrent use, but the sync runs its writes one at a time to stay
// within the limit of two concurrent writes per user.
type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	httpClient   *http.Client
	store        *TokenStore
	logger       *slog.Logger

	mu    sync.Mutex
	token *Token

	// Overridable in tests.
	maxAttempts int
	chunkSize   int
	sleep       func(ctx context.Context, d time.Duration) error
	now         func() time.Time
}

// New returns a client. Call EnsureToken before any request that acts on the user.
func New(o Options) *Client {
	httpClient := o.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Client{
		baseURL:      o.BaseURL,
		clientID:     o.ClientID,
		clientSecret: o.ClientSecret,
		httpClient:   httpClient,
		store:        o.Store,
		logger:       logger,
		maxAttempts:  defaultAttempts,
		chunkSize:    defaultChunkSize,
		sleep:        sleepCtx,
		now:          time.Now,
	}
}

type request struct {
	method string
	path   string
	query  url.Values
	body   any
	auth   bool
}

// do sends the request and decodes a successful response into out. It retries rate limits (429), server errors (5xx)
// and network errors, refreshes the access token once on a 401, and returns an *APIError for any other failure.
// Writes carry an Idempotency-Key that stays the same across retries, so a retried write is applied once.
func (c *Client) do(ctx context.Context, r request, out any) (http.Header, error) {
	var body []byte
	if r.body != nil {
		var err error
		if body, err = json.Marshal(r.body); err != nil {
			return nil, fmt.Errorf("wetrakr: failure encoding request body: %w", err)
		}
	}
	var idempotencyKey string
	if r.method != http.MethodGet {
		idempotencyKey = uuid.NewString()
	}
	refreshed := false
	for attempt := 1; ; attempt++ {
		status, header, respBody, err := c.send(ctx, r, body, idempotencyKey)
		var wait time.Duration
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			err = fmt.Errorf("wetrakr: %s %s: %w", r.method, r.path, err)
			wait = backoff(attempt)
		case status >= 200 && status < 300:
			if out != nil && len(bytes.TrimSpace(respBody)) > 0 {
				if err := json.Unmarshal(respBody, out); err != nil {
					return header, fmt.Errorf("wetrakr: %s %s: failure decoding response: %w", r.method, r.path, err)
				}
			}
			return header, nil
		case status == http.StatusUnauthorized && r.auth && !refreshed:
			refreshed = true
			apiErr := parseError(r.method, r.path, status, respBody)
			c.logger.Debug("access token rejected, refreshing", "code", apiErr.Code)
			if err := c.Refresh(ctx); err != nil {
				return header, fmt.Errorf("%w; %w", apiErr, err)
			}
			attempt--
			continue
		case status == http.StatusTooManyRequests:
			apiErr := parseError(r.method, r.path, status, respBody)
			if apiErr.IsQuota() {
				return header, apiErr
			}
			err = apiErr
			wait = rateLimitWait(header, attempt)
		case status >= 500:
			err = parseError(r.method, r.path, status, respBody)
			wait = backoff(attempt)
		default:
			return header, parseError(r.method, r.path, status, respBody)
		}
		if attempt >= c.maxAttempts {
			return header, fmt.Errorf("giving up after %d attempts: %w", attempt, err)
		}
		c.logger.Debug("retrying request", "method", r.method, "path", r.path, "attempt", attempt, "wait", wait, "error", err)
		if err := c.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

func (c *Client) send(ctx context.Context, r request, body []byte, idempotencyKey string) (int, http.Header, []byte, error) {
	u := c.baseURL + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("wetrakr-api-key", c.clientID)
	req.Header.Set("wetrakr-api-version", apiVersion)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "imdb-wetrakr-sync")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if r.auth {
		token := c.currentToken()
		if token == nil {
			return 0, nil, nil, ErrNoToken
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, respBody, nil
}

// compactRows walks a compact listing with the X-Pagination-Next cursor and returns every row.
func (c *Client) compactRows(ctx context.Context, path string, query url.Values) ([]Row, error) {
	q := url.Values{"compact": {"true"}, "limit": {strconv.Itoa(compactPageSize)}}
	for k, v := range query {
		q[k] = v
	}
	var rows []Row
	seen := map[string]bool{}
	for {
		var page []Row
		header, err := c.do(ctx, request{method: http.MethodGet, path: path, query: q, auth: true}, &page)
		if err != nil {
			return nil, err
		}
		rows = append(rows, page...)
		next := header.Get("X-Pagination-Next")
		if next == "" || len(page) == 0 {
			return rows, nil
		}
		if seen[next] {
			return nil, fmt.Errorf("wetrakr: GET %s: pagination cursor %s repeated", path, next)
		}
		seen[next] = true
		q.Set("after", next)
	}
}

// pagedGet walks a page-numbered listing and returns every element.
func pagedGet[T any](ctx context.Context, c *Client, path string, limit int) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(limit)}}
		var items []T
		header, err := c.do(ctx, request{method: http.MethodGet, path: path, query: q, auth: true}, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		pageCount, _ := strconv.Atoi(header.Get("X-Pagination-Page-Count"))
		if len(items) == 0 || page >= pageCount {
			return all, nil
		}
	}
}

// write posts the items in chunks and merges the results. On failure it returns the results of the chunks that
// succeeded together with the error.
func (c *Client) write(ctx context.Context, path string, items Items) (WriteResult, error) {
	var total WriteResult
	for _, chunk := range items.chunks(c.chunkSize) {
		var resp writeResponse
		if _, err := c.do(ctx, request{method: http.MethodPost, path: path, body: chunk, auth: true}, &resp); err != nil {
			return total, err
		}
		total.merge(resp.result())
	}
	return total, nil
}

func backoff(attempt int) time.Duration {
	d := time.Second << (attempt - 1)
	if d > maxBackoff {
		d = maxBackoff
	}
	return d/2 + rand.N(d/2+1)
}

// rateLimitWait honours Retry-After, then RateLimit-Reset (seconds until the window resets), then falls back to backoff.
func rateLimitWait(h http.Header, attempt int) time.Duration {
	for _, name := range []string{"Retry-After", "RateLimit-Reset"} {
		if secs, err := strconv.Atoi(h.Get(name)); err == nil && secs >= 0 {
			d := time.Duration(secs)*time.Second + 250*time.Millisecond
			return min(d, maxRateLimitWait)
		}
	}
	return backoff(attempt)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
