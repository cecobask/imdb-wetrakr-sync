package syncer

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/cecobask/imdb-wetrakr-sync/internal/config"
	"github.com/cecobask/imdb-wetrakr-sync/internal/media"
	"github.com/cecobask/imdb-wetrakr-sync/internal/wetrakr"
)

const (
	sectionWatchlist = "watchlist"
	sectionRatings   = "ratings"
	sectionHistory   = "history"

	ratingTolerance  = 0.05
	maxDescription   = 2000
	externalIMDb     = "imdb"
	listURLFormat    = "https://www.imdb.com/list/%s/"
	listMarkerFormat = "[imdb:%s]"
)

var listMarkerRe = regexp.MustCompile(`\[imdb:(ls[0-9]+)\]`)

// syncWatchlist mirrors the IMDb watchlist into the "planning" tracking status. Titles that already have another
// status on WeTrakr are skipped, because moving them to planning would remove them from that status.
func (s *Syncer) syncWatchlist(ctx context.Context, watchlist []media.Item, state *trackingState) error {
	sec := s.report.section(sectionWatchlist)
	if watchlist == nil {
		s.logger.Warn("no IMDb watchlist data, skipping the watchlist")
		return nil
	}
	add := newChange(sec, "add to planning", false, s.api.AddTracking)
	remove := newChange(sec, "remove from planning", true, s.api.RemoveTracking)
	inIMDb := make(map[string]bool, len(watchlist))
	for _, item := range watchlist {
		inIMDb[item.ID] = true
		if item.Kind != media.KindMovie && item.Kind != media.KindShow {
			sec.Skipped++
			s.logger.Info("skipping watchlist item: only movies and shows can be planned", "item", imdbLabel(item), "kind", item.Kind)
			continue
		}
		if _, ok := state.planning[item.ID]; ok {
			continue
		}
		if row, ok := state.other[item.ID]; ok {
			sec.Skipped++
			s.logger.Info("skipping watchlist item: it already has another status on WeTrakr", "item", imdbLabel(item), "status", row.Status)
			continue
		}
		wi := byIMDb(item)
		wi.Status = wetrakr.StatusPlanning
		wi.TrackedAt = timestamp(item.Created)
		add.add(string(item.Kind), item.ID, imdbLabel(item), wi)
	}
	for id, row := range sortedRows(state.planning) {
		if inIMDb[id] || (row.Type != wetrakr.TypeMovie && row.Type != wetrakr.TypeShow) {
			continue
		}
		remove.add(row.Type, fmt.Sprintf("wetrakr:%d", row.ID), rowLabel(row), wetrakr.Item{ID: row.ID, Status: wetrakr.StatusPlanning})
	}
	if err := s.run(ctx, add); err != nil {
		return err
	}
	return s.run(ctx, remove)
}

// syncLists mirrors every IMDb list into a WeTrakr list. WeTrakr lists are matched by a marker in their description,
// so renaming a list on IMDb renames it on WeTrakr instead of creating a new one.
func (s *Syncer) syncLists(ctx context.Context, lists []media.List) error {
	if lists == nil {
		s.logger.Warn("no IMDb list data, skipping lists")
		return nil
	}
	wtLists, err := s.api.Lists(ctx)
	if err != nil {
		sec := s.report.section("lists")
		return record(sec, fmt.Errorf("failure fetching WeTrakr lists: %w", err))
	}
	byMarker := map[string]wetrakr.List{}
	for _, l := range wtLists {
		if m := listMarkerRe.FindStringSubmatch(l.Description); m != nil {
			byMarker[m[1]] = l
		}
	}
	claimed := map[int64]bool{}
	for _, list := range lists {
		sec := s.report.section("list")
		sec.List = list.Name
		wt, how := matchList(list, wtLists, byMarker, claimed)
		if wt != nil {
			claimed[wt.ID] = true
		}
		if err := s.syncList(ctx, sec, list, wt, how); err != nil {
			return err
		}
	}
	return nil
}

type matchKind int

const (
	matchNone matchKind = iota
	matchMarker
	matchName
)

