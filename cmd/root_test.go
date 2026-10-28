package cmd

import (
	"context"
	"log/slog"
	"testing"

	"github.com/spf13/cobra"
)

func TestLogLevelFromConfigAndFlags(t *testing.T) {
	tests := []struct {
		name      string
		env       string
		args      []string
		wantLevel slog.Level
	}{
		{name: "default", wantLevel: slog.LevelInfo},
		{name: "from the environment", env: "debug", wantLevel: slog.LevelDebug},
		{name: "flag overrides the environment", env: "debug", args: []string{"--log-level", "warn"}, wantLevel: slog.LevelWarn},
		{name: "flag alone", args: []string{"--log-level", "debug"}, wantLevel: slog.LevelDebug},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("IWS_LOG_LEVEL", tt.env)
			flags := &globalFlags{}
			root := newRootCommand(flags)
			var log *slog.Logger
			root.AddCommand(&cobra.Command{
				Use: "probe",
				RunE: func(cmd *cobra.Command, _ []string) error {
					var err error
					_, log, err = flags.load(cmd)
					return err
				},
			})
			root.SetArgs(append([]string{"probe"}, tt.args...))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn} {
				if got, want := log.Enabled(context.Background(), level), level >= tt.wantLevel; got != want {
					t.Errorf("level %s enabled: got %v, want %v", level, got, want)
				}
			}
		})
	}
}
