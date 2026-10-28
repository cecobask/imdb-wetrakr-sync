package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func environ(vars ...string) func() []string {
	return func() []string { return vars }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), false, environ())
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	if cfg.Sync.Mode != want.Sync.Mode || cfg.Sync.Timeout != want.Sync.Timeout || !cfg.IMDb.Headless ||
		cfg.WeTrakr.ListPrivacy != "private" || cfg.WeTrakr.TokenFile != "wetrakr-token.json" || cfg.Sync.History {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadRequiredFileMissing(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), true, environ()); err == nil {
		t.Fatal("expected an error for a missing required file")
	}
}

func TestLoadFileThenEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
IMDB:
  COOKIEATMAIN: from-file
  LISTS:
    - ls000000001
  HEADLESS: false
WETRAKR:
  CLIENTID: id-from-file
SYNC:
  MODE: full
  TIMEOUT: 5m
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true, environ(
		"IWS_IMDB_COOKIEATMAIN=from-env",
		"IWS_IMDB_LISTS=ls000000002, ls000000003,",
		"IWS_IMDB_IGNOREDLISTS=ls000000004",
		"IWS_SYNC_HISTORY=true",
		"IWS_SYNC_RATINGS=false",
		"IWS_SYNC_TIMEOUT=45m",
		"IWS_WETRAKR_CLIENTID=",
		"OTHER_VAR=ignored",
	))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IMDb.CookieAtMain != "from-env" {
		t.Errorf("cookie: got %q", cfg.IMDb.CookieAtMain)
	}
	if !slices.Equal(cfg.IMDb.Lists, []string{"ls000000002", "ls000000003"}) {
		t.Errorf("lists: got %v", cfg.IMDb.Lists)
	}
	if !slices.Equal(cfg.IMDb.IgnoredLists, []string{"ls000000004"}) {
		t.Errorf("ignored lists: got %v", cfg.IMDb.IgnoredLists)
	}
	if cfg.IMDb.Headless {
		t.Error("headless: expected false from file")
	}
	if cfg.WeTrakr.ClientID != "id-from-file" {
		t.Errorf("empty env must not override file: got %q", cfg.WeTrakr.ClientID)
	}
	if cfg.Sync.Mode != ModeFull || !cfg.Sync.History || cfg.Sync.Ratings || !cfg.Sync.Watchlist {
		t.Errorf("sync: got %+v", cfg.Sync)
	}
	if cfg.Sync.Timeout != 45*time.Minute {
		t.Errorf("timeout: got %s", cfg.Sync.Timeout)
	}
}

func TestValidate(t *testing.T) {
	valid := func() Config {
		c := Default()
		c.IMDb.CookieAtMain = "cookie"
		c.WeTrakr.ClientID = "id"
		c.WeTrakr.ClientSecret = "secret"
		return c
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "missing cookie", mutate: func(c *Config) { c.IMDb.CookieAtMain = "" }, wantErr: "COOKIEATMAIN"},
		{name: "placeholder cookie", mutate: func(c *Config) { c.IMDb.CookieAtMain = "<at-main>" }, wantErr: "COOKIEATMAIN"},
		{name: "export dir instead of cookie", mutate: func(c *Config) { c.IMDb.CookieAtMain = ""; c.IMDb.ExportDir = "dir" }},
		{name: "missing client id", mutate: func(c *Config) { c.WeTrakr.ClientID = "" }, wantErr: "CLIENTID"},
		{name: "bad list id", mutate: func(c *Config) { c.IMDb.Lists = []string{"ur123"} }, wantErr: "invalid IMDb list id"},
		{name: "bad mode", mutate: func(c *Config) { c.Sync.Mode = "yolo" }, wantErr: "SYNC.MODE"},
		{name: "bad privacy", mutate: func(c *Config) { c.WeTrakr.ListPrivacy = "secret" }, wantErr: "LISTPRIVACY"},
		{name: "nothing enabled", mutate: func(c *Config) {
			c.Sync.Watchlist, c.Sync.Lists, c.Sync.Ratings, c.Sync.History = false, false, false, false
		}, wantErr: "nothing to sync"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid()
			tt.mutate(&c)
			err := c.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadIMDbExportDir(t *testing.T) {
	cfg, err := Load("", false, environ("IWS_IMDB_EXPORTDIR=exports"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IMDb.ExportDir != "exports" {
		t.Errorf("imdb: %+v", cfg.IMDb)
	}
}

func TestLoadLog(t *testing.T) {
	cfg, err := Load("", false, environ())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "info" || cfg.Log.Format != "text" {
		t.Errorf("defaults: %+v", cfg.Log)
	}
	cfg, err = Load("", false, environ("IWS_LOG_LEVEL=debug", "IWS_LOG_FORMAT=json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "json" {
		t.Errorf("from env: %+v", cfg.Log)
	}
}

func TestValidateJoinsErrorsOnOneLine(t *testing.T) {
	c := Default()
	err := c.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("message has a newline: %q", err)
	}
	if !strings.Contains(err.Error(), "WETRAKR.CLIENTID is required; WETRAKR.CLIENTSECRET is required; IMDB.COOKIEATMAIN is required") {
		t.Errorf("message: %q", err)
	}
	if joinErrors(nil, nil) != nil {
		t.Error("joining nil errors must return nil")
	}
	sentinel := errors.New("sentinel")
	if !errors.Is(joinErrors(errors.New("a"), joinErrors(sentinel)), sentinel) {
		t.Error("joined errors must stay inspectable")
	}
}

func TestUsernameRequiredInGitHubActions(t *testing.T) {
	base := []string{"IWS_WETRAKR_CLIENTID=id", "IWS_WETRAKR_CLIENTSECRET=secret"}
	tests := []struct {
		name    string
		env     []string
		wantErr bool
	}{
		{name: "locally without a username", env: base},
		{name: "in GitHub Actions without a username", env: append([]string{"GITHUB_ACTIONS=true"}, base...), wantErr: true},
		{name: "in GitHub Actions with a username", env: append([]string{"GITHUB_ACTIONS=true", "IWS_WETRAKR_USERNAME=me"}, base...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load("", false, environ(tt.env...))
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.ValidateWeTrakr()
			if got := err != nil && strings.Contains(err.Error(), "WETRAKR.USERNAME is required in GitHub Actions"); got != tt.wantErr {
				t.Errorf("error: %v", err)
			}
		})
	}
}