func matchList(list media.List, wtLists []wetrakr.List, byMarker map[string]wetrakr.List, claimed map[int64]bool) (*wetrakr.List, matchKind) {
	if l, ok := byMarker[list.ID]; ok {
		return &l, matchMarker
	}
	for _, l := range wtLists {
		if claimed[l.ID] || listMarkerRe.MatchString(l.Description) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(l.Name), strings.TrimSpace(list.Name)) {
			return &l, matchName
		}
	}
	return nil, matchNone
}

func listDescription(id string) string {
	return fmt.Sprintf("Synced from the IMDb list "+listURLFormat+" by imdb-wetrakr-sync. "+listMarkerFormat, id, id)
}

func withMarker(description, id string) string {
	marker := fmt.Sprintf(listMarkerFormat, id)
	if strings.TrimSpace(description) == "" {
		return listDescription(id)
	}
	room := maxDescription - len(marker) - 2
	if len(description) > room {
		description = description[:room]
	}
	return description + "\n\n" + marker
}

func (s *Syncer) syncList(ctx context.Context, sec *Section, list media.List, wt *wetrakr.List, how matchKind) error {
	dryRun := s.opts.Mode == config.ModeDryRun
	switch {
	case how == matchNone && dryRun:
		s.logger.Info("would create list", sec.logAttrs("privacy", s.opts.ListPrivacy)...)
	case how == matchNone:
		s.logger.Info("creating list", sec.logAttrs("privacy", s.opts.ListPrivacy)...)
		created, err := s.api.CreateList(ctx, wetrakr.ListInput{Name: list.Name, Description: listDescription(list.ID), Privacy: s.opts.ListPrivacy})
		if err != nil {
			return record(sec, fmt.Errorf("failure creating the list (the free WeTrakr plan allows 5 lists): %w", err))
		}
		wt = created
	case how == matchName:
		in := wetrakr.ListInput{Description: withMarker(wt.Description, list.ID)}
		if err := s.updateList(ctx, sec, wt, in, "adopt existing list with the same name"); err != nil {
			return err
		}
	case how == matchMarker && wt.Name != list.Name:
		if err := s.updateList(ctx, sec, wt, wetrakr.ListInput{Name: list.Name}, "rename list from "+wt.Name); err != nil {
			return err
		}
	}

	var rows []wetrakr.Row
	if wt != nil {
		var err error
		if rows, err = s.api.ListItems(ctx, wt.ID); err != nil {
			return record(sec, fmt.Errorf("failure fetching the items of WeTrakr list %d: %w", wt.ID, err))
		}
	}
	var listID int64
	if wt != nil {
		listID = wt.ID
	}
	add := newChange(sec, "add to list", false, func(ctx context.Context, items wetrakr.Items) (wetrakr.WriteResult, error) {
		return s.api.AddListItems(ctx, listID, items)
	})
	remove := newChange(sec, "remove from list", true, func(ctx context.Context, items wetrakr.Items) (wetrakr.WriteResult, error) {
		return s.api.RemoveListItems(ctx, listID, items)
	})
	existing := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.IDs.IMDb != "" {
			existing[row.IDs.IMDb] = true
		}
	}
	inIMDb := make(map[string]bool, len(list.Items))
	for _, item := range list.Items {
		inIMDb[item.ID] = true
		if existing[item.ID] {
			continue
		}
		wi := byIMDb(item)
		if item.Kind == media.KindPerson && !dryRun {
			// People only take a WeTrakr id in list bodies, so resolve it first.
			match, err := s.api.LookupExternal(ctx, externalIMDb, item.ID, wetrakr.TypePerson)
			if wetrakr.IsNotFound(err) {
				sec.NotFound = append(sec.NotFound, imdbLabel(item))
				s.logger.Warn("not found on WeTrakr", sec.logAttrs("item", imdbLabel(item))...)
				continue
			}
			if err != nil {
				return record(sec, fmt.Errorf("failure resolving %s: %w", item.ID, err))
			}
			wi = wetrakr.Item{ID: match.ID}
			add.add(string(item.Kind), fmt.Sprintf("wetrakr:%d", match.ID), imdbLabel(item), wi)
			continue
		}
		add.add(string(item.Kind), item.ID, imdbLabel(item), wi)
	}
	for _, row := range rows {
		if row.IDs.IMDb == "" || inIMDb[row.IDs.IMDb] {
			continue
		}
		remove.add(row.Type, fmt.Sprintf("wetrakr:%d", row.ID), rowLabel(row), wetrakr.Item{ID: row.ID})
	}
	if err := s.run(ctx, add); err != nil {
		return err
	}
	return s.run(ctx, remove)
}

