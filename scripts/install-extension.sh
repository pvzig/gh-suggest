#!/usr/bin/env bash
# Idempotent install of the gh-suggest CLI extension.
#
# Cursor Cloud Agents (and some CI images) export GITHUB_TOKEN for repo checkout.
# gh prefers that token over ~/.config/gh. When the token is missing repo scope or
# is invalid, `gh extension install` against the public pvzig/gh-suggest repo fails
# with HTTP 401 even though anonymous access works. Unset agent tokens for install.

set -euo pipefail

readonly EXTENSION_REPO="pvzig/gh-suggest"
readonly PIN="${GH_SUGGEST_PIN:-v0.1.0}"

gh_unscoped() {
  env -u GITHUB_TOKEN -u GH_TOKEN gh "$@"
}

if gh_unscoped extension list 2>/dev/null | grep -q "${EXTENSION_REPO}"; then
  exit 0
fi

gh_unscoped extension install "${EXTENSION_REPO}" --pin "${PIN}"
