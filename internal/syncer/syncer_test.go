package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cecobask/imdb-wetrakr-sync/internal/config"
	"github.com/cecobask/imdb-wetrakr-sync/internal/imdb"
	"github.com/cecobask/imdb-wetrakr-sync/internal/media"
	"github.com/cecobask/imdb-wetrakr-sync/internal/wetrakr"
)

type fakeSource struct{ data *imdb.Data }

func (f fakeSource) Fetch(context.Context, imdb.Request) (*imdb.Data, error) { return f.data, nil }
func (f fakeSource) Close() error                                            { return nil }

type call struct {
	op     string
	listID int64
	items  wetrakr.Items
	input  wetrakr.ListInput
}

type fakeAPI struct {
	tracking  map[string][]wetrakr.Row
	ratings   []wetrakr.Row
	lists     []wetrakr.List
	listItems map[int64][]wetrakr.Row
	people    map[string]int64
	calls     []call
	// failOn makes the named operation fail with the error.
	failOn   map[string]error
	nextID   int64
	notFound map[string]bool
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{tracking: map[string][]wetrakr.Row{}, listItems: map[int64][]wetrakr.Row{}, people: map[string]int64{}, failOn: map[string]error{}, nextID: 100, notFound: map[string]bool{}}
}

func (f *fakeAPI) write(op string, listID int64, items wetrakr.Items) (wetrakr.WriteResult, error) {
	f.calls = append(f.calls, call{op: op, listID: listID, items: items})
	if err := f.failOn[op]; err != nil {
		return wetrakr.WriteResult{}, err
	}
	res := wetrakr.WriteResult{}
	n := 0
	for _, group := range [][]wetrakr.Item{items.Movies, items.Shows, items.Episodes, items.People} {
		for _, it := range group {
			if it.IDs != nil && f.notFound[it.IDs.IMDb] {
				res.NotFound.Movies = append(res.NotFound.Movies, it)
				continue
			}
			n++
		}
	}
	if strings.HasPrefix(op, "remove") {
		res.Removed = n
	} else {
		res.Added = n
	}
	return res, nil
}

func (f *fakeAPI) Account(context.Context) (*wetrakr.Account, error) {
	if err := f.failOn["account"]; err != nil {
		return nil, err
	}
	return &wetrakr.Account{}, nil
}
func (f *fakeAPI) Tracking(_ context.Context, status string) ([]wetrakr.Row, error) {
	return f.tracking[status], nil
}
func (f *fakeAPI) AddTracking(_ context.Context, items wetrakr.Items) (wetrakr.WriteResult, error) {
	return f.write("add-tracking", 0, items)
}
func (f *fakeAPI) RemoveTracking(_ context.Context, items wetrakr.Items) (wetrakr.WriteResult, error) {
	return f.write("remove-tracking", 0, items)
}
func (f *fakeAPI) Ratings(context.Context) ([]wetrakr.Row, error) { return f.ratings, nil }
func (f *fakeAPI) AddRatings(_ context.Context, items wetrakr.Items) (wetrakr.WriteResult, error) {
	return f.write("add-ratings", 0, items)
}
func (f *fakeAPI) RemoveRatings(_ context.Context, items wetrakr.Items) (wetrakr.WriteResult, error) {
	return f.write("remove-ratings", 0, items)
}
func (f *fakeAPI) Lists(context.Context) ([]wetrakr.List, error) { return f.lists, nil }
func (f *fakeAPI) CreateList(_ context.Context, in wetrakr.ListInput) (*wetrakr.List, error) {
	f.calls = append(f.calls, call{op: "create-list", input: in})
	if err := f.failOn["create-list"]; err != nil {
		return nil, err
	}
	f.nextID++
	l := wetrakr.List{ID: f.nextID, Name: in.Name, Description: in.Description, Privacy: in.Privacy}
	f.lists = append(f.lists, l)
	return &l, nil
}
func (f *fakeAPI) UpdateList(_ context.Context, id int64, in wetrakr.ListInput) error {
	f.calls = append(f.calls, call{op: "update-list", listID: id, input: in})
	return nil
}
func (f *fakeAPI) ListItems(_ context.Context, id int64) ([]wetrakr.Row, error) {
	return f.listItems[id], nil
}
func (f *fakeAPI) AddListItems(_ context.Context, id int64, items wetrakr.Items) (wetrakr.WriteResult, error) {
	return f.write("add-list-items", id, items)
}
func (f *fakeAPI) RemoveListItems(_ context.Context, id int64, items wetrakr.Items) (wetrakr.WriteResult, error) {
	return f.write("remove-list-items", id, items)
}
func (f *fakeAPI) LookupExternal(_ context.Context, _, id, _ string) (*wetrakr.ExternalMatch, error) {
	if pid, ok := f.people[id]; ok {
		return &wetrakr.ExternalMatch{ID: pid, Type: "person"}, nil
	}
	return nil, &wetrakr.APIError{Status: 404}
}

