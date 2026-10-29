//go:build browser

package imdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const testCSV = "Const,Your Rating,Date Rated,Title,Title Type\ntt0000001,7,2024-01-01,Good,Movie\n"

func newTestSession(t *testing.T) (*browserSession, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	// CI names the browser, so a broken one fails there instead of skipping the tests.
	path := os.Getenv("IWS_IMDB_BROWSERPATH")
	s, err := startBrowser(ctx, browserConfig{headless: true, browserPath: path}, discard)
	if err != nil {
		if path != "" {
			t.Fatalf("failure starting %s: %v", path, err)
		}
		t.Skipf("no usable Chrome: %v", err)
	}
	t.Cleanup(s.close)
	return s, ctx
}

func TestBrowserHelpers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>
			<a id="dl" href="/export.csv">Download</a>
			<script>setTimeout(() => { const d = document.createElement("div"); d.id = "late"; document.body.appendChild(d); }, 500)</script>
		</body></html>`))
	})
	mux.HandleFunc("/export.csv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="ratings.csv"`)
		_, _ = w.Write([]byte(testCSV))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s, ctx := newTestSession(t)
	if s.userAgent == "" || strings.Contains(s.userAgent, "Headless") {
		t.Errorf("user agent not overridden: %q", s.userAgent)
	}
	if err := s.navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}

	idx, err := s.waitAny(ctx, 5*time.Second, "#missing", "#late")
	if err != nil || idx != 1 {
		t.Fatalf("waitAny: idx=%d err=%v", idx, err)
	}
	if idx, err := s.waitAny(ctx, 300*time.Millisecond, "#never"); err != nil || idx != -1 {
		t.Fatalf("waitAny timeout: idx=%d err=%v", idx, err)
	}
	if ok, err := s.exists(ctx, "#dl"); err != nil || !ok {
		t.Fatalf("exists: %v %v", ok, err)
	}
	ua, err := eval[string](ctx, s, "navigator.userAgent")
	if err != nil || strings.Contains(ua, "Headless") {
		t.Errorf("navigator.userAgent: %q %v", ua, err)
	}

	data, err := s.download(ctx, "#dl", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != testCSV {
		t.Errorf("downloaded %q", data)
	}

	if err := s.setCookie(ctx, cookieAtMain, "secret"); err != nil {
		t.Fatal(err)
	}
	cookies, err := chromedp.Call(s.ctx, network.GetCookies, network.GetCookiesParams{URLs: []string{"https://www.imdb.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cookies.Cookies {
		if c.Name == cookieAtMain && c.Value == "secret" {
			found = true
		}
	}
	if !found {
		t.Errorf("cookie not set: %+v", cookies.Cookies)
	}
}

func TestParseExportsPage(t *testing.T) {
	// The page data as the exports page has it, trimmed to the fields the client reads.
	const nextData = `{"props":{"pageProps":{
		"requestContext":{"timestamp":"2026-10-29T21:29:25.919Z"},
		"mainColumnData":{"getExports":{"total":2,"edges":[
			{"node":{"startedOn":"2026-10-29T17:48:26.486Z","status":{"id":"PROCESSING"},"exportType":"LIST",
				"listExportMetadata":{"id":"ls024163529","listClassId":"WATCH_LIST","name":"WATCHLIST"}}},
			{"node":{"startedOn":"2026-10-29T17:47:17.120Z","status":{"id":"READY"},"exportType":"RATINGS"}}
		]}}
	}}}`
	pages := map[string]string{
		"/exports": `<script id="__NEXT_DATA__" type="application/json">` + nextData + `</script>`,
		"/broken":  `<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{}}}</script>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>" + pages[r.URL.Path] + "</body></html>"))
	}))
	defer srv.Close()

	s, ctx := newTestSession(t)
	if err := s.navigate(ctx, srv.URL+"/exports"); err != nil {
		t.Fatal(err)
	}
	page, err := parseExportsPage(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want := &exportsPage{
		Timestamp: time.Date(2026, 10, 29, 21, 29, 25, 919000000, time.UTC),
		Rows: []exportRow{
			{Index: 0, Type: "LIST", ListID: "ls024163529", Name: "WATCHLIST", Status: "PROCESSING", StartedOn: time.Date(2026, 10, 29, 17, 48, 26, 486000000, time.UTC)},
			{Index: 1, Type: "RATINGS", Status: "READY", StartedOn: time.Date(2026, 10, 29, 17, 47, 17, 120000000, time.UTC)},
		},
	}
	if !page.Timestamp.Equal(want.Timestamp) || len(page.Rows) != len(want.Rows) {
		t.Fatalf("page: got %+v, want %+v", page, want)
	}
	for i, row := range page.Rows {
		w := want.Rows[i]
		if row.Index != w.Index || row.Type != w.Type || row.ListID != w.ListID || row.Name != w.Name || row.Status != w.Status || !row.StartedOn.Equal(w.StartedOn) {
			t.Errorf("row %d: got %+v, want %+v", i, row, w)
		}
	}

	if err := s.navigate(ctx, srv.URL+"/broken"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseExportsPage(ctx, s); err == nil {
		t.Error("broken page: no error")
	}
}

func TestLoadDoesNotWaitForStalledResources(t *testing.T) {
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><div id="content"></div><img src="/stalled.png"></body></html>`))
	})
	// An ad or tracker that never answers holds back the load event of the page.
	mux.HandleFunc("/stalled.png", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(release)

	s, ctx := newTestSession(t)
	for name, load := range map[string]func() error{
		"navigate": func() error { return s.navigate(ctx, srv.URL) },
		"reload":   func() error { return s.reload(ctx) },
	} {
		started := time.Now()
		if err := load(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if took := time.Since(started); took > loadGrace+5*time.Second {
			t.Errorf("%s took %s", name, took)
		}
		if ok, err := s.exists(ctx, "#content"); err != nil || !ok {
			t.Errorf("%s: content not loaded: %v %v", name, ok, err)
		}
	}
}
