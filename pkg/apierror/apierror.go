package apierror

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Error represents a structured API error response.
//
// Code is the HTTP status and stays a number: clients already depend on it.
// ErrorCode is the stable machine-readable identifier clients translate by
// (see catalog.go); Message is always English.
type Error struct {
	Code       int               `json:"code"`
	ErrorCode  string            `json:"error_code,omitempty"`
	Message    string            `json:"message"`
	Details    string            `json:"details,omitempty"`
	Validation map[string]string `json:"validation,omitempty"`
}

// MarshalJSON guarantees every response carries a stable error_code: an error
// built directly (without a factory) gets the generic code of its HTTP status.
func (e *Error) MarshalJSON() ([]byte, error) {
	type plain Error
	c := plain(*e)
	if c.ErrorCode == "" {
		c.ErrorCode = codeForStatus(c.Code)
	}
	return json.Marshal(c)
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return CodeBadRequest
	case http.StatusUnauthorized:
		return CodeUnauthorized
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusConflict:
		return CodeConflict
	case http.StatusTooManyRequests:
		return CodeRateLimited
	case http.StatusServiceUnavailable:
		return CodeServiceUnavailable
	}
	return CodeInternalError
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
	return &Error{Code: http.StatusBadRequest, ErrorCode: CodeBadRequest, Message: message}
}

// BadRequestWithDetails creates a 400 error with details.
func BadRequestWithDetails(message, details string) *Error {
	return &Error{Code: http.StatusBadRequest, ErrorCode: CodeBadRequest, Message: message, Details: details}
}

// ValidationError creates a 400 error with field-level validation details.
func ValidationError(fields map[string]string) *Error {
	return &Error{
		Code:       http.StatusBadRequest,
		ErrorCode:  CodeValidationFailed,
		Message:    "Validation failed",
		Validation: fields,
	}
}

// Unauthorized creates a 401 error.
func Unauthorized(message string) *Error {
	if message == "" {
		message = "Unauthorized"
	}
	return &Error{Code: http.StatusUnauthorized, ErrorCode: CodeUnauthorized, Message: message}
}

// Forbidden creates a 403 error.
func Forbidden(message string) *Error {
	if message == "" {
		message = "Forbidden"
	}
	return &Error{Code: http.StatusForbidden, ErrorCode: CodeForbidden, Message: message}
}

// ForbiddenWithDetails creates a 403 error with details.
func ForbiddenWithDetails(message, details string) *Error {
	return &Error{Code: http.StatusForbidden, ErrorCode: CodeForbidden, Message: message, Details: details}
}

// NotFound creates a 404 error.
func NotFound(resource string) *Error {
	return &Error{
		Code:      http.StatusNotFound,
		ErrorCode: CodeNotFound,
		Message:   fmt.Sprintf("%s not found", resource),
	}
}

// NotFoundWithDetails creates a 404 error that also says what to do about it.
// Use it where "not found" is a fork in the flow rather than a dead end.
func NotFoundWithDetails(resource, details string) *Error {
	return &Error{
		Code:      http.StatusNotFound,
		ErrorCode: CodeNotFound,
		Message:   fmt.Sprintf("%s not found", resource),
		Details:   details,
	}
}

// Conflict creates a 409 error.
func Conflict(message string) *Error {
	return &Error{Code: http.StatusConflict, ErrorCode: CodeConflict, Message: message}
}

// TooManyRequests creates a 429 error.
func TooManyRequests(message string) *Error {
	if message == "" {
		message = "Rate limit exceeded"
	}
	return &Error{Code: http.StatusTooManyRequests, ErrorCode: CodeRateLimited, Message: message}
}

// ServiceUnavailable creates a 503 error.
func ServiceUnavailable(message string) *Error {
	if message == "" {
		message = "Service unavailable"
	}
	return &Error{Code: http.StatusServiceUnavailable, ErrorCode: CodeServiceUnavailable, Message: message}
}

// InternalError creates a 500 error.
func InternalError(message string) *Error {
	if message == "" {
		message = "Internal server error"
	}
	return &Error{Code: http.StatusInternalServerError, ErrorCode: CodeInternalError, Message: message}
}

// Wrap creates an InternalError that wraps an underlying error.
func Wrap(err error) *Error {
	return &Error{
		Code:      http.StatusInternalServerError,
		ErrorCode: CodeInternalError,
		Message:   "Internal server error",
		Details:   err.Error(),
	}
}
