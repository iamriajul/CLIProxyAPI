# Quota Exhaustion & Resume Timing Specification for Harnesses

## Overview

When upstream LLM provider accounts exhaust their usage quotas or rate limits, CLIProxyAPI schedules credential cooldowns and failover. When all accounts in a pool for a requested model are exhausted or cooling down, CLIProxyAPI returns an error response.

To allow meta-harnesses (such as Paseo, autonomous agent supervisors, and cron schedulers) to know **when they can resume work**, CLIProxyAPI embeds a standardized UTC recovery timestamp directly into the error `message` string:

```text
RETRY_AFTER:2026-10-13T17:30:00Z
```

This document specifies the pattern contract, error response formats, and parsing guidance for harness developers.

---

## Pattern Specification

### Suffix Contract

```text
RETRY_AFTER:<RFC3339-UTC-TIMESTAMP>
```

- **Prefix**: `RETRY_AFTER:` (uppercase, followed immediately by colon, **no space** after colon).
- **Format**: ISO 8601 / RFC 3339 format in UTC with the explicit `Z` indicator:
  `YYYY-MM-DDTHH:MM:SSZ` (e.g., `2026-10-13T17:30:00Z`).
- **Placement**: Placed at the very end of the error `message` string, separated by a single preceding space.
- **Rounding**: Any sub-second fractional remainder is rounded up (ceiling) to the nearest full second so that the client timer will not fire before the upstream quota window resets.
- **Presence**: Appended **only** when CLIProxyAPI knows the earliest account that is going to become available in the future. If no account has a known recovery window (e.g. permanently disabled credentials), the suffix is omitted.

---

## Response Examples Across Protocols

### 1. OpenAI Chat Completions & Responses API (`/v1/chat/completions`, `/v1/responses`)

**HTTP Status**: `429 Too Many Requests` (or `503 Service Unavailable`)

```json
{
  "error": {
    "code": "model_cooldown",
    "message": "All credentials for model gpt-4o are cooling down via provider openai RETRY_AFTER:2026-10-13T17:30:00Z",
    "model": "gpt-4o",
    "reset_seconds": 120,
    "reset_time": "2m0s"
  }
}
```

Or when returning an upstream rate limit error:

```json
{
  "error": {
    "message": "Rate limit reached for requests RETRY_AFTER:2026-10-13T17:30:00Z",
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded"
  }
}
```

### 2. Anthropic Claude API (`/v1/messages`)

**HTTP Status**: `429 Too Many Requests`

```json
{
  "type": "error",
  "error": {
    "type": "rate_limit_error",
    "message": "All credentials for model claude-3-7-sonnet are cooling down RETRY_AFTER:2026-10-13T17:30:00Z"
  }
}
```

### 3. Google Gemini API (`/v1beta/models/...`)

**HTTP Status**: `429 Too Many Requests`

```json
{
  "error": {
    "code": 429,
    "message": "You have exhausted your capacity on this model. Your quota will reset after 2m. RETRY_AFTER:2026-10-13T17:30:00Z",
    "status": "RESOURCE_EXHAUSTED"
  }
}
```

### 4. Selection / Auth Unavailable (`503 Service Unavailable`)

When credentials exist but all candidates are in cooldown:

```json
{
  "error": {
    "code": "internal_server_error",
    "message": "auth_unavailable: no auth available (providers=codex, model=gpt-5) RETRY_AFTER:2026-10-13T17:30:00Z",
    "type": "server_error"
  }
}
```

### 5. HTTP Response Headers

In addition to the in-message suffix, CLIProxyAPI continues to set the standard HTTP response header for HTTP-aware clients:

```http
Retry-After: 120
```

---

## Harness Implementation Reference

Meta-harnesses (such as Paseo) can catch errors from their LLM client, check for the suffix in the error message string, and hook a wake-up timer.

### Regular Expression

```regex
RETRY_AFTER:(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z)
```

### TypeScript / JavaScript Reference Implementation

```typescript
export interface RetryAfterResult {
  hasRetryAfter: boolean;
  resumeAt: Date | null;
  delayMs: number;
}

const RETRY_AFTER_REGEX = /RETRY_AFTER:(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z)/;

export function parseRetryAfterFromError(error: unknown): RetryAfterResult {
  const errorMessage =
    typeof error === 'string'
      ? error
      : (error as { message?: string })?.message ??
        (error as { error?: { message?: string } })?.error?.message ??
        '';

  const match = errorMessage.match(RETRY_AFTER_REGEX);
  if (!match) {
    return { hasRetryAfter: false, resumeAt: null, delayMs: 0 };
  }

  const resumeAt = new Date(match[1]);
  const delayMs = Math.max(0, resumeAt.getTime() - Date.now());

  return {
    hasRetryAfter: true,
    resumeAt,
    delayMs,
  };
}

// Example usage in an agent harness loop:
try {
  await executeAgentPrompt(prompt);
} catch (err) {
  const { hasRetryAfter, resumeAt, delayMs } = parseRetryAfterFromError(err);
  if (hasRetryAfter && delayMs > 0) {
    console.log(`Quota exhausted. Resuming work at ${resumeAt?.toISOString()} (waiting ${Math.round(delayMs / 1000)}s)...`);
    setTimeout(() => {
      resumeAgentWork();
    }, delayMs);
  } else {
    throw err;
  }
}
```

### Python Reference Implementation

```python
import re
from datetime import datetime, timezone

RETRY_AFTER_PATTERN = re.compile(r"RETRY_AFTER:(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z)")

def parse_retry_after(error_message: str):
    """
    Returns (resume_at_datetime, delay_seconds) or (None, None).
    """
    match = RETRY_AFTER_PATTERN.search(str(error_message))
    if not match:
        return None, None

    iso_str = match.group(1).replace("Z", "+00:00")
    resume_at = datetime.fromisoformat(iso_str)
    now = datetime.now(timezone.utc)
    delay_seconds = max(0.0, (resume_at - now).total_seconds())

    return resume_at, delay_seconds

# Example usage:
# resume_at, wait_secs = parse_retry_after(err)
# if resume_at:
#     schedule_resume_timer(wait_secs)
```
