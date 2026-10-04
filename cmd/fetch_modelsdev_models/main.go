// Command fetch_modelsdev_models refreshes the embedded models.dev-derived
// snapshots: the opencode/zai sections of internal/registry/models/models.json
// and the generated bodies of provider_builtins_opencode.go /
// provider_builtins_zai.go. Route maps and hand-written helpers are preserved.
//
// Usage:
//
//	go run ./cmd/fetch_modelsdev_models [flags]
//
// Flags:
//
//	--api-url <url>       models.dev catalog URL (default: https://models.dev/api.json)
//	--models-json <path>  models.json to update (default: internal/registry/models/models.json)
//	--builtins-dir <dir>  registry dir holding provider_builtins_*.go (default: internal/registry)
//	--check               exit 1 with a summary when output differs; write nothing
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

import "sort"

const defaultModelsDevURL = "https://models.dev/api.json"

func main() {
	var apiURL, modelsJSONPath, builtinsDir string
	var check, allowEmpty bool
	flag.StringVar(&apiURL, "api-url", defaultModelsDevURL, "models.dev catalog URL")
	flag.StringVar(&modelsJSONPath, "models-json", filepath.Join("internal", "registry", "models", "models.json"), "models.json to update")
	flag.StringVar(&builtinsDir, "builtins-dir", filepath.Join("internal", "registry"), "registry dir holding provider_builtins_*.go")
	flag.BoolVar(&check, "check", false, "exit 1 when output differs; write nothing. Fetch failures and stale snapshots both exit 1 with distinct error: prefixes (fetch vs stale snapshots)")
	flag.BoolVar(&allowEmpty, "allow-empty", false, "permit writing empty opencode/zai sections (default refuses: an empty section means upstream glitch, and clobbering the offline fallback is unrecoverable by refresh)")
	flag.Parse()

	data, err := fetchCatalog(apiURL)
	if err != nil {
		fatalf("fetch: %v", err)
	}
	sections, err := registry.ConvertModelsDevCatalog(data)
	if err != nil {
		fatalf("convert: %v", err)
	}
	if err := checkSectionsNonEmpty(sections, allowEmpty); err != nil {
		fatalf("%v", err)
	}
	opencode, zai := sections.Opencode, sections.Zai
	stamp := time.Now().UTC().Format(time.RFC3339)

	modelsJSON, err := os.ReadFile(modelsJSONPath)
	if err != nil {
		fatalf("read %s: %v", modelsJSONPath, err)
	}
	updatedJSON, err := rewriteModelsJSON(modelsJSON, opencode, zai)
	if err != nil {
		fatalf("rewrite %s: %v", modelsJSONPath, err)
	}

	opencodePath := filepath.Join(builtinsDir, "provider_builtins_opencode.go")
	zaiPath := filepath.Join(builtinsDir, "provider_builtins_zai.go")
	opencodeSrc, err := os.ReadFile(opencodePath)
	if err != nil {
		fatalf("read %s: %v", opencodePath, err)
	}
	zaiSrc, err := os.ReadFile(zaiPath)
	if err != nil {
		fatalf("read %s: %v", zaiPath, err)
	}
	// Candidates reuse the checked-in fetch stamps so --check is stable:
	// unchanged model data regenerates byte-identical files.
	opencodeStamp := extractFetchStamp(string(opencodeSrc))
	if opencodeStamp == "" {
		opencodeStamp = stamp
	}
	zaiStamp := extractFetchStamp(string(zaiSrc))
	if zaiStamp == "" {
		zaiStamp = stamp
	}
	updatedOpencode, err := rewriteBuiltinFile(string(opencodeSrc), "opencode-go", opencode, opencodeStamp)
	if err != nil {
		fatalf("rewrite %s: %v", opencodePath, err)
	}
	// Route maps derive from the raw catalog (per-model provider.npm), not
	// from the converted sections — ModelInfo deliberately carries no route
	// field; the maps are the registry's only representation of it.
	updatedOpencode, err = rewriteRoutesSection(updatedOpencode, renderOpencodeRoutes(opencodeRoutesFromCatalog(data)), true)
	if err != nil {
		fatalf("rewrite %s routes: %v", opencodePath, err)
	}
	updatedZai, err := rewriteBuiltinFile(string(zaiSrc), "zai-coding-plan", zai, zaiStamp)
	if err != nil {
		fatalf("rewrite %s: %v", zaiPath, err)
	}
	updatedOpencode = gofmtBuiltin(opencodePath, updatedOpencode)
	updatedZai = gofmtBuiltin(zaiPath, updatedZai)
	if check {
		var diffs []string
		if !bytes.Equal(modelsJSON, updatedJSON) {
			diffs = append(diffs, modelsJSONPath)
		}
		if string(opencodeSrc) != updatedOpencode {
			diffs = append(diffs, opencodePath)
		}
		if string(zaiSrc) != updatedZai {
			diffs = append(diffs, zaiPath)
		}
		if len(diffs) > 0 {
			fatalf("stale snapshots: %s (run without --check to refresh)", strings.Join(diffs, ", "))
		}
		fmt.Println("snapshots are current")
		return
	}

	writeFileAtomic(modelsJSONPath, updatedJSON)
	writeFileAtomic(opencodePath, []byte(updatedOpencode))
	writeFileAtomic(zaiPath, []byte(updatedZai))
	fmt.Printf("refreshed %s (%d opencode, %d zai models)\n", modelsJSONPath, len(opencode), len(zai))
}

