package syncer

import "log/slog"

// Section is the outcome of one part of the sync, such as the watchlist or one list.
type Section struct {
	Name string
	// List is the name of the IMDb list, for the sections of lists.
	List string
	// Added and Removed count the changes WeTrakr confirmed.
	Added   int
	Removed int
	// WouldAdd and WouldRemove count the changes the mode did not allow (dry-run, or removals in add-only).
	WouldAdd    int
	WouldRemove int
	// Skipped counts the items that were left alone on purpose, such as a watchlist title already watched.
	Skipped  int
	NotFound []string
	Errors   []error
}

// logAttrs identifies the section in log lines, followed by the extra attributes.
func (s *Section) logAttrs(extra ...any) []any {
	attrs := []any{"section", s.Name}
	if s.List != "" {
		attrs = append(attrs, "list", s.List)
	}
	return append(attrs, extra...)
}

func (s *Section) fail(err error) {
	s.Errors = append(s.Errors, err)
}

// Report is the outcome of a sync run.
type Report struct {
	Mode     string
	Sections []*Section
}

func (r *Report) section(name string) *Section {
	s := &Section{Name: name}
	r.Sections = append(r.Sections, s)
	return s
}

// Failed reports whether any section had an error.
func (r *Report) Failed() bool {
	for _, s := range r.Sections {
		if len(s.Errors) > 0 {
			return true
		}
	}
	return false
}

// Log writes the report through the logger: one line per section, its errors, and a line with the totals.
func (r *Report) Log(logger *slog.Logger) {
	var total Section
	for _, s := range r.Sections {
		for _, err := range s.Errors {
			logger.Error("sync error", s.logAttrs("error", err)...)
		}
		logger.Info("sync summary", s.logAttrs(s.counts()...)...)
		total.Added += s.Added
		total.Removed += s.Removed
		total.WouldAdd += s.WouldAdd
		total.WouldRemove += s.WouldRemove
		total.Skipped += s.Skipped
		total.NotFound = append(total.NotFound, s.NotFound...)
		total.Errors = append(total.Errors, s.Errors...)
	}
	attrs := append([]any{"mode", r.Mode}, total.counts()...)
	if r.Failed() {
		logger.Error("sync finished with errors", attrs...)
		return
	}
	logger.Info("sync finished", attrs...)
}

func (s *Section) counts() []any {
	return []any{
		"added", s.Added,
		"removed", s.Removed,
		"would_add", s.WouldAdd,
		"would_remove", s.WouldRemove,
		"skipped", s.Skipped,
		"not_found", len(s.NotFound),
		"errors", len(s.Errors),
	}
}
