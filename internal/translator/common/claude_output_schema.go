package common

import (
	"sort"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// claudeStrictStringFormats are the string formats Claude's structured
// outputs enforce. Any other format is passed on as a description hint.
var claudeStrictStringFormats = map[string]bool{
	"date-time": true, "time": true, "date": true, "duration": true, "email": true,
	"hostname": true, "uri": true, "ipv4": true, "ipv6": true, "uuid": true,
}

// ClaudeStrictSchema rewrites a JSON schema into the subset Claude's native
// structured outputs accept, the way Anthropic's Python and TypeScript SDKs do
// before they send one (transform_schema in anthropic-sdk-python). Claude
// rejects with a 400 a schema that uses numeric bounds, string lengths, most
// array bounds, or additionalProperties other than false, so:
//
//   - every object gets additionalProperties: false;
//   - oneOf becomes anyOf, and a type list ["string","null"] becomes an anyOf
//     of one schema per type;
//   - only primitive enum and const values, supported string formats, and
//     minItems 0 or 1 are kept;
//   - every other keyword (minimum, maxLength, pattern, default, ...) moves
//     into the description as "{keyword: value}", so the model can still
//     follow it.
//
// It returns false for a schema Claude cannot hold, each of which Claude
// answers with a 400: a recursive one, a $ref that is not a top-level
// $defs / definitions entry of this schema, more optional properties or
// union-typed nodes than Claude compiles (claudeStrictMaxOptional,
// claudeStrictMaxUnions), or a node with no type, anyOf, oneOf, allOf, enum,
// or const.
func ClaudeStrictSchema(schema ojson.Result) (string, bool) {
	if !schema.IsObject() || claudeSchemaIsRecursive(schema) {
		return "", false
	}
	out, ok := claudeStrictSchemaNode(schema)
	if !ok {
		return "", false
	}
	optional, unions := claudeSchemaComplexity(ojson.Parse(out))
	if optional > claudeStrictMaxOptional || unions > claudeStrictMaxUnions {
		return "", false
	}
	return out, true
}

// Claude refuses to compile a schema with more optional properties, or more
// properties typed as a union (anyOf, or a type list), than these, counted
// across the whole schema: "Schemas contains too many optional parameters
// (25) ... (limit: 24)" and "too many parameters with union types (17 ...)".
const (
	claudeStrictMaxOptional = 24
	claudeStrictMaxUnions   = 16
)

// claudeSchemaComplexity counts a rewritten schema's optional properties and
// anyOf nodes, $defs included.
func claudeSchemaComplexity(node ojson.Result) (optional, unions int) {
	switch {
	case node.IsArray():
		for _, item := range node.Array() {
			o, u := claudeSchemaComplexity(item)
			optional, unions = optional+o, unions+u
		}
		return optional, unions
	case !node.IsObject():
		return 0, 0
	}
	if node.Get("anyOf").Exists() {
		unions++
	}
	if props := node.Get("properties"); props.IsObject() {
		required := map[string]bool{}
		for _, name := range node.Get("required").Array() {
			required[name.String()] = true
		}
		props.ForEach(func(name, _ ojson.Result) bool {
			if !required[name.String()] {
				optional++
			}
			return true
		})
	}
	node.ForEach(func(key, value ojson.Result) bool {
		switch key.String() {
		case "enum", "const", "required", "description", "title":
			return true
		}
		o, u := claudeSchemaComplexity(value)
		optional, unions = optional+o, unions+u
		return true
	})
	return optional, unions
}

func claudeStrictSchemaNode(node ojson.Result) (string, bool) {
	if !node.IsObject() {
		return "", false
	}
	var keys []string
	vals := map[string]ojson.Result{}
	node.ForEach(func(key, value ojson.Result) bool {
		k := key.String()
		if _, seen := vals[k]; !seen {
			keys = append(keys, k)
		}
		vals[k] = value
		return true
	})
	pop := func(k string) (ojson.Result, bool) {
		v, ok := vals[k]
		delete(vals, k)
		return v, ok
	}
	out := newClaudeSchemaFields(keys)
	setRaw := func(k, raw string) { out.set(k, k, raw) }

	for _, defsKey := range []string{"$defs", "definitions"} {
		defs, ok := pop(defsKey)
		if !ok {
			continue
		}
		raw, okDefs := claudeStrictDefs(defs)
		if !okDefs {
			return "", false
		}
		setRaw(defsKey, raw)
	}

	if ref, ok := pop("$ref"); ok {
		setRaw("$ref", ref.Raw)
		return out.String()
	}

	typ, _ := pop("type")
	if typ.IsArray() {
		return claudeStrictTypeList(out, node, typ, vals)
	}
	anyOf, hasAnyOf := pop("anyOf")
	oneOf, hasOneOf := pop("oneOf")
	allOf, hasAllOf := pop("allOf")

	hasLiterals := false
	if enum, ok := vals["enum"]; ok && enum.IsArray() && len(enum.Array()) > 0 && claudeAllPrimitive(enum.Array()) {
		setRaw("enum", enum.Raw)
		delete(vals, "enum")
		hasLiterals = true
	}
	if constVal, ok := vals["const"]; ok && claudeIsPrimitive(constVal) {
		setRaw("const", constVal.Raw)
		delete(vals, "const")
		hasLiterals = true
	}
	typeName := ""
	if !hasLiterals {
		typeName = typ.String()
	}

	switch {
	case hasAnyOf && anyOf.IsArray():
		raw, ok := claudeStrictSchemaList(anyOf)
		if !ok {
			return "", false
		}
		setRaw("anyOf", raw)
	case hasOneOf && oneOf.IsArray():
		raw, ok := claudeStrictSchemaList(oneOf)
		if !ok {
			return "", false
		}
		out.set("anyOf", "oneOf", raw)
	case hasAllOf && allOf.IsArray():
		raw, ok := claudeStrictSchemaList(allOf)
		if !ok {
			return "", false
		}
		setRaw("allOf", raw)
	case !hasLiterals:
		if typ.Type != ojson.String || typeName == "" {
			return "", false
		}
		setRaw("type", typ.Raw)
	}

	for _, k := range []string{"description", "title"} {
		if v, ok := pop(k); ok {
			setRaw(k, v.Raw)
		}
	}

	switch typeName {
	case "object":
		props := ojson.NewDocString("{}")
		if p, ok := pop("properties"); ok {
			if !p.IsObject() {
				return "", false
			}
			failed := false
			p.ForEach(func(name, prop ojson.Result) bool {
				raw, okProp := claudeStrictSchemaNode(prop)
				if !okProp {
					failed = true
					return false
				}
				_ = props.SetRaw(ojson.Escape(name.String()), raw)
				return true
			})
			if failed {
				return "", false
			}
		}
		setRaw("properties", props.String())
		delete(vals, "additionalProperties")
		setRaw("additionalProperties", "false")
		if required, ok := pop("required"); ok {
			if !required.IsArray() {
				return "", false
			}
			for _, name := range required.Array() {
				if name.Type != ojson.String {
					return "", false
				}
			}
			setRaw("required", required.Raw)
		}
	case "string":
		if f, ok := vals["format"]; ok && claudeStrictStringFormats[f.String()] {
			setRaw("format", f.Raw)
			delete(vals, "format")
		}
	case "array":
		if items, ok := vals["items"]; ok && items.IsObject() {
			raw, okItems := claudeStrictSchemaNode(items)
			if !okItems {
				return "", false
			}
			setRaw("items", raw)
			delete(vals, "items")
		}
		if minItems, ok := vals["minItems"]; ok && minItems.Type == ojson.Number && (minItems.Raw == "0" || minItems.Raw == "1") {
			setRaw("minItems", minItems.Raw)
			delete(vals, "minItems")
		}
	case "", "boolean", "integer", "number", "null":
	default:
		return "", false
	}

	// Whatever is left is a keyword Claude does not enforce; the description
	// carries it so the model might still follow it.
	var extra []string
	for _, k := range keys {
		if v, ok := vals[k]; ok {
			extra = append(extra, k+": "+v.Raw)
		}
	}
	if len(extra) > 0 {
		desc := ""
		if raw, ok := out.fields["description"]; ok {
			desc = ojson.Parse(raw).String() + "\n\n"
		}
		descDoc := ojson.NewDocString(`{"d":""}`)
		_ = descDoc.Set("d", desc+"{"+strings.Join(extra, ", ")+"}")
		setRaw("description", descDoc.Get("d").Raw)
	}
	return out.String()
}

// claudeSchemaFields collects a rewritten schema node's keywords and writes
// them in the source node's key order, so a schema already inside Claude's
// subset comes out unchanged. A renamed keyword (oneOf -> anyOf) takes its
// source's place; a new one goes last.
type claudeSchemaFields struct {
	order  map[string]int
	at     map[string]int
	fields map[string]string
	next   int
}

func newClaudeSchemaFields(keys []string) *claudeSchemaFields {
	order := make(map[string]int, len(keys))
	for i, k := range keys {
		order[k] = i
	}
	return &claudeSchemaFields{order: order, at: map[string]int{}, fields: map[string]string{}, next: len(keys)}
}

func (f *claudeSchemaFields) set(key, from, raw string) {
	if _, ok := f.at[key]; !ok {
		if i, okFrom := f.order[from]; okFrom {
			f.at[key] = i
		} else {
			f.at[key] = f.next
			f.next++
		}
	}
	f.fields[key] = raw
}

func (f *claudeSchemaFields) String() (string, bool) {
	keys := make([]string, 0, len(f.fields))
	for k := range f.fields {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return f.at[keys[i]] < f.at[keys[j]] })
	doc := ojson.NewDocString("{}")
	for _, k := range keys {
		if err := doc.SetRaw(ojson.Escape(k), f.fields[k]); err != nil {
			return "", false
		}
	}
	return doc.String(), true
}

