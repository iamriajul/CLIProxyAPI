package registry

import (
	"errors"
	"strings"
	"testing"
)

// zaiLiveModelsCapturedPayload is a real response from Z.AI's plan-scoped
// discovery endpoint (GET https://api.z.ai/api/v1/models), captured with a real
// GLM Coding Plan key. It is the reference for every field decision the
// converter makes, and in particular for the three distinct reasoning shapes it
// has to keep apart:
//
//   - glm-5.3 declares a ladder (low/high/max)
//   - glm-5.3-flash declares a ladder and image input
//   - glm-5-turbo declares an EMPTY ladder while still reasoning, which means
//     "toggle, no levels" and must never be back-filled with a guess
//
// Two entries deliberately omit fields the first one carries, so the parser is
// also pinned against the sparse entries the endpoint really returns.
const zaiLiveModelsCapturedPayload = `{
  "models": [
    {
      "apply_patch_tool_type": "freeform",
      "base_instructions": "",
      "context_window": 1048576,
      "default_reasoning_level": "max",
      "default_reasoning_summary": "none",
      "description": "Z.ai's latest flagship model",
      "display_name": "glm-5.3",
      "effective_context_window_percent": 95,
      "experimental_supported_tools": [],
      "input_modalities": ["text"],
      "max_context_window": 1048576,
      "priority": 0,
      "shell_type": "shell_command",
      "slug": "glm-5.3",
      "support_verbosity": false,
      "supported_in_api": true,
      "supported_reasoning_levels": [
        {"description": "Light reasoning", "effort": "low"},
        {"description": "Enhanced reasoning", "effort": "high"},
        {"description": "Deep reasoning", "effort": "max"}
      ],
      "supports_parallel_tool_calls": true,
      "supports_reasoning_summaries": true,
      "truncation_policy": {"limit": 10000, "mode": "bytes"},
      "visibility": "list"
    },
    {
      "slug": "glm-5.3-flash",
      "display_name": "glm-5.3-flash",
      "description": "Fast multimodal coding model",
      "context_window": 1048576,
      "max_context_window": 1048576,
      "input_modalities": ["text", "image"],
      "output_modalities": ["text"],
      "supported_reasoning_levels": [{"effort": "low"}, {"effort": "high"}, {"effort": "max"}],
      "supports_parallel_tool_calls": true,
      "priority": 1,
      "visibility": "list"
    },
    {
      "slug": "glm-5-turbo",
      "display_name": "glm-5-turbo",
      "description": "Agent-optimized model",
      "context_window": 204800,
      "max_context_window": 204800,
      "input_modalities": ["text"],
      "output_modalities": ["text"],
      "supported_reasoning_levels": [],
      "supports_parallel_tool_calls": true,
      "priority": 2,
      "visibility": "list"
    },
    {
      "slug": "glm-4.7-plain",
      "display_name": "glm-4.7-plain",
      "context_window": 200000,
      "input_modalities": ["text"],
      "output_modalities": ["text"],
      "visibility": "list"
    }
  ]
}`

func zaiLiveModelByID(t *testing.T, models []*ModelInfo, id string) *ModelInfo {
	t.Helper()
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	t.Fatalf("discovered models carry no lane %q (got %v)", id, idsOf(models))
	return nil
}

