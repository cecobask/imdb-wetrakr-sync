package imdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/cecobask/awswaf/pkg/aws"

	"github.com/cecobask/imdb-wetrakr-sync/internal/media"
)

const (
	baseURL      = "https://www.imdb.com"
	cookieDomain = ".imdb.com"
	cookieAtMain = "at-main"
	cookieWAF    = "aws-waf-token"
	wafDomain    = "imdb.com"

	pathExports   = "/exports/"
	pathList      = "/list/%s/"
	pathLists     = "/profile/lists/"
	pathRatings   = "/user/%s/ratings/"
	pathWatchlist = "/list/watchlist/"

	selectorWAF                = "script[src*='token.awswaf.com']"
	selectorUserMenu           = "#navUserMenu"
	selectorSignIn             = "a[href*='/registration/signin']"
	selectorErrorPageTitle     = "h1[data-testid='error-page-title']"
	selectorPrivateList        = "div[data-testid='list-page-mc-private-list-content']"
	selectorExportButton       = "div[data-testid='hero-list-subnav-export-button'] button"
	selectorActionsMenuButton  = "button[data-testid='hero-list-subnav-actions-menu-button']"
	selectorActionsMenu        = "ul[data-testid='hero-list-subnav-actions-menu']"
	selectorListHyperlink      = "a.ipc-metadata-list-summary-item__t"
	selectorExportRow          = "li[data-testid='user-ll-item']"
	selectorExportStatusButton = "[data-testid='export-status-button']"
	attrDownloadTarget         = "data-iws-download"

	wafAttempts     = 3
	signInLoads     = 2
	pageTimeout     = 20 * time.Second
	downloadTimeout = 2 * time.Minute
	pollMinInterval = 10 * time.Second
	pollMaxInterval = 30 * time.Second
	// The wait for exports keeps this much of the sync timeout for the WeTrakr side: a quarter, within these bounds.
	exportWaitMinReserve = 2 * time.Minute
	exportWaitMaxReserve = 5 * time.Minute
	menuOpenAttempts     = 5
)

// BrowserOptions configures the browser source.
type BrowserOptions struct {
	CookieAtMain string
	Headless     bool
	BrowserPath  string
	// SaveDir, when set, receives a copy of every downloaded export.
	SaveDir string
}

// BrowserSource downloads the IMDb exports by driving a headless Chrome signed in with the user's cookies.
type BrowserSource struct {
	opts    BrowserOptions
	logger  *slog.Logger
	session *browserSession
}

func NewBrowserSource(opts BrowserOptions, logger *slog.Logger) *BrowserSource {
	return &BrowserSource{opts: opts, logger: logger}
}

func (b *BrowserSource) Close() error {
	if b.session != nil {
		b.session.close()
		b.session = nil
	}
	return nil
}

// resource is one export: the watchlist, the ratings or a list.
type resource struct {
	kind    string // watchlist, ratings or list
	id      string // the list id; empty for the ratings
	name    string // the list name, when known
	pageURL string
	data    []byte
}

func (r *resource) String() string {
	if r.kind != "list" {
		return r.kind
	}
	if r.name == "" {
		return "list " + r.id
	}
	return fmt.Sprintf("list %s (%s)", r.name, r.id)
}

// logAttrs describes the resource as separate log attributes, which keeps the log lines free of nested quoting.
func (r *resource) logAttrs(extra ...any) []any {
	attrs := []any{"resource", r.kind}
	if r.kind == "list" {
		if r.name != "" {
			attrs = append(attrs, "list", r.name)
		}
		attrs = append(attrs, "id", r.id)
	}
	return append(attrs, extra...)
}

// exportOf reports whether an export on the exports page is an export of the resource.
func (r *resource) exportOf(row exportRow) bool {
	if r.kind == "ratings" {
		return row.Type == "RATINGS"
	}
	return row.Type == "LIST" && row.ListID == r.id
}