// claudeStrictTypeList turns a node whose type is a list into an anyOf of one
// strict schema per type, keeping the description and title on the anyOf.
// out already holds the node's rewritten $defs / definitions.
func claudeStrictTypeList(out *claudeSchemaFields, node, types ojson.Result, vals map[string]ojson.Result) (string, bool) {
	variants := ojson.NewDocString("[]")
	for _, t := range types.Array() {
		if t.Type != ojson.String {
			return "", false
		}
		variant := ojson.NewDocString(node.Raw)
		_ = variant.Set("type", t.String())
		for _, k := range []string{"$defs", "definitions", "description", "title"} {
			_ = variant.Delete(ojson.Escape(k))
		}
		raw, ok := claudeStrictSchemaNode(ojson.Parse(variant.String()))
		if !ok {
			return "", false
		}
		_ = variants.SetRaw("-1", raw)
	}
	out.set("anyOf", "type", variants.String())
	for _, k := range []string{"description", "title"} {
		if v, ok := vals[k]; ok {
			out.set(k, k, v.Raw)
		}
	}
	return out.String()
}

func claudeStrictDefs(defs ojson.Result) (string, bool) {
	if !defs.IsObject() {
		return "", false
	}
	out := ojson.NewDocString("{}")
	ok := true
	defs.ForEach(func(name, def ojson.Result) bool {
		raw, okDef := claudeStrictSchemaNode(def)
		if !okDef {
			ok = false
			return false
		}
		_ = out.SetRaw(ojson.Escape(name.String()), raw)
		return true
	})
	return out.String(), ok
}

