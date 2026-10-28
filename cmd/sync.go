package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/cecobask/imdb-wetrakr-sync/internal/imdb"
	"github.com/cecobask/imdb-wetrakr-sync/internal/syncer"
)

func newSyncCommand(flags *globalFlags) *cobra.Command {
	var (
		mode        string
		exportDir   string
		saveExports string
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync IMDb to WeTrakr",
		Long: `Sync the IMDb watchlist, lists and ratings to WeTrakr.

Modes:
  dry-run   log the changes without making them (default)
  add-only  add missing items, never remove anything
  full      add and remove, so WeTrakr mirrors IMDb`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := flags.load(cmd)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("mode") {
				cfg.Sync.Mode = mode
			}
			if cmd.Flags().Changed("imdb-export-dir") {
				cfg.IMDb.ExportDir = exportDir
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Sync.Timeout)
			defer cancel()

			client := newClient(cfg, log)
			if err := client.EnsureToken(ctx); err != nil {
				return err
			}
			var source imdb.Source
			if cfg.IMDb.ExportDir != "" {
				log.Info("reading IMDb exports from a directory", "dir", cfg.IMDb.ExportDir)
				source = imdb.NewDirSource(cfg.IMDb.ExportDir, log)
			} else {
				source = imdb.NewBrowserSource(imdb.BrowserOptions{
					CookieAtMain: cfg.IMDb.CookieAtMain,
					Headless:     cfg.IMDb.Headless,
					BrowserPath:  cfg.IMDb.BrowserPath,
					SaveDir:      saveExports,
				}, log)
			}
			defer func() { _ = source.Close() }()

			s := syncer.New(source, client, syncer.Options{
				Mode:        cfg.Sync.Mode,
				Watchlist:   cfg.Sync.Watchlist,
				Lists:       cfg.Sync.Lists,
				Ratings:     cfg.Sync.Ratings,
				History:     cfg.Sync.History,
				ListIDs:     cfg.IMDb.Lists,
				IgnoredList: cfg.IMDb.IgnoredLists,
				ListPrivacy: cfg.WeTrakr.ListPrivacy,
				Username:    cfg.WeTrakr.Username,
			}, log)
			report, runErr := s.Run(ctx)
			report.Log(log)
			if runErr != nil {
				return runErr
			}
			if report.Failed() {
				return errReported
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "", "sync mode: dry-run, add-only or full (overrides SYNC.MODE)")
	cmd.Flags().StringVar(&exportDir, "imdb-export-dir", "", "read IMDb CSV exports from this directory instead of downloading them (overrides IMDB.EXPORTDIR)")
	cmd.Flags().StringVar(&saveExports, "save-exports", "", "save the downloaded IMDb CSV exports to this directory")
	return cmd
}
