package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// RecoveryTimeProvider represents an error that reports an earliest recovery timestamp.
type RecoveryTimeProvider interface {
	EarliestRecoveryTime() (time.Time, bool)
}

// EarliestRecoveryTime extracts the earliest UTC recovery timestamp from an error chain, if known.
func EarliestRecoveryTime(err error) (time.Time, bool) {
	if err == nil {
		return time.Time{}, false
	}
	var rtp RecoveryTimeProvider
	if errors.As(err, &rtp) && rtp != nil {
		return rtp.EarliestRecoveryTime()
	}
	var busy *HomeConcurrencyBusyError
	if errors.As(err, &busy) && busy != nil {
		if ra := busy.retryAfter; ra > 0 {
			return time.Now().Add(ra).UTC(), true
		}
	}
	var exhausted *homeRetryRoundExhaustedError
	if errors.As(err, &exhausted) && exhausted != nil {
		if ra := exhausted.RetryAfter(); ra != nil && *ra > 0 {
			return time.Now().Add(*ra).UTC(), true
		}
	}
	var cooldown *homeDispatchRetryAfterError
	if errors.As(err, &cooldown) && cooldown != nil {
		if ra := cooldown.RetryAfter(); ra != nil && *ra > 0 {
			return time.Now().Add(*ra).UTC(), true
		}
	}
	type retryAfterGetter interface {
		RetryAfter() *time.Duration
	}
	var rag retryAfterGetter
	if errors.As(err, &rag) && rag != nil {
		if ra := rag.RetryAfter(); ra != nil && *ra > 0 {
			return time.Now().Add(*ra).UTC(), true
		}
	}
	return time.Time{}, false
}

// FormatRetryAfterSuffix formats the standard RETRY_AFTER:<RFC3339> suffix for a recovery instant.
// Fractional seconds are rounded up so the timestamp guarantees that the recovery boundary has passed.
func FormatRetryAfterSuffix(recoverAt time.Time) string {
	if recoverAt.IsZero() {
		return ""
	}
	rec := recoverAt.UTC()
	if rec.Nanosecond() != 0 {
		rec = rec.Add(time.Second).Truncate(time.Second)
	}
	return fmt.Sprintf("RETRY_AFTER:%s", rec.Format(time.RFC3339))
}

var retryAfterSuffixPattern = regexp.MustCompile(`RETRY_AFTER:\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

type recoveryTimeCarrierError struct {
	error
	recoverAt time.Time
}

func (e *recoveryTimeCarrierError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.error
}

func (e *recoveryTimeCarrierError) EarliestRecoveryTime() (time.Time, bool) {
	if e == nil || e.recoverAt.IsZero() {
		return time.Time{}, false
	}
	return e.recoverAt, true
}

// StatusCode keeps the wrapped error's status visible without errors.As.
func (e *recoveryTimeCarrierError) StatusCode() int {
	if e == nil {
		return 0
	}
	return statusCodeFromError(e.error)
}

// SafeResponseHeaders exposes the carried instant as a Retry-After header.
func (e *recoveryTimeCarrierError) SafeResponseHeaders() http.Header {
	if e == nil || e.recoverAt.IsZero() {
		return nil
	}
	return safeRetryAfterHeader(time.Until(e.recoverAt))
}

func (e *recoveryTimeCarrierError) Error() string {
	if e == nil || e.error == nil {
		return ""
	}
	text := e.error.Error()
	// The carrier's instant is authoritative, so it replaces any RETRY_AFTER
	// an upstream or per-credential error already wrote into the text.
	if !e.recoverAt.IsZero() && strings.Contains(text, "RETRY_AFTER:") {
		return retryAfterSuffixPattern.ReplaceAllString(text, FormatRetryAfterSuffix(e.recoverAt))
	}
	if !e.recoverAt.IsZero() {
		suffix := FormatRetryAfterSuffix(e.recoverAt)
		if suffix != "" {
			trimmed := strings.TrimSpace(text)
			if trimmed != "" && json.Valid([]byte(trimmed)) {
				if ojson.GetBytes([]byte(trimmed), "error.message").Exists() {
					msgVal := ojson.GetBytes([]byte(trimmed), "error.message").String()
					if updated, errSet := ojson.SetBytes([]byte(trimmed), "error.message", fmt.Sprintf("%s %s", msgVal, suffix)); errSet == nil {
						return string(updated)
					}
				} else if ojson.GetBytes([]byte(trimmed), "message").Exists() {
					msgVal := ojson.GetBytes([]byte(trimmed), "message").String()
					if updated, errSet := ojson.SetBytes([]byte(trimmed), "message", fmt.Sprintf("%s %s", msgVal, suffix)); errSet == nil {
						return string(updated)
					}
				} else if ojson.GetBytes([]byte(trimmed), "error").Type == ojson.String {
					msgVal := ojson.GetBytes([]byte(trimmed), "error").String()
					if updated, errSet := ojson.SetBytes([]byte(trimmed), "error", fmt.Sprintf("%s %s", msgVal, suffix)); errSet == nil {
						return string(updated)
					}
				} else if ojson.GetBytes([]byte(trimmed), "detail").Exists() {
					msgVal := ojson.GetBytes([]byte(trimmed), "detail").String()
					if updated, errSet := ojson.SetBytes([]byte(trimmed), "detail", fmt.Sprintf("%s %s", msgVal, suffix)); errSet == nil {
						return string(updated)
					}
				} else {
					if updated, errSet := ojson.SetBytes([]byte(trimmed), "message", suffix); errSet == nil {
						return string(updated)
					}
				}
				return text
			}
			text = fmt.Sprintf("%s %s", text, suffix)
		}
	}
	return text
}

// WithErrorRecoveryTime wraps an error with an earliest recovery instant.
func WithErrorRecoveryTime(err error, recoverAt time.Time) error {
	if err == nil || recoverAt.IsZero() {
		return err
	}
	return &recoveryTimeCarrierError{error: err, recoverAt: recoverAt.UTC()}
}
