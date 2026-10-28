// Package imdb reads the watchlist, lists and ratings of an IMDb user from IMDb's CSV exports, either by driving a
// headless browser with the user's at-main cookie or from CSV files on disk.
package imdb

import (
	"context"
	"regexp"

	"github.com/cecobask/imdb-wetrakr-sync/internal/media"
)

var (
	listIDRe = regexp.MustCompile(`^ls[0-9]+$`)
	userIDRe = regexp.MustCompile(`^ur[0-9]+$`)
)

// Request selects what to fetch.
type Request struct {
	Watchlist bool
	Ratings   bool
	Lists     bool
	// ListIDs restricts the lists to these ids. When empty, every list of the user is fetched.
	ListIDs []string
	// IgnoredListIDs are skipped.
	IgnoredListIDs []string
}

// Data is what was fetched. A section that was not requested is nil.
type Data struct {
	Watchlist []media.Item
	Ratings   []media.Rating
	Lists     []media.List
}

// Source fetches IMDb data.
type Source interface {
	Fetch(ctx context.Context, req Request) (*Data, error)
	Close() error
}
