#!/usr/bin/env bash
# fork-gh.sh — run `gh` against THIS fork, never upstream.
#
# Why this exists: this checkout has an `upstream` remote (router-for-me) for
# rebasing, and `gh` picks the repo by remote NAME, preferring a remote called
# "upstream" over "origin". So a bare `gh ...` targets the upstream project,
# not the fork. That silently aimed release dispatches at router-for-me and
# got a confusing HTTP 403, and it will aim any other bare call at upstream.
# Neither `GH_REPO` nor `GH_HOST` overrides this (gh resolves from the remotes
# and wins with the named "upstream"), so the repo must be passed explicitly.
#
# This script always passes --repo, so a call can never land on upstream:
#
#   scripts/fork-gh.sh release view v7.3.925
#   scripts/fork-gh.sh run list --workflow release.yaml --limit 5
#
# To scope gh for interactive use, define a shell function/alias that appends
# the flag (see docs/fork-sync.md), or just call this script.
set -euo pipefail

FORK_REPO="${FORK_REPO:-iamriajul/CLIProxyAPI}"

if [[ $# -eq 0 ]]; then
  echo "usage: scripts/fork-gh.sh <gh subcommand> [args...]" >&2
  exit 2
fi

exec gh --repo "$FORK_REPO" "$@"
