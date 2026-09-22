package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestRewriteModelsJSONPreservesOtherKeys(t *testing.T) {
	raw := []byte("{\n  \"claude\": [{\"id\": \"c\"}],\n  \"opencode\": [{\"id\": \"stale\"}],\n  \"zai\": []\n}\n")
	oc := []*registry.ModelInfo{{ID: "fresh", Object: "model", OwnedBy: "opencode", Type: "opencode"}}
	zai := []*registry.ModelInfo{{ID: "glm-5.3", Object: "model", OwnedBy: "zai", Type: "zai"}}
	updated, err := rewriteModelsJSON(raw, oc, zai)
	if err != nil {
		t.Fatalf("rewriteModelsJSON: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(updated, &doc); err != nil {
		t.Fatalf("updated JSON invalid: %v\n%s", err, updated)
	}
	if string(doc["claude"]) != "[{\"id\": \"c\"}]" && !strings.Contains(string(doc["claude"]), `"c"`) {
		t.Fatalf("claude key not preserved: %s", doc["claude"])
	}
	if !strings.Contains(string(doc["opencode"]), `"fresh"`) {
		t.Fatalf("opencode not replaced: %s", doc["opencode"])
	}
	if !strings.Contains(string(doc["zai"]), `"glm-5.3"`) {
		t.Fatalf("zai not replaced: %s", doc["zai"])
	}
	if !strings.HasSuffix(string(updated), "}\n") {
		t.Fatal("output must end with } + newline")
	}
}

func TestRewriteModelsJSONKeepsKeyOrderAndBytes(t *testing.T) {
	raw := []byte("{\n  \"zebra\": [\n    {\n      \"id\": \"z\"\n    }\n  ],\n  \"opencode\": [],\n  \"apple\": 1\n}\n")
	oc := []*registry.ModelInfo{{ID: "fresh", Object: "model", OwnedBy: "opencode", Type: "opencode"}}
	zai := []*registry.ModelInfo{{ID: "g", Object: "model", OwnedBy: "zai", Type: "zai"}}
	updated, err := rewriteModelsJSON(raw, oc, zai)
	if err != nil {
		t.Fatalf("rewriteModelsJSON: %v", err)
	}
	out := string(updated)
	// Original key order (zebra, opencode, apple) must survive, not sorted.
	zebra := strings.Index(out, `"zebra"`)
	opencode := strings.Index(out, `"opencode"`)
	apple := strings.Index(out, `"apple"`)
	if zebra < 0 || opencode < 0 || apple < 0 {
		t.Fatalf("keys missing:\n%s", out)
	}
	if !(zebra < opencode && opencode < apple) {
		t.Fatalf("key order changed (want zebra,opencode,apple):\n%s", out)
	}
	// Untouched zebra section must be byte-identical to the input span.
	if !strings.Contains(out, "\"zebra\": [\n    {\n      \"id\": \"z\"\n    }\n  ]") {
		t.Fatalf("zebra section reformatted:\n%s", out)
	}
}

func TestRewriteBuiltinFilePreservesOutsideMarkers(t *testing.T) {
	src := "package registry\n\nvar route = map[string]bool{\"a\": true}\n\nfunc infos() []*ModelInfo {\n// modelsdev:generated:begin\n\told()\n// modelsdev:generated:end\n\treturn models\n}\n"
	models := []*registry.ModelInfo{{
		ID: "glm-5.2", Object: "model", Created: 1781308800, OwnedBy: "opencode", Type: "opencode",
		DisplayName: "GLM-5.2", ContextLength: 1000000, MaxCompletionTokens: 131072,
		Thinking:                 &registry.ThinkingSupport{Levels: []string{"high", "max"}},
		SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"},
		SupportedParameters: []string{"tool_choice"},
	}}
	updated, err := rewriteBuiltinFile(src, "opencode-go", models, "2026-09-22T00:00:00Z")
	if err != nil {
		t.Fatalf("rewriteBuiltinFile: %v", err)
	}
	if !strings.Contains(updated, `var route = map[string]bool{"a": true}`) {
		t.Fatal("hand-written route map not preserved")
	}
	if strings.Contains(updated, "old()") {
		t.Fatal("stale generated body not replaced")
	}
	if !strings.Contains(updated, `ID: "glm-5.2"`) || !strings.Contains(updated, `ContextLength: 1000000`) {
		t.Fatalf("entry not rendered:\n%s", updated)
	}
	if !strings.Contains(updated, `Levels: []string{"high", "max"}`) {
		t.Fatal("thinking levels not rendered")
	}
	if _, err := rewriteBuiltinFile("no markers", "x", models, "s"); err == nil {
		t.Fatal("expected error when markers missing")
	}
}

func TestRewriteBuiltinFileStampStable(t *testing.T) {
	models := []*registry.ModelInfo{{
		ID: "glm-5.2", Object: "model", OwnedBy: "opencode", Type: "opencode", DisplayName: "GLM-5.2",
	}}
	src := "package registry\n\n// Code generated from models.dev (opencode-go provider, fetched 2026-09-22T00:00:00Z).\n// DO NOT EDIT BY HAND — regenerate with: go run ./cmd/fetch_modelsdev_models\n\nfunc infos() []*ModelInfo {\n// modelsdev:generated:begin\n\told()\n// modelsdev:generated:end\n\treturn models\n}\n"
	first, err := rewriteBuiltinFile(src, "opencode-go", models, "2026-09-22T00:00:00Z")
	if err != nil {
		t.Fatalf("rewriteBuiltinFile: %v", err)
	}
	// A second pass reusing the checked-in stamp must be byte-identical,
	// otherwise --check could never report "current".
	second, err := rewriteBuiltinFile(first, "opencode-go", models, extractFetchStamp(first))
	if err != nil {
		t.Fatalf("rewriteBuiltinFile: %v", err)
	}
	if first != second {
		t.Fatalf("rewrite not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if got := extractFetchStamp(first); got != "2026-09-22T00:00:00Z" {
		t.Fatalf("extractFetchStamp = %q", got)
	}
	if got := extractFetchStamp("no stamp here"); got != "" {
		t.Fatalf("extractFetchStamp without header = %q, want empty", got)
	}
}
