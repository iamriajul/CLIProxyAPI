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

## muse-builtin-fallback

**Muse models survive a remote catalog without a muse section**

The startup updater replaces the embedded catalog wholesale with the remote
`router-for-me/models` one, which ships no `muse` section — that silently
unregisters every Muse credential (empty `/v1/models`, reported against the
first fork release). The Spark family is upserted over the catalog entries
(same pattern as `WithCodexBuiltins`/`WithXAIBuiltins`), so a missing or
partial section can never drop Muse models.

```bash
grep -q WithMuseBuiltins internal/registry/model_definitions.go
go test ./internal/registry/ -run 'TestGetMuseModelsFallsBackWhenCatalogSectionEmpty|TestGetMuseModelsMergesPartialCatalogSection'
```

## muse-cloak

**Muse requests carry the official-client fingerprint in every harness**

Claude Code, Agent SDK, Codex, Gemini CLI, and OpenAI-style clients all reach
Muse subscriptions through OpenAI/Responses translation, and the upstream
request is cloaked to the Muse client family (`User-Agent: muse-code` plus the
mandatory `x-api-version`) instead of leaking the calling harness or Go's
transport default. Per-credential `cloak_mode` (`auto` default, `always`,
`never`) in the muse auth JSON, global `disable-muse-cloak-mode` kill-switch.

```bash
grep -q DisableMuseCloakMode internal/config/config.go
go test ./internal/runtime/executor/ -run 'TestMuseHarnessMatrix|TestMuseCloakNeverKeepsTransportIdentity|TestMuseCloakAutoPassesNativeClient|TestResolveMuseCloakMode|TestDetectMuseNativeRequest|TestMuseShouldCloakContract|TestApplyMuseCloakHeaders'
```

## muse-quota-probe

**Management api-call resolves the Muse account token for quota probes**

Muse files may store the credential as combined JSON; quota callers need the
account token, not the blob. `resolveTokenForAuth` unwraps it via
`ResolveMuseOAuthToken` for muse providers and otherwise behaves exactly as
before, so the key endpoint doubles as the usage endpoint through the
existing api-call proxy with no new routes.

```bash
grep -q ResolveMuseOAuthToken internal/api/handlers/management/api_tools.go
go test ./internal/api/handlers/management/ -run TestResolveTokenForAuthUnwrapsMuseCombinedCredential
```

## opencode-provider

**OpenCode Zen Go gateway provider (API-key auth, tri-route executor)**

Zen Go keys are subscription API keys pasted from the Zen console (no
OAuth): `POST /v0/management/opencode/import` validates against the live
gateway models endpoint and saves a type-opencode auth file. The executor
routes per model per the reference catalog: Claude-protocol lanes through the
Claude executor, Responses-native lanes through /v1/responses, everything
else through OpenAI chat completions; gateway lanes that reject tool_choice
have it stripped. Models ride a static snapshot (live Zen lane list merged
at build time) upserted as builtins so catalog refreshes cannot drop them.

```bash
grep -q "opencode/import" internal/api/server_management.go
go test ./internal/auth/opencode/...
go test ./internal/runtime/executor/ -run 'TestOpencode|TestMuseHarnessMatrix'
go test ./internal/registry/ -run TestGetOpencodeModelsCoverGatewayLanes
```