// opencodeRoutesFromCatalog derives the model -> lane map for the whole
// opencode-go roster straight from the raw catalog plus the pinned lanes.
func opencodeRoutesFromCatalog(data []byte) map[string]opencodeLane {
	npm := extractOpencodeNPM(data)
	lanes := make(map[string]opencodeLane, len(npm)+len(opencodePinnedLanes()))
	// Absent ids (no npm hint) are plain chat lanes by default; enumerate only
	// models with a hint or a pin so the generated maps stay minimal.
	for id, hint := range npm {
		if lane := opencodeLaneFor(id, hint); lane != opencodeChatLane {
			lanes[id] = lane
		}
	}
	// Pins apply even when models.dev omits the id or changes its npm hint, so
	// regeneration cannot silently drop a gateway-verified lane.
	for id, lane := range opencodePinnedLanes() {
		if lane != opencodeChatLane {
			lanes[id] = lane
		}
	}
	return lanes
}

// checkSectionsNonEmpty refuses to publish empty provider sections: an empty
// section means the upstream payload dropped a key or a lane family, and the
// CLI (unlike the runtime overlay, which keeps last-good data) would
// permanently clobber the checked-in offline fallback.
func checkSectionsNonEmpty(sections registry.ModelsDevSections, allowEmpty bool) error {
	if allowEmpty {
		return nil
	}
	if !sections.HasOpencode || len(sections.Opencode) == 0 {
		return fmt.Errorf("refusing to clobber: opencode-go section missing or empty (pass --allow-empty to override)")
	}
	if !sections.HasZai || len(sections.Zai) == 0 {
		return fmt.Errorf("refusing to clobber: zai-coding-plan section missing or empty (pass --allow-empty to override)")
	}
	return nil
}

