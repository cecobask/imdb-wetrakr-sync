package imdb

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/cecobask/imdb-wetrakr-sync/internal/media"
)

// Column names of the IMDb exports. Columns are looked up by name, so IMDb adding, removing or reordering other
// columns does not break the parser.
const (
	colConst      = "Const"
	colCreated    = "Created"
	colTitle      = "Title"
	colTitleType  = "Title Type"
	colYourRating = "Your Rating"
	colDateRated  = "Date Rated"
	colName       = "Name"
)

// ErrUnknownFormat means the CSV header matches none of the known export formats.
var ErrUnknownFormat = errors.New("unrecognized IMDb export format")

type table struct {
	cols map[string]int
	rows [][]string
}

func (t table) has(cols ...string) bool {
	for _, c := range cols {
		if _, ok := t.cols[c]; !ok {
			return false
		}
	}
	return true
}

func (t table) get(row []string, col string) string {
	i, ok := t.cols[col]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func readTable(data []byte) (table, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(data))
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return table{}, fmt.Errorf("%w: empty file", ErrUnknownFormat)
	}
	if err != nil {
		return table{}, fmt.Errorf("failure reading csv header: %w", err)
	}
	t := table{cols: make(map[string]int, len(header))}
	for i, h := range header {
		t.cols[strings.TrimSpace(h)] = i
	}
	t.rows, err = r.ReadAll()
	if err != nil {
		return table{}, fmt.Errorf("failure reading csv rows: %w", err)
	}
	return t, nil
}

// ParseList parses the export of a list or the watchlist. Lists of titles and lists of people are supported.
func ParseList(data []byte, logger *slog.Logger) ([]media.Item, error) {
	t, err := readTable(data)
	if err != nil {
		return nil, err
	}
	people := !t.has(colTitleType) && t.has(colName)
	if !t.has(colConst) || (!people && !t.has(colTitleType)) {
		return nil, fmt.Errorf("%w: missing %s or %s column", ErrUnknownFormat, colConst, colTitleType)
	}
	items := make([]media.Item, 0, len(t.rows))
	for _, row := range t.rows {
		item := media.Item{ID: t.get(row, colConst), Created: parseDate(t.get(row, colCreated))}
		if item.ID == "" {
			continue
		}
		if people {
			item.Kind, item.Title = media.KindPerson, t.get(row, colName)
		} else {
			titleType := t.get(row, colTitleType)
			kind, ok := KindOf(titleType)
			item.Title = t.get(row, colTitle)
			if !ok {
				logger.Warn("skipping item with unsupported title type", "id", item.ID, "title", item.Title, "type", titleType)
				continue
			}
			item.Kind = kind
		}
		items = append(items, item)
	}
	return items, nil
}

// ParseRatings parses the export of the ratings.
func ParseRatings(data []byte, logger *slog.Logger) ([]media.Rating, error) {
	t, err := readTable(data)
	if err != nil {
		return nil, err
	}
	if !t.has(colConst, colYourRating, colTitleType) {
		return nil, fmt.Errorf("%w: missing %s, %s or %s column", ErrUnknownFormat, colConst, colYourRating, colTitleType)
	}
	ratings := make([]media.Rating, 0, len(t.rows))
	for _, row := range t.rows {
		id, title, titleType := t.get(row, colConst), t.get(row, colTitle), t.get(row, colTitleType)
		if id == "" {
			continue
		}
		value, err := strconv.ParseFloat(t.get(row, colYourRating), 64)
		if err != nil || value <= 0 || value > 10 {
			logger.Warn("skipping rating with invalid value", "id", id, "title", title, "rating", t.get(row, colYourRating))
			continue
		}
		kind, ok := KindOf(titleType)
		if !ok || kind == media.KindPerson {
			logger.Warn("skipping rating with unsupported title type", "id", id, "title", title, "type", titleType)
			continue
		}
		ratings = append(ratings, media.Rating{
			Item:  media.Item{ID: id, Kind: kind, Title: title, Created: parseDate(t.get(row, colDateRated))},
			Value: value,
		})
	}
	return ratings, nil
}

// IsRatingsExport reports whether the CSV is a ratings export rather than a list export.
func IsRatingsExport(data []byte) bool {
	t, err := readTable(data)
	return err == nil && t.has(colYourRating, colDateRated) && !t.has("Position")
}

// KindOf maps an IMDb title type to a media kind. It returns false for types that WeTrakr has no equivalent for.
func KindOf(titleType string) (media.Kind, bool) {
	switch strings.ToLower(strings.TrimSpace(titleType)) {
	case "movie", "tv movie", "short", "tv short", "video", "tv special":
		return media.KindMovie, true
	case "tv series", "tv mini series", "tv miniseries":
		return media.KindShow, true
	case "tv episode":
		return media.KindEpisode, true
	case "person":
		return media.KindPerson, true
	default:
		return "", false
	}
}

func parseDate(s string) time.Time {
	for _, layout := range []string{time.DateOnly, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
