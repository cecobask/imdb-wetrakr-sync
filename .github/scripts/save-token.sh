#!/usr/bin/env bash
# Saves wetrakr-token.json to the WETRAKR_TOKEN repository secret when it changed since the last save. WeTrakr rotates
# the refresh token on every refresh, so a token that is not saved can't be used by the next run.
set -euo pipefail

token_file="${1:-wetrakr-token.json}"
checksum_file="${token_file}.sha256"

if [ ! -s "$token_file" ]; then
  echo "No token to save."
  exit 0
fi
if [ -f "$checksum_file" ] && sha256sum --status -c "$checksum_file"; then
  echo "The token did not change."
  exit 0
fi
if [ -z "${GH_TOKEN:-}" ]; then
  echo "::error::The WeTrakr token changed but the GH_PAT secret is missing, so it can't be saved"
  exit 1
fi
gh secret set WETRAKR_TOKEN --repo "$GITHUB_REPOSITORY" < "$token_file"
sha256sum "$token_file" > "$checksum_file"
echo "Saved the WeTrakr token to the WETRAKR_TOKEN secret."
