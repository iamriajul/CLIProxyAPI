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

## opencode-provider

**OpenCode Zen Go gateway provider (API-key auth, tri-route executor)**

Zen Go keys are subscription API keys pasted from the Zen console (no
OAuth): `POST /v0/management/opencode/import` validates against the live
gateway models endpoint and saves a type-opencode auth file. The executor
routes per model per the reference catalog: Claude-protocol lanes through the
Claude executor, Responses-native lanes through /v1/responses, everything
else through OpenAI chat completions; gateway lanes that reject tool_choice
have it stripped. Models ride live models.dev `opencode-go` sections
refreshed every 3 hours (was: a static oh-my-pi snapshot merged at build
time — correct at integration, but already drifting within days); the
embedded catalog plus builtins stay as the offline fallback.

```bash
grep -q "opencode/import" internal/api/server_management.go
go test ./internal/auth/opencode/...
go test ./internal/runtime/executor/ -run 'TestOpencode'
go test ./internal/registry/ -run TestGetOpencodeModelsCoverGatewayLanes
```

## zai-oauth

**Z.AI GLM Coding Plan OAuth (browser code + provisioned durable key)**

Z.AI allowlists only the zcode:// native-scheme callback, so login is
authorize-in-browser plus paste-back through the generic oauth-callback
endpoint (same UX as the xAI manual flow). The token exchange yields a
durable id.secret key stored as the credential. GLM lanes ride Anthropic by
default with glm-5.3-flash on the OpenAI coding lane; lane windows and
capabilities ride live models.dev `zai-coding-plan` sections refreshed every
3 hours (was: a static 16-model oh-my-pi snapshot mixing pay-per-token
families — the plan itself has 7 lanes); the key is sent
verbatim on every path (Z.AI rejects Bearer, which the shared Claude
delegation would otherwise stamp — so the Anthropic lanes run natively, not
delegated). Dashboard keys paste via POST /v0/management/zai/import
(validated with a minimal coding-lane completion, same key shape, identical
routing and quota). No refresh: minted keys are durable.

```bash
grep -q "zai-auth-url" internal/api/server_management.go
grep -q "zai/import" internal/api/server_management.go
go test ./internal/auth/zai/...
go test ./internal/runtime/executor/ -run 'TestZai'
go test ./internal/registry/ -run TestGetZaiModelsCoverCodingPlan
```

## zai-key-import

**Z.AI dashboard keys import without the browser flow**

Same plan, second credential type: `POST /v0/management/zai/import`
validates the pasted key with a minimal coding-lane completion and saves a
type-zai file carrying the identical key shape, so routing, executors, and
quota behave exactly like OAuth-provisioned files. The OAuth page card links
the dashboard key page directly.

```bash
grep -q "zai/import" internal/api/server_management.go
go test ./internal/auth/zai/ -run TestValidateKey
```

## modelsdev-catalog

**OpenCode Go + Z.AI model data rides live models.dev sections**

`internal/registry/modelsdev*.go` fetches `https://models.dev/api.json`
every 3 hours, filters exactly `opencode-go` and `zai-coding-plan`
(never Zen `opencode` or pay-per-token `zai`), and overlays the live
sections over the embedded catalog plus builtins. New upstream lanes
appear with no repo work; `go run ./cmd/fetch_modelsdev_models`
refreshes the offline snapshots. Runs under Home mode, skipped by
`--local-model`.

```bash
go test ./internal/registry/ -run 'TestConvertModelsDevCatalog|TestModelsDevLive|TestTryRefreshModelsDev|TestGetOpencodeModelsCoverGatewayLanes|TestGetZaiModelsCoverCodingPlan|TestModelsDevLiveBeatsFallback'
go test ./cmd/fetch_modelsdev_models/
go test ./cmd/server/ -run 'TestModelCatalogUpdaterPlan'
```

Freshness is operator-visible: `GET /v0/management/modelsdev/status`
reports per-provider live/fallback source, counts, fetch time, and the
latest error; `POST /v0/management/modelsdev/refresh` triggers one fetch
outside the ticker. The TUI dashboard renders both rows in a Model Catalog
card (best-effort: old servers without the endpoint render nothing).

```bash
go test ./internal/api/handlers/management/ -run TestGetModelsDevStatus_Shape
go test ./internal/tui/ -run 'TestCatalogAge|TestRenderCatalogSectionStates'
go test ./internal/registry/ -run TestGetModelsDevStatus
```
