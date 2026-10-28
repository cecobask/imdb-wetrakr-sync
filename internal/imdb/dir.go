package imdb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/cecobask/imdb-wetrakr-sync/internal/media"
)

// File names used by DirSource and by SaveExport.
const (
	WatchlistFile = "watchlist.csv"
	RatingsFile   = "ratings.csv"
)

var listFileRe = regexp.MustCompile(`^(ls[0-9]+)(?:[ _-]+(.*))?\.csv$`)

// ListFileName is the file name of a list export: "<list id> <list name>.csv".
func ListFileName(id, name string) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" {
		return id + ".csv"
	}
	return id + " " + name + ".csv"
}

// DirSource reads IMDb exports from a directory instead of downloading them. The directory holds watchlist.csv,
// ratings.csv and one "<list id> <list name>.csv" file per list. Missing files are treated as absent sections.
type DirSource struct {
	dir    string
	logger *slog.Logger
}

func NewDirSource(dir string, logger *slog.Logger) *DirSource {
	return &DirSource{dir: dir, logger: logger}
}

func (d *DirSource) Close() error { return nil }

func (d *DirSource) Fetch(_ context.Context, req Request) (*Data, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, fmt.Errorf("failure reading IMDb export directory: %w", err)
	}
	data := &Data{}
	if req.Watchlist {
		raw, err := d.read(WatchlistFile)
		if err != nil {
			return nil, err
		}
		if raw != nil {
			if data.Watchlist, err = ParseList(raw, d.logger); err != nil {
				return nil, fmt.Errorf("%s: %w", WatchlistFile, err)
			}
		} else {
			d.logger.Warn("watchlist export not found, skipping the watchlist", "file", filepath.Join(d.dir, WatchlistFile))
		}
	}
	if req.Ratings {
		raw, err := d.read(RatingsFile)
		if err != nil {
			return nil, err
		}
		if raw != nil {
			if data.Ratings, err = ParseRatings(raw, d.logger); err != nil {
				return nil, fmt.Errorf("%s: %w", RatingsFile, err)
			}
		} else {
			d.logger.Warn("ratings export not found, skipping the ratings", "file", filepath.Join(d.dir, RatingsFile))
		}
	}
	if req.Lists {
		for _, e := range entries {
			m := listFileRe.FindStringSubmatch(e.Name())
			if e.IsDir() || m == nil || !wanted(m[1], req) {
				continue
			}
			raw, err := d.read(e.Name())
			if err != nil {
				return nil, err
			}
			items, err := ParseList(raw, d.logger)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", e.Name(), err)
			}
			name := strings.TrimSpace(m[2])
			if name == "" {
				name = m[1]
			}
			data.Lists = append(data.Lists, media.List{ID: m[1], Name: name, Items: items})
		}
		if data.Lists == nil {
			data.Lists = []media.List{}
		}
	}
	return data, nil
}

func (d *DirSource) read(name string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(d.dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failure reading %s: %w", name, err)
	}
	return raw, nil
}

// wanted reports whether the list is selected by the request.
func wanted(id string, req Request) bool {
	if slices.Contains(req.IgnoredListIDs, id) {
		return false
	}
	return len(req.ListIDs) == 0 || slices.Contains(req.ListIDs, id)
}

// SaveExport writes a downloaded export into dir with the file names DirSource reads.
func SaveExport(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}
