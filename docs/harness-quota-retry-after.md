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
- **Trigger**: Appended only when an account recovery timestamp is known and pending in the future.

## Example

```json
{
  "error": {
    "message": "All credentials for model gpt-4o are cooling down via provider openai RETRY_AFTER:2026-10-13T17:30:00Z",
    "code": "model_cooldown"
  }
}
```
