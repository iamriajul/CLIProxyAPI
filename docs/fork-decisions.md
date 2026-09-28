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

## litellm-discovery

**LiteLLM rich-client discovery endpoints (Oh My Pi gateway use)**

`GET /model_group/info`, `/v2/model/info`, `/model/info` and `/v1/model/info`
serve the live registry as LiteLLM-shaped rich entries (`model_group`,
`litellm_params`, `model_info` with `max_input_tokens`,
`max_output_tokens`, `supports_vision/reasoning/function_calling`,
`supported_openai_params`, task `mode`) so OMP `discovery.type: litellm`
gateways learn CPA context windows and capabilities instead of falling back
to bare `/v1/models` ids. Only native `openai` lanes report provider
`openai` (Responses route); every translated lane reports its own provider
and stays on chat completions. Same Bearer auth as `/v1/*`.

```bash
grep -q "model_group/info" internal/api/server_routes.go
grep -q "handleLiteLLMModelInfo" internal/api/server_litellm.go
go test ./internal/api/ -run 'TestLiteLLMDiscovery'
```

## claude-auto-mode-classifier

**Anthropic auto-mode server-side classifier review survives the proxy**

Claude Code's auto mode asks Anthropic's server to review an action as part
of the session's own model requests. That travels as the `safeguards` request
field plus the `dangerous-tool-use` beta, and the verdict returns as
`safeguard_results` on `message_delta`. A gateway that breaks either half
makes the client fall back to its own billable classifier requests — the
notice in <https://code.claude.com/docs/en/auto-mode-classifier-billing> and
the failure behind router-for-me/CLIProxyAPI#6015.

Anthropic upstreams pass both halves through untouched. Two gaps are closed
here. `safeguards` is stripped for non-Anthropic Claude-compatible upstreams
(Kimi, custom gateways), which reject unknown top-level fields and would turn
every auxiliary request into a hard 400; it is kept for Anthropic, where
dropping it would silently disable the review the request asks for.
`X-Claude-Code-Prompt-Id` joins the confirmed-native gateway hint list, which
Anthropic documents as open: capabilities arrive with each Claude Code
release, so the full documented set is pinned rather than the fields observed
so far.

Unaffected by design: a Codex or Gemini harness sends Responses/Gemini wire
format, whose translator rebuilds the body from modeled fields, so there is
no `safeguards` to carry and no server-side review to request.

```bash
grep -q 'stripAnthropicOnlyClassifierFields' internal/runtime/executor/claude_executor_request.go
grep -q 'X-Claude-Code-Prompt-Id' internal/runtime/executor/claude_executor_request.go
go test ./internal/runtime/executor/ -run 'TestClaudeExecutor_AutoModeClassifier'
```

## last-request-stats-endpoint
**Last-request stats endpoint (`GET /v1/last-request-stats`) for GUI polling**
`internal/tps` subscribes to the usage record bus (the same registration shape
as the usage queue plugin) and keeps the most recent generation per model.
`GET /v1/last-request-stats[?model=<id>]` serves that on the v1 group. TPS is
output tokens per second of generation time, so TTFT is excluded when the
upstream reported one. Only successful generating requests are recorded, so
every number shown describes tokens that were actually produced. A record that
arrives out of order (older than what is stored) never regresses the reported
request, which a single slot per model would otherwise do. The read path makes
no upstream calls, so a client UI can poll it on an interval (e.g. 30s) while
visible. Deliberately minimal: the body is one flat object with no nesting, and
there is no in-body empty state — a model that has not run, or an idle session,
is a 404 with the standard error envelope, so a client has exactly two states
(200 with the object, or 404) and never has to interpret a null or a zero-filled
record. An unmatched model never falls back to another model's reading. No
aggregation parameters: the endpoint answers one request, and a rolling average
is a later, additive change driven by an actual UI need. Machine contract:
api/v1-last-request-stats.schema.json, enforced by
TestLastRequestStatsSchemaParity.

The route shipped as `/v1/last-request-tps` in v8.0.901 and was renamed to
`/v1/last-request-stats` in v8.0.902: the body already reported token counts and
timings, not only throughput, so the old name undersold it. The body and every
field are byte-identical across the rename. The old path is deliberately NOT
served — the rename landed within hours of the tag and no client had adopted the
name, so there is no compatibility shim to carry.

```bash
grep -q '"/last-request-stats"' internal/api/server_routes.go
! grep -rq '"/last-request-tps"' internal/api/server_routes.go
go test ./internal/tps/
go test ./internal/api/ -run 'TestHandleLastRequestStats|TestLastRequestStatsSchemaParity'
```

## modelsdev-custom-providers
**Custom (OpenAI-compatible) providers inherit models.dev capabilities**

The fixed `opencode-go` / `zai-coding-plan` sections are keyed by models.dev
provider ID, so a provider configured with a base URL and an API key matched
none of them and inherited nothing: clients were told a guessed
low/medium/high reasoning ladder, no context window, and no supported
parameters. The catalog is now indexed by normalized base URL as well, so a
custom provider inherits what models.dev publishes for that endpoint.
Normalization folds scheme, host case, userinfo, a trailing "/v1" and
trailing slashes — the differences operators write over — while leaving the
path intact, so `/api/paas/v4` and `/api/coding/paas/v4` stay distinct APIs.
Providers with no `api` field are not reachable by base URL and are skipped.

Published and internal reasoning levels are deliberately separate fields. The
internal ladder gates request-time validation and must stay non-nil for a
reasoning model, or the pipeline would strip reasoning configuration before it
reached the upstream. The published ladder is what client catalogs advertise
and carries only levels models.dev declares, so an unknown endpoint or a
reasoning-toggle-only model advertises nothing and the client falls back to
its own reference data instead of being offered a level the model rejects.
The curated opencode/zai sections keep their default ladder, which is correct
for those fixed lanes.

`models-dev-provider` pins which catalog entry supplies a model, resolved
against that provider's own catalog so metadata also works through a proxy in
front of the endpoint. Explicit configuration always wins; the catalog only
fills fields left unset. The request-time snapshot reads the same catalog as
the published metadata, so a level advertised as supported is not rejected by
ValidateConfig when a client sends it.

Duplicate model IDs on one endpoint are the normal case: several vendors
publish both a pay-per-token and a coding-plan route on one host. Agreeing
entries collapse to one copy. Entries that disagree on a client-visible limit
are dropped with a warning naming both routes, because picking either would
advertise a number the upstream may reject.

A refresh re-registers affected custom providers by base URL and, separately,
by pinned provider — a proxied provider's base URL is absent from the catalog,
so base-URL matching alone left it stale until the next restart.

```bash
grep -q 'models-dev-provider' internal/config/config_types.go
grep -q 'GetModelsDevByBaseURL' sdk/cliproxy/service_models.go
grep -q 'modelsdev/providers' internal/api/server_management.go
go test ./internal/registry/ -run 'TestLoadModelsDevCustomProviders|TestGetModelsDevProvider|TestModelsDevBaseKey'
go test ./sdk/cliproxy/ -run 'TestBuildOpenAICompatibilityConfigModels'
go test ./sdk/cliproxy/auth/ -run 'TestCompatCapabilities'
go test ./internal/api/handlers/management/ -run 'TestGetModelsDevProviders'
go test ./internal/config/ -run 'TestOpenAICompatibilityModelsDevProvider'
```
