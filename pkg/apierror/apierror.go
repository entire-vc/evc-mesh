package apierror

import (
	"fmt"
	"net/http"
)

// Error represents a structured API error response.
type Error struct {
	Code       int               `json:"code"`
	Message    string            `json:"message"`
	Details    string            `json:"details,omitempty"`
	Validation map[string]string `json:"validation,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("[%d] %s: %s", e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// StatusCode returns the HTTP status code for this error.
func (e *Error) StatusCode() int {
	return e.Code
}

// --- Factory functions for common errors ---

// BadRequest creates a 400 error.
func BadRequest(message string) *Error {
	return &Error{Code: http.StatusBadRequest, Message: message}
}

// BadRequestWithDetails creates a 400 error with details.
func BadRequestWithDetails(message, details string) *Error {
	return &Error{Code: http.StatusBadRequest, Message: message, Details: details}
}

// ValidationError creates a 400 error with field-level validation details.
func ValidationError(fields map[string]string) *Error {
	return &Error{
		Code:       http.StatusBadRequest,
		Message:    "Validation failed",
		Validation: fields,
	}
}

// Unauthorized creates a 401 error.
func Unauthorized(message string) *Error {
	if message == "" {
		message = "Unauthorized"
	}
	return &Error{Code: http.StatusUnauthorized, Message: message}
}

// Forbidden creates a 403 error.
func Forbidden(message string) *Error {
	if message == "" {
		message = "Forbidden"
	}
	return &Error{Code: http.StatusForbidden, Message: message}
}

// NotFound creates a 404 error.
func NotFound(resource string) *Error {
	return &Error{
		Code:    http.StatusNotFound,
		Message: fmt.Sprintf("%s not found", resource),
	}
}

// Conflict creates a 409 error.
func Conflict(message string) *Error {
	return &Error{Code: http.StatusConflict, Message: message}
}

// TooManyRequests creates a 429 error.
func TooManyRequests(message string) *Error {
	if message == "" {
		message = "Rate limit exceeded"
	}
	return &Error{Code: http.StatusTooManyRequests, Message: message}
}

// ServiceUnavailable creates a 503 error.
func ServiceUnavailable(message string) *Error {
	if message == "" {
		message = "Service unavailable"
	}
	return &Error{Code: http.StatusServiceUnavailable, Message: message}
}

// InternalError creates a 500 error.
func InternalError(message string) *Error {
	if message == "" {
		message = "Internal server error"
	}
	return &Error{Code: http.StatusInternalServerError, Message: message}
}

// Wrap creates an InternalError that wraps an underlying error.
func Wrap(err error) *Error {
	return &Error{
		Code:    http.StatusInternalServerError,
		Message: "Internal server error",
		Details: err.Error(),
	}
}
