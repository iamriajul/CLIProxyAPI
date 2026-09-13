# Fork decisions

Every behaviour this fork changes inside official CLIProxyAPI files, and the
command that proves each one still works.

The fork is a rebase queue: `main` is the upstream release tag we track, plus
one commit per change, with no merge commits. The commits hold the code; this
file holds the why and the proof.

After rebasing onto a new upstream release, run:

```bash
bash scripts/fork-verify.sh
```

That script parses this file directly, so there is no second copy to keep in
sync — edit a decision here and the runner picks it up. CI job
`fork-decisions` runs the same script on PRs and on `main`.

Changing an official file? Add a section here with a command that fails
without your change. A decision with no command is a decision nothing
protects. Changes confined to fork-owned files (no upstream counterpart, e.g.
`.github/workflows/docker-ghcr.yml`) cannot conflict on sync and need no
section.

## muse-code-oauth

**Muse Code subscription OAuth (muse-spark models)**

Device-code login against `auth.meta.com` (client `1031625952748946`),
subscription key mint at `api.meta.ai/muse-code/key`, durable `muse` auth
files, native `muse` executor against `https://api.meta.ai/v1` with the
mandatory `x-api-version` header, `muse-spark-*` model catalog, `muse`
thinking levels, `muse-auth-url` management route, TUI entry, and
`--muse-login` CLI. Upstream issue
`router-for-me/CLIProxyAPI#5777`; drop this commit when upstream ships it.

```bash
go test ./internal/auth/muse/...
go test ./internal/registry/ -run TestGetMuseModelsIncludesSparkFamily
go test ./internal/runtime/executor/ -run 'TestMuseRequestToFormatMatchesWireProtocol|TestMusePrepareRequestAddsVersionAndKey|TestMusePrepareRequestCombinedCredential|TestMuseRefreshReusesMintedKey|TestNormalizeMuseToolsConvertsCustom'
grep -q 'muse-auth-url' internal/api/server_management.go
grep -q 'GetMuseModels' sdk/cliproxy/service_models.go
```

## release-notes-cap
**Fork release notes cap the changelog at 300 entries**

Upstream's release job builds notes from the full tag range. A fork's first
tag has no ancestor tag, so the range becomes the entire repo history and
GitHub rejects the body (HTTP 422, 125000-character limit), which fails the
release and skips every build job. Capping keeps fork releases publishable;
later tags diff small and are unaffected.

```bash
grep -q 'head -n 300 "$changelog_entries_file"' .github/workflows/release.yaml
grep -q 'head -c 100000 "$changelog_notes_file"' .github/workflows/release.yaml
```

## retarget-skip-in-fork

**Upstream's main-to-dev retarget bot stays off in the fork**

The queue targets `main` by design and the fork has no `dev` branch, so the
bot's retarget call fails (`base 'dev' was not found`) and leaves a red check
on every PR. It runs only where `dev` exists.

```bash
grep -q "github.repository == 'router-for-me/CLIProxyAPI'" .github/workflows/auto-retarget-main-pr-to-dev.yml
```
