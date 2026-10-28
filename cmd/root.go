// Package cmd holds the command line interface of iws.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/cecobask/imdb-wetrakr-sync/internal/config"
	"github.com/cecobask/imdb-wetrakr-sync/internal/logger"
	"github.com/cecobask/imdb-wetrakr-sync/internal/wetrakr"
)

// Version is set at build time with -ldflags "-X github.com/cecobask/imdb-wetrakr-sync/cmd.Version=…".
var Version = "dev"

const defaultConfigFile = "config.yaml"

type globalFlags struct {
	configFile string
	logLevel   string
	logFormat  string
	// logger is set once the flags are parsed, so failures can be logged in the chosen format.
	logger *slog.Logger
}

// errReported marks a failure that was already printed, so Execute only sets the exit code.
var errReported = errors.New("sync finished with errors")

// Execute runs the command line and returns the process exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	flags := &globalFlags{}
	root := newRootCommand(flags)
	if err := root.ExecuteContext(ctx); err != nil {
		switch {
		case errors.Is(err, errReported):
		case flags.logger != nil:
			flags.logger.Error("command failed", "error", err)
		default:
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		return 1
	}
	return 0
}

func newRootCommand(flags *globalFlags) *cobra.Command {
	root := &cobra.Command{
		Use:           "iws",
		Aliases:       []string{"imdb-wetrakr-sync"},
		Short:         "Sync your IMDb watchlist, lists and ratings to WeTrakr",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&flags.configFile, "config-file", defaultConfigFile, "path to the YAML config file (optional; IWS_* environment variables override it)")
	root.PersistentFlags().StringVar(&flags.logLevel, "log-level", "info", "log level: debug, info, warn or error (overrides LOG.LEVEL)")
	root.PersistentFlags().StringVar(&flags.logFormat, "log-format", "text", "log format: text or json (overrides LOG.FORMAT)")
	root.AddCommand(newAuthCommand(flags), newSyncCommand(flags), newVersionCommand())
	return root
}

// load reads the config and builds the logger from LOG.LEVEL and LOG.FORMAT, which the flags override.
func (f *globalFlags) load(cmd *cobra.Command) (*config.Config, *slog.Logger, error) {
	// A logger from the flags alone, so a failure to load the config is still logged.
	log, err := logger.New(os.Stderr, f.logFormat, f.logLevel)
	if err != nil {
		return nil, nil, err
	}
	f.logger = log
	required := cmd.Flags().Changed("config-file")
	cfg, err := config.Load(f.configFile, required, os.Environ)
	if err != nil {
		return nil, nil, err
	}
	if cmd.Flags().Changed("log-level") {
		cfg.Log.Level = f.logLevel
	}
	if cmd.Flags().Changed("log-format") {
		cfg.Log.Format = f.logFormat
	}
	if log, err = logger.New(os.Stderr, cfg.Log.Format, cfg.Log.Level); err != nil {
		return nil, nil, err
	}
	f.logger = log
	return cfg, log, nil
}

func newClient(cfg *config.Config, log *slog.Logger) *wetrakr.Client {
	return wetrakr.New(wetrakr.Options{
		BaseURL:      cfg.WeTrakr.BaseURL,
		ClientID:     cfg.WeTrakr.ClientID,
		ClientSecret: cfg.WeTrakr.ClientSecret,
		Store:        wetrakr.NewTokenStore(cfg.WeTrakr.TokenFile),
		Logger:       log,
	})
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), Version)
		},
	}
}
