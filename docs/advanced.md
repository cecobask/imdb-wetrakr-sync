# Advanced usage

This page is for people comfortable with a terminal. To run the sync on GitHub without installing anything, follow the
[README](../README.md) instead.

- [Command line](#command-line)
- [Configuration](#configuration)
- [Container](#container)
- [How the sync works](#how-the-sync-works)
- [GitHub Actions internals](#github-actions-internals)
- [Development](#development)

## Command line

You need [Go](https://go.dev/dl/) 1.27 or later and Chrome or Chromium.

```sh
make build
cp config.example.yaml config.yaml   # then fill it in
./build/iws auth                     # logs a code to enter at https://wetrakr.com/activate
./build/iws sync                     # uses SYNC.MODE (dry-run by default)
./build/iws sync --mode add-only
```

`make auth` and `make sync` build and run in one step. `config.yaml`, `.env` and token files are git-ignored.

| Command | What it does |
|---|---|
| `iws auth` | Authorizes your WeTrakr account with the device flow and saves the tokens to `WETRAKR.TOKENFILE`. |
| `iws auth --if-needed` | Keeps a saved token that still works and only authorizes when there is none or WeTrakr rejects it. |
| `iws sync` | Runs the sync. `--mode` overrides `SYNC.MODE`. |
| `iws sync --save-exports DIR` | Also keeps the downloaded IMDb CSV files in `DIR`. `exports` is git-ignored. |
| `iws sync --imdb-export-dir DIR` | Reads the IMDb CSV files from `DIR` instead of downloading them. Needs no browser and no IMDb cookie. |
| `iws version` | Prints the version. |

Global flags: `--config-file` (default `config.yaml`), `--log-level` (`debug`, `info`, `warn`, `error`) and
`--log-format` (`text` or `json`). The two log flags override `LOG.LEVEL` and `LOG.FORMAT`. A missing `config.yaml` is
fine when every setting comes from environment variables, but a file named with `--config-file` must exist.

`--imdb-export-dir` reads `watchlist.csv`, `ratings.csv` and one `<list id> <list name>.csv` per list, which is the
layout `--save-exports` writes. A missing file is skipped, never treated as empty, so it can't remove anything from
WeTrakr.

The sync refreshes the WeTrakr access token by itself and saves each rotated refresh token. A refresh token expires
after 180 days without use, after which you run `iws auth` again.

## Configuration

Settings come from `config.yaml` (see [`config.example.yaml`](../config.example.yaml)) and from environment variables
named `IWS_<SECTION>_<KEY>`, for example `IWS_SYNC_MODE=full`. Environment variables override the file, and empty ones
are ignored. List values take comma-separated ids, for example `IWS_IMDB_LISTS=ls000000001,ls000000002`. In GitHub
Actions, the workflow maps the repository secrets and variables onto these environment variables (see
[GitHub Actions internals](#github-actions-internals)).

| Key | Default | Meaning |
|---|---|---|
| `LOG.LEVEL` | `info` | `debug`, `info`, `warn` or `error`. The browser's logs follow it: `debug` includes every DevTools message, with the cookie value blanked out. |
| `LOG.FORMAT` | `text` | `text` or `json`. |
| `IMDB.COOKIEATMAIN` | | Value of the `at-main` cookie of a signed-in imdb.com session. Required unless `IMDB.EXPORTDIR` is set. |
| `IMDB.LISTS` | all lists | IMDb list ids to sync, such as `ls123456789`. |
| `IMDB.IGNOREDLISTS` | | IMDb list ids to leave out. |
| `IMDB.HEADLESS` | `true` | Run Chrome without a window. Set to `false` to watch what the browser does. |
| `IMDB.BROWSERPATH` | auto | Path to Chrome or Chromium. |
| `IMDB.EXPORTDIR` | | Read the IMDb CSV files from this directory instead of downloading them. Same as `--imdb-export-dir`. |
| `WETRAKR.CLIENTID`, `WETRAKR.CLIENTSECRET` | | Credentials of your app from <https://wetrakr.com/profile/settings/api>. |
| `WETRAKR.USERNAME` | | When set, auth and sync refuse a token of any other WeTrakr account. Required in GitHub Actions, where run logs can be public and anyone could enter the authorization code. |
| `WETRAKR.TOKENFILE` | `wetrakr-token.json` | Where the tokens are saved. |
| `WETRAKR.LISTPRIVACY` | `private` | Privacy of the lists the sync creates: `public`, `friends` or `private`. |
| `SYNC.MODE` | `dry-run` | `dry-run` only logs the changes, `add-only` never removes anything, `full` makes WeTrakr mirror IMDb. |
| `SYNC.WATCHLIST`, `SYNC.LISTS`, `SYNC.RATINGS` | `true` | Which parts to sync. |
| `SYNC.HISTORY` | `false` | Mark rated movies and episodes as watched on the date they were rated. Only adds, even in `full` mode. |
| `SYNC.TIMEOUT` | `30m` | Limit for the whole run. The wait for IMDb exports keeps a quarter of it (2 to 5 minutes) for WeTrakr. |

## Container

The image is built on [chromedp/headless-shell](https://hub.docker.com/r/chromedp/headless-shell) and keeps the token
in `/data/wetrakr-token.json`. Mount `/data` so refreshed tokens survive between runs. Copy
[`.env.example`](../.env.example) to `.env` and fill it in.

```sh
make package                                  # builds iws:dev
mkdir -p data
docker run --rm -it --env-file .env -v "$PWD/data:/data" iws:dev auth
docker run --rm --env-file .env -v "$PWD/data:/data" iws:dev sync
```

`make sync-container` builds the image and runs the sync in one step. Published images are available at
`ghcr.io/cecobask/imdb-wetrakr-sync`, tagged `main` for the latest commit and with the version number for each release.

## How the sync works

- **IMDb.** IMDb has no public API. `iws` opens imdb.com in a headless Chrome driven by
  [chromedp](https://github.com/chromedp/chromedp), signs in with your `at-main` cookie, solves the AWS WAF challenge,
  requests the CSV exports of the watchlist, the lists and the ratings, waits on the exports page until they are ready,
  and downloads them. Before requesting exports it counts the existing ones, so an older export is never mistaken for
  the new one.
- **WeTrakr.** It reads the current state with compact pages of up to 5,000 rows, compares by IMDb id, and writes the
  differences in batches of 1,000 items. Rate limits are retried with the delay the API asks for. Writes carry an
  idempotency key, so a retried write is applied once. A daily quota error stops the run.
- **Watchlist** titles go to the `planning` status. Only movies and shows are copied, and a title that already has
  another status on WeTrakr is left alone.
- **Lists** are linked by a marker such as `[imdb:ls123456789]` in the WeTrakr list description, so renaming a list on
  IMDb renames it on WeTrakr. A WeTrakr list with the same name and no marker is adopted. Lists are never deleted.
- **Ratings.** In full mode, WeTrakr ratings of seasons and people are left alone, because IMDb has no equivalent.

## GitHub Actions internals

[`sync.yaml`](../.github/workflows/sync.yaml) checks that `GH_PAT` can access the repository secrets (`iws` validates
its own settings), builds `iws`, restores the token from the `WETRAKR_TOKEN` secret, runs `iws auth --if-needed`
(which waits up to 10 minutes for the device code to be approved when needed), runs the sync, and finally saves the
token with [`save-token.sh`](../.github/scripts/save-token.sh), even when a step failed or the run was cancelled.
Saving uses the `GH_PAT` secret because the default `GITHUB_TOKEN` can't write secrets.

Secrets and variables become `IWS_*` environment variables with the same name, for example the `SYNC_MODE` variable
becomes `IWS_SYNC_MODE`. The **Run workflow** form's `mode` input overrides `SYNC_MODE` for one run.

The CI and image workflows only run in `cecobask/imdb-wetrakr-sync`, not in forks.

## Development

```sh
make test          # unit tests
make test-browser  # also the chromedp tests (needs Chrome)
make lint          # needs golangci-lint
```