func (f *fakeAPI) callsOf(op string) []call {
	var out []call
	for _, c := range f.calls {
		if c.op == op {
			out = append(out, c)
		}
	}
	return out
}

func imdbIDs(items []wetrakr.Item) []string {
	var out []string
	for _, it := range items {
		if it.IDs != nil {
			out = append(out, it.IDs.IMDb)
		}
	}
	return out
}

func row(typ string, id int64, imdbID, status string) wetrakr.Row {
	return wetrakr.Row{Type: typ, ID: id, IDs: wetrakr.IDs{IMDb: imdbID}, Status: status}
}

func movie(id string) media.Item {
	return media.Item{ID: id, Kind: media.KindMovie, Title: "Movie " + id, Created: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)}
}

func runSync(t *testing.T, api *fakeAPI, data *imdb.Data, opts Options) *Report {
	t.Helper()
	if opts.ListPrivacy == "" {
		opts.ListPrivacy = "private"
	}
	report, err := New(fakeSource{data}, api, opts, slog.New(slog.DiscardHandler)).Run(context.Background())
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	return report
}

func sectionOf(t *testing.T, r *Report, name string) *Section {
	t.Helper()
	for _, s := range r.Sections {
		label := s.Name
		if s.List != "" {
			label += " " + s.List
		}
		if label == name {
			return s
		}
	}
	t.Fatalf("section %q not found in %+v", name, r.Sections)
	return nil
}

func watchlistFixture() (*fakeAPI, *imdb.Data) {
	api := newFakeAPI()
	api.tracking[wetrakr.StatusPlanning] = []wetrakr.Row{row("movie", 1, "tt1", "planning"), row("movie", 9, "tt9", "planning"), row("show", 8, "tt8", "planning")}
	api.tracking[wetrakr.StatusWatched] = []wetrakr.Row{row("movie", 2, "tt2", "watched")}
	api.tracking[wetrakr.StatusDropped] = []wetrakr.Row{row("show", 3, "tt3", "dropped")}
	data := &imdb.Data{Watchlist: []media.Item{
		movie("tt1"), // already planned
		movie("tt2"), // watched: skip
		{ID: "tt3", Kind: media.KindShow, Title: "Dropped show"}, // dropped: skip
		{ID: "tt4", Kind: media.KindShow, Title: "New show"},     // add
		movie("tt5"), // add
		{ID: "tt6", Kind: media.KindEpisode, Title: "An episode"}, // episode: skip
	}}
	return api, data
}

func TestWatchlistFull(t *testing.T) {
	api, data := watchlistFixture()
	report := runSync(t, api, data, Options{Mode: config.ModeFull, Watchlist: true})

	adds := api.callsOf("add-tracking")
	if len(adds) != 1 {
		t.Fatalf("add calls: %+v", adds)
	}
	if got := imdbIDs(adds[0].items.Movies); !slices.Equal(got, []string{"tt5"}) {
		t.Errorf("movies added: %v", got)
	}
	if got := imdbIDs(adds[0].items.Shows); !slices.Equal(got, []string{"tt4"}) {
		t.Errorf("shows added: %v", got)
	}
	if it := adds[0].items.Movies[0]; it.Status != "planning" || it.TrackedAt != "2025-01-02T00:00:00Z" {
		t.Errorf("planning item: %+v", it)
	}
	removes := api.callsOf("remove-tracking")
	if len(removes) != 1 || len(removes[0].items.Movies) != 1 || removes[0].items.Movies[0].ID != 9 ||
		len(removes[0].items.Shows) != 1 || removes[0].items.Shows[0].ID != 8 || removes[0].items.Movies[0].Status != "planning" {
		t.Fatalf("remove calls: %+v", removes)
	}
	sec := sectionOf(t, report, sectionWatchlist)
	if sec.Added != 2 || sec.Removed != 2 || sec.Skipped != 3 {
		t.Errorf("section: %+v", sec)
	}
}