// writeFileAtomic writes via temp file plus rename so a crash or ENOSPC
// between the three snapshot writes can never leave a split tree behind:
// readers always see the old or the new file, never a truncation.
func writeFileAtomic(path string, data []byte) {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		fatalf("write %s: %v", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		fatalf("write %s: %v", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		fatalf("write %s: %v", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		fatalf("write %s: %v", path, err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

// gofmtBuiltin formats rewritten Go source so checked-in files (always
// gofmt'd) compare stable under --check. A format failure is fatal: writing
// unformatted output would break the repo's gofmt gate.
func gofmtBuiltin(path, src string) string {
	formatted, err := format.Source([]byte(src))
	if err != nil {
		fatalf("format %s: %v", path, err)
	}
	return string(formatted)
}

func fetchCatalog(apiURL string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			fmt.Fprintf(os.Stderr, "warning: close response body: %v\n", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}
	// Same cap as the runtime updater (registry.MaxModelsDevSize); the +1
	// detects overflow so a truncated payload fails here instead of
	// writing snapshots the updater would reject.
	data, err := io.ReadAll(io.LimitReader(resp.Body, registry.MaxModelsDevSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > registry.MaxModelsDevSize {
		return nil, fmt.Errorf("catalog exceeds %d bytes, rejecting", registry.MaxModelsDevSize)
	}
	return data, nil
}

// rewriteModelsJSON replaces only the opencode and zai top-level keys,
// preserving every other key's order and bytes exactly as in the input.
func rewriteModelsJSON(raw []byte, opencode, zai []*registry.ModelInfo) ([]byte, error) {
	keys, doc, err := decodeTopLevelOrdered(raw)
	if err != nil {
		return nil, fmt.Errorf("decode models.json: %w", err)
	}
	ocJSON, err := json.MarshalIndent(opencode, "  ", "  ")
	if err != nil {
		return nil, err
	}
	zaiJSON, err := json.MarshalIndent(zai, "  ", "  ")
	if err != nil {
		return nil, err
	}
	replaced := map[string]json.RawMessage{"opencode": ocJSON, "zai": zaiJSON}
	for key := range replaced {
		if _, exists := doc[key]; !exists {
			keys = append(keys, key)
		}
		doc[key] = replaced[key]
	}
	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, key := range keys {
		keyJSON, _ := json.Marshal(key)
		buf.WriteString("  ")
		buf.Write(keyJSON)
		buf.WriteString(": ")
		buf.Write(bytes.TrimSpace(doc[key]))
		if i+1 < len(keys) {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

// decodeTopLevelOrdered decodes a JSON object while recording key order,
// so the writer can keep untouched sections byte-for-byte stable.
func decodeTopLevelOrdered(raw []byte) ([]string, map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, nil, fmt.Errorf("top-level JSON value must be an object")
	}
	var keys []string
	doc := make(map[string]json.RawMessage)
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, nil, fmt.Errorf("non-string object key")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		doc[key] = value
	}
	return keys, doc, nil
}

const (
	generatedBegin = "// modelsdev:generated:begin"
	generatedEnd   = "// modelsdev:generated:end"

	routesBegin = "// modelsdev:routes:begin"
	routesEnd   = "// modelsdev:routes:end"
)

// opencodeLane is one model's gateway routing. route is the non-chat wire the
// lane serves natively ("anthropic" or "responses"; "chat" for a plain chat
// lane). chatUnsupported marks lanes the gateway refuses at /chat/completions
// with ModelProtocolUnsupported, so callers speaking any other wire must be
// translated to route instead of falling back to chat.
type opencodeLane struct {
	route           string
	chatUnsupported bool
}

var opencodeChatLane = opencodeLane{route: "chat"}

// opencodePinnedLanes are gateway-verified lanes that win over the models.dev
// npm hint. Regeneration must reproduce these exactly; dropping one silently
// regresses the lane back to npm routing. Precedence: pins > family prefix >
// npm hint > chat default.
//
// Every pin was verified against the live gateway on 2026-10-04 by sending a
// minimal request to /chat/completions, /responses and /messages for each
// model; a refused wire answers 400 ModelProtocolUnsupported. omp's
// behavior.kdl pins some of these lanes too, but where omp and the live
// gateway disagree the gateway wins (omp pins minimax-m2.7 to chat, which the
// gateway refuses).
func opencodePinnedLanes() map[string]opencodeLane {
	return map[string]opencodeLane{
		// No npm hint, but the lane serves all three wires; pinning Responses
		// lets Responses callers skip translation while chat stays available.
		"deepseek-v4-flash": {route: "responses"},
		// Serves chat and /messages. models.dev dropped its anthropic npm hint,
		// so the pin keeps Claude callers on the native /messages lane.
		"minimax-m2.5": {route: "anthropic"},
		// Served only at /messages: chat and Responses callers are translated.
		"minimax-m2.7": {route: "anthropic", chatUnsupported: true},
	}
}

// opencodeResponsesOnlyPrefixes are id prefixes whose whole family is served
// only at /responses (opencode.ai/docs/go/#endpoints, live-verified for the
// muse-spark lanes on 2026-10-04). The prefix covers revisions the gateway
// ships before models.dev publishes them. Rendered verbatim into the
// generated block.
var opencodeResponsesOnlyPrefixes = []string{"muse-spark-"}

// extractOpencodeNPM returns model id -> models.dev per-model provider.npm
// for the opencode-go section only. This is the same field opencode's own
// client reads (model.provider?.npm ?? provider.npm) to pick the wire SDK:
// "@ai-sdk/openai" means the model is served at /responses and
// "@ai-sdk/anthropic" at bare-base /messages; absence means the
// openai-compatible client, i.e. /chat/completions.
func extractOpencodeNPM(data []byte) map[string]string {
	var catalog struct {
		OpencodeGo struct {
			Models map[string]struct {
				Provider struct {
					NPM string `json:"npm"`
				} `json:"provider"`
			} `json:"models"`
		} `json:"opencode-go"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		fatalf("decode npm hints: %v", err)
	}
	out := make(map[string]string, len(catalog.OpencodeGo.Models))
	for id, model := range catalog.OpencodeGo.Models {
		if npm := strings.TrimSpace(model.Provider.NPM); npm != "" {
			out[id] = npm
		}
	}
	return out
}

// opencodeLaneFor maps one model to its gateway lane. Precedence:
// gateway-verified pin, then a responses-only family prefix, then the
// models.dev npm hint (what opencode's own client routes on), then the chat
// default, which is the wire the gateway serves most broadly and the safe
// fallback for unknown future npm values.
//
// "@ai-sdk/openai" lanes are the gateway's OpenAI-upstream models, which it
// serves only at /responses (every such lane refused chat on 2026-10-04).
// "@ai-sdk/anthropic" lanes serve /messages and, except where pinned, chat.
func opencodeLaneFor(id, npm string) opencodeLane {
	if lane, pinned := opencodePinnedLanes()[id]; pinned {
		return lane
	}
	for _, prefix := range opencodeResponsesOnlyPrefixes {
		if strings.HasPrefix(id, prefix) {
			return opencodeLane{route: "responses", chatUnsupported: true}
		}
	}
	switch npm {
	case "@ai-sdk/openai":
		return opencodeLane{route: "responses", chatUnsupported: true}
	case "@ai-sdk/anthropic":
		return opencodeLane{route: "anthropic"}
	default:
		return opencodeChatLane
	}
}

// renderOpencodeRoutes renders the generated route maps plus the family
// prefix list. Sorted ids keep regen output deterministic; plain chat lanes
// stay out entirely (chat is the default in OpencodeUpstreamRoute).
func renderOpencodeRoutes(lanes map[string]opencodeLane) string {
	var anthropic, responses, chatUnsupported []string
	for id, lane := range lanes {
		switch lane.route {
		case "anthropic":
			anthropic = append(anthropic, id)
		case "responses":
			responses = append(responses, id)
		}
		if lane.chatUnsupported {
			chatUnsupported = append(chatUnsupported, id)
		}
	}
	sort.Strings(anthropic)
	sort.Strings(responses)
	sort.Strings(chatUnsupported)
	render := func(name string, ids []string) string {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "var %s = map[string]bool{\n", name)
		for _, id := range ids {
			fmt.Fprintf(&buf, "\t%q: true,\n", id)
		}
		buf.WriteString("}")
		return buf.String()
	}
	prefixes := append([]string(nil), opencodeResponsesOnlyPrefixes...)
	sort.Strings(prefixes)
	var buf bytes.Buffer
	buf.WriteString("// Generated from models.dev per-model provider.npm, overridden by the\n")
	buf.WriteString("// gateway-verified pins in cmd/fetch_modelsdev_models (opencodePinnedLanes).\n")
	buf.WriteString("// Regenerate with: go run ./cmd/fetch_modelsdev_models\n")
	buf.WriteString("//\n")
	buf.WriteString("// opencodeAnthropicRouteModels serve /messages and opencodeResponsesRouteModels\n")
	buf.WriteString("// serve /responses; callers already speaking that wire ride it untranslated.\n")
	buf.WriteString(render("opencodeAnthropicRouteModels", anthropic))
	buf.WriteString("\n\n")
	buf.WriteString(render("opencodeResponsesRouteModels", responses))
	buf.WriteString("\n\n")
	buf.WriteString("// opencodeChatUnsupportedModels are lanes the gateway refuses at\n")
	buf.WriteString("// /chat/completions (ModelProtocolUnsupported); every caller is translated to\n")
	buf.WriteString("// the lane's route instead.\n")
	buf.WriteString(render("opencodeChatUnsupportedModels", chatUnsupported))
	buf.WriteString("\n\n")
	buf.WriteString("// opencodeResponsesOnlyPrefixes lists id prefixes whose whole family is\n")
	buf.WriteString("// served only at /responses. Exact entries stay in the maps above; the prefix\n")
	buf.WriteString("// covers ids the gateway ships before models.dev publishes them.\n")
	buf.WriteString("var opencodeResponsesOnlyPrefixes = []string{\n")
	for _, prefix := range prefixes {
		fmt.Fprintf(&buf, "\t%q,\n", prefix)
	}
	buf.WriteString("}")
	return buf.String()
}

// rewriteRoutesSection replaces the body between the routes markers. When
// required is false a missing marker pair is not an error (the zai builtins
// file carries no route maps).
func rewriteRoutesSection(src, body string, required bool) (string, error) {
	begin := strings.Index(src, routesBegin)
	end := strings.Index(src, routesEnd)
	if begin < 0 || end < 0 || end < begin {
		if required {
			return "", fmt.Errorf("missing %s/%s markers", routesBegin, routesEnd)
		}
		return src, nil
	}
	var buf bytes.Buffer
	buf.WriteString(src[:begin+len(routesBegin)])
	buf.WriteString("\n")
	buf.WriteString(body)
	buf.WriteString("\n")
	buf.WriteString(src[end:])
	return buf.String(), nil
}

// rewriteBuiltinFile replaces the generated body between the marker comments
// and refreshes the header stamp. Everything outside the markers is preserved.
func rewriteBuiltinFile(src, providerID string, models []*registry.ModelInfo, stamp string) (string, error) {
	begin := strings.Index(src, generatedBegin)
	end := strings.Index(src, generatedEnd)
	if begin < 0 || end < 0 || end < begin {
		return "", fmt.Errorf("missing %s/%s markers", generatedBegin, generatedEnd)
	}
	var buf bytes.Buffer
	buf.WriteString(src[:begin+len(generatedBegin)])
	buf.WriteString("\n")
	buf.WriteString(renderBuiltinModels(models))
	buf.WriteString("\n\t")
	buf.WriteString(src[end:])
	out, err := replaceHeaderStamp(buf.String(), providerID, stamp)
	if err != nil {
		return "", err
	}
	return out, nil
}

func replaceHeaderStamp(src, providerID, stamp string) (string, error) {
	lines := strings.Split(src, "\n")
	var generated, doNotEdit bool
	for i, line := range lines {
		if strings.HasPrefix(line, "// Code generated from ") {
			lines[i] = "// Code generated from models.dev (" + providerID + " provider, fetched " + stamp + ")."
			generated = true
		}
		if strings.HasPrefix(line, "// DO NOT EDIT BY HAND") {
			lines[i] = "// DO NOT EDIT BY HAND — regenerate with: go run ./cmd/fetch_modelsdev_models"
			doNotEdit = true
		}
	}
	if !generated || !doNotEdit {
		return "", fmt.Errorf("generated header lines missing (want // Code generated from ... and // DO NOT EDIT BY HAND)")
	}
	return strings.Join(lines, "\n"), nil
}

// extractFetchStamp returns the RFC3339 stamp embedded in a generated header
// line ("... fetched <stamp>)."), or "" when the file predates stamp headers.
func extractFetchStamp(src string) string {
	const prefix = "fetched "
	idx := strings.Index(src, prefix)
	if idx < 0 {
		return ""
	}
	rest := src[idx+len(prefix):]
	end := strings.Index(rest, ").")
	if end <= 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func renderBuiltinModels(models []*registry.ModelInfo) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\tmodels := make([]*ModelInfo, 0, %d)\n", len(models))
	for _, m := range models {
		buf.WriteString("\tmodels = append(models, &ModelInfo{\n")
		fmt.Fprintf(&buf, "\t\tID: %q,\n", m.ID)
		buf.WriteString("\t\tObject: \"model\",\n")
		if m.Created > 0 {
			fmt.Fprintf(&buf, "\t\tCreated: %d,\n", m.Created)
		}
		fmt.Fprintf(&buf, "\t\tOwnedBy: %q,\n", m.OwnedBy)
		fmt.Fprintf(&buf, "\t\tType: %q,\n", m.Type)
		fmt.Fprintf(&buf, "\t\tDisplayName: %q,\n", m.DisplayName)
		if m.Description != "" {
			fmt.Fprintf(&buf, "\t\tDescription: %q,\n", m.Description)
		}
		if m.ContextLength > 0 {
			fmt.Fprintf(&buf, "\t\tContextLength: %d,\n", m.ContextLength)
		}
		if m.MaxCompletionTokens > 0 {
			fmt.Fprintf(&buf, "\t\tMaxCompletionTokens: %d,\n", m.MaxCompletionTokens)
		}
		if m.InputTokenLimit > 0 {
			fmt.Fprintf(&buf, "\t\tInputTokenLimit: %d,\n", m.InputTokenLimit)
		}
		if m.OutputTokenLimit > 0 {
			fmt.Fprintf(&buf, "\t\tOutputTokenLimit: %d,\n", m.OutputTokenLimit)
		}
		if m.Thinking != nil && len(m.Thinking.Levels) > 0 {
			buf.WriteString("\t\tThinking: &ThinkingSupport{Levels: []string{")
			for i, level := range m.Thinking.Levels {
				if i > 0 {
					buf.WriteString(", ")
				}
				fmt.Fprintf(&buf, "%q", level)
			}
			buf.WriteString("}},\n")
		}
		if len(m.SupportedInputModalities) > 0 {
			buf.WriteString("\t\tSupportedInputModalities: []string{")
			for i, mod := range m.SupportedInputModalities {
				if i > 0 {
					buf.WriteString(", ")
				}
				fmt.Fprintf(&buf, "%q", mod)
			}
			buf.WriteString("},\n")
		}
		if len(m.SupportedOutputModalities) > 0 {
			buf.WriteString("\t\tSupportedOutputModalities: []string{")
			for i, mod := range m.SupportedOutputModalities {
				if i > 0 {
					buf.WriteString(", ")
				}
				fmt.Fprintf(&buf, "%q", mod)
			}
			buf.WriteString("},\n")
		}
		if len(m.SupportedParameters) > 0 {
			buf.WriteString("\t\tSupportedParameters: []string{")
			for i, param := range m.SupportedParameters {
				if i > 0 {
					buf.WriteString(", ")
				}
				fmt.Fprintf(&buf, "%q", param)
			}
			buf.WriteString("},\n")
		}
		// Explicit flags are json:"-" and never survive models.json; the
		// generated builtins carry them so builtin-sourced entries constrain
		// harnesses exactly like live overlay entries.
		if m.ExplicitThinking {
			buf.WriteString("\t\tExplicitThinking: true,\n")
		}
		if m.ExplicitInputModalities {
			buf.WriteString("\t\tExplicitInputModalities: true,\n")
		}
		buf.WriteString("\t})\n")
	}
	return strings.TrimRight(buf.String(), "\n")
}
