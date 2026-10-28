package imdb

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Rows as the exports page lists them, newest first (taken from a real page).
var pageRows = []exportRow{
	{Index: 0, Href: "/list/ls024163529/?ref_=exp_t_1", Name: "cecobask's Watchlist", Ready: true},
	{Index: 1, Href: "/list/ls4154799008/?ref_=exp_t_2", Name: "watched-2026", Ready: true},
	{Index: 2, Href: "/user/p.bngbdelewbg5xo6675jsuzfuvi/ratings/?ref_=exp_t_3", Name: "Your ratings", Ready: true},
	{Index: 3, Href: "/list/ls024163529/?ref_=exp_t_4", Name: "cecobask's Watchlist", Ready: true},
	{Index: 4, Href: "/list/ls4154799008/?ref_=exp_t_5", Name: "watched-2026", Ready: true},
	{Index: 5, Href: "/user/p.bngbdelewbg5xo6675jsuzfuvi/ratings/?ref_=exp_t_6", Name: "Your ratings", Ready: true},
}

func TestResourceMatches(t *testing.T) {
	ratings := &resource{kind: "ratings"}
	list := &resource{kind: "list", id: "ls4154799008"}
	tests := []struct {
		r    *resource
		href string
		want bool
	}{
		{ratings, "/user/p.bngbdelewbg5xo6675jsuzfuvi/ratings/?ref_=exp_t_3", true},
		{ratings, "/user/ur90652267/ratings", true},
		{ratings, "https://www.imdb.com/user/ur90652267/ratings/", true},
		{ratings, "/list/ls4154799008/", false},
		{list, "/list/ls4154799008/?ref_=exp_t_2", true},
		{list, "https://www.imdb.com/list/ls4154799008", true},
		{list, "/list/ls41547990080/", false},
		{list, "/user/ur1/ratings/", false},
	}
	for _, tt := range tests {
		if got := tt.r.matches(tt.href); got != tt.want {
			t.Errorf("%s matches %q: got %v, want %v", tt.r, tt.href, got, tt.want)
		}
	}
}

func TestStateOf(t *testing.T) {
	ratings := &resource{kind: "ratings", before: 2}
	if state, _ := stateOf(pageRows, ratings); state != exportNotListed {
		t.Errorf("no new row yet: got state %d", state)
	}

	fresh := append([]exportRow{{Href: "/user/ur1/ratings/", Processing: true}}, pageRows...)
	if state, _ := stateOf(fresh, ratings); state != exportProcessing {
		t.Errorf("new row processing: got state %d", state)
	}

	fresh[0] = exportRow{Index: 0, Href: "/user/ur1/ratings/", Ready: true}
	if state, row := stateOf(fresh, ratings); state != exportReady || row.Index != 0 {
		t.Errorf("new row ready: got state %d row %+v", state, row)
	}

	list := &resource{kind: "list", id: "ls4154799008", before: 1}
	if state, row := stateOf(pageRows, list); state != exportReady || row.Index != 1 {
		t.Errorf("list: got state %d row %+v", state, row)
	}
}

func TestWaitBudget(t *testing.T) {
	now := time.Now()
	if got := waitBudget(context.Background(), now); !got.IsZero() {
		t.Errorf("no deadline: got %v", got)
	}
	for timeout, reserve := range map[time.Duration]time.Duration{
		30 * time.Minute: 5 * time.Minute,
		12 * time.Minute: 3 * time.Minute,
		4 * time.Minute:  2 * time.Minute,
	} {
		ctx, cancel := context.WithDeadline(context.Background(), now.Add(timeout))
		if got := now.Add(timeout).Sub(waitBudget(ctx, now)); got != reserve {
			t.Errorf("timeout %s: reserve %s, want %s", timeout, got, reserve)
		}
		cancel()
	}
}

func TestLogAttrsHaveNoNestedQuotes(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	r := &resource{kind: "list", id: "ls4154799008", name: "watched-2026"}
	logger.Info("downloading IMDb export", r.logAttrs()...)
	if got := buf.String(); !strings.Contains(got, "resource=list list=watched-2026 id=ls4154799008") || strings.Contains(got, `\"`) {
		t.Errorf("log line: %s", got)
	}
	if got := r.String(); got != "list watched-2026 (ls4154799008)" {
		t.Errorf("String: %q", got)
	}
}

func TestRedact(t *testing.T) {
	msg := `-> {"method":"Network.setCookie","params":{"name":"at-main","value":"Atza|secret-value"}}`
	got := redact(msg, []string{"", "Atza|secret-value"})
	if strings.Contains(got, "secret-value") || !strings.Contains(got, `"value":"[redacted]"`) {
		t.Errorf("redact: %s", got)
	}
}