func TestWatchlistAddOnlyAndDryRun(t *testing.T) {
	api, data := watchlistFixture()
	report := runSync(t, api, data, Options{Mode: config.ModeAddOnly, Watchlist: true})
	if len(api.callsOf("add-tracking")) != 1 || len(api.callsOf("remove-tracking")) != 0 {
		t.Fatalf("add-only calls: %+v", api.calls)
	}
	if sec := sectionOf(t, report, sectionWatchlist); sec.WouldRemove != 2 || sec.Added != 2 {
		t.Errorf("add-only section: %+v", sec)
	}

	api, data = watchlistFixture()
	report = runSync(t, api, data, Options{Mode: config.ModeDryRun, Watchlist: true})
	if len(api.calls) != 0 {
		t.Fatalf("dry-run must not write: %+v", api.calls)
	}
	if sec := sectionOf(t, report, sectionWatchlist); sec.WouldAdd != 2 || sec.WouldRemove != 2 {
		t.Errorf("dry-run section: %+v", sec)
	}
}

func TestMissingWatchlistDataNeverRemoves(t *testing.T) {
	api, _ := watchlistFixture()
	runSync(t, api, &imdb.Data{}, Options{Mode: config.ModeFull, Watchlist: true})
	if len(api.calls) != 0 {
		t.Fatalf("expected no writes without watchlist data: %+v", api.calls)
	}
}

func TestRatings(t *testing.T) {
	api := newFakeAPI()
	api.ratings = []wetrakr.Row{
		{Type: "movie", ID: 1, IDs: wetrakr.IDs{IMDb: "tt1"}, Rating: 8},
		{Type: "movie", ID: 2, IDs: wetrakr.IDs{IMDb: "tt2"}, Rating: 6},
		{Type: "episode", ID: 3, IDs: wetrakr.IDs{IMDb: "tt3"}, Rating: 7},
		{Type: "season", ID: 4, IDs: wetrakr.IDs{IMDb: "tt4"}, Rating: 7},
		{Type: "movie", ID: 5, Rating: 7}, // no IMDb id
	}
	api.notFound["tt7"] = true
	data := &imdb.Data{Ratings: []media.Rating{
		{Item: movie("tt1"), Value: 8}, // unchanged
		{Item: movie("tt2"), Value: 9}, // updated
		{Item: media.Item{ID: "tt6", Kind: media.KindShow, Title: "Show"}, Value: 10}, // added
		{Item: movie("tt7"), Value: 5}, // not found on WeTrakr
	}}
	report := runSync(t, api, data, Options{Mode: config.ModeFull, Ratings: true})

	adds := api.callsOf("add-ratings")
	if len(adds) != 1 || !slices.Equal(imdbIDs(adds[0].items.Movies), []string{"tt2", "tt7"}) || !slices.Equal(imdbIDs(adds[0].items.Shows), []string{"tt6"}) {
		t.Fatalf("add calls: %+v", adds)
	}
	if it := adds[0].items.Movies[0]; *it.Rating != 9 || it.RatedAt != "2025-01-02T00:00:00Z" {
		t.Errorf("rating item: %+v", it)
	}
	removes := api.callsOf("remove-ratings")
	if len(removes) != 1 || len(removes[0].items.Episodes) != 1 || removes[0].items.Episodes[0].ID != 3 || removes[0].items.Len() != 1 {
		t.Fatalf("remove calls (seasons and rows without IMDb ids must stay): %+v", removes)
	}
	sec := sectionOf(t, report, sectionRatings)
	if sec.Added != 2 || sec.Removed != 1 || len(sec.NotFound) != 1 || !strings.Contains(sec.NotFound[0], "tt7") {
		t.Errorf("section: %+v", sec)
	}
}

