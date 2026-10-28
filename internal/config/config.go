// Package config loads and validates the application configuration from a YAML file and IWS_ environment variables.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const (
	EnvPrefix = "IWS_"

	ModeDryRun  = "dry-run"
	ModeAddOnly = "add-only"
	ModeFull    = "full"

	DefaultBaseURL = "https://api.wetrakr.com"
)

var (
	modes      = []string{ModeDryRun, ModeAddOnly, ModeFull}
	privacies  = []string{"public", "friends", "private"}
	listIDRe   = regexp.MustCompile(`^ls[0-9]{9,10}$`)
	listKeys   = []string{"IMDB.LISTS", "IMDB.IGNOREDLISTS"}
	dummyValue = regexp.MustCompile(`^<.*>$`)
)

type Config struct {
	Log     Log     `koanf:"LOG"`
	IMDb    IMDb    `koanf:"IMDB"`
	WeTrakr WeTrakr `koanf:"WETRAKR"`
	Sync    Sync    `koanf:"SYNC"`

	// githubActions is set when running in GitHub Actions, where run logs can be public.
	githubActions bool
}

// Log configures the logger. The --log-level and --log-format flags override it.
type Log struct {
	Level  string `koanf:"LEVEL"`
	Format string `koanf:"FORMAT"`
}

type IMDb struct {
	CookieAtMain string   `koanf:"COOKIEATMAIN"`
	Lists        []string `koanf:"LISTS"`
	IgnoredLists []string `koanf:"IGNOREDLISTS"`
	Headless     bool     `koanf:"HEADLESS"`
	BrowserPath  string   `koanf:"BROWSERPATH"`
	// ExportDir, when set, is read for the IMDb CSV exports instead of downloading them.
	ExportDir string `koanf:"EXPORTDIR"`
}

type WeTrakr struct {
	ClientID     string `koanf:"CLIENTID"`
	ClientSecret string `koanf:"CLIENTSECRET"`
	TokenFile    string `koanf:"TOKENFILE"`
	ListPrivacy  string `koanf:"LISTPRIVACY"`
	// Username, when set, makes auth and sync refuse a token of any other WeTrakr account.
	Username string `koanf:"USERNAME"`
	BaseURL  string `koanf:"BASEURL"`
}

type Sync struct {
	Mode      string        `koanf:"MODE"`
	Watchlist bool          `koanf:"WATCHLIST"`
	Lists     bool          `koanf:"LISTS"`
	Ratings   bool          `koanf:"RATINGS"`
	History   bool          `koanf:"HISTORY"`
	Timeout   time.Duration `koanf:"TIMEOUT"`
}

// Default returns the configuration used for every key that the file and the environment leave unset.
func Default() Config {
	return Config{
		Log: Log{
			Level:  "info",
			Format: "text",
		},
		IMDb: IMDb{
			Headless: true,
		},
		WeTrakr: WeTrakr{
			TokenFile:   "wetrakr-token.json",
			ListPrivacy: "private",
			BaseURL:     DefaultBaseURL,
		},
		Sync: Sync{
			Mode:      ModeDryRun,
			Watchlist: true,
			Lists:     true,
			Ratings:   true,
			History:   false,
			Timeout:   30 * time.Minute,
		},
	}
}

// Load reads the YAML file at path (optional unless required is true) and then applies IWS_ environment variables on top.
func Load(path string, required bool, environ func() []string) (*Config, error) {
	k := koanf.New(".")
	if path != "" {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			if required || !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("failure loading config file %s: %w", path, err)
			}
		}
	}
	if environ == nil {
		environ = os.Environ
	}
	envProvider := env.Provider(".", env.Opt{
		Prefix:        EnvPrefix,
		EnvironFunc:   environ,
		TransformFunc: transformEnv,
	})
	if err := k.Load(envProvider, nil); err != nil {
		return nil, fmt.Errorf("failure loading environment variables: %w", err)
	}
	cfg := Default()
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "koanf"}); err != nil {
		return nil, fmt.Errorf("failure decoding config: %w", err)
	}
	cfg.normalize()
	cfg.githubActions = slices.Contains(environ(), "GITHUB_ACTIONS=true")
	return &cfg, nil
}

// transformEnv maps IWS_SECTION_KEY=value to SECTION.KEY, ignores empty values and splits list keys on commas.
func transformEnv(k, v string) (string, any) {
	section, key, ok := strings.Cut(strings.TrimPrefix(k, EnvPrefix), "_")
	if !ok || v == "" {
		return "", nil
	}
	path := strings.ToUpper(section) + "." + strings.ToUpper(key)
	if slices.Contains(listKeys, path) {
		return path, splitList(v)
	}
	return path, v
}

