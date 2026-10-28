package wetrakr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

type recorded struct {
	method, path, query string
	header              http.Header
	body                string
}

type fakeServer struct {
	t        *testing.T
	mu       sync.Mutex
	requests []recorded
	handler  func(w http.ResponseWriter, r *http.Request, n int)
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(body)})
	n := len(f.requests)
	f.mu.Unlock()
	f.handler(w, r, n)
}

func newTestClient(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int)) (*Client, *fakeServer, *TokenStore) {
	t.Helper()
	fs := &fakeServer{t: t, handler: handler}
	srv := httptest.NewServer(fs)
	t.Cleanup(srv.Close)
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	c := New(Options{BaseURL: srv.URL, ClientID: "cid", ClientSecret: "csecret", Store: store})
	c.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	c.token = &Token{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(72 * time.Hour).Unix()}
	return c, fs, store
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestHeaders(t *testing.T) {
	c, fs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, 200, map[string]any{"id": 44, "plan": "free", "info": map[string]any{"username": "bob"}})
	})
	acct, err := c.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if acct.Info.Username != "bob" {
		t.Errorf("username: got %q", acct.Info.Username)
	}
	h := fs.requests[0].header
	for name, want := range map[string]string{
		"wetrakr-api-key":     "cid",
		"wetrakr-api-version": "1",
		"Authorization":       "Bearer access",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("header %s: got %q, want %q", name, got, want)
		}
	}
	if h.Get("Idempotency-Key") != "" {
		t.Error("GET requests must not carry an idempotency key")
	}
}

func TestRetryReplaysBodyWithSameIdempotencyKey(t *testing.T) {
	c, fs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch n {
		case 1:
			w.Header().Set("Retry-After", "1")
			writeJSON(w, 429, map[string]any{"error": "CONCURRENCY_LIMIT", "message": "slow down"})
		case 2:
			writeJSON(w, 502, map[string]any{"message": "bad gateway"})
		default:
			writeJSON(w, 200, map[string]any{"added": map[string]any{"total": 1, "movies": 1}})
		}
	})
	var waits []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	rating := 8.0
	res, err := c.AddRatings(context.Background(), Items{Movies: []Item{{IDs: &IDs{IMDb: "tt0000001"}, Rating: &rating}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Errorf("added: got %d", res.Added)
	}
	if len(fs.requests) != 3 {
		t.Fatalf("requests: got %d, want 3", len(fs.requests))
	}
	key := fs.requests[0].header.Get("Idempotency-Key")
	for i, r := range fs.requests {
		if r.body != fs.requests[0].body || r.body == "" {
			t.Errorf("request %d body %q differs from first %q", i, r.body, fs.requests[0].body)
		}
		if r.header.Get("Idempotency-Key") != key || key == "" {
			t.Errorf("request %d idempotency key %q differs from %q", i, r.header.Get("Idempotency-Key"), key)
		}
	}
	if waits[0] < time.Second || waits[0] > 2*time.Second {
		t.Errorf("Retry-After not honoured: waited %s", waits[0])
	}
}

func TestQuotaErrorIsFatal(t *testing.T) {
	c, fs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, 429, map[string]any{"error": "QUOTA_EXCEEDED", "message": "daily quota"})
	})
	_, err := c.Ratings(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.IsQuota() {
		t.Fatalf("expected quota error, got %v", err)
	}
	if len(fs.requests) != 1 {
		t.Errorf("quota errors must not be retried: %d requests", len(fs.requests))
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	c, fs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, 503, map[string]any{"message": "down"})
	})
	if _, err := c.Ratings(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if len(fs.requests) != defaultAttempts {
		t.Errorf("requests: got %d, want %d", len(fs.requests), defaultAttempts)
	}
}

func TestErrorShapes(t *testing.T) {
	tests := []struct {
		body      string
		code, msg string
	}{
		{`{"message":"Not found"}`, "", "Not found"},
		{`{"error":"PLAN_LIMIT_REACHED","message":"too many lists"}`, "PLAN_LIMIT_REACHED", "too many lists"},
		{`{"success":false,"error":{"code":"INVALID_SEASON_ID","message":"bad season"}}`, "INVALID_SEASON_ID", "bad season"},
		{`not json`, "", "not json"},
	}
	for _, tt := range tests {
		e := parseError("GET", "/x", 400, []byte(tt.body))
		if e.Code != tt.code || e.Message != tt.msg {
			t.Errorf("%s: got code=%q msg=%q", tt.body, e.Code, e.Message)
		}
	}
	if !IsPlanLimit(&APIError{Status: 420}) {
		t.Error("420 must be a plan limit error")
	}
}