func TestHistory(t *testing.T) {
	api := newFakeAPI()
	api.tracking[wetrakr.StatusWatched] = []wetrakr.Row{row("movie", 1, "tt1", "watched")}
	api.tracking[wetrakr.StatusDropped] = []wetrakr.Row{row("movie", 2, "tt2", "dropped")}
	api.tracking[wetrakr.StatusPlanning] = []wetrakr.Row{row("movie", 3, "tt3", "planning")}
	data := &imdb.Data{Ratings: []media.Rating{
		{Item: movie("tt1"), Value: 8}, // already watched
		{Item: movie("tt2"), Value: 8}, // dropped: skip
		{Item: movie("tt3"), Value: 8}, // planning: becomes watched
		{Item: media.Item{ID: "tt4", Kind: media.KindEpisode, Created: time.Date(2024, 8, 4, 0, 0, 0, 0, time.UTC)}, Value: 10},
		{Item: media.Item{ID: "tt5", Kind: media.KindShow}, Value: 10}, // shows are not marked watched
	}}
	report := runSync(t, api, data, Options{Mode: config.ModeFull, History: true})
	adds := api.callsOf("add-tracking")
	if len(adds) != 1 {
		t.Fatalf("calls: %+v", api.calls)
	}
	items := adds[0].items
	if !slices.Equal(imdbIDs(items.Movies), []string{"tt3"}) || !slices.Equal(imdbIDs(items.Episodes), []string{"tt4"}) || len(items.Shows) != 0 {
		t.Errorf("items: %+v", items)
	}
	if items.AllowRewatch == nil || *items.AllowRewatch || items.Episodes[0].Status != "watched" || items.Episodes[0].TrackedAt != "2024-08-04T00:00:00Z" {
		t.Errorf("history body: %+v", items)
	}
	if len(api.callsOf("remove-tracking")) != 0 {
		t.Error("history must never remove plays")
	}
	if sec := sectionOf(t, report, sectionHistory); sec.Skipped != 1 || sec.Added != 2 {
		t.Errorf("section: %+v", sec)
	}
}

func TestLists(t *testing.T) {
	api := newFakeAPI()
	api.lists = []wetrakr.List{
		{ID: 10, Name: "Old Name", Description: "Synced [imdb:ls000000001]"},
		{ID: 20, Name: "favourites", Description: "my own list"},
		{ID: 30, Name: "Unrelated"},
	}
	api.listItems[10] = []wetrakr.Row{row("movie", 1, "tt1", ""), row("movie", 2, "tt2", "")}
	api.people["nm1"] = 555
	data := &imdb.Data{Lists: []media.List{
		{ID: "ls000000001", Name: "New Name", Items: []media.Item{movie("tt1"), movie("tt3")}},
		{ID: "ls000000002", Name: "Favourites", Items: []media.Item{{ID: "nm1", Kind: media.KindPerson, Title: "Person"}, {ID: "nm2", Kind: media.KindPerson, Title: "Unknown"}}},
		{ID: "ls000000003", Name: "Brand New", Items: []media.Item{movie("tt4")}},
	}}
	report := runSync(t, api, data, Options{Mode: config.ModeFull, Lists: true, ListPrivacy: "friends"})

	updates := api.callsOf("update-list")
	if len(updates) != 2 {
		t.Fatalf("updates: %+v", updates)
	}
	if updates[0].listID != 10 || updates[0].input.Name != "New Name" {
		t.Errorf("rename: %+v", updates[0])
	}
	if updates[1].listID != 20 || !strings.HasPrefix(updates[1].input.Description, "my own list") || !strings.HasSuffix(updates[1].input.Description, "[imdb:ls000000002]") {
		t.Errorf("adopt: %+v", updates[1])
	}
	creates := api.callsOf("create-list")
	if len(creates) != 1 || creates[0].input.Name != "Brand New" || creates[0].input.Privacy != "friends" || !strings.Contains(creates[0].input.Description, "[imdb:ls000000003]") {
		t.Fatalf("creates: %+v", creates)
	}

	adds := api.callsOf("add-list-items")
	if len(adds) != 3 {
		t.Fatalf("adds: %+v", adds)
	}
	if adds[0].listID != 10 || !slices.Equal(imdbIDs(adds[0].items.Movies), []string{"tt3"}) {
		t.Errorf("list 10 adds: %+v", adds[0])
	}
	if adds[1].listID != 20 || len(adds[1].items.People) != 1 || adds[1].items.People[0].ID != 555 {
		t.Errorf("people adds: %+v", adds[1])
	}
	if adds[2].listID != 101 || !slices.Equal(imdbIDs(adds[2].items.Movies), []string{"tt4"}) {
		t.Errorf("new list adds: %+v", adds[2])
	}
	removes := api.callsOf("remove-list-items")
	if len(removes) != 1 || removes[0].listID != 10 || removes[0].items.Movies[0].ID != 2 {
		t.Fatalf("removes: %+v", removes)
	}
	if sec := sectionOf(t, report, "list Favourites"); len(sec.NotFound) != 1 {
		t.Errorf("favourites section: %+v", sec)
	}
}