// TestZaiLiveModelsCoverCodingPlan pins the live half of the Z.AI lane
// contract against a captured payload. The lane roster is plan-scoped, so the
// contract is not a fixed count but the exact set the plan served, plus the
// capability fields a client acts on. A new Z.AI lane fails here against a
// payload that has not been re-captured, which is the same trip-wire the
// offline snapshot has: discovery is live, but what the fork publishes is
// still pinned to something real.
func TestZaiLiveModelsCoverCodingPlan(t *testing.T) {
	models, err := ConvertZaiLiveModelsCatalog([]byte(zaiLiveModelsCapturedPayload))
	if err != nil {
		t.Fatalf("ConvertZaiLiveModelsCatalog: %v", err)
	}
	// The four lanes the captured plan served. This list is the trip-wire: a
	// new Z.AI lane must show up here as a re-captured payload, not slip in
	// unnoticed.
	if got := idsOf(models); len(got) != 4 || got[0] != "glm-4.7-plain" || got[1] != "glm-5-turbo" || got[2] != "glm-5.3" || got[3] != "glm-5.3-flash" {
		t.Fatalf("discovered lanes = %v, want the captured plan lanes", got)
	}

	flagship := zaiLiveModelByID(t, models, "glm-5.3")
	if flagship.DisplayName != "glm-5.3" {
		t.Fatalf("display name = %q, want the provider's own label", flagship.DisplayName)
	}
	if flagship.Description != "Z.ai's latest flagship model" {
		t.Fatalf("description = %q, want the provider's own text", flagship.Description)
	}
	// effective_context_window_percent is honored: 95% of 1048576 is the
	// ceiling a client may actually use, and advertising the raw max would let
	// it build a prompt the upstream truncates.
	if want := 1048576 * 95 / 100; flagship.ContextLength != want {
		t.Fatalf("context length = %d, want the effective window %d", flagship.ContextLength, want)
	}
	if flagship.MaxContextLength != 1048576 {
		t.Fatalf("max context length = %d, want the raw 1048576", flagship.MaxContextLength)
	}
	if flagship.InputTokenLimit != flagship.ContextLength {
		t.Fatalf("input token limit = %d, want the effective window %d", flagship.InputTokenLimit, flagship.ContextLength)
	}
	if flagship.OutputTokenLimit != 0 || flagship.MaxCompletionTokens != 0 {
		t.Fatalf("output limit = %d/%d, want none: the Codex catalog format declares no output bound",
			flagship.OutputTokenLimit, flagship.MaxCompletionTokens)
	}
	if len(flagship.SupportedInputModalities) != 1 || flagship.SupportedInputModalities[0] != "text" {
		t.Fatalf("input modalities = %v, want [text]", flagship.SupportedInputModalities)
	}
	if len(flagship.SupportedOutputModalities) != 1 || flagship.SupportedOutputModalities[0] != "text" {
		t.Fatalf("output modalities = %v, want [text]", flagship.SupportedOutputModalities)
	}
	if !flagship.ExplicitInputModalities || !flagship.ExplicitThinking {
		t.Fatal("a discovered lane must be explicit on both constraints: the provider catalog, not a default, is the source")
	}
	if want := []string{"tool_choice"}; !equalModelsDevStrings(flagship.SupportedParameters, want) {
		t.Fatalf("supported parameters = %v, want %v", flagship.SupportedParameters, want)
	}

	// A declared ladder is published exactly as declared, in declared order.
	if flagship.Thinking == nil || !equalModelsDevStrings(flagship.Thinking.Levels, []string{"low", "high", "max"}) {
		t.Fatalf("thinking = %#v, want the declared low/high/max ladder", flagship.Thinking)
	}

	flash := zaiLiveModelByID(t, models, "glm-5.3-flash")
	if !equalModelsDevStrings(flash.SupportedInputModalities, []string{"text", "image"}) {
		t.Fatalf("flash input modalities = %v, want [text image]", flash.SupportedInputModalities)
	}
	// A sparse entry with only the tool-call flag still reasons, and its
	// declared ladder is published as-is.
	if flash.Thinking == nil || !equalModelsDevStrings(flash.Thinking.Levels, []string{"low", "high", "max"}) {
		t.Fatalf("flash thinking = %#v, want the declared ladder", flash.Thinking)
	}
	// A lane with neither reasoning flag nor levels does not reason, and
	// advertising otherwise would offer a level it rejects.
	plain := zaiLiveModelByID(t, models, "glm-4.7-plain")
	if plain.Thinking != nil || plain.ExplicitThinking {
		t.Fatalf("plain lane thinking = %#v, want none", plain.Thinking)
	}

	// The toggle-only lane. This is the case the whole separation exists for.
	turbo := zaiLiveModelByID(t, models, "glm-5-turbo")
	if turbo.Thinking == nil {
		t.Fatal("toggle-only lane lost its reasoning: a non-nil Thinking is what keeps reasoning configuration forwarded")
	}
	if len(turbo.Thinking.Levels) != 0 {
		t.Fatalf("toggle-only lane published levels %v; an empty declaration must stay empty", turbo.Thinking.Levels)
	}
	if !turbo.ExplicitThinking {
		t.Fatal("toggle-only lane must still be explicit, or a client catalog would invent a ladder for it")
	}
	if turbo.ContextLength != 204800 {
		t.Fatalf("turbo context length = %d, want 204800", turbo.ContextLength)
	}
}

