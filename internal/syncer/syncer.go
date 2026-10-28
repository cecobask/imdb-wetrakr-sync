// Package syncer compares the IMDb data with the WeTrakr state and applies the differences.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/cecobask/imdb-wetrakr-sync/internal/config"
	"github.com/cecobask/imdb-wetrakr-sync/internal/imdb"
	"github.com/cecobask/imdb-wetrakr-sync/internal/media"
	"github.com/cecobask/imdb-wetrakr-sync/internal/wetrakr"
)

// API is the part of the WeTrakr client the syncer uses.
type API interface {
	Account(ctx context.Context) (*wetrakr.Account, error)
	Tracking(ctx context.Context, status string) ([]wetrakr.Row, error)
	AddTracking(ctx context.Context, items wetrakr.Items) (wetrakr.WriteResult, error)
	RemoveTracking(ctx context.Context, items wetrakr.Items) (wetrakr.WriteResult, error)
	Ratings(ctx context.Context) ([]wetrakr.Row, error)
	AddRatings(ctx context.Context, items wetrakr.Items) (wetrakr.WriteResult, error)
	RemoveRatings(ctx context.Context, items wetrakr.Items) (wetrakr.WriteResult, error)
	Lists(ctx context.Context) ([]wetrakr.List, error)
	CreateList(ctx context.Context, in wetrakr.ListInput) (*wetrakr.List, error)
	UpdateList(ctx context.Context, id int64, in wetrakr.ListInput) error
	ListItems(ctx context.Context, id int64) ([]wetrakr.Row, error)
	AddListItems(ctx context.Context, id int64, items wetrakr.Items) (wetrakr.WriteResult, error)
	RemoveListItems(ctx context.Context, id int64, items wetrakr.Items) (wetrakr.WriteResult, error)
	LookupExternal(ctx context.Context, source, id, mediaType string) (*wetrakr.ExternalMatch, error)
}

// Options selects what to sync and how.
type Options struct {
	Mode        string
	Watchlist   bool
	Lists       bool
	Ratings     bool
	History     bool
	ListIDs     []string
	IgnoredList []string
	ListPrivacy string
	// Username, when set, is the only WeTrakr account the sync writes to.
	Username string
}

type Syncer struct {
	source imdb.Source
	api    API
	opts   Options
	logger *slog.Logger
	report *Report
}

func New(source imdb.Source, api API, opts Options, logger *slog.Logger) *Syncer {
	return &Syncer{source: source, api: api, opts: opts, logger: logger}
}

// Run performs the sync and returns its report. The error is non-nil when the sync could not run at all, for example
// on an authentication or quota failure; failures limited to one section are recorded in the report instead.
func (s *Syncer) Run(ctx context.Context) (*Report, error) {
	s.report = &Report{Mode: s.opts.Mode}
	account, err := s.api.Account(ctx)
	if err != nil {
		return s.report, fmt.Errorf("failure fetching the WeTrakr account: %w", err)
	}
	if err := wetrakr.CheckAccount(account, s.opts.Username); err != nil {
		return s.report, fmt.Errorf("%w: run the authorization again with your own account", err)
	}
	s.logger.Info("signed in to WeTrakr", "user", account.Info.Username, "plan", account.Plan, "mode", s.opts.Mode)

	req := imdb.Request{
		Watchlist:      s.opts.Watchlist,
		Ratings:        s.opts.Ratings || s.opts.History,
		Lists:          s.opts.Lists,
		ListIDs:        s.opts.ListIDs,
		IgnoredListIDs: s.opts.IgnoredList,
	}
	s.logger.Info("fetching IMDb data")
	data, err := s.source.Fetch(ctx, req)
	if err != nil {
		return s.report, fmt.Errorf("failure fetching IMDb data: %w", err)
	}
	s.logger.Info("fetched IMDb data", "watchlist", len(data.Watchlist), "lists", len(data.Lists), "ratings", len(data.Ratings))

	var tracking *trackingState
	if s.opts.Watchlist || s.opts.History {
		if tracking, err = s.loadTracking(ctx); err != nil {
			return s.report, err
		}
	}
	steps := []struct {
		enabled bool
		run     func(context.Context) error
	}{
		{s.opts.Watchlist, func(ctx context.Context) error { return s.syncWatchlist(ctx, data.Watchlist, tracking) }},
		{s.opts.Lists, func(ctx context.Context) error { return s.syncLists(ctx, data.Lists) }},
		{s.opts.Ratings, func(ctx context.Context) error { return s.syncRatings(ctx, data.Ratings) }},
		{s.opts.History, func(ctx context.Context) error { return s.syncHistory(ctx, data.Ratings, tracking) }},
	}
	for _, step := range steps {
		if !step.enabled {
			continue
		}
		if err := step.run(ctx); err != nil {
			return s.report, err
		}
	}
	return s.report, nil
}

// fatal reports whether an error must stop the whole sync rather than only the current section.
func fatal(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, wetrakr.ErrNoToken) {
		return true
	}
	var apiErr *wetrakr.APIError
	return errors.As(err, &apiErr) && (apiErr.IsQuota() || apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden)
}