// matches reports whether a link on the exports page points to the resource. Ratings rows link to
// /user/<id>/ratings, where IMDb may use either the ur… or the p.… id of the user, so any user id matches: the page
// only lists the exports of the signed-in user.
func (r *resource) matches(href string) bool {
	path, _, _ := strings.Cut(href, "?")
	if u, err := url.Parse(path); err == nil && u.Path != "" {
		path = u.Path
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	switch r.kind {
	case "ratings":
		return len(segments) >= 3 && segments[0] == "user" && segments[2] == "ratings"
	default:
		return len(segments) >= 2 && segments[0] == "list" && segments[1] == r.id
	}
}

// listRef is a list found on the profile page.
type listRef struct {
	id   string
	name string
}

func (b *BrowserSource) Fetch(ctx context.Context, req Request) (*Data, error) {
	session, err := startBrowser(ctx, browserConfig{headless: b.opts.Headless, browserPath: b.opts.BrowserPath, redact: []string{b.opts.CookieAtMain}}, b.logger)
	if err != nil {
		return nil, err
	}
	b.session = session
	if err := b.signIn(ctx); err != nil {
		return nil, err
	}
	userID, watchlistID, err := b.hydrate(ctx)
	if err != nil {
		return nil, err
	}
	b.logger.Info("signed in to IMDb", "user", userID)

	var resources []*resource
	if req.Ratings {
		resources = append(resources, &resource{kind: "ratings", pageURL: baseURL + fmt.Sprintf(pathRatings, userID)})
	}
	if req.Lists {
		var refs []listRef
		if len(req.ListIDs) == 0 {
			if refs, err = b.discoverLists(ctx); err != nil {
				return nil, err
			}
		} else {
			for _, id := range req.ListIDs {
				refs = append(refs, listRef{id: id})
			}
		}
		for _, ref := range refs {
			if ref.id == watchlistID {
				b.logger.Warn("the watchlist is configured as a list, ignoring it: use SYNC.WATCHLIST instead", "id", ref.id)
				continue
			}
			if !wanted(ref.id, req) {
				continue
			}
			resources = append(resources, &resource{kind: "list", id: ref.id, name: ref.name, pageURL: baseURL + fmt.Sprintf(pathList, ref.id)})
		}
	}
	if req.Watchlist {
		resources = append(resources, &resource{kind: "watchlist", id: watchlistID, pageURL: baseURL + pathWatchlist})
	}
	if len(resources) == 0 {
		return b.parse(req, nil)
	}

	since, err := b.exportsStartTime(ctx)
	if err != nil {
		return nil, err
	}
	var exported []*resource
	for _, r := range resources {
		ok, err := b.startExport(ctx, r)
		if err != nil {
			return nil, err
		}
		if ok {
			exported = append(exported, r)
		}
	}
	if err := b.downloadExports(ctx, exported, since); err != nil {
		return nil, err
	}
	return b.parse(req, exported)
}

// signIn sets the at-main cookie and checks that IMDb sees the user as signed in.
func (b *BrowserSource) signIn(ctx context.Context) error {
	// Load the site first: cookies set before the WAF challenge completes get lost in its redirects.
	if err := b.open(ctx, baseURL+"/"); err != nil {
		return err
	}
	if err := b.session.setCookie(ctx, cookieAtMain, b.opts.CookieAtMain); err != nil {
		return fmt.Errorf("failure setting the %s cookie: %w", cookieAtMain, err)
	}
	// IMDb ignores at-main on a request without its ubid-main browser id, and issues that id in the response, so
	// the session takes effect from the second load on.
	for range signInLoads {
		if err := b.session.reload(ctx); err != nil {
			return fmt.Errorf("failure reloading IMDb: %w", err)
		}
		if err := b.handleWAF(ctx); err != nil {
			return err
		}
		idx, err := b.session.waitAny(ctx, pageTimeout, selectorUserMenu, selectorSignIn)
		if err != nil {
			return err
		}
		if idx == 0 {
			return nil
		}
	}
	return errors.New("failure signing in to IMDb with the provided cookie: copy a new IMDB.COOKIEATMAIN from a signed-in browser")
}

// open navigates to the URL and solves the WAF challenge when IMDb shows one.
func (b *BrowserSource) open(ctx context.Context, url string) error {
	if err := b.session.navigate(ctx, url); err != nil {
		return fmt.Errorf("failure opening %s: %w", url, err)
	}
	return b.handleWAF(ctx)
}

// handleWAF solves the AWS WAF challenge with the awswaf solver, sets the resulting token cookie and reloads.
func (b *BrowserSource) handleWAF(ctx context.Context) error {
	for attempt := 1; attempt <= wafAttempts; attempt++ {
		present, err := b.session.exists(ctx, selectorWAF)
		if err != nil || !present {
			return err
		}
		b.logger.Debug("solving the AWS WAF challenge", "attempt", attempt)
		html, err := eval[string](ctx, b.session, "document.documentElement.outerHTML")
		if err != nil {
			return err
		}
		goku, host, err := aws.Extract(html)
		if err != nil || host == "" {
			return fmt.Errorf("failure reading the AWS WAF challenge (host %s): %v", host, err)
		}
		waf, err := aws.NewAwsWaf(host, wafDomain, b.session.userAgent, goku, "")
		if err != nil {
			return fmt.Errorf("failure creating the AWS WAF solver: %w", err)
		}
		token, err := waf.Run()
		if err != nil || token == "" {
			b.logger.Debug("AWS WAF solver failed", "error", err)
			continue
		}
		if err := b.session.setCookie(ctx, cookieWAF, token); err != nil {
			return fmt.Errorf("failure setting the AWS WAF token: %w", err)
		}
		if err := b.session.reload(ctx); err != nil {
			return fmt.Errorf("failure reloading after the AWS WAF challenge: %w", err)
		}
	}
	if present, err := b.session.exists(ctx, selectorWAF); err != nil || present {
		return fmt.Errorf("failure solving the AWS WAF challenge after %d attempts: %v", wafAttempts, err)
	}
	return nil
}

type userIDs struct {
	AuthorID        string `json:"authorId"`
	AuthorProfileID string `json:"authorProfileId"`
	ListID          string `json:"listId"`
	UserLink        string `json:"userLink"`
}

// hydrate reads the user id and the watchlist id from the watchlist page.
func (b *BrowserSource) hydrate(ctx context.Context) (userID, watchlistID string, err error) {
	if err := b.open(ctx, baseURL+pathWatchlist); err != nil {
		return "", "", err
	}
	if _, err := b.session.waitAny(ctx, pageTimeout, "#__NEXT_DATA__"); err != nil {
		return "", "", err
	}
	ids, err := eval[userIDs](ctx, b.session, `(() => {
		let d = {};
		try {
			d = JSON.parse(document.querySelector("#__NEXT_DATA__").textContent).props.pageProps.aboveTheFoldData || {};
		} catch (e) {}
		const link = document.querySelector("a[href*='/user/ur']");
		return {
			authorId: String(d.authorId || ""),
			authorProfileId: String(d.authorProfileId || ""),
			listId: String(d.listId || ""),
			userLink: link ? link.getAttribute("href") : "",
		};
	})()`)
	if err != nil {
		return "", "", fmt.Errorf("failure reading the IMDb user id: %w", err)
	}
	// IMDb has used both fields for the ur… id, and the other one for a p.… profile id, so take whichever matches.
	for _, candidate := range []string{ids.AuthorID, ids.AuthorProfileID, userIDFromHref(ids.UserLink)} {
		if userIDRe.MatchString(candidate) {
			userID = candidate
			break
		}
	}
	if userID == "" {
		return "", "", fmt.Errorf("failure reading the IMDb user id from the watchlist page (got %+v): the page layout probably changed", ids)
	}
	if !listIDRe.MatchString(ids.ListID) {
		return "", "", fmt.Errorf("failure reading the IMDb watchlist id (got %s): the page layout probably changed", ids.ListID)
	}
	return userID, ids.ListID, nil
}

func userIDFromHref(href string) string {
	_, rest, ok := strings.Cut(href, "/user/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	id, _, _ = strings.Cut(id, "?")
	return id
}

// discoverLists returns every list on the user's profile.
func (b *BrowserSource) discoverLists(ctx context.Context) ([]listRef, error) {
	if err := b.open(ctx, baseURL+pathLists); err != nil {
		return nil, err
	}
	if _, err := b.session.waitAny(ctx, pageTimeout, selectorListHyperlink, "footer"); err != nil {
		return nil, err
	}
	// The page loads more lists as it scrolls, so scroll until the count stops growing.
	count := -1
	for range 50 {
		n, err := eval[int](ctx, b.session, fmt.Sprintf("document.querySelectorAll(%s).length", jsString(selectorListHyperlink)))
		if err != nil {
			return nil, err
		}
		if n == count {
			break
		}
		count = n
		if _, err := eval[bool](ctx, b.session, "window.scrollTo(0, document.body.scrollHeight), true"); err != nil {
			return nil, err
		}
		if err := sleep(ctx, 1500*time.Millisecond); err != nil {
			return nil, err
		}
	}
	type link struct {
		Href string `json:"href"`
		Name string `json:"name"`
	}
	links, err := eval[[]link](ctx, b.session, fmt.Sprintf("[...document.querySelectorAll(%s)].map(a => ({href: a.getAttribute('href') || '', name: a.textContent.trim()}))", jsString(selectorListHyperlink)))
	if err != nil {
		return nil, err
	}
	var refs []listRef
	seen := map[string]bool{}
	for _, l := range links {
		for part := range strings.SplitSeq(l.Href, "/") {
			if listIDRe.MatchString(part) && !seen[part] {
				seen[part] = true
				refs = append(refs, listRef{id: part, name: l.Name})
			}
		}
	}
	b.logger.Info("discovered IMDb lists", "count", len(refs))
	return refs, nil
}

// startExport opens the page of the resource and clicks its export action. It returns false when the resource cannot
// be exported, which IMDb does for empty lists, and the resource is then skipped rather than treated as empty.
func (b *BrowserSource) startExport(ctx context.Context, r *resource) (bool, error) {
	if err := b.open(ctx, r.pageURL); err != nil {
		return false, err
	}
	idx, err := b.session.waitAny(ctx, pageTimeout, selectorExportButton, selectorActionsMenuButton, selectorErrorPageTitle, selectorPrivateList)
	if err != nil {
		return false, err
	}
	if r.kind == "list" && r.name == "" && idx <= 1 {
		if name, err := eval[string](ctx, b.session, `(document.querySelector("h1") || {}).textContent || ""`); err == nil {
			r.name = strings.TrimSpace(name)
		}
	}
	b.logger.Info("starting IMDb export", r.logAttrs()...)
	switch idx {
	case 0:
		if err := b.session.click(ctx, selectorExportButton); err != nil {
			return false, fmt.Errorf("failure clicking the export button of the %s: %w", r, err)
		}
	case 1:
		if err := b.clickExportMenuItem(ctx); err != nil {
			return false, fmt.Errorf("failure exporting the %s: %w", r, err)
		}
	case 2:
		b.logger.Warn("IMDb resource not found, skipping it", r.logAttrs("url", r.pageURL)...)
		return false, nil
	case 3:
		b.logger.Warn("IMDb list is private and belongs to someone else, skipping it", r.logAttrs()...)
		return false, nil
	default:
		b.logger.Warn("IMDb offers no export for this resource (empty lists cannot be exported), skipping it", r.logAttrs()...)
		return false, nil
	}
	// Give the export request time to reach IMDb before leaving the page; the exports page confirms it.
	return true, sleep(ctx, 3*time.Second)
}

// clickExportMenuItem opens the actions menu and clicks its Export item. The menu sometimes renders before the page
// finishes hydrating and then ignores the click, so it retries.
func (b *BrowserSource) clickExportMenuItem(ctx context.Context) error {
	findAndClick := fmt.Sprintf(`(() => {
		const menu = document.querySelector(%s);
		if (!menu) return false;
		const item = [...menu.querySelectorAll("li")].find(li => li.textContent.trim().toLowerCase().includes("export"));
		if (!item) return false;
		(item.querySelector("button, a, [role='menuitem']") || item).click();
		return true;
	})()`, jsString(selectorActionsMenu))
	for attempt := 1; attempt <= menuOpenAttempts; attempt++ {
		if err := b.session.click(ctx, selectorActionsMenuButton); err != nil {
			return err
		}
		if _, err := b.session.waitAny(ctx, 5*time.Second, selectorActionsMenu); err != nil {
			return err
		}
		clicked, err := eval[bool](ctx, b.session, findAndClick)
		if err != nil {
			return err
		}
		if clicked {
			return nil
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	return errors.New("the actions menu has no Export item")
}

// exportRow is one export on the exports page.
type exportRow struct {
	Index     int       `json:"index"`
	Type      string    `json:"type"` // LIST, for the watchlist too, or RATINGS
	ListID    string    `json:"listId"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // READY once it can be downloaded
	StartedOn time.Time `json:"startedOn"`
}

func (row exportRow) ready() bool {
	return row.Status == "READY"
}

type exportsPage struct {
	// Timestamp is the time of the IMDb server when it rendered the page, so it compares with the start times of the
	// exports without depending on the local clock.
	Timestamp time.Time   `json:"timestamp"`
	Rows      []exportRow `json:"rows"`
}

// readExportsPage reads the exports from the data the exports page is rendered from, newest first, in the order of
// the rendered rows. The rendered rows carry no start time, so a new export looks just like one from an earlier run,
// and the page shows only the newest 50 until it is scrolled. The data has the start times and the newest 50 exports,
// which hold the exports of a run unless it has more than 50 resources.
func (b *BrowserSource) readExportsPage(ctx context.Context) (*exportsPage, error) {
	if err := b.open(ctx, baseURL+pathExports); err != nil {
		return nil, err
	}
	if _, err := b.session.waitAny(ctx, pageTimeout, selectorExportRow, "footer"); err != nil {
		return nil, err
	}
	return parseExportsPage(ctx, b.session)
}

func parseExportsPage(ctx context.Context, s *browserSession) (*exportsPage, error) {
	page, err := eval[*exportsPage](ctx, s, `(() => {
		let props = {};
		try {
			props = JSON.parse(document.querySelector("#__NEXT_DATA__").textContent).props.pageProps || {};
		} catch (e) {}
		const exports = (props.mainColumnData || {}).getExports;
		if (!exports) return null;
		return {
			timestamp: (props.requestContext || {}).timestamp || null,
			rows: (exports.edges || []).map((edge, index) => {
				const node = edge.node || {};
				const list = node.listExportMetadata || {};
				return {
					index,
					type: node.exportType || "",
					listId: list.id || "",
					name: list.name || "",
					status: (node.status || {}).id || "",
					startedOn: node.startedOn || null,
				};
			}),
		};
	})()`)
	if err != nil {
		return nil, fmt.Errorf("failure reading the IMDb exports page: %w", err)
	}
	if page == nil || page.Timestamp.IsZero() {
		return nil, errors.New("failure reading the IMDb exports page: the page layout probably changed")
	}
	return page, nil
}

// exportsStartTime returns the IMDb server time before this run starts new exports, so older exports are never
// mistaken for the new ones.
func (b *BrowserSource) exportsStartTime(ctx context.Context) (time.Time, error) {
	page, err := b.readExportsPage(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return page.Timestamp, nil
}

// exportState is the state of the export this run requested for a resource.
type exportState int

const (
	exportNotListed exportState = iota
	exportProcessing
	exportReady
)

// stateOf returns the state of the export this run requested for the resource, which started at or after since, and
// the newest export of the resource, which is an earlier one when the new one is not listed yet.
func stateOf(rows []exportRow, r *resource, since time.Time) (exportState, exportRow) {
	var newest exportRow
	found := false
	for _, row := range rows {
		if r.exportOf(row) && (!found || row.StartedOn.After(newest.StartedOn)) {
			newest, found = row, true
		}
	}
	switch {
	case !found || newest.StartedOn.Before(since):
		return exportNotListed, newest
	case newest.ready():
		return exportReady, newest
	default:
		return exportProcessing, newest
	}
}

// waitBudget returns until when to wait for the exports. Large exports, such as thousands of ratings, can take many
// minutes, so the wait may use most of the sync timeout but keeps a reserve for the WeTrakr side of the sync.
func waitBudget(ctx context.Context, now time.Time) time.Time {
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Time{}
	}
	reserve := min(max(deadline.Sub(now)/4, exportWaitMinReserve), exportWaitMaxReserve)
	return deadline.Add(-reserve)
}

// downloadExports polls the exports page until every export this run requested is ready, then downloads them.
func (b *BrowserSource) downloadExports(ctx context.Context, resources []*resource, since time.Time) error {
	if len(resources) == 0 {
		return nil
	}
	until := waitBudget(ctx, time.Now())
	interval := pollMinInterval
	var rows []exportRow
	for {
		page, err := b.readExportsPage(ctx)
		if err != nil {
			return err
		}
		rows = page.Rows
		var waiting []string
		processing, notListed := 0, 0
		for _, r := range resources {
			switch state, _ := stateOf(rows, r, since); state {
			case exportNotListed:
				notListed++
				waiting = append(waiting, r.String())
			case exportProcessing:
				processing++
				waiting = append(waiting, r.String())
			}
		}
		if len(waiting) == 0 {
			break
		}
		if !until.IsZero() && time.Now().Add(interval).After(until) {
			b.logger.Warn("IMDb exports are still not ready, continuing without waiting longer (raise SYNC.TIMEOUT to wait longer)", "waiting_for", strings.Join(waiting, ", "))
			break
		}
		b.logger.Info("waiting for IMDb exports", "waiting_for", strings.Join(waiting, ", "), "processing", processing, "not_listed_yet", notListed, "next_check", interval)
		if err := sleep(ctx, interval); err != nil {
			return fmt.Errorf("IMDb exports did not finish in time (raise SYNC.TIMEOUT): %w", err)
		}
		interval = min(interval+5*time.Second, pollMaxInterval)
	}

	for _, r := range resources {
		state, row := stateOf(rows, r, since)
		if state != exportReady {
			// Fall back to the newest earlier export when the new one never showed up, rather than skipping the data.
			if state == exportProcessing || !row.ready() {
				b.logger.Warn("IMDb export is not ready, skipping it", r.logAttrs()...)
				continue
			}
			b.logger.Warn("the new IMDb export never appeared, using the newest earlier export instead", r.logAttrs()...)
		}
		if r.kind == "list" && r.name == "" {
			r.name = row.Name
		}
		// Mark the download button of the rendered row of the export, and check that the row is the resource's.
		mark := fmt.Sprintf(`(() => {
			document.querySelectorAll("[%[1]s]").forEach(e => e.removeAttribute("%[1]s"));
			const li = document.querySelectorAll(%[2]s)[%[3]d];
			const button = li && li.querySelector(%[4]s);
			if (!button) return "";
			button.setAttribute("%[1]s", "1");
			const link = li.querySelector(%[5]s) || li.querySelector("a[href]");
			return link ? link.getAttribute("href") : "";
		})()`, attrDownloadTarget, jsString(selectorExportRow), row.Index, jsString(selectorExportStatusButton), jsString(selectorListHyperlink))
		if href, err := eval[string](ctx, b.session, mark); err != nil || !r.matches(href) {
			return fmt.Errorf("failure finding the download button of the %s (row %d links to %q): %v", r, row.Index, href, err)
		}
		b.logger.Info("downloading IMDb export", r.logAttrs()...)
		data, err := b.session.download(ctx, fmt.Sprintf("[%s='1']", attrDownloadTarget), downloadTimeout)
		if err != nil {
			return fmt.Errorf("failure downloading the export of the %s: %w", r, err)
		}
		r.data = data
		if b.opts.SaveDir != "" {
			if err := SaveExport(b.opts.SaveDir, r.fileName(), data); err != nil {
				b.logger.Warn("failure saving the export", r.logAttrs("error", err)...)
			}
		}
	}
	return nil
}

func (r *resource) fileName() string {
	switch r.kind {
	case "watchlist":
		return WatchlistFile
	case "ratings":
		return RatingsFile
	default:
		return ListFileName(r.id, r.name)
	}
}

func (b *BrowserSource) parse(req Request, resources []*resource) (*Data, error) {
	data := &Data{}
	if req.Lists {
		data.Lists = []media.List{}
	}
	for _, r := range resources {
		if r.data == nil {
			continue
		}
		switch r.kind {
		case "watchlist":
			items, err := ParseList(r.data, b.logger)
			if err != nil {
				return nil, fmt.Errorf("failure parsing the watchlist export: %w", err)
			}
			data.Watchlist = items
		case "ratings":
			ratings, err := ParseRatings(r.data, b.logger)
			if err != nil {
				return nil, fmt.Errorf("failure parsing the ratings export: %w", err)
			}
			data.Ratings = ratings
		case "list":
			items, err := ParseList(r.data, b.logger)
			if err != nil {
				return nil, fmt.Errorf("failure parsing the export of the %s: %w", r, err)
			}
			data.Lists = append(data.Lists, media.List{ID: r.id, Name: r.name, Items: items})
		}
	}
	return data, nil
}
