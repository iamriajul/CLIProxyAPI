package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

type testRecoveryError struct {
	msg       string
	recoverAt time.Time
}

func (e *testRecoveryError) Error() string { return e.msg }
func (e *testRecoveryError) EarliestRecoveryTime() (time.Time, bool) {
	if e.recoverAt.IsZero() {
		return time.Time{}, false
	}
	return e.recoverAt, true
}

func TestBuildErrorResponseBodyWithError_AppendsRetryAfter_PlainText(t *testing.T) {
	t.Parallel()

	recoverAt := time.Date(2026, 10, 13, 17, 30, 0, 0, time.UTC)
	testErr := &testRecoveryError{
		msg:       "all credentials for model are cooling down",
		recoverAt: recoverAt,
	}

	body := BuildErrorResponseBodyWithError(http.StatusTooManyRequests, testErr.msg, testErr)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	rawErr, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object: %s", string(body))
	}

	message, _ := rawErr["message"].(string)
	wantSuffix := "RETRY_AFTER:2026-10-13T17:30:00Z"
	if !strings.HasSuffix(message, wantSuffix) {
		t.Fatalf("message = %q, want suffix %q", message, wantSuffix)
	}
	if strings.Contains(message, "RETRY_AFTER: ") {
		t.Fatalf("message = %q contains space after RETRY_AFTER:", message)
	}
}

func TestBuildErrorResponseBodyWithError_AppendsRetryAfter_JSON(t *testing.T) {
	t.Parallel()

	recoverAt := time.Date(2026, 10, 13, 17, 30, 0, 0, time.UTC)
	testErr := &testRecoveryError{
		msg:       `{"error":{"message":"quota exhausted on upstream","type":"rate_limit_error"}}`,
		recoverAt: recoverAt,
	}

	body := BuildErrorResponseBodyWithError(http.StatusTooManyRequests, testErr.msg, testErr)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	rawErr, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object: %s", string(body))
	}

	message, _ := rawErr["message"].(string)
	wantSuffix := "RETRY_AFTER:2026-10-13T17:30:00Z"
	if !strings.HasSuffix(message, wantSuffix) {
		t.Fatalf("message = %q, want suffix %q", message, wantSuffix)
	}
	if strings.Contains(message, "RETRY_AFTER: ") {
		t.Fatalf("message = %q contains space after RETRY_AFTER:", message)
	}
}

func TestBuildErrorResponseBodyWithError_NoRetryAfterWhenUnknown(t *testing.T) {
	t.Parallel()

	testErr := &testRecoveryError{
		msg: "some other error",
	}

	body := BuildErrorResponseBodyWithError(http.StatusInternalServerError, testErr.msg, testErr)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	rawErr := payload["error"].(map[string]any)
	message, _ := rawErr["message"].(string)
	if strings.Contains(message, "RETRY_AFTER") {
		t.Fatalf("message = %q should not contain RETRY_AFTER", message)
	}
}

func TestWriteErrorResponse_AppendsRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	recoverAt := time.Date(2026, 10, 13, 17, 30, 0, 0, time.UTC)
	testErr := &testRecoveryError{
		msg:       "quota cooling down",
		recoverAt: recoverAt,
	}

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      testErr,
	})

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	rawErr := payload["error"].(map[string]any)
	message, _ := rawErr["message"].(string)
	wantSuffix := "RETRY_AFTER:2026-10-13T17:30:00Z"
	if !strings.HasSuffix(message, wantSuffix) {
		t.Fatalf("message = %q, want suffix %q", message, wantSuffix)
	}
}