func (s *Syncer) updateList(ctx context.Context, sec *Section, wt *wetrakr.List, in wetrakr.ListInput, what string) error {
	if s.opts.Mode == config.ModeDryRun {
		s.logger.Info("would "+what, sec.logAttrs("wetrakr_list", wt.ID)...)
		return nil
	}
	s.logger.Info(what, sec.logAttrs("wetrakr_list", wt.ID)...)
	if err := s.api.UpdateList(ctx, wt.ID, in); err != nil {
		return record(sec, fmt.Errorf("failure updating WeTrakr list %d: %w", wt.ID, err))
	}
	return nil
}

// syncRatings mirrors the IMDb ratings. Season and people ratings on WeTrakr are left alone, because IMDb has no
// equivalent for them.
func (s *Syncer) syncRatings(ctx context.Context, ratings []media.Rating) error {
	sec := s.report.section(sectionRatings)
	if ratings == nil {
		s.logger.Warn("no IMDb ratings data, skipping ratings")
		return nil
	}
	rows, err := s.api.Ratings(ctx)
	if err != nil {
		return record(sec, fmt.Errorf("failure fetching WeTrakr ratings: %w", err))
	}
	current := make(map[string]wetrakr.Row, len(rows))
	for _, row := range rows {
		if row.IDs.IMDb != "" {
			current[row.IDs.IMDb] = row
		}
	}
	add := newChange(sec, "rate", false, s.api.AddRatings)
	remove := newChange(sec, "remove rating", true, s.api.RemoveRatings)
	inIMDb := make(map[string]bool, len(ratings))
	for _, r := range ratings {
		inIMDb[r.ID] = true
		label := fmt.Sprintf("%s: %g", imdbLabel(r.Item), r.Value)
		if row, ok := current[r.ID]; ok {
			if math.Abs(row.Rating-r.Value) < ratingTolerance {
				continue
			}
			label = fmt.Sprintf("%s: %g → %g", imdbLabel(r.Item), row.Rating, r.Value)
		}
		wi := byIMDb(r.Item)
		wi.Rating = &r.Value
		wi.RatedAt = timestamp(r.Created)
		add.add(string(r.Kind), r.ID, label, wi)
	}
	for id, row := range sortedRows(current) {
		if inIMDb[id] || !slices.Contains([]string{wetrakr.TypeMovie, wetrakr.TypeShow, wetrakr.TypeEpisode}, row.Type) {
			continue
		}
		remove.add(row.Type, fmt.Sprintf("wetrakr:%d", row.ID), rowLabel(row), wetrakr.Item{ID: row.ID})
	}
	if err := s.run(ctx, add); err != nil {
		return err
	}
	return s.run(ctx, remove)
}

// syncHistory marks rated movies and episodes as watched, on the date they were rated. It only adds plays, never
// removes them, and leaves titles alone that already have a status other than planning.
func (s *Syncer) syncHistory(ctx context.Context, ratings []media.Rating, state *trackingState) error {
	sec := s.report.section(sectionHistory)
	if ratings == nil {
		s.logger.Warn("no IMDb ratings data, skipping history")
		return nil
	}
	allowRewatch := false
	add := newChange(sec, "mark as watched", false, func(ctx context.Context, items wetrakr.Items) (wetrakr.WriteResult, error) {
		items.AllowRewatch = &allowRewatch
		return s.api.AddTracking(ctx, items)
	})
	for _, r := range ratings {
		if r.Kind != media.KindMovie && r.Kind != media.KindEpisode {
			continue
		}
		if row, ok := state.other[r.ID]; ok {
			if row.Status != wetrakr.StatusWatched {
				sec.Skipped++
				s.logger.Info("skipping history item: it already has another status on WeTrakr", "item", imdbLabel(r.Item), "status", row.Status)
			}
			continue
		}
		wi := byIMDb(r.Item)
		wi.Status = wetrakr.StatusWatched
		if wi.TrackedAt = timestamp(r.Created); wi.TrackedAt == "" {
			wi.TrackedAtUnknown = true
		}
		add.add(string(r.Kind), r.ID, imdbLabel(r.Item), wi)
	}
	return s.run(ctx, add)
}
