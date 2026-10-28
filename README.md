# IMDb to WeTrakr sync

Copies your **IMDb watchlist, lists and ratings** to [WeTrakr](https://wetrakr.com) and keeps them up to date by itself,
twice a day.

It runs for free on GitHub, so there is nothing to install. You need about 15 minutes, a web browser on a computer, and
no technical knowledge.

> Comfortable with a terminal? You can also run it on your own computer or with Docker. See
> **[Advanced usage](docs/advanced.md)**.

- [What gets copied](#what-gets-copied)
- [Set it up](#set-it-up)
- [Choose what to sync](#choose-what-to-sync)
- [Get updates](#get-updates)
- [Troubleshooting](#troubleshooting)
- [Questions](#questions)
- [Advanced usage](docs/advanced.md): command line, Docker, every setting, and how the sync works

## What gets copied

| On IMDb | On WeTrakr |
|---|---|
| The movies and shows on your watchlist | Your **Planning** list. A title you have already marked on WeTrakr, for example as watched or dropped, is left as it is. |
| Your lists | A WeTrakr list for each IMDb list, kept **private** unless you choose otherwise. Renaming a list on IMDb renames it on WeTrakr. |
| Your ratings | Your ratings, on the same 1 to 10 scale. |
| Your ratings, if you want | Rated movies and episodes marked as **watched** on the day you rated them. This is off unless you turn it on. |

Nothing is ever deleted from WeTrakr unless you choose the **full** mode (see [step 8](#8-turn-on-syncing)). Even then,
the sync never deletes whole lists or removes titles from your watched history.

## Set it up

You need:

- an [IMDb](https://www.imdb.com) account,
- a [WeTrakr](https://wetrakr.com) account,
- a free [GitHub](https://github.com/signup) account. GitHub is the website that runs the sync for you.

Do the steps in order. Each one takes a minute or two.

### 1. Make your own copy

1. Sign in to GitHub.
2. Open [this project's page](https://github.com/cecobask/imdb-wetrakr-sync) and click **Fork** near the top right,
   then **Create fork**.

GitHub calls your copy a *fork*. Everything below happens in your fork, never on the original project's page. Your
fork's address looks like `https://github.com/YOUR-NAME/imdb-wetrakr-sync`.

### 2. Turn on the sync in your fork

GitHub keeps automatic tasks switched off in new forks until you allow them.

1. In your fork, click the **Actions** tab.
2. Click **I understand my workflows, go ahead and enable them**.
3. Click **sync** in the list on the left. If you see a message that scheduled workflows are disabled, click
   **Enable workflow**.

### 3. Copy your IMDb sign-in cookie

IMDb has no official way for other apps to read your data. The sync signs in as you with a *cookie*: a small piece of
text your browser keeps to remember that you are signed in. **Treat it like a password and never share it.**

Sign in on [imdb.com](https://www.imdb.com) first, then follow the steps for your browser.

<details>
<summary><b>Chrome or Edge</b></summary>

1. On imdb.com, press **F12** (on a Mac: **Cmd + Option + I**). A panel opens.
2. Click the **Application** tab at the top of the panel. If you can't see it, click **»** to show more tabs.
3. On the left, open **Cookies** and click **https://www.imdb.com**.
4. Type `at-main` in the **Filter** box.
5. Click the `at-main` row and copy the text shown in its **Value** column.

</details>

<details>
<summary><b>Firefox</b></summary>

1. On imdb.com, press **F12** (on a Mac: **Cmd + Option + I**). A panel opens.
2. Click the **Storage** tab at the top of the panel.
3. On the left, open **Cookies** and click **https://www.imdb.com**.
4. Find the `at-main` row, double-click its **Value** and copy it.

</details>

<details>
<summary><b>Safari</b></summary>

1. Open **Safari > Settings > Advanced** and tick **Show features for web developers**.
2. On imdb.com, choose **Develop > Show Web Inspector** in the menu bar.
3. Click the **Storage** tab, then **Cookies** on the left.
4. Find the `at-main` row, double-click its **Value** and copy it.

</details>

The value is long and usually starts with `Atza|`. Copy all of it, then paste it somewhere safe for step 6.

Stay signed in to IMDb in that browser. Signing out can make the cookie stop working, and you would then need to copy a
new one.

### 4. Create a WeTrakr app

The sync needs permission to update your WeTrakr account.

1. Sign in on [wetrakr.com](https://wetrakr.com) and open
   [wetrakr.com/profile/settings/api](https://wetrakr.com/profile/settings/api).
2. Create an app. Any name works, for example `IMDb sync`.
3. Copy the **Client ID** and the **Client Secret** for step 6.

Also note your WeTrakr **username**. You need it in step 6 too.

### 5. Create a GitHub access token

This lets the sync remember your WeTrakr sign-in between runs, so you only connect WeTrakr once.

1. On GitHub, click your profile picture (top right), then **Settings**.
2. At the bottom of the left menu, click **Developer settings**, then **Personal access tokens > Fine-grained tokens**.
3. Click **Generate new token**.
4. Fill in the form:
   - **Token name**: `imdb-wetrakr-sync`
   - **Expiration**: the longest option offered. Note the date: the sync stops working when the token expires, and you
     then create a new one (see [Troubleshooting](#troubleshooting)).
   - **Repository access**: **Only select repositories**, then pick your fork, `imdb-wetrakr-sync`.
   - **Permissions**: under repository permissions, find **Secrets** and set it to **Read and write**.
5. Click **Generate token** and copy the token. GitHub shows it only once.

### 6. Save your details in your fork

GitHub keeps these *secrets* hidden. Nobody can see them, including you after saving.

1. In your fork, click **Settings** (the tab at the top, not your profile settings).
2. On the left, click **Secrets and variables > Actions**.
3. For each row below, click **New repository secret**, type the **Name** exactly as shown, paste the value into
   **Secret**, and click **Add secret**.

| Name | Value |
|---|---|
| `IMDB_COOKIEATMAIN` | The IMDb cookie from step 3 |
| `WETRAKR_CLIENTID` | The Client ID from step 4 |
| `WETRAKR_CLIENTSECRET` | The Client Secret from step 4 |
| `GH_PAT` | The GitHub token from step 5 |

Your WeTrakr username isn't secret, so it's saved as a *variable* instead:

1. On the same page, click the **Variables** tab, then **New repository variable**.
2. Enter `WETRAKR_USERNAME` as the **Name** and your WeTrakr username as the **Value**, then click **Add variable**.

### 7. Connect WeTrakr and run the first sync

1. In your fork, click the **Actions** tab, then **sync** on the left.
2. Click **Run workflow** on the right, leave the options as they are, and click the green **Run workflow** button.
3. After a few seconds a run appears in the list. Click it.
4. Wait about a minute. A box appears on the page with a short code, like
   `Open https://wetrakr.com/activate and enter the code K7PX4M`. If you don't see it, click **sync** in the middle of
   the page, then the **Authorize WeTrakr** step: the code is in the lines that appear.
5. Open [wetrakr.com/activate](https://wetrakr.com/activate), enter the code, and approve. Do this within 10 minutes,
   otherwise the code expires and you run the sync again.

The run then continues by itself and takes a few minutes. When it finishes, the run shows a green check mark.

**This first run is only a preview.** It changes nothing on WeTrakr and shows what it would do. To see that:

1. Click **sync** in the middle of the run page, then the **Sync** step.
2. Each line with `would`, such as `would add to planning`, `would rate` or `would create list`, is a change the sync
   would make. The `sync summary` lines at the end give the totals.

### 8. Turn on syncing

When the preview looks right, tell the sync to make the changes.

1. In your fork, click **Settings > Secrets and variables > Actions**, then the **Variables** tab.
2. Click **New repository variable**.
3. Enter `SYNC_MODE` as the **Name** and one of these as the **Value**:

| Value | What happens |
|---|---|
| `add-only` | **Recommended to start.** Copies everything that's missing on WeTrakr and never removes anything. |
| `full` | Makes WeTrakr match IMDb. Removing a title from your IMDb watchlist, a list or your ratings also removes it on WeTrakr. Whole lists and watched history are never removed. |
| `dry-run` | Preview only. This is what happens when `SYNC_MODE` isn't set. |

That's it. The sync now runs every 12 hours by itself. To run it right away, use **Run workflow** as in step 7. The
**Run workflow** form also lets you pick a different mode for that one run, for example a `dry-run` preview before
switching to `full`.

## Choose what to sync

You can change what the sync does with more variables, added the same way as `SYNC_MODE` in step 8. All of them are
optional.

| Name | Value | What it does |
|---|---|---|
| `SYNC_WATCHLIST` | `true` or `false` | Copy your watchlist. On unless set to `false`. |
| `SYNC_LISTS` | `true` or `false` | Copy your lists. On unless set to `false`. |
| `SYNC_RATINGS` | `true` or `false` | Copy your ratings. On unless set to `false`. |
| `SYNC_HISTORY` | `true` or `false` | Mark rated movies and episodes as watched on the day you rated them. Off unless set to `true`. |
| `IMDB_LISTS` | list ids, separated by commas | Copy only these lists. Without it, all your lists are copied. |
| `IMDB_IGNOREDLISTS` | list ids, separated by commas | Never copy these lists. |
| `WETRAKR_LISTPRIVACY` | `private`, `friends` or `public` | Who can see the lists the sync creates on WeTrakr. `private` unless set. |
| `SYNC_TIMEOUT` | for example `45m` | How long a run may take. `30m` unless set. Raise it, up to `55m`, if you have thousands of ratings. |
| `LOG_LEVEL` | `debug`, `info`, `warn` or `error` | How much the run log shows. `info` (the default) shows what the sync does. `debug` shows much more, which helps when reporting a problem. `warn` shows only problems, and `error` only failures. |

A **list id** is the part of the list's address that starts with `ls`. For `https://www.imdb.com/list/ls4154799008/`
the id is `ls4154799008`.

To change a variable later, click the pencil icon next to it on the **Variables** tab.

These are all the settings available on GitHub. [Advanced usage](docs/advanced.md#configuration) lists a few more for
running the sync on your own computer.

## Get updates

IMDb changes its website from time to time, and this project gets fixes when that happens. Your fork does not update
by itself:

1. Open your fork on GitHub.
2. If the page says your fork is behind, click **Sync fork**, then **Update branch**.

Your settings are safe, because they live in secrets and variables, not in the files. Updating every now and then also
counts as activity, which keeps GitHub from pausing the twice-daily runs (see [Troubleshooting](#troubleshooting)).

## Troubleshooting

GitHub emails you when a run fails. Open the run, click **sync**, and look for the red step and its message. Find the
message below. Some of these messages are only warnings and can show up in a run that succeeded.

**Nothing changed on WeTrakr.**
The sync is still in preview mode. Set `SYNC_MODE` to `add-only` or `full`, as in [step 8](#8-turn-on-syncing).

**`... is required`, for example `WETRAKR.CLIENTID is required`**
A secret or variable from [step 6](#6-save-your-details-in-your-fork) is missing or its name is misspelled. The
message names the setting with a dot, so `WETRAKR.CLIENTID` means the `WETRAKR_CLIENTID` secret, and
`WETRAKR.USERNAME` means the `WETRAKR_USERNAME` variable. Names must match exactly, in capital letters. A username
saved as a secret instead of a variable isn't found: delete the secret and add it as a variable.

**`The GH_PAT secret is missing or can't access the secrets of ...`**
The `GH_PAT` secret isn't saved, or the GitHub token from [step 5](#5-create-a-github-access-token) has expired,
doesn't include your fork, or lacks the **Secrets: Read and write** permission. Create a new token and save it as the
`GH_PAT` secret. To replace an existing one, click the pencil icon next to it on the secrets page.

**`failure signing in to IMDb with the provided cookie`**
The IMDb cookie has expired. Copy a new one as in [step 3](#3-copy-your-imdb-sign-in-cookie) and replace the
`IMDB_COOKIEATMAIN` secret.

**`the device code expired before it was approved`**
The code wasn't entered within 10 minutes. Run the sync again and enter the new code sooner. This also happens when a
scheduled run needs you to connect WeTrakr again while you aren't around. Run it by hand when you are.

**`the WeTrakr token belongs to the account ...`**
The code was approved with a WeTrakr account other than the one in `WETRAKR_USERNAME`. The sync discarded that sign-in
for your safety. Check the `WETRAKR_USERNAME` variable, run the sync again, and approve the code while signed in to your
own WeTrakr account.

**The sync asks for a WeTrakr code on every run.**
It can't save your WeTrakr sign-in. Look for an error in the **Save the WeTrakr token** step, which usually means the
`GH_PAT` token needs fixing as described above.

**`IMDb exports are still not ready`**
IMDb took longer than usual to prepare your data, which can happen with thousands of ratings. The sync skips that part
this time and tries again on the next run. If it keeps happening, set the `SYNC_TIMEOUT` variable to `45m` or `55m`.

**`failure creating the list (the free WeTrakr plan allows 5 lists)` or `PLAN_LIMIT_REACHED`**
A free WeTrakr account can have 5 lists with up to 250 titles each. The rest of the sync still runs. Use
`IMDB_LISTS` or `IMDB_IGNOREDLISTS` to choose which lists to copy.

**`QUOTA_EXCEEDED`, `APP_QUOTA_EXCEEDED` or `APP_USER_QUOTA_EXCEEDED`**
WeTrakr's daily request limit was reached, either for your account or for your WeTrakr app. The sync stops, and the
next run continues where this one stopped.

**`not found on WeTrakr`**
WeTrakr doesn't have that title in its catalog yet. Nothing to fix on your side; the sync tries again on every run.

**The sync stopped running on its own.**
GitHub pauses scheduled runs in a fork after 60 days without activity, and emails you when it does. Open the
**Actions** tab, click **sync**, and click **Enable workflow**. Using **Sync fork** now and then, as described in
[Get updates](#get-updates), prevents this.

**`the page layout probably changed` or `failure solving the AWS WAF challenge`**
IMDb changed its website. Check for an update as described in [Get updates](#get-updates). If there's none yet,
[report it](https://github.com/cecobask/imdb-wetrakr-sync/issues). It helps to add the variable `LOG_LEVEL` with the
value `debug`, run the sync again, and attach the log of the **Sync** step. Your cookie is blanked out of it. Delete
the variable afterwards: the debug log is very long, makes runs slower, and includes the pages the sync opened on
IMDb, which anyone can read in a public fork.

## Questions

**Is it free?**
Yes. GitHub runs it for free in public repositories such as your fork, and WeTrakr's free plan works too, within its
limits of 5 lists with 250 titles each.

**Who can see my data?**
Your fork is public, like the original project, and so are the logs of its runs. They show the titles being synced
and your WeTrakr username, and, while it is valid, the code to connect WeTrakr. Your cookie, tokens and other secrets
are never shown. If someone
else enters your code with their own WeTrakr account, the sync notices that the account doesn't match
`WETRAKR_USERNAME` and discards it.

**Does it change anything on IMDb?**
No. It only reads from IMDb. It does create data exports in your IMDb account, which you can see under
[imdb.com/exports](https://www.imdb.com/exports/). IMDb removes them after a few weeks.

**Can I stop it?**
In your fork, click **Actions > sync**, then the **...** menu and **Disable workflow**. To remove its access, delete the
app on WeTrakr and the token on GitHub.

**Can I run it on my own computer instead?**
Yes. See [Advanced usage](docs/advanced.md) for the command line and the Docker image.
