#!/usr/bin/env bash
# fork-verify.sh — run every proof command in docs/fork-decisions.md.
#
# Each `## <id>` section must contain at least one ```bash block; every block
# is executed with `bash -euo pipefail`. A decision with no command, or any
# failing command, fails the run and names the decision. CI job
# `fork-decisions` runs this script; run it locally after every rebase onto a
# new upstream tag.
set -euo pipefail

DECISIONS_FILE="${1:-docs/fork-decisions.md}"
if [[ ! -f "$DECISIONS_FILE" ]]; then
  echo "fork-verify: decisions file not found: $DECISIONS_FILE" >&2
  exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

# Split the decisions file into one file per ## section.
awk -v dir="$tmpdir" '
  /^## / {
    id = $2
    gsub(/[^a-zA-Z0-9_.-]/, "", id)
    file = dir "/" id ".md"
    print "" > file
    current = file
    next
  }
  current != "" { print > current }
' "$DECISIONS_FILE"

failures=0
checked=0
shopt -s nullglob
for section in "$tmpdir"/*.md; do
  id="$(basename "$section" .md)"
  # Extract ```bash blocks (strip the fences).
  awk '/^```bash$/ { inblock=1; next } /^```$/ { inblock=0; next } inblock { print }' "$section" > "$section.sh"
  if [[ ! -s "$section.sh" ]]; then
    echo "fork-verify: decision '$id' has no proof command" >&2
    failures=$((failures + 1))
    continue
  fi
  checked=$((checked + 1))
  echo "fork-verify: [$id]"
  # Same strictness as this gate script itself: a proof that trips pipefail
  # or nounset must fail here, not slip through under plain `bash -e`.
  if ! bash -euo pipefail "$section.sh"; then
    echo "fork-verify: decision '$id' FAILED" >&2
    failures=$((failures + 1))
  fi
done

if [[ "$checked" -eq 0 ]]; then
  echo "fork-verify: no decisions found in $DECISIONS_FILE" >&2
  exit 1
fi
if [[ "$failures" -gt 0 ]]; then
  echo "fork-verify: $failures decision(s) failed out of $checked checked" >&2
  exit 1
fi
echo "fork-verify: all $checked decision(s) green"
