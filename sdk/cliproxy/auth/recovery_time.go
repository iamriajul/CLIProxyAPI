package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

func (e *recoveryTimeCarrierError) Error() string {
	if e == nil || e.error == nil {
		return ""
	}
	text := e.error.Error()
	if !e.recoverAt.IsZero() && !strings.Contains(text, "RETRY_AFTER:") {
		suffix := FormatRetryAfterSuffix(e.recoverAt)
		if suffix != "" {
			trimmed := strings.TrimSpace(text)
			if trimmed != "" && json.Valid([]byte(trimmed)) {
				if gjson.GetBytes([]byte(trimmed), "error.message").Exists() {
					msgVal := gjson.GetBytes([]byte(trimmed), "error.message").String()
					if updated, errSet := sjson.SetBytes([]byte(trimmed), "error.message", fmt.Sprintf("%s %s", msgVal, suffix)); errSet == nil {
						return string(updated)
					}
				} else if gjson.GetBytes([]byte(trimmed), "message").Exists() {
					msgVal := gjson.GetBytes([]byte(trimmed), "message").String()
					if updated, errSet := sjson.SetBytes([]byte(trimmed), "message", fmt.Sprintf("%s %s", msgVal, suffix)); errSet == nil {
						return string(updated)
					}
				} else if gjson.GetBytes([]byte(trimmed), "error").Type == gjson.String {
					msgVal := gjson.GetBytes([]byte(trimmed), "error").String()
					if updated, errSet := sjson.SetBytes([]byte(trimmed), "error", fmt.Sprintf("%s %s", msgVal, suffix)); errSet == nil {
						return string(updated)
					}
				} else if gjson.GetBytes([]byte(trimmed), "detail").Exists() {
					msgVal := gjson.GetBytes([]byte(trimmed), "detail").String()
					if updated, errSet := sjson.SetBytes([]byte(trimmed), "detail", fmt.Sprintf("%s %s", msgVal, suffix)); errSet == nil {
						return string(updated)
					}
				} else {
					if updated, errSet := sjson.SetBytes([]byte(trimmed), "message", suffix); errSet == nil {
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
