package imdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const (
	// documentTimeout bounds the wait for the document of a page to load and be parsed.
	documentTimeout = time.Minute
	// loadGrace bounds the wait for the load event of a page once its document is parsed.
	loadGrace = 15 * time.Second
)

// browserSession is a headless Chrome tab with the helpers the IMDb client needs.
type browserSession struct {
	ctx         context.Context
	cancel      context.CancelFunc
	downloadDir string
	userAgent   string
	logger      *slog.Logger
}

type browserConfig struct {
	headless    bool
	browserPath string
	// redact holds secrets, such as the cookie value, to blank out of the browser logs.
	redact []string
}

// startBrowser launches Chrome and opens a tab. The browser lives until ctx ends or the session is closed.
func startBrowser(ctx context.Context, cfg browserConfig, logger *slog.Logger) (*browserSession, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", cfg.headless),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-features", "Translate,BlinkGenPropertyTrees,IsolateOrigins,site-per-process"),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("lang", "en-US"),
		chromedp.WindowSize(1440, 900),
		chromedp.DisableGPU,
	)
	if cfg.browserPath != "" {
		opts = append(opts, chromedp.ExecPath(cfg.browserPath))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	// chromedp logs through the application logger, so it follows the global log level and format.
	logf := func(level slog.Level) func(string, ...any) {
		return func(format string, args ...any) {
			logger.Log(ctx, level, redact(fmt.Sprintf(format, args...), cfg.redact), "component", "chromedp")
		}
	}
	ctxOpts := []chromedp.ContextOption{
		chromedp.WithLogf(logf(slog.LevelInfo)),
		chromedp.WithErrorf(logf(slog.LevelError)),
	}
	// The debug log has every DevTools message, so only format them when they are shown.
	if logger.Enabled(ctx, slog.LevelDebug) {
		ctxOpts = append(ctxOpts, chromedp.WithDebugf(logf(slog.LevelDebug)))
	}
	tabCtx, cancelTab := chromedp.NewContext(allocCtx, ctxOpts...)
	s := &browserSession{
		ctx:    tabCtx,
		cancel: func() { cancelTab(); cancelAlloc() },
		logger: logger,
	}
	if err := s.init(); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *browserSession) init() error {
	if err := chromedp.Do(s.ctx); err != nil {
		return fmt.Errorf("failure starting the browser (set IMDB.BROWSERPATH if Chrome is not found): %w", err)
	}
	version, err := chromedp.CallBrowser(s.ctx, browser.GetVersion, cdp.Empty{})
	if err != nil {
		return fmt.Errorf("failure reading the browser version: %w", err)
	}
	// Headless Chrome announces itself in the user agent, which bot protection looks for.
	s.userAgent = strings.ReplaceAll(version.UserAgent, "HeadlessChrome", "Chrome")
	if _, err := chromedp.Call(s.ctx, emulation.SetUserAgentOverride, emulation.SetUserAgentOverrideParams{
		UserAgent:      s.userAgent,
		AcceptLanguage: "en-US,en;q=0.9",
	}); err != nil {
		return fmt.Errorf("failure setting the user agent: %w", err)
	}
	if s.downloadDir, err = os.MkdirTemp("", "iws-downloads-*"); err != nil {
		return fmt.Errorf("failure creating the download directory: %w", err)
	}
	events := true
	if _, err := chromedp.CallBrowser(s.ctx, browser.SetDownloadBehavior, browser.SetDownloadBehaviorParams{
		Behavior:      browser.SetDownloadBehaviorBehaviorAllowAndName,
		DownloadPath:  s.downloadDir,
		EventsEnabled: &events,
	}); err != nil {
		return fmt.Errorf("failure enabling downloads: %w", err)
	}
	s.logger.Debug("browser started", "user_agent", s.userAgent)
	return nil
}

func (s *browserSession) close() {
	s.cancel()
	if s.downloadDir != "" {
		_ = os.RemoveAll(s.downloadDir)
	}
}

// with returns the session context bounded by ctx, so one step can have its own deadline without closing the browser.
func (s *browserSession) with(ctx context.Context) (context.Context, context.CancelFunc) {
	c, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(ctx, cancel)
	if deadline, ok := ctx.Deadline(); ok {
		var cancelDeadline context.CancelFunc
		c, cancelDeadline = context.WithDeadline(c, deadline)
		return c, func() { cancelDeadline(); stop(); cancel() }
	}
	return c, func() { stop(); cancel() }
}

func (s *browserSession) navigate(ctx context.Context, url string) error {
	return s.load(ctx, func(c context.Context) error {
		res, err := chromedp.Call(c, page.Navigate, page.NavigateParams{URL: url})
		if err == nil && res.ErrorText != "" {
			err = errors.New(res.ErrorText)
		}
		return err
	})
}

func (s *browserSession) reload(ctx context.Context) error {
	return s.load(ctx, func(c context.Context) error {
		_, err := chromedp.Call(c, page.Reload, page.ReloadParams{})
		return err
	})
}