func TestCompactPagination(t *testing.T) {
	c, fs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch r.URL.Query().Get("after") {
		case "":
			w.Header().Set("X-Pagination-Next", "cursor-1")
			writeJSON(w, 200, []Row{{Type: "movie", ID: 1, IDs: IDs{IMDb: "tt1"}}})
		case "cursor-1":
			writeJSON(w, 200, []Row{{Type: "show", ID: 2, IDs: IDs{IMDb: "tt2"}}})
		}
	})
	rows, err := c.Tracking(context.Background(), StatusPlanning)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1].IDs.IMDb != "tt2" || rows[0].Status != StatusPlanning {
		t.Fatalf("rows: %+v", rows)
	}
	q := fs.requests[0].query
	if fs.requests[0].path != "/sync/tracking/planning/all" || q != "compact=true&limit=5000" {
		t.Errorf("first request: %s?%s", fs.requests[0].path, q)
	}
}

func TestPagedListItemsNestedAndFlat(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("X-Pagination-Page-Count", "2")
		if r.URL.Query().Get("page") == "1" {
			writeJSON(w, 200, []map[string]any{{"rank": 1, "movie": map[string]any{"id": 1, "ids": map[string]any{"imdb": "tt1"}}}})
			return
		}
		writeJSON(w, 200, []map[string]any{{"rank": 2, "type": "show", "id": 2, "ids": map[string]any{"imdb": "tt2", "tmdb": 5}}})
	})
	rows, err := c.ListItems(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Type != "movie" || rows[0].IDs.IMDb != "tt1" || rows[1].Type != "show" || rows[1].ID != 2 {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestWriteChunking(t *testing.T) {
	c, fs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, 200, map[string]any{
			"added":    map[string]any{"total": 2},
			"notFound": map[string]any{"movies": []any{map[string]any{"ids": map[string]any{"imdb": "tt-missing-" + strconv.Itoa(n)}}}},
		})
	})
	c.chunkSize = 2
	allow := false
	items := Items{AllowRewatch: &allow}
	for i := range 5 {
		items.Add(TypeMovie, Item{IDs: &IDs{IMDb: fmt.Sprintf("tt%d", i)}, Status: StatusWatched})
	}
	res, err := c.AddTracking(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.requests) != 3 {
		t.Fatalf("requests: got %d, want 3", len(fs.requests))
	}
	if res.Added != 6 || len(res.NotFound.Movies) != 3 {
		t.Errorf("merged result: %+v", res)
	}
	var last Items
	if err := json.Unmarshal([]byte(fs.requests[2].body), &last); err != nil {
		t.Fatal(err)
	}
	if len(last.Movies) != 1 || last.AllowRewatch == nil || *last.AllowRewatch {
		t.Errorf("last chunk: %s", fs.requests[2].body)
	}
}

func TestRefreshOn401SavesRotatedToken(t *testing.T) {
	c, fs, store := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch {
		case r.URL.Path == "/oauth/token/refresh":
			writeJSON(w, 200, map[string]any{"access_token": "access-2", "refresh_token": "refresh-2", "expires_in": 604800})
		case r.Header.Get("Authorization") == "Bearer access":
			writeJSON(w, 401, map[string]any{"error": "INVALID_TOKEN", "message": "expired"})
		default:
			writeJSON(w, 200, []Row{})
		}
	})
	if _, err := c.Ratings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.requests) != 3 {
		t.Fatalf("requests: got %d, want 3", len(fs.requests))
	}
	if fs.requests[1].body != `{"refresh_token":"refresh"}` {
		t.Errorf("refresh body: %s", fs.requests[1].body)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "access-2" || saved.RefreshToken != "refresh-2" || saved.ExpiresAt == 0 {
		t.Errorf("saved token: %+v", saved)
	}
}

func TestEnsureTokenRefreshesNearExpiry(t *testing.T) {
	c, fs, store := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, 200, map[string]any{"access_token": "access-2", "refresh_token": "old-is-ignored", "new_refresh_token": "refresh-2", "expires_in": 604800})
	})
	c.token = nil
	if err := store.Save(&Token{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.requests) != 1 || fs.requests[0].path != "/oauth/token/refresh" {
		t.Fatalf("expected one refresh request, got %+v", fs.requests)
	}
	if got := c.currentToken(); got.RefreshToken != "refresh-2" {
		t.Errorf("rotated refresh token not used: %+v", got)
	}

	// A fresh token is not refreshed.
	fs.requests = nil
	if err := c.EnsureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.requests) != 0 {
		t.Errorf("unexpected requests: %+v", fs.requests)
	}
}

func TestEnsureTokenWithoutFile(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {})
	c.token = nil
	if err := c.EnsureToken(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("expected ErrNoToken, got %v", err)
	}
}

