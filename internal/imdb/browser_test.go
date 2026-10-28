//go:build browser

package imdb

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	s, err := startBrowser(ctx, browserConfig{headless: true}, discard)
	if err != nil {
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
