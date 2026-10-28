package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/cecobask/imdb-wetrakr-sync/internal/wetrakr"
)

func newAuthCommand(flags *globalFlags) *cobra.Command {
	var ifNeeded bool
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authorize iws with your WeTrakr account and save the token",
		Long: `Authorize iws with your WeTrakr account using the device flow.

The command logs a code and a URL. Open the URL, sign in to WeTrakr and enter the code.
The tokens are saved to WETRAKR.TOKENFILE and refreshed automatically by the sync command.

With --if-needed, a saved token that still works is kept (and refreshed when close to expiry), and the device flow only
starts when there is no token or WeTrakr rejects it. This is how the GitHub Actions workflow authorizes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := flags.load(cmd)
			if err != nil {
				return err
			}
			if err := cfg.ValidateWeTrakr(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client := newClient(cfg, log)
			if ifNeeded {
				account, err := checkToken(ctx, client)
				if err == nil {
					err = wetrakr.CheckAccount(account, cfg.WeTrakr.Username)
				}
				var wrongAccount *wetrakr.WrongAccountError
				switch {
				case err == nil:
					log.Info("the saved WeTrakr token is valid", "user", account.Info.Username, "token_file", cfg.WeTrakr.TokenFile)
					return nil
				case !wetrakr.NeedsReauth(err) && !errors.As(err, &wrongAccount):
					return err
				}
				log.Warn("the saved WeTrakr token is missing or no longer valid, starting authorization", "reason", err)
			}
			dc, err := client.RequestDeviceCode(ctx)
			if err != nil {
				return fmt.Errorf("failure requesting a device code: %w", err)
			}
			expiresIn := time.Duration(dc.ExpiresIn) * time.Second
			log.Info("open the verification URL, sign in and enter the code", "url", dc.VerificationURL, "code", dc.UserCode, "expires_in", expiresIn)
			if os.Getenv("GITHUB_ACTIONS") == "true" {
				// A workflow command, so the code also shows as an annotation on the run page.
				fmt.Fprintf(cmd.OutOrStdout(), "::notice title=Authorize WeTrakr::Open %s and enter the code %s within %s\n", dc.VerificationURL, dc.UserCode, expiresIn)
			}
			if _, err := client.PollDeviceToken(ctx, dc); err != nil {
				return err
			}
			account, err := client.Account(ctx)
			if err != nil {
				return fmt.Errorf("authorized, but failure fetching the account: %w", err)
			}
			if err := wetrakr.CheckAccount(account, cfg.WeTrakr.Username); err != nil {
				// Someone else may have entered the code first (it is visible in public workflow logs): drop their token.
				if delErr := wetrakr.NewTokenStore(cfg.WeTrakr.TokenFile).Delete(); delErr != nil {
					return fmt.Errorf("%w; %w", err, delErr)
				}
				return fmt.Errorf("%w: the token was discarded, authorize again with your own account", err)
			}
			log.Info("authorized", "user", account.Info.Username, "token_file", cfg.WeTrakr.TokenFile)
			return nil
		},
	}
	cmd.Flags().BoolVar(&ifNeeded, "if-needed", false, "keep a saved token that still works and only authorize when it is missing or rejected")
	return cmd
}

// checkToken loads the saved token, refreshes it when close to expiry and checks that WeTrakr accepts it.
func checkToken(ctx context.Context, client *wetrakr.Client) (*wetrakr.Account, error) {
	if err := client.EnsureToken(ctx); err != nil {
		return nil, err
	}
	return client.Account(ctx)
}