func TestListPlanLimitContinues(t *testing.T) {
	api := newFakeAPI()
	api.failOn["create-list"] = &wetrakr.APIError{Status: 420, Code: wetrakr.CodePlanLimitReached}
	data := &imdb.Data{
		Lists:   []media.List{{ID: "ls000000001", Name: "One", Items: []media.Item{movie("tt1")}}},
		Ratings: []media.Rating{{Item: movie("tt2"), Value: 7}},
	}
	report := runSync(t, api, data, Options{Mode: config.ModeFull, Lists: true, Ratings: true})
	if !report.Failed() {
		t.Error("report must record the failure")
	}
	if len(api.callsOf("add-ratings")) != 1 {
		t.Error("ratings must still sync after a list failure")
	}
}

func TestListsDryRunDoesNotCreate(t *testing.T) {
	api := newFakeAPI()
	data := &imdb.Data{Lists: []media.List{{ID: "ls000000001", Name: "One", Items: []media.Item{movie("tt1"), {ID: "nm1", Kind: media.KindPerson}}}}}
	report := runSync(t, api, data, Options{Mode: config.ModeDryRun, Lists: true})
	if len(api.calls) != 0 {
		t.Fatalf("dry-run must not write: %+v", api.calls)
	}
	if sec := sectionOf(t, report, "list One"); sec.WouldAdd != 2 {
		t.Errorf("section: %+v", sec)
	}
}

func TestQuotaErrorAborts(t *testing.T) {
	api := newFakeAPI()
	api.failOn["add-ratings"] = &wetrakr.APIError{Status: 429, Code: wetrakr.CodeQuotaExceeded}
	data := &imdb.Data{
		Ratings: []media.Rating{{Item: movie("tt2"), Value: 7}},
	}
	opts := Options{Mode: config.ModeFull, Ratings: true, History: true, ListPrivacy: "private"}
	_, err := New(fakeSource{data}, api, opts, slog.New(slog.DiscardHandler)).Run(context.Background())
	var apiErr *wetrakr.APIError
	if !errors.As(err, &apiErr) || !apiErr.IsQuota() {
		t.Fatalf("expected quota error, got %v", err)
	}
	if len(api.callsOf("add-tracking")) != 0 {
		t.Error("history must not run after a fatal error")
	}
}

func TestReportLogsInConfiguredFormat(t *testing.T) {
	var buf bytes.Buffer
	report := &Report{Mode: config.ModeDryRun}
	sec := report.section(sectionWatchlist)
	sec.WouldAdd, sec.Skipped = 23, 3
	failed := report.section("list One")
	failed.fail(errors.New("boom"))
	report.Log(slog.New(slog.NewJSONHandler(&buf, nil)))

	var lines []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		lines = append(lines, m)
	}
	if len(lines) != 4 {
		t.Fatalf("lines: %v", lines)
	}
	if l := lines[0]; l["msg"] != "sync summary" || l["section"] != "watchlist" || l["would_add"] != 23.0 || l["skipped"] != 3.0 {
		t.Errorf("watchlist line: %v", l)
	}
	if l := lines[1]; l["level"] != "ERROR" || l["error"] != "boom" {
		t.Errorf("error line: %v", l)
	}
	if l := lines[3]; l["msg"] != "sync finished with errors" || l["mode"] != "dry-run" || l["would_add"] != 23.0 || l["errors"] != 1.0 {
		t.Errorf("total line: %v", l)
	}
}

func TestWrongAccountAborts(t *testing.T) {
	api := newFakeAPI()
	data := &imdb.Data{Ratings: []media.Rating{{Item: movie("tt1"), Value: 7}}}
	opts := Options{Mode: config.ModeFull, Ratings: true, Username: "someone-else"}
	_, err := New(fakeSource{data}, api, opts, slog.New(slog.DiscardHandler)).Run(context.Background())
	var wrong *wetrakr.WrongAccountError
	if !errors.As(err, &wrong) {
		t.Fatalf("expected a WrongAccountError, got %v", err)
	}
	if len(api.calls) != 0 {
		t.Errorf("must not write to another account: %+v", api.calls)
	}
}