func TestDeviceFlow(t *testing.T) {
	c, fs, store := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch {
		case r.URL.Path == "/oauth/device/code":
			writeJSON(w, 200, map[string]any{"device_code": "dev", "user_code": "K7PX4M", "verification_url": "https://wetrakr.com/activate", "expires_in": 600, "interval": 5})
		case n == 2:
			writeJSON(w, 400, map[string]any{"error": "authorization_pending"})
		case n == 3:
			writeJSON(w, 429, map[string]any{"error": "slow_down"})
		default:
			writeJSON(w, 200, map[string]any{"access_token": "access-new", "refresh_token": "refresh-new", "expires_in": 604800})
		}
	})
	c.token = nil
	var waits []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	dc, err := c.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "K7PX4M" {
		t.Errorf("user code: %q", dc.UserCode)
	}
	tok, err := c.PollDeviceToken(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access-new" {
		t.Errorf("token: %+v", tok)
	}
	if got := waits; len(got) != 3 || got[0] != 5*time.Second || got[2] != 10*time.Second {
		t.Errorf("poll intervals: %v", got)
	}
	var body map[string]string
	_ = json.Unmarshal([]byte(fs.requests[1].body), &body)
	if body["code"] != "dev" || body["client_id"] != "cid" || body["client_secret"] != "csecret" {
		t.Errorf("token poll body: %v", body)
	}
	if saved, err := store.Load(); err != nil || saved.RefreshToken != "refresh-new" {
		t.Errorf("saved token: %+v, %v", saved, err)
	}
}

func TestDeviceFlowDenied(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, http.StatusTeapot, map[string]any{"error": "access_denied"})
	})
	if _, err := c.PollDeviceToken(context.Background(), &DeviceCode{DeviceCode: "dev", Interval: 1, ExpiresIn: 600}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCount(t *testing.T) {
	tests := map[string]int{
		`{"total":3,"movies":1}`: 3,
		`{"movies":2,"shows":1}`: 3,
		`{"planning":{"movies":2,"shows":1},"watched":{"movies":1}}`: 4,
		`{"movies":[{"id":1},{"id":2}]}`:                             2,
		`{}`:                                                         0,
		``:                                                           0,
	}
	for in, want := range tests {
		if got := count(json.RawMessage(in)); got != want {
			t.Errorf("count(%s): got %d, want %d", in, got, want)
		}
	}
}

func TestTokenExpiryFromJWT(t *testing.T) {
	// {"exp":2000000000}
	tok := &Token{AccessToken: "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjIwMDAwMDAwMDB9.sig"}
	if got := tok.expiry(); got.Unix() != 2000000000 {
		t.Errorf("expiry: got %v", got)
	}
}

func TestNeedsReauth(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{ErrNoToken, true},
		{fmt.Errorf("wrapped: %w", ErrNoToken), true},
		{fmt.Errorf("failure refreshing: %w", &APIError{Status: 401, Code: CodeInvalidRefreshToken}), true},
		{errors.Join(&APIError{Status: 401, Code: CodeTokenRevoked}, errors.New("refresh failed")), true},
		{&APIError{Status: 429, Code: CodeQuotaExceeded}, false},
		{&APIError{Status: 503}, false},
		{errors.New("network down"), false},
	}
	for _, tt := range tests {
		if got := NeedsReauth(tt.err); got != tt.want {
			t.Errorf("NeedsReauth(%v): got %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestCorruptTokenFileNeedsReauth(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	if err := os.WriteFile(store.Path(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !NeedsReauth(err) {
		t.Fatalf("expected a re-authorization error, got %v", err)
	}
}

func TestRefreshRejectedNeedsReauth(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, 401, map[string]any{"error": CodeInvalidRefreshToken})
	})
	c.token.ExpiresAt = time.Now().Unix()
	if err := c.EnsureToken(context.Background()); !NeedsReauth(err) {
		t.Fatalf("expected a re-authorization error, got %v", err)
	}
}

func TestCheckAccount(t *testing.T) {
	a := &Account{}
	a.Info.Username = "cecobask"
	for _, want := range []string{"", "cecobask", "CecoBask", " @cecobask "} {
		if err := CheckAccount(a, want); err != nil {
			t.Errorf("CheckAccount(%q): %v", want, err)
		}
	}
	var wrong *WrongAccountError
	if err := CheckAccount(a, "someone-else"); !errors.As(err, &wrong) || wrong.Got != "cecobask" {
		t.Errorf("expected a WrongAccountError, got %v", err)
	}
}

func TestTokenStoreDelete(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	if err := store.Delete(); err != nil {
		t.Fatalf("deleting a missing file: %v", err)
	}
	if err := store.Save(&Token{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNoToken) {
		t.Errorf("expected ErrNoToken after delete, got %v", err)
	}
}