// record adds a section error, or returns it when it is fatal.
func record(sec *Section, err error) error {
	sec.fail(err)
	if fatal(err) {
		return err
	}
	return nil
}

// trackingState indexes the tracking statuses of the user by IMDb id.
type trackingState struct {
	planning map[string]wetrakr.Row
	// other holds every title in a status other than planning.
	other map[string]wetrakr.Row
}

func (s *Syncer) loadTracking(ctx context.Context) (*trackingState, error) {
	state := &trackingState{planning: map[string]wetrakr.Row{}, other: map[string]wetrakr.Row{}}
	for _, status := range wetrakr.AllStatuses {
		rows, err := s.api.Tracking(ctx, status)
		if err != nil {
			return nil, fmt.Errorf("failure fetching WeTrakr titles in status %s: %w", status, err)
		}
		for _, row := range rows {
			if row.IDs.IMDb == "" {
				continue
			}
			if status == wetrakr.StatusPlanning {
				state.planning[row.IDs.IMDb] = row
			} else {
				state.other[row.IDs.IMDb] = row
			}
		}
		s.logger.Debug("fetched WeTrakr tracking status", "status", status, "count", len(rows))
	}
	return state, nil
}

// change is a batch of writes of one kind, with the labels used to log them.
type change struct {
	sec    *Section
	remove bool
	verb   string
	items  wetrakr.Items
	labels map[string]string
	order  []string
	apply  func(context.Context, wetrakr.Items) (wetrakr.WriteResult, error)
}

func newChange(sec *Section, verb string, remove bool, apply func(context.Context, wetrakr.Items) (wetrakr.WriteResult, error)) *change {
	return &change{sec: sec, verb: verb, remove: remove, labels: map[string]string{}, apply: apply}
}

// add queues an item. key identifies the item in the logs and in the not-found list of the response.
func (c *change) add(mediaType, key, label string, item wetrakr.Item) {
	c.items.Add(mediaType, item)
	c.labels[key] = label
	c.order = append(c.order, key)
}

// run applies the change, or logs it when the mode does not allow it.
func (s *Syncer) run(ctx context.Context, c *change) error {
	n := c.items.Len()
	if n == 0 {
		return nil
	}
	allowed := s.opts.Mode == config.ModeFull || (s.opts.Mode == config.ModeAddOnly && !c.remove)
	if !allowed {
		for _, key := range c.order {
			s.logger.Info("would "+c.verb, c.sec.logAttrs("item", c.labels[key])...)
		}
		if c.remove {
			c.sec.WouldRemove += n
		} else {
			c.sec.WouldAdd += n
		}
		return nil
	}
	for _, key := range c.order {
		s.logger.Info(c.verb, c.sec.logAttrs("item", c.labels[key])...)
	}
	res, err := c.apply(ctx, c.items)
	s.collect(c, res)
	if err != nil {
		return record(c.sec, fmt.Errorf("failure to %s %d items: %w", c.verb, n, err))
	}
	return nil
}

func (s *Syncer) collect(c *change, res wetrakr.WriteResult) {
	if c.remove {
		c.sec.Removed += res.Removed
	} else {
		c.sec.Added += res.Added
	}
	for _, group := range [][]wetrakr.Item{res.NotFound.Movies, res.NotFound.Shows, res.NotFound.Seasons, res.NotFound.Episodes, res.NotFound.People} {
		for _, item := range group {
			key := itemKey(item)
			label := c.labels[key]
			if label == "" {
				label = key
			}
			c.sec.NotFound = append(c.sec.NotFound, label)
			s.logger.Warn("not found on WeTrakr", c.sec.logAttrs("item", label)...)
		}
	}
	for _, raw := range res.Errored {
		s.logger.Warn("WeTrakr rejected items", c.sec.logAttrs("errored", string(raw))...)
		c.sec.fail(fmt.Errorf("WeTrakr rejected items: %s", raw))
	}
}

func itemKey(item wetrakr.Item) string {
	if item.IDs != nil && item.IDs.IMDb != "" {
		return item.IDs.IMDb
	}
	return fmt.Sprintf("wetrakr:%d", item.ID)
}

func imdbLabel(item media.Item) string {
	if item.Title == "" {
		return item.ID
	}
	return fmt.Sprintf("%s (%s)", item.Title, item.ID)
}

func rowLabel(row wetrakr.Row) string {
	id := row.IDs.IMDb
	if id == "" {
		id = fmt.Sprintf("wetrakr %s %d", row.Type, row.ID)
	}
	if row.Title == "" {
		return id
	}
	return fmt.Sprintf("%s (%s)", row.Title, id)
}

// sortedRows iterates the rows in IMDb id order, so the logs and the request bodies are deterministic.
func sortedRows(rows map[string]wetrakr.Row) iter.Seq2[string, wetrakr.Row] {
	return func(yield func(string, wetrakr.Row) bool) {
		for _, id := range slices.Sorted(maps.Keys(rows)) {
			if !yield(id, rows[id]) {
				return
			}
		}
	}
}

func byIMDb(item media.Item) wetrakr.Item {
	return wetrakr.Item{IDs: &wetrakr.IDs{IMDb: item.ID}}
}

func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
