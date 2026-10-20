package service

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/karthikponna/tryboxi/internal/scheduler"
)

// Error is an API-level error with a stable code.
type Error struct {
	Code       string
	Message    string
	Status     int
	Retryable  bool
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(status int, code, format string, args ...any) *Error {
	return &Error{Code: code, Status: status, Message: fmt.Sprintf(format, args...)}
}

func errNotFound(what, id string) *Error {
	return errf(http.StatusNotFound, "not_found", "%s %s not found", what, id)
}

func errInvalid(format string, args ...any) *Error {
	return errf(http.StatusBadRequest, "invalid_request", format, args...)
}

func errState(format string, args ...any) *Error {
	return errf(http.StatusConflict, "invalid_state", format, args...)
}

func errLost(format string, args ...any) *Error {
	return errf(http.StatusGone, "sandbox_lost", format, args...)
}

// AsError converts any error into an *Error for the API layer.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if c, ok := scheduler.IsCapacity(err); ok {
		return &Error{Code: "capacity_exhausted", Status: http.StatusServiceUnavailable, Message: c.Error(), Retryable: true, RetryAfter: c.RetryAfter}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errf(http.StatusNotFound, "not_found", "not found")
	}
	if errors.Is(err, errTimeout) {
		return &Error{Code: "timeout", Status: http.StatusGatewayTimeout, Message: err.Error(), Retryable: true}
	}
	return &Error{Code: "internal", Status: http.StatusInternalServerError, Message: err.Error(), Retryable: true}
}

var errTimeout = errors.New("timed out")