// load runs the navigation and waits for the new document of the main frame to be parsed, then for its load event,
// but only for loadGrace. chromedp's Navigate and Reload wait for the load event without a limit, and an ad or tracker
// on IMDb that stalls holds it back for minutes. The callers wait for the elements they need anyway.
func (s *browserSession) load(ctx context.Context, navigate func(context.Context) error) error {
	c, cancel := s.with(ctx)
	defer cancel()
	tree, err := chromedp.Call(c, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return err
	}
	frameID := tree.FrameTree.Frame.ID
	// Subscribe before navigating, so no event is lost. The timer ends the wait by ending the subscription.
	ec, stop := context.WithCancel(c)
	defer stop()
	timer := time.AfterFunc(documentTimeout, stop)
	defer timer.Stop()
	events := chromedp.Events(ec, page.LifecycleEvent)
	if err := navigate(c); err != nil {
		return err
	}
	// The lifecycle of the new document starts with init, and its events carry its loader id, which tells them apart
	// from late events of the previous document.
	var loaderID cdp.LoaderID
	parsed := false
	for ev, err := range events {
		if err != nil {
			if ec.Err() == nil {
				return err
			}
			break
		}
		if ev.FrameID != frameID {
			continue
		}
		switch {
		case ev.Name == "init":
			loaderID = ev.LoaderID
		case ev.LoaderID != loaderID || loaderID == "":
		case ev.Name == "DOMContentLoaded":
			parsed = true
			timer.Reset(loadGrace)
		case ev.Name == "load":
			return nil
		}
	}
	switch {
	case c.Err() != nil:
		return c.Err()
	case !parsed:
		return fmt.Errorf("the page did not load in %s", documentTimeout)
	}
	s.logger.Debug("the page is still loading, continuing without waiting for it", "waited", loadGrace)
	return nil
}

// eval runs a JavaScript expression and decodes its JSON result into T.
func eval[T any](ctx context.Context, s *browserSession, expression string) (T, error) {
	c, cancel := s.with(ctx)
	defer cancel()
	return chromedp.Run(c, chromedp.Evaluate[T](expression, chromedp.EvalAwaitPromise))
}

// exists reports whether an element matches the CSS selector right now.
func (s *browserSession) exists(ctx context.Context, selector string) (bool, error) {
	return eval[bool](ctx, s, fmt.Sprintf("document.querySelector(%s) !== null", jsString(selector)))
}

// waitAny polls until one of the selectors matches and returns its index, or -1 when the timeout passes first.
func (s *browserSession) waitAny(ctx context.Context, timeout time.Duration, selectors ...string) (int, error) {
	list, _ := json.Marshal(selectors)
	expr := fmt.Sprintf("%s.findIndex(s => document.querySelector(s) !== null)", list)
	deadline := time.Now().Add(timeout)
	for {
		idx, err := eval[int](ctx, s, expr)
		if err != nil {
			return -1, err
		}
		if idx >= 0 || time.Now().After(deadline) {
			return idx, nil
		}
		if err := sleep(ctx, 250*time.Millisecond); err != nil {
			return -1, err
		}
	}
}

func (s *browserSession) click(ctx context.Context, selector string) error {
	c, cancel := s.with(ctx)
	defer cancel()
	return chromedp.Do(c, chromedp.Click(chromedp.CSS(selector)))
}

func (s *browserSession) setCookie(ctx context.Context, name, value string) error {
	c, cancel := s.with(ctx)
	defer cancel()
	secure := true
	_, err := chromedp.Call(c, network.SetCookie, network.SetCookieParams{
		Name:   name,
		Value:  value,
		Domain: cookieDomain,
		Path:   "/",
		Secure: &secure,
	})
	return err
}

// download clicks the element and returns the content of the file the click downloads.
func (s *browserSession) download(ctx context.Context, selector string, timeout time.Duration) ([]byte, error) {
	c, cancel := s.with(ctx)
	defer cancel()
	c, cancelTimeout := context.WithTimeout(c, timeout)
	defer cancelTimeout()
	// Subscribe before the click, so no event is lost.
	progress := chromedp.BrowserEvents(c, browser.DownloadProgress)
	begin := chromedp.BrowserEvents(c, browser.DownloadWillBegin)
	if err := chromedp.Do(c, chromedp.Click(chromedp.CSS(selector))); err != nil {
		return nil, fmt.Errorf("failure clicking the download button: %w", err)
	}
	var guid string
	for ev, err := range begin {
		if err != nil {
			return nil, fmt.Errorf("the download did not start: %w", err)
		}
		guid = ev.GUID
		// The query string is a signed link to the user's data, so it stays out of the logs.
		u, _, _ := strings.Cut(ev.URL, "?")
		s.logger.Debug("download started", "url", u, "file", ev.SuggestedFilename)
		break
	}
	for ev, err := range progress {
		if err != nil {
			return nil, fmt.Errorf("the download did not finish: %w", err)
		}
		if ev.GUID != guid {
			continue
		}
		switch ev.State {
		case browser.DownloadProgressStateCompleted:
			path := ev.FilePath
			if path == "" {
				path = filepath.Join(s.downloadDir, guid)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("failure reading the downloaded file: %w", err)
			}
			_ = os.Remove(path)
			return data, nil
		case browser.DownloadProgressStateCanceled:
			return nil, errors.New("the download was canceled")
		}
	}
	return nil, errors.New("the download did not finish")
}

// redact blanks out every occurrence of the secrets, such as the cookie value sent with Network.setCookie.
func redact(msg string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
		}
	}
	return msg
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
