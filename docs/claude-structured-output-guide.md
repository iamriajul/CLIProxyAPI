# Getting JSON that matches a schema from Claude through the proxy

This guide is for clients that call the proxy's OpenAI-compatible endpoints
(`/v1/chat/completions`, `/v1/responses`) with a Claude model and need the
reply to match a JSON schema. It covers two cases:

- [One request, one JSON answer](#one-request-one-json-answer): send a strict
  `json_schema` response format.
- [An agent loop with tools that ends in a JSON answer](#an-agent-loop-that-ends-in-a-json-answer):
  use the `StructuredOutput` tool pattern that Anthropic's Claude Agent SDK
  uses.

The decision record behind the proxy's behaviour is the
`claude-structured-outputs` section of [fork-decisions.md](fork-decisions.md).

## One request, one JSON answer

Send the schema as a `json_schema` response format and mark it strict.

Chat Completions:

```json
{
  "model": "claude-sonnet-5-5",
  "messages": [{"role": "user", "content": "Is this conversation finished? ..."}],
  "response_format": {
    "type": "json_schema",
    "json_schema": {
      "name": "verdict",
      "strict": true,
      "schema": {
        "type": "object",
        "properties": {
          "complete": {"type": "boolean"},
          "reason": {"type": "string"}
        },
        "required": ["complete", "reason"],
        "additionalProperties": false
      }
    }
  }
}
```

Responses: put the same fields in `text.format`
(`{"type": "json_schema", "name": "verdict", "strict": true, "schema": {...}}`).

### What the proxy sends to Claude

With `strict: true`, on a Claude model that supports structured outputs
(Claude Opus 4.1 and every later model; not Claude 3 or the first Opus 4 and
Sonnet 4 releases), the proxy sends two things:

1. **Claude's native structured output**, `output_config.format`. Claude's
   output is constrained to the schema as it is generated, so the reply
   parses and matches the schema's shape.
2. **A system-prompt instruction** with the full schema. This is upstream's
   mechanism. It is redundant when native output is on, and it is the only
   mechanism when native output is not.

Claude's native output accepts only a subset of JSON Schema. Before sending,
the proxy rewrites the schema into that subset, the way Anthropic's Python
and TypeScript SDKs do:

| Your schema has | Claude receives |
|---|---|
| An object without `additionalProperties: false`, or with any other value | `additionalProperties: false` |
| `oneOf` | `anyOf` |
| `"type": ["string", "null"]` | `anyOf: [{"type": "string"}, {"type": "null"}]` |
| `minimum`, `maximum`, `multipleOf`, `minLength`, `maxLength`, `pattern`, `maxItems`, `minItems` above 1, `uniqueItems`, `default`, and other unsupported keywords | The keyword, moved into the node's `description` as `{keyword: value}` |
| A string `format` other than `date-time`, `time`, `date`, `duration`, `email`, `hostname`, `uri`, `ipv4`, `ipv6`, `uuid` | The format, moved into the description |

A keyword moved into the description is a hint to the model, not a guarantee.
If your schema relies on one (a number range, a string length, a regex),
validate the reply against your original schema in your client.

### When the proxy sends the instruction only

In these cases Claude gets only the system-prompt instruction. The reply
usually matches, but nothing holds it to the schema, so parse it defensively:

- The format is not marked `strict: true`. A non-strict schema asks for best
  effort, and enforcing a loose schema could fail the whole request with a
  400.
- The format is `json_object`, or a `json_schema` with no schema.
- The schema is recursive (a `$ref` cycle, or a `$ref` to `#`), or has a
  `$ref` that does not point into `$defs` or `definitions`. Claude's native
  output rejects recursive schemas.
- The model does not support structured outputs, or is a model a
  Claude-compatible endpoint serves under its own name (GLM, Kimi, ...).
- The route is Antigravity. Its requests are Gemini-shaped, and Claude 5.5
  on Antigravity rejects the forced tool call a native schema turns into
  there (router-for-me/CLIProxyAPI#7000).

### What can still break the shape

Native output holds the shape of a completed reply. Two endings bypass it,
so check the finish reason before you parse:

- **A refusal.** Claude can decline for safety reasons. The reply may then
  not match the schema.
- **The token limit.** A reply cut off at `max_tokens` (`finish_reason:
  "length"`) is incomplete JSON. Raise `max_tokens` and retry.

The first request with a new schema is slower while Claude compiles it.
Claude caches the compiled schema for 24 hours.

## An agent loop that ends in a JSON answer

When the model must call your tools for several turns and then return a
structured answer, follow the Claude Agent SDK, which runs exactly this kind
of loop: it does not use a response format, it uses a tool.
When a caller sets `outputFormat`, the SDK adds a tool named
`StructuredOutput` whose input schema is the caller's schema. The agent calls
it to hand back its answer, the SDK validates the input, and on a mismatch
the agent tries again. You can build the same loop through the proxy.

### Build the loop

1. **Define the tool.** Add a function tool named `StructuredOutput` whose
   parameters are your schema. Set `strict: true` on it. Write the schema
   inside Claude's subset yourself: the proxy rewrites response formats, not
   tool schemas. That means `additionalProperties: false` on every object,
   and no numeric bounds, string lengths, patterns, or recursion.

   ```json
   {
     "type": "function",
     "function": {
       "name": "StructuredOutput",
       "description": "Return your final answer. Call this exactly once, when the task is done.",
       "strict": true,
       "parameters": {
         "type": "object",
         "properties": {"todos": {"type": "array", "items": {"type": "string"}}},
         "required": ["todos"],
         "additionalProperties": false
       }
     }
   }
   ```

2. **Tell the model when to call it.** In the system prompt, say that the
   final answer goes through `StructuredOutput` and nowhere else.

3. **Leave `tool_choice` at `auto`.** Claude Opus 5.5, Claude Sonnet 5.5, and
   Claude Fable 5.1 and later reject a forced tool choice with a 400. On
   Claude 5.5 models the proxy turns `"required"` or a named function into
   `auto`; on Fable it passes the forced choice on and the request fails. Send
   `auto` yourself, and handle a turn that ends without the call. The Agent
   SDK does not force the call either.

4. **Validate the arguments.** Parse the `StructuredOutput` call's arguments
   and validate them against your schema with a JSON Schema validator. The
   Agent SDK validates with draft-07 and treats `format` as an annotation.
   `strict: true` already makes Claude's arguments match the schema's shape,
   so this step catches what the subset cannot express.

5. **On a mismatch, ask again.** Answer the call with a tool result that
   lists the validation errors. The Agent SDK marks that result as an error.
   The model then calls `StructuredOutput` again. On a match, answer the call
   with a short success result and end the loop.

6. **Cap the retries.** Stop after a few failed attempts and report an
   error. The Agent SDK reports `error_max_structured_output_retries`. A loop
   without a cap can spend tokens forever on a schema the task cannot fill.

7. **Handle a turn that ends without the call.** If the model stops
   (`finish_reason: "stop"`) without calling `StructuredOutput`, send a user
   message that reminds it to call `StructuredOutput` with its final answer,
   and count that against the same cap.

8. **Read the answer from the call, not the text.** The validated arguments
   are the answer. Ignore any text the model wrote around the call.

9. **Keep the mechanism out of your UI.** If you show or log tool calls, skip
   `StructuredOutput` and its result, so it does not appear as a step the
   agent chose.

### Tips from the Agent SDK

- **Keep schemas focused.** Deeply nested schemas with many required fields
  fail validation more often. Start simple.
- **Make fields optional when the task might not find them.** A required
  field the agent cannot fill makes it guess or burn retries.
- **Write a clear prompt.** An ambiguous task produces answers that miss the
  schema.
- **Treat a refusal or an API error as a failure,** even after an earlier
  valid call: a later turn can replace the result.
