package auth

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestModelCooldownError_AppendsRetryAfter(t *testing.T) {
	t.Parallel()

	recoverAt := time.Date(2026, 10, 13, 17, 30, 0, 0, time.UTC)
	// resetIn is 30 seconds
	resetIn := 30 * time.Second

	err := newModelCooldownErrorWithRecoverAt("gpt-5", "codex", resetIn, recoverAt, nil)
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal([]byte(err.Error()), &payload); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error: %v", errUnmarshal)
	}

	rawErr, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object in payload: %v", payload)
	}

	message, _ := rawErr["message"].(string)
	wantSuffix := "RETRY_AFTER:2026-10-13T17:30:00Z"
	if !strings.HasSuffix(message, wantSuffix) {
		t.Fatalf("message = %q, want suffix %q", message, wantSuffix)
	}

	// Verify no space between RETRY_AFTER: and the timestamp
	if strings.Contains(message, "RETRY_AFTER: ") {
		t.Fatalf("message = %q contains space after RETRY_AFTER:", message)
	}

	recTime, ok := EarliestRecoveryTime(err)
	if !ok {
		t.Fatal("EarliestRecoveryTime returned false")
	}
	if !recTime.Equal(recoverAt) {
		t.Fatalf("EarliestRecoveryTime = %v, want %v", recTime, recoverAt)
	}
}

func TestModelCooldownError_NoRetryAfterWhenUnknown(t *testing.T) {
	t.Parallel()

	err := newModelCooldownErrorWithRecoverAt("gpt-5", "codex", 0, time.Time{}, nil)
	var payload map[string]any
	if errUnmarshal := json.Unmarshal([]byte(err.Error()), &payload); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error: %v", errUnmarshal)
	}
	rawErr := payload["error"].(map[string]any)
	message, _ := rawErr["message"].(string)
	if strings.Contains(message, "RETRY_AFTER") {
		t.Fatalf("message = %q should not contain RETRY_AFTER", message)
	}
}

func TestWithErrorRecoveryTime_JSONErrorPreservesValidity(t *testing.T) {
	t.Parallel()

	recoverAt := time.Date(2026, 10, 13, 17, 30, 0, 0, time.UTC)
	rawJSON := `{"error":{"message":"upstream rate limited","type":"rate_limit_error"}}`
	wrapped := WithErrorRecoveryTime(errors.New(rawJSON), recoverAt)

	var payload map[string]any
	if err := json.Unmarshal([]byte(wrapped.Error()), &payload); err != nil {
		t.Fatalf("wrapped.Error() produced invalid JSON: %v, string: %s", err, wrapped.Error())
	}

	rawErr := payload["error"].(map[string]any)
	msg := rawErr["message"].(string)
	wantSuffix := "RETRY_AFTER:2026-10-13T17:30:00Z"
	if !strings.HasSuffix(msg, wantSuffix) {
		t.Fatalf("message = %q, want suffix %q", msg, wantSuffix)
	}

	t.Run("string error field", func(t *testing.T) {
		t.Parallel()
		raw := `{"error":"simple error string"}`
		wrappedErr := WithErrorRecoveryTime(errors.New(raw), recoverAt)
		var p map[string]any
		if err := json.Unmarshal([]byte(wrappedErr.Error()), &p); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if !strings.HasSuffix(p["error"].(string), wantSuffix) {
			t.Fatalf("error = %q, want suffix %q", p["error"], wantSuffix)
		}
	})

	t.Run("detail field", func(t *testing.T) {
		t.Parallel()
		raw := `{"detail":"something failed"}`
		wrappedErr := WithErrorRecoveryTime(errors.New(raw), recoverAt)
		var p map[string]any
		if err := json.Unmarshal([]byte(wrappedErr.Error()), &p); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if !strings.HasSuffix(p["detail"].(string), wantSuffix) {
			t.Fatalf("detail = %q, want suffix %q", p["detail"], wantSuffix)
		}
	})

	t.Run("no recognized message field adds message field", func(t *testing.T) {
		t.Parallel()
		raw := `{"code":429}`
		wrappedErr := WithErrorRecoveryTime(errors.New(raw), recoverAt)
		var p map[string]any
		if err := json.Unmarshal([]byte(wrappedErr.Error()), &p); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if p["message"] != wantSuffix {
			t.Fatalf("message = %q, want %q", p["message"], wantSuffix)
		}
	})
}
