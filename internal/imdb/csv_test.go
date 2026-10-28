package imdb

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cecobask/imdb-wetrakr-sync/internal/media"
)

var discard = slog.New(slog.DiscardHandler)

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseListReorderedColumns(t *testing.T) {
	items, err := ParseList(readFixture(t, "testdata/list_reordered.csv"), discard)
	if err != nil {
		t.Fatal(err)
	}
	want := []media.Item{
		{ID: "tt0000001", Kind: media.KindMovie, Title: `Movie, With "Quotes"`, Created: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "tt0000002", Kind: media.KindShow, Title: "A Mini Series", Created: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: "tt0000004", Kind: media.KindEpisode, Title: "An Episode", Created: time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC)},
		{ID: "tt0000006", Kind: media.KindMovie, Title: "A Special", Created: time.Date(2025, 1, 6, 0, 0, 0, 0, time.UTC)},
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(want), items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Errorf("item %d: got %+v, want %+v", i, items[i], want[i])
		}
	}
}

func TestParsePeopleList(t *testing.T) {
	items, err := ParseList(readFixture(t, "testdata/people.csv"), discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Kind != media.KindPerson || items[0].ID != "nm0634240" || items[0].Title != "Christopher Nolan" {
		t.Fatalf("items: %+v", items)
	}
}

func TestParseRatings(t *testing.T) {
	ratings, err := ParseRatings(readFixture(t, "testdata/ratings_bad.csv"), discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(ratings) != 1 || ratings[0].ID != "tt0000001" || ratings[0].Value != 7 || ratings[0].Created.Format(time.DateOnly) != "2024-01-01" {
		t.Fatalf("ratings: %+v", ratings)
	}
}

func TestParseUnknownFormat(t *testing.T) {
	if _, err := ParseList([]byte("foo,bar\n1,2\n"), discard); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("expected ErrUnknownFormat, got %v", err)
	}
	if _, err := ParseRatings([]byte(""), discard); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("expected ErrUnknownFormat, got %v", err)
	}
}

func TestIsRatingsExport(t *testing.T) {
	if !IsRatingsExport(readFixture(t, "../../testdata/sample/ratings.csv")) {
		t.Error("ratings export not detected")
	}
	if IsRatingsExport(readFixture(t, "../../testdata/sample/watchlist.csv")) {
		t.Error("list export detected as ratings")
	}
}

func TestDirSource(t *testing.T) {
	src := NewDirSource("../../testdata/sample", discard)
	data, err := src.Fetch(context.Background(), Request{Watchlist: true, Ratings: true, Lists: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Watchlist) != 3 || len(data.Ratings) != 4 || len(data.Lists) != 1 {
		t.Fatalf("data: %d watchlist, %d ratings, %d lists", len(data.Watchlist), len(data.Ratings), len(data.Lists))
	}
	if l := data.Lists[0]; l.ID != "ls000000001" || l.Name != "Nolan Favorites" || len(l.Items) != 3 {
		t.Errorf("list: %+v", l)
	}

	data, err = src.Fetch(context.Background(), Request{Lists: true, IgnoredListIDs: []string{"ls000000001"}})
	if err != nil {
		t.Fatal(err)
	}
	if data.Watchlist != nil || data.Ratings != nil || len(data.Lists) != 0 || data.Lists == nil {
		t.Errorf("filtered data: %+v", data)
	}
}

func TestListFileNameRoundTrip(t *testing.T) {
	dir := t.TempDir()
	name := ListFileName("ls123456789", `Best: of / 2025`)
	if err := SaveExport(dir, name, readFixture(t, "testdata/people.csv")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ls123456789 Best_ of _ 2025.csv")); err != nil {
		t.Fatal(err)
	}
	data, err := NewDirSource(dir, discard).Fetch(context.Background(), Request{Lists: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Lists) != 1 || data.Lists[0].Name != "Best_ of _ 2025" {
		t.Errorf("lists: %+v", data.Lists)
	}
}