// TestZaiLiveModelsRejectErrorEnvelope pins the failure modes that would
// otherwise empty the lane. Z.AI answers a rejected key with HTTP 200 and an
// error body, so a status-only check accepts it and discovers zero models.
func TestZaiLiveModelsRejectErrorEnvelope(t *testing.T) {
	for name, payload := range map[string]string{
		"rejected key":                 `{"code":401,"msg":"token expired or incorrect","success":false}`,
		"forbidden":                    `{"code":403,"msg":"plan not entitled","success":false}`,
		"success false without code":   `{"msg":"token expired or incorrect","success":false}`,
		"empty roster":                 `{"models":[]}`,
		"no models key":                `{}`,
		"success true with error code": `{"code":500,"msg":"internal error","success":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			models, err := ConvertZaiLiveModelsCatalog([]byte(payload))
			if err == nil {
				t.Fatalf("payload accepted and produced %d models; an accepted error body empties the lane", len(models))
			}
			if len(models) != 0 {
				t.Fatalf("rejected payload produced %d models", len(models))
			}
		})
	}
}

// TestZaiLiveModelErrorCredential pins which failures are the credential's
// fault, because only those earn the long suppression window: a transient
// transport fault must not be backed off as if the key were bad.
func TestZaiLiveModelErrorCredential(t *testing.T) {
	for _, tc := range []struct {
		payload    string
		message    string
		credential bool
	}{
		{`{"code":401,"msg":"token expired or incorrect","success":false}`, "token expired or incorrect", true},
		{`{"code":403,"msg":"forbidden","success":false}`, "forbidden", true},
		{`{"code":500,"msg":"boom","success":false}`, "boom", false},
		{`{"code":0,"msg":"unknown","success":false}`, "unknown", false},
	} {
		_, err := ConvertZaiLiveModelsCatalog([]byte(tc.payload))
		var modelErr *ZaiLiveModelError
		if !errors.As(err, &modelErr) {
			t.Fatalf("payload %s: error is not a ZaiLiveModelError: %v", tc.payload, err)
		}
		if got := modelErr.Credential(); got != tc.credential {
			t.Fatalf("payload %s: Credential() = %v, want %v", tc.payload, got, tc.credential)
		}
		if !strings.Contains(modelErr.Error(), tc.message) {
			t.Fatalf("error text %q drops the provider's own message %q", modelErr.Error(), tc.message)
		}
	}
	if !errors.Is(ErrZaiLiveModelsNoCredential, ErrZaiLiveModelsNoCredential) {
		t.Fatal("ErrZaiLiveModelsNoCredential is not a stable sentinel")
	}
}

// TestZaiLiveModelsPrecedence pins the lane's fallback order: lanes discovered
// from Z.AI win for the credential that discovered them, and every other
// reader — including the same credential before its first successful discovery
// — gets the embedded snapshot instead of an empty list.
func TestZaiLiveModelsPrecedence(t *testing.T) {
	resetModelsDevLiveForTest()
	resetZaiLiveModelsForTest()
	t.Cleanup(func() {
		resetModelsDevLiveForTest()
		resetZaiLiveModelsForTest()
	})

	offline := GetZaiModels()
	if len(offline) == 0 {
		t.Fatal("offline fallback returned no lanes before any discovery")
	}

	discovered, err := ConvertZaiLiveModelsCatalog([]byte(zaiLiveModelsCapturedPayload))
	if err != nil {
		t.Fatalf("ConvertZaiLiveModelsCatalog: %v", err)
	}
	const key = "ak-1.2"
	if !SetZaiLiveModels(GetZaiLiveModelsCacheKey("auth-1", key), discovered) {
		t.Fatal("first store of a live lane set must report a change")
	}
	if SetZaiLiveModels(GetZaiLiveModelsCacheKey("auth-1", key), discovered) {
		t.Fatal("storing an unchanged lane set must not report a change, or every periodic refresh would re-register")
	}
	// The credential-scoped reader resolves the same key the runtime stores
	// under — auth ID plus key digest — so it must see the discovered lanes.
	withLive := GetZaiModelsForCredential("auth-1", key)
	if got := idsOf(withLive); len(got) != 4 || got[0] != "glm-4.7-plain" {
		t.Fatalf("credential-scoped lanes = %v, want the discovered plan lanes", got)
	}
	// A different credential on the same deployment may hold a different plan.
	if got := idsOf(GetZaiModelsForCredential("auth-2", key)); len(got) != len(offline) {
		t.Fatalf("a second auth id saw %d lanes with the same key, want the %d offline lanes", len(got), len(offline))
	}
	// No credential in hand reads the offline snapshot, never some
	// credential's live set.
	if got := idsOf(GetZaiModels()); len(got) != len(offline) {
		t.Fatalf("GetZaiModels() = %d lanes, want the offline snapshot's %d", len(got), len(offline))
	}
	// A re-minted key for the same auth is a different plan key, so it must not
	// inherit the previous key's lanes.
	if got := idsOf(GetZaiModelsForCredential("auth-1", "ak-1.3")); len(got) != len(offline) {
		t.Fatalf("a re-minted key inherited %d lanes, want the offline %d", len(got), len(offline))
	}
	if _, ok := ZaiLiveModelsFetchedAt(GetZaiLiveModelsCacheKey("auth-1", key)); !ok {
		t.Fatal("a stored lane set must report its fetch time")
	}
	if _, ok := ZaiLiveModelsFetchedAt(GetZaiLiveModelsCacheKey("auth-1", "never-seen")); ok {
		t.Fatal("an undiscovered credential reported a fetch time")
	}
}

// TestZaiLiveModelsStatus pins the freshness row the management surface and
// TUI card render for the Z.AI source, including the fallback counts a cold
// boot must show.
func TestZaiLiveModelsStatus(t *testing.T) {
	resetZaiLiveModelsForTest()
	t.Cleanup(resetZaiLiveModelsForTest)

	status := ZaiLiveModelsStatus()
	if len(status.Providers) != 1 {
		t.Fatalf("providers = %d, want 1 Z.AI row", len(status.Providers))
	}
	row := status.Providers[0]
	if row.ID != "zai-coding-plan" {
		t.Fatalf("row id = %q, want zai-coding-plan", row.ID)
	}
	if row.Source != "fallback" || row.FetchedAt != nil {
		t.Fatalf("cold boot row = %#v, want a fallback row with no fetch time", row)
	}
	if row.Models != len(WithZaiBuiltins(cloneModelInfos(getModels().ZAI))) {
		t.Fatalf("cold boot count = %d, want the offline lane count", row.Models)
	}

	discovered, err := ConvertZaiLiveModelsCatalog([]byte(zaiLiveModelsCapturedPayload))
	if err != nil {
		t.Fatalf("ConvertZaiLiveModelsCatalog: %v", err)
	}
	SetZaiLiveModels(GetZaiLiveModelsCacheKey("auth-1", "ak-1.2"), discovered)
	row = ZaiLiveModelsStatus().Providers[0]
	if row.Source != "live" || row.Models != 4 || row.FetchedAt == nil {
		t.Fatalf("live row = %#v, want a live row with 4 lanes and a fetch time", row)
	}
}

// TestZaiLiveModelsSkipHidden pins the one visibility value this fork honors.
// Anything unrecognized keeps the lane: dropping a real plan lane on a
// vocabulary it has not seen is the worse failure.
func TestZaiLiveModelsSkipHidden(t *testing.T) {
	for visibility, wantHidden := range map[string]bool{
		"hide":   true,
		"hidden": true,
		"list":   false,
		"":       false,
		"other":  false,
	} {
		payload := `{"models":[{"slug":"glm-5.3","display_name":"glm-5.3","context_window":1024,"visibility":"` + visibility + `"}]}`
		models, err := ConvertZaiLiveModelsCatalog([]byte(payload))
		if visibility == "hide" || visibility == "hidden" {
			if err == nil {
				t.Fatalf("visibility %q published %d lanes", visibility, len(models))
			}
			continue
		}
		if err != nil {
			t.Fatalf("visibility %q rejected the lane: %v", visibility, err)
		}
		if wantHidden || len(models) != 1 {
			t.Fatalf("visibility %q produced %d lanes", visibility, len(models))
		}
	}
}
