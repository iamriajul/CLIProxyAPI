# Quota Exhaustion RETRY_AFTER Specification

When quota exhaustion or model cooldown occurs and the earliest account recovery time is known, CLIProxyAPI appends this suffix to the error `message` string:

```text
RETRY_AFTER:2026-10-13T17:30:00Z
```

## Contract

- **Pattern**: `RETRY_AFTER:<UTC_DATETIME>` (no space after colon).
- **Timezone**: Always UTC ending with `Z` (`YYYY-MM-DDTHH:MM:SSZ`).
- **Rounding**: Fractional seconds round up to the next full second.
- **Location**: Always at the very end of the error `message` string.
- **Trigger**: Appended only when an account recovery timestamp is known and pending in the future, to error bodies the proxy builds itself. A direct response (an upstream error body relayed as-is) is never rewritten, and neither is a raw upstream error that has not gone through the proxy's routing; a request that gives up after routing still reports the upstream's own reset for the credential it used. This follows upstream v8.0.67, which pins both (`TestClaudeCooldownDirectResponseIsUnchanged`, `TestXAIExecutorExecuteReturnsTerminalStreamFailures`). Until then the fork also rewrote direct responses and any error carrying an upstream retry hint, which was harmless while nothing upstream depended on those bodies staying verbatim.
- **Pool scope**: When a request gives up because every credential that serves the model is exhausted or cooling down, the timestamp is the earliest recovery across all of them, including credentials the request already attempted. It replaces any `RETRY_AFTER` already in an upstream message, and the `Retry-After` header carries the same instant. Credentials that will not recover on their own (disabled, unauthorized) never set it. A single-credential pool reports that credential's own reset. Home mode reports the instant its control plane computes.

## Example

```json
{
  "error": {
    "message": "All credentials for model gpt-4o are cooling down via provider openai RETRY_AFTER:2026-10-13T17:30:00Z",
    "code": "model_cooldown"
  }
}
```