func splitList(v string) []string {
	var out []string
	for part := range strings.SplitSeq(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (c *Config) normalize() {
	c.Sync.Mode = strings.ToLower(strings.TrimSpace(c.Sync.Mode))
	c.WeTrakr.ListPrivacy = strings.ToLower(strings.TrimSpace(c.WeTrakr.ListPrivacy))
	c.WeTrakr.BaseURL = strings.TrimRight(c.WeTrakr.BaseURL, "/")
	c.IMDb.Lists = trimAll(c.IMDb.Lists)
	c.IMDb.IgnoredLists = trimAll(c.IMDb.IgnoredLists)
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ValidateWeTrakr checks the settings needed to talk to the WeTrakr API.
func (c *Config) ValidateWeTrakr() error {
	var errs []error
	if c.WeTrakr.ClientID == "" || dummyValue.MatchString(c.WeTrakr.ClientID) {
		errs = append(errs, errors.New("WETRAKR.CLIENTID is required"))
	}
	if c.WeTrakr.ClientSecret == "" || dummyValue.MatchString(c.WeTrakr.ClientSecret) {
		errs = append(errs, errors.New("WETRAKR.CLIENTSECRET is required"))
	}
	if c.WeTrakr.TokenFile == "" {
		errs = append(errs, errors.New("WETRAKR.TOKENFILE is required"))
	}
	if c.githubActions && c.WeTrakr.Username == "" {
		// Anyone who can read a public run log can enter the authorization code with their own account.
		errs = append(errs, errors.New("WETRAKR.USERNAME is required in GitHub Actions, where run logs can be public"))
	}
	if c.WeTrakr.BaseURL == "" {
		errs = append(errs, errors.New("WETRAKR.BASEURL must not be empty"))
	}
	return joinErrors(errs...)
}

// Validate checks the full configuration needed by the sync command.
func (c *Config) Validate() error {
	errs := []error{c.ValidateWeTrakr()}
	if c.IMDb.ExportDir == "" && (c.IMDb.CookieAtMain == "" || dummyValue.MatchString(c.IMDb.CookieAtMain)) {
		errs = append(errs, errors.New("IMDB.COOKIEATMAIN is required unless IMDB.EXPORTDIR is set"))
	}
	for _, id := range slices.Concat(c.IMDb.Lists, c.IMDb.IgnoredLists) {
		if !listIDRe.MatchString(id) {
			errs = append(errs, fmt.Errorf("invalid IMDb list id %s: expected the format ls123456789", id))
		}
	}
	if !slices.Contains(modes, c.Sync.Mode) {
		errs = append(errs, fmt.Errorf("invalid SYNC.MODE %s: expected one of %s", c.Sync.Mode, strings.Join(modes, ", ")))
	}
	if !slices.Contains(privacies, c.WeTrakr.ListPrivacy) {
		errs = append(errs, fmt.Errorf("invalid WETRAKR.LISTPRIVACY %s: expected one of %s", c.WeTrakr.ListPrivacy, strings.Join(privacies, ", ")))
	}
	if c.Sync.Timeout <= 0 {
		errs = append(errs, errors.New("SYNC.TIMEOUT must be positive"))
	}
	if !c.Sync.Watchlist && !c.Sync.Lists && !c.Sync.Ratings && !c.Sync.History {
		errs = append(errs, errors.New("nothing to sync: enable at least one of SYNC.WATCHLIST, SYNC.LISTS, SYNC.RATINGS, SYNC.HISTORY"))
	}
	return joinErrors(errs...)
}

// multiError is a list of errors with a one-line message, so it reads well inside a log line.
type multiError []error

func (m multiError) Error() string {
	parts := make([]string, len(m))
	for i, err := range m {
		parts[i] = err.Error()
	}
	return strings.Join(parts, "; ")
}

func (m multiError) Unwrap() []error { return m }

// joinErrors works like errors.Join, but flattens nested lists and joins the messages with "; " instead of newlines.
func joinErrors(errs ...error) error {
	var flat multiError
	for _, err := range errs {
		if nested, ok := err.(multiError); ok {
			flat = append(flat, nested...)
		} else if err != nil {
			flat = append(flat, err)
		}
	}
	if len(flat) == 0 {
		return nil
	}
	return flat
}
