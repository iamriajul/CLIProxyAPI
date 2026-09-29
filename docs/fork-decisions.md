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
capabilities are discovered from Z.AI's own plan-scoped catalog (see
zai-direct-discovery); the key is sent
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
go test ./internal/registry/ -run 'TestGetZaiModelsCoverCodingPlan|TestZaiLiveModelsCoverCodingPlan'
```

## zai-direct-discovery

**Z.AI plan lanes are discovered from Z.AI itself, not from models.dev**

Z.AI publishes a plan-scoped catalog at `GET /api/v1/models` on the origin of
whatever `base_url` the credential records. The response is the Codex client
model catalog format, so the field names are the Codex field names and the
semantics follow `internal/client/codex/models/models.go`. Z.AI's own client
sends `?client_version`, but the response does not vary with it, so the bare
path is requested rather than guessing a value for a parameter that changes
nothing.

Discovery authenticates with `Authorization: Bearer <key>`, which this endpoint
accepts. That is the opposite of the inference and quota lanes, which send the
plan key verbatim because those endpoints reject the Bearer prefix. The two
are kept independent on purpose: a proxy or gateway that silently re-authenticates
one path will not silently re-authenticate the other, and a valid-credential
fault therefore surfaces as a discovery failure rather than as a lane that
quietly stops working.

The converter is pinned against a response captured verbatim from a live
Coding Plan key (`zaiLiveModelsCapturedPayload`), so every field decision below
is anchored to bytes the provider actually returned rather than to an
abbreviated reading of them.

Discovery is **per credential**, not per provider: the catalog is scoped to
the plan the key is on, so two credentials on one deployment may legitimately
serve different lanes and a single global overlay would be wrong for at least
one of them. Entries are keyed by auth ID plus a digest of the key, so a
re-minted key never inherits the previous key's lanes and the store never holds
a credential. Precedence at registration is: lanes discovered for **this**
credential, then the embedded models.dev `zai-coding-plan` snapshot plus
builtins, then the builtins alone. A credential with no live result — never
discovered, currently failing, or refreshing with a rejected key — reads the
offline catalog, so a discovery fault degrades the lane instead of emptying
it.

**HTTP 200 is not success.** Z.AI answers a bad key with HTTP 200 and an
error body. Two shapes were observed on this endpoint:
`{"code":401,"msg":"token expired or incorrect","success":false}` and
`{"code":1000,"msg":"Authentication Failed","success":false}` — the second is
Z.AI's own code, not an HTTP status, and it is what the endpoint really
returns for a bad key. Error detection therefore inspects the payload, never
the status, and a payload that decodes to no usable models is refused outright:
accepting it would unregister the lane. A missing credential never reaches the
network: there is nothing to ask, and "no lanes" must not be mistaken for an
empty plan. A rejected credential (401, 403, or Z.AI's 1000) backs off for 15
minutes so a re-registration is not a request to be told the same key is bad; a
transport or payload fault backs off for one minute, because backing a
transient fault off as if the key were bad would hide a plan change for a
quarter of an hour. Discovered lanes are reused for an hour.

Discovery runs **after** registration, not before: a credential registering
for the first time has nothing cached, so asking first would always answer
from the offline snapshot. Registering publishes the snapshot immediately and
a changed live set then triggers a re-registration that supersedes it. The
call is synchronous, unlike the Antigravity capability probe — that probe is
best-effort enrichment of a list that is already correct, whereas a stale lane
list is the defect being fixed.

Two decisions in the converter are load-bearing:

`effective_context_window_percent` **is** honored. `context_window` is the
ceiling Z.AI enforces; the percentage is the share of it a client may actually
use. Every captured lane declares 95, so the flagship 1 MiB window is
advertised as ~996k and `glm-5-turbo`'s 204800 as ~194k. Advertising the raw
maximum would let a client fill the context right up to the point the upstream
starts truncating. The raw `max_context_window` is kept in `MaxContextLength`
so a client catalog can still report the full window. An absent or out-of-range
percentage leaves the window untouched rather than guessing a ratio the
provider did not state.

An empty `supported_reasoning_levels` list is **preserved as empty**, never
back-filled with a guessed low/medium/high ladder: publishing a level the
model rejects is a request-time failure, and the captured `glm-5-turbo` entry
is exactly this case — it carries `supports_reasoning_summaries: true` with an
empty ladder, so the provider says the model reasons and publishes no levels
for it. Reasoning capability itself comes from the catalog's two explicit
booleans (`supports_reasoning_summaries`, and `supports_parallel_tool_calls`
as the fallback Z.AI's stated invariant provides), never from the length of
the level list — a model can reason with no ladder, and a non-reasoning model
publishes none either. The `Thinking` struct stays non-nil for a reasoning
lane because that is what keeps `internal/thinking` forwarding reasoning
configuration: `ValidateConfig` guards its level-membership check on
`len(support.Levels) > 0`, and `clampBudget` returns the value unchanged when
no budget range is declared, so an empty ladder is inert in both directions.

The Codex catalog format publishes no per-model output limit, and its
`truncation_policy` is byte-identical across every captured lane
(`{"limit": 10000, "mode": "bytes"}`) — a fixed compaction threshold, not a
completion bound. A discovered lane therefore carries **no** output limit
rather than one invented from the plan's known ceiling. Clients read "unknown"
instead of a number Z.AI never declared. `output_modalities` is likewise absent
from the response, so output modalities fall back to the codec's text-only
default.

models.dev remains the source for the embedded offline snapshot and for
custom-provider capability inheritance; only its runtime live overlay stops
carrying the Z.AI section.

The exact-7 test became two tests. `TestGetZaiModelsCoverCodingPlan` keeps the
offline contract: the snapshot's plan lanes, no pay-per-token lane, and the
builtins still routable with the catalog section wiped. It no longer counts,
because the offline snapshot's count is a property of the checked-in file that
the regen CLI owns.
`TestZaiLiveModelsCoverCodingPlan` pins the live contract against the captured
payload: the exact lane set that plan served, plus the effective context
window, the declared ladder, the preserved empty ladder, and the modalities. A
live lane count is a property of the plan that served the request, not of this
repo, so demanding a fixed number there would be wrong for every plan but one —
what must not drift silently is the *published* data, and that stays pinned to a
real response.

```bash
grep -q 'zaiLiveModelsPath = "/api/v1/models"' sdk/cliproxy/zai_live_models.go
grep -q 'GetZaiModelsForCredential(a.ID' sdk/cliproxy/service_models.go
go test ./internal/registry/ -run 'TestZaiLiveModels|TestGetZaiModelsCoverCodingPlan'
go test ./sdk/cliproxy/ -run 'TestZai'
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