func claudeStrictSchemaList(list ojson.Result) (string, bool) {
	out := ojson.NewDocString("[]")
	for _, item := range list.Array() {
		raw, ok := claudeStrictSchemaNode(item)
		if !ok {
			return "", false
		}
		_ = out.SetRaw("-1", raw)
	}
	return out.String(), true
}

func claudeIsPrimitive(v ojson.Result) bool {
	switch v.Type {
	case ojson.Null, ojson.False, ojson.True, ojson.Number, ojson.String:
		return true
	}
	return false
}

func claudeAllPrimitive(values []ojson.Result) bool {
	for _, v := range values {
		if !claudeIsPrimitive(v) {
			return false
		}
	}
	return true
}

// claudeSchemaIsRecursive reports whether schema refers back to itself, or
// refers somewhere Claude cannot resolve. Claude's structured outputs reject
// both, so it reports true for a cycle among the $defs / definitions and for
// any reference that is not "#/$defs/<name>" or "#/definitions/<name>" naming
// an entry this schema defines: "#" (the root), a deeper pointer such as
// "#/$defs/A/properties/b", a missing definition, or an external document.
func claudeSchemaIsRecursive(schema ojson.Result) bool {
	defined := map[string]bool{}
	for _, defsKey := range []string{"$defs", "definitions"} {
		schema.Get(ojson.Escape(defsKey)).ForEach(func(name, _ ojson.Result) bool {
			defined["#/"+defsKey+"/"+name.String()] = true
			return true
		})
	}
	resolvable := func(ref string) bool {
		for _, prefix := range []string{"#/$defs/", "#/definitions/"} {
			if name, ok := strings.CutPrefix(ref, prefix); ok {
				return name != "" && !strings.ContainsAny(name, "/~") && defined[ref]
			}
		}
		return false
	}
	unsupported := false
	claudeWalkSchemaRefs(schema, func(ref string) {
		if !resolvable(ref) {
			unsupported = true
		}
	})
	if unsupported {
		return true
	}
	edges := map[string][]string{}
	for _, defsKey := range []string{"$defs", "definitions"} {
		schema.Get(ojson.Escape(defsKey)).ForEach(func(name, def ojson.Result) bool {
			from := "#/" + defsKey + "/" + name.String()
			claudeWalkSchemaRefs(def, func(ref string) { edges[from] = append(edges[from], ref) })
			return true
		})
	}
	const (
		visiting = iota + 1
		done
	)
	state := map[string]int{}
	var visit func(string) bool
	visit = func(n string) bool {
		switch state[n] {
		case visiting:
			return true
		case done:
			return false
		}
		state[n] = visiting
		for _, next := range edges[n] {
			if visit(next) {
				return true
			}
		}
		state[n] = done
		return false
	}
	for n := range edges {
		if visit(n) {
			return true
		}
	}
	return false
}

func claudeWalkSchemaRefs(node ojson.Result, fn func(string)) {
	switch {
	case node.IsObject():
		node.ForEach(func(key, value ojson.Result) bool {
			if key.String() == "$ref" && value.Type == ojson.String {
				fn(value.String())
				return true
			}
			claudeWalkSchemaRefs(value, fn)
			return true
		})
	case node.IsArray():
		for _, item := range node.Array() {
			claudeWalkSchemaRefs(item, fn)
		}
	}
}
