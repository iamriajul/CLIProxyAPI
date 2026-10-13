package auth

import (
	"strings"
	"testing"
	"time"
)

func TestAuthUnavailableError_AppendsRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	recoverAt := time.Date(2026, 10, 13, 17, 30, 0, 0, time.UTC)

	err := newAuthUnavailableErrorWithCause(recoverAt, now, nil)
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	errText := err.Error()
	wantSuffix := "RETRY_AFTER:2026-10-13T17:30:00Z"
	if !strings.HasSuffix(errText, wantSuffix) {
		t.Fatalf("err.Error() = %q, want suffix %q", errText, wantSuffix)
	}

	// Verify no space between RETRY_AFTER: and timestamp
	if strings.Contains(errText, "RETRY_AFTER: ") {
		t.Fatalf("err.Error() = %q contains space after RETRY_AFTER:", errText)
	}

	recTime, ok := EarliestRecoveryTime(err)
	if !ok {
		t.Fatal("EarliestRecoveryTime returned false")
	}
	if !recTime.Equal(recoverAt) {
		t.Fatalf("EarliestRecoveryTime = %v, want %v", recTime, recoverAt)
	}
}

func TestAuthUnavailableError_NoRetryAfterWhenPastOrZero(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	err := newAuthUnavailableErrorWithCause(now, now, nil)
	if strings.Contains(err.Error(), "RETRY_AFTER") {
		t.Fatalf("err.Error() = %q should not contain RETRY_AFTER", err.Error())
	}
}