**OpenCode Go model data rides live models.dev sections; Z.AI reads Z.AI's own
catalog instead**

`internal/registry/modelsdev*.go` fetches `https://models.dev/api.json`
every 3 hours, indexes the whole catalog by base URL for custom providers, and
overlays the live `opencode-go` section over the embedded catalog plus builtins.
New upstream gateway lanes appear with no repo work;
`go run ./cmd/fetch_modelsdev_models` refreshes the offline snapshots. Runs
under Home mode, skipped by `--local-model`.

The Z.AI section is no longer a runtime live overlay. That lane discovers its
plan-scoped roster from Z.AI itself (see zai-direct-discovery), so a
third-party section must not be able to overwrite it. The section is still
parsed for two things: `go run ./cmd/fetch_modelsdev_models` refreshes the
embedded Z.AI offline snapshot from it, and the custom-provider index covers
every provider models.dev publishes — Z.AI's coding-plan base URL included — for
an operator who points a custom provider at that endpoint instead of importing
a Z.AI credential. Exactly two providers are ever parsed: a payload carrying
neither is an error, so a shape change cannot silently yield empty sections.

Freshness is operator-visible: `GET /v0/management/modelsdev/status` reports
per-provider live/fallback source, counts, fetch time, and the latest error,
for both catalog sources; `POST /v0/management/modelsdev/refresh` triggers one
models.dev fetch outside the ticker. The TUI dashboard renders both rows in a
Model Catalog card (best-effort: old servers without the endpoint render
nothing).

```bash
go test ./internal/registry/ -run 'TestConvertModelsDevCatalog|TestModelsDevLive|TestTryRefreshModelsDev|TestGetOpencodeModelsCoverGatewayLanes|TestGetModelsDevStatus'
go test ./internal/registry/ -run 'TestGetZaiModelsCoverCodingPlan'
go test ./cmd/fetch_modelsdev_models/
go test ./cmd/server/ -run 'TestModelCatalogUpdaterPlan'
go test ./internal/api/handlers/management/ -run TestGetModelsDevStatus_Shape
go test ./internal/tui/ -run 'TestCatalogAge|TestRenderCatalogSectionStates'
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
output tokens per second of the whole request. It used to exclude TTFT, which
was the right decode rate when TTFT was the first generated token. The value
recorded is the first response byte. On a buffered body that byte arrives as
the response finishes, and dividing by the remainder reported 2.1e6 tok/s for
a 28 tok/s request and 886 for one whose end-to-end rate was 10. CPA Manager
Plus divides by total latency for the same reason. `generation_ms` stays on
the body so a client can still show the tail. Only successful generating
requests are recorded, so
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
The curated opencode section keeps its default ladder, which is correct for
that fixed gateway. The Z.AI section is no longer a runtime source: its lanes
come from Z.AI's own catalog, where the published ladder is whatever Z.AI
declares and an empty declaration stays empty (see zai-direct-discovery).

`models-dev-provider` pins which catalog entry supplies a model, resolved
against that provider's own catalog so metadata also works through a proxy in
front of the endpoint. This is not a rare edge case: of the ~3.9k distinct
model IDs in the models.dev catalog, about 29% (1134) have more than one
publisher, and a popular name such as `glm-5.2` is published by 29 providers
with differing limits. The base URL resolves the answer whenever it identifies
the serving route, but a proxy in front of an endpoint defeats that, which is
what the pin is for. Explicit configuration always wins; the catalog only
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
