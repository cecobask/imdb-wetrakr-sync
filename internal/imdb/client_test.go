package imdb

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

// Exports as the exports page data lists them, newest first, after two runs that exported the same resources (taken
// from a real page).
var (
	run1     = time.Date(2026, 10, 29, 5, 47, 0, 0, time.UTC)
	run2     = run1.Add(12 * time.Hour)
	pageRows = []exportRow{
		{Index: 0, Type: "LIST", ListID: "ls024163529", Name: "WATCHLIST", Status: "READY", StartedOn: run2.Add(70 * time.Second)},
		{Index: 1, Type: "LIST", ListID: "ls4154799008", Name: "watched-2026", Status: "READY", StartedOn: run2.Add(40 * time.Second)},
		{Index: 2, Type: "RATINGS", Status: "READY", StartedOn: run2.Add(17 * time.Second)},
		{Index: 3, Type: "LIST", ListID: "ls024163529", Name: "WATCHLIST", Status: "READY", StartedOn: run1.Add(70 * time.Second)},
		{Index: 4, Type: "LIST", ListID: "ls4154799008", Name: "watched-2026", Status: "READY", StartedOn: run1.Add(40 * time.Second)},
		{Index: 5, Type: "RATINGS", Status: "READY", StartedOn: run1.Add(17 * time.Second)},
	}
)

func TestResourceExportOf(t *testing.T) {
	ratings := &resource{kind: "ratings"}
	list := &resource{kind: "list", id: "ls4154799008"}
	watchlist := &resource{kind: "watchlist", id: "ls024163529"}
	tests := []struct {
		r    *resource
		row  exportRow
		want bool
	}{
		{ratings, pageRows[2], true},
		{ratings, pageRows[1], false},
		{list, pageRows[1], true},
		{list, pageRows[0], false},
		{list, exportRow{Type: "RATINGS", ListID: "ls4154799008"}, false},
		{watchlist, pageRows[0], true},
		{watchlist, pageRows[2], false},
	}
	for _, tt := range tests {
		if got := tt.r.exportOf(tt.row); got != tt.want {
			t.Errorf("%s exportOf %+v: got %v, want %v", tt.r, tt.row, got, tt.want)
		}
	}
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
	ratings := &resource{kind: "ratings"}
	list := &resource{kind: "list", id: "ls4154799008"}

	// The page looks the same before and after a run that exports the same resources, so only the start times tell
	// the new exports apart.
	run3 := run2.Add(12 * time.Hour)
	if state, row := stateOf(pageRows, ratings, run3); state != exportNotListed || row.Index != 2 {
		t.Errorf("no new export yet: got state %d row %+v", state, row)
	}

	fresh := append([]exportRow{{Index: 0, Type: "RATINGS", Status: "PROCESSING", StartedOn: run3.Add(time.Minute)}}, pageRows...)
	if state, _ := stateOf(fresh, ratings, run3); state != exportProcessing {
		t.Errorf("new export processing: got state %d", state)
	}

	fresh[0].Status = "READY"
	if state, row := stateOf(fresh, ratings, run3); state != exportReady || row.Index != 0 {
		t.Errorf("new export ready: got state %d row %+v", state, row)
	}

	if state, row := stateOf(pageRows, list, run2); state != exportReady || row.Index != 1 {
		t.Errorf("list: got state %d row %+v", state, row)
	}

	// The newest export wins even when the rows are out of order.
	reversed := slices.Clone(pageRows)
	slices.Reverse(reversed)
	if state, row := stateOf(reversed, list, run2); state != exportReady || row.Index != 1 {
		t.Errorf("reversed: got state %d row %+v", state, row)
	}

	if state, row := stateOf(nil, list, run2); state != exportNotListed || row.ready() {
		t.Errorf("no exports: got state %d row %+v", state, row)
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
