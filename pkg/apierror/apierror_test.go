package apierror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Error interface compliance
// ---------------------------------------------------------------------------

func TestError_ImplementsErrorInterface(t *testing.T) {
	var err error = &Error{Code: 400, Message: "test"}
	assert.NotNil(t, err)
	assert.Implements(t, (*error)(nil), &Error{})
}

// ---------------------------------------------------------------------------
// Error() string output
// ---------------------------------------------------------------------------

func TestError_ErrorString(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		expected string
	}{
		{
			name:     "without_details",
			err:      &Error{Code: 400, Message: "Bad request"},
			expected: "[400] Bad request",
		},
		{
			name:     "with_details",
			err:      &Error{Code: 500, Message: "Internal server error", Details: "connection refused"},
			expected: "[500] Internal server error: connection refused",
		},
		{
			name:     "empty_details_treated_as_no_details",
			err:      &Error{Code: 404, Message: "Not found", Details: ""},
			expected: "[404] Not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

// ---------------------------------------------------------------------------
// StatusCode()
// ---------------------------------------------------------------------------

func TestError_StatusCode(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		expected int
	}{
		{"400", &Error{Code: 400}, 400},
		{"401", &Error{Code: 401}, 401},
		{"403", &Error{Code: 403}, 403},
		{"404", &Error{Code: 404}, 404},
		{"409", &Error{Code: 409}, 409},
		{"429", &Error{Code: 429}, 429},
		{"500", &Error{Code: 500}, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.StatusCode())
		})
	}
}

// ---------------------------------------------------------------------------
// Factory functions
// ---------------------------------------------------------------------------

func TestBadRequest(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{"custom_message", "invalid input"},
		{"empty_message", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := BadRequest(tt.message)
			assert.Equal(t, http.StatusBadRequest, err.Code)
			assert.Equal(t, tt.message, err.Message)
			assert.Equal(t, "", err.Details)
			assert.Nil(t, err.Validation)
			assert.Equal(t, http.StatusBadRequest, err.StatusCode())
		})
	}
}

func TestBadRequestWithDetails(t *testing.T) {
	tests := []struct {
		name    string
		message string
		details string
	}{
		{"with_both", "parse error", "unexpected token at position 42"},
		{"empty_details", "bad input", ""},
		{"empty_message", "", "some details"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := BadRequestWithDetails(tt.message, tt.details)
			assert.Equal(t, http.StatusBadRequest, err.Code)
			assert.Equal(t, tt.message, err.Message)
			assert.Equal(t, tt.details, err.Details)
			assert.Nil(t, err.Validation)
		})
	}
}

func TestBadRequestWithDetails_ErrorString(t *testing.T) {
	err := BadRequestWithDetails("parse error", "unexpected EOF")
	assert.Equal(t, "[400] parse error: unexpected EOF", err.Error())
}

func TestValidationError(t *testing.T) {
	fields := map[string]string{
		"title":    "required",
		"priority": "must be one of: urgent, high, medium, low, none",
	}

	err := ValidationError(fields)
	assert.Equal(t, http.StatusBadRequest, err.Code)
	assert.Equal(t, "Validation failed", err.Message)
	assert.Equal(t, "", err.Details)
	assert.Equal(t, fields, err.Validation)
	assert.Equal(t, http.StatusBadRequest, err.StatusCode())
}

func TestValidationError_EmptyFields(t *testing.T) {
	err := ValidationError(map[string]string{})
	assert.Equal(t, http.StatusBadRequest, err.Code)
	assert.Equal(t, "Validation failed", err.Message)
	assert.NotNil(t, err.Validation)
	assert.Empty(t, err.Validation)
}

func TestValidationError_NilFields(t *testing.T) {
	err := ValidationError(nil)
	assert.Equal(t, http.StatusBadRequest, err.Code)
	assert.Equal(t, "Validation failed", err.Message)
	assert.Nil(t, err.Validation)
}

func TestUnauthorized(t *testing.T) {
	tests := []struct {
		name            string
		message         string
		expectedMessage string
	}{
		{"default_message", "", "Unauthorized"},
		{"custom_message", "token expired", "token expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Unauthorized(tt.message)
			assert.Equal(t, http.StatusUnauthorized, err.Code)
			assert.Equal(t, tt.expectedMessage, err.Message)
			assert.Equal(t, http.StatusUnauthorized, err.StatusCode())
		})
	}
}

func TestForbidden(t *testing.T) {
	tests := []struct {
		name            string
		message         string
		expectedMessage string
	}{
		{"default_message", "", "Forbidden"},
		{"custom_message", "insufficient permissions", "insufficient permissions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Forbidden(tt.message)
			assert.Equal(t, http.StatusForbidden, err.Code)
			assert.Equal(t, tt.expectedMessage, err.Message)
			assert.Equal(t, http.StatusForbidden, err.StatusCode())
		})
	}
}

func TestNotFound(t *testing.T) {
	tests := []struct {
		name            string
		resource        string
		expectedMessage string
	}{
		{"task", "Task", "Task not found"},
		{"project", "Project", "Project not found"},
		{"workspace", "Workspace", "Workspace not found"},
		{"empty_resource", "", " not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NotFound(tt.resource)
			assert.Equal(t, http.StatusNotFound, err.Code)
			assert.Equal(t, tt.expectedMessage, err.Message)
			assert.Equal(t, http.StatusNotFound, err.StatusCode())
		})
	}
}

func TestConflict(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{"slug_conflict", "slug already exists"},
		{"empty_message", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Conflict(tt.message)
			assert.Equal(t, http.StatusConflict, err.Code)
			assert.Equal(t, tt.message, err.Message)
			assert.Equal(t, http.StatusConflict, err.StatusCode())
		})
	}
}

func TestTooManyRequests(t *testing.T) {
	tests := []struct {
		name            string
		message         string
		expectedMessage string
	}{
		{"default_message", "", "Rate limit exceeded"},
		{"custom_message", "try again in 60 seconds", "try again in 60 seconds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := TooManyRequests(tt.message)
			assert.Equal(t, http.StatusTooManyRequests, err.Code)
			assert.Equal(t, tt.expectedMessage, err.Message)
			assert.Equal(t, http.StatusTooManyRequests, err.StatusCode())
		})
	}
}

func TestInternalError(t *testing.T) {
	tests := []struct {
		name            string
		message         string
		expectedMessage string
	}{
		{"default_message", "", "Internal server error"},
		{"custom_message", "database connection lost", "database connection lost"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := InternalError(tt.message)
			assert.Equal(t, http.StatusInternalServerError, err.Code)
			assert.Equal(t, tt.expectedMessage, err.Message)
			assert.Equal(t, http.StatusInternalServerError, err.StatusCode())
		})
	}
}

func TestWrap(t *testing.T) {
	tests := []struct {
		name        string
		underlying  error
		wantDetails string
	}{
		{
			name:        "simple_error",
			underlying:  errors.New("connection refused"),
			wantDetails: "connection refused",
		},
		{
			name:        "formatted_error",
			underlying:  fmt.Errorf("failed to query %s: %w", "tasks", errors.New("timeout")),
			wantDetails: "failed to query tasks: timeout",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Wrap(tt.underlying)
			assert.Equal(t, http.StatusInternalServerError, err.Code)
			assert.Equal(t, "Internal server error", err.Message)
			assert.Equal(t, tt.wantDetails, err.Details)
			assert.Equal(t, http.StatusInternalServerError, err.StatusCode())
		})
	}
}

func TestWrap_ErrorStringIncludesDetails(t *testing.T) {
	err := Wrap(errors.New("disk full"))
	assert.Equal(t, "[500] Internal server error: disk full", err.Error())
}

// ---------------------------------------------------------------------------
// Error() string for all factory functions
// ---------------------------------------------------------------------------

func TestFactoryFunctions_ErrorStringFormat(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		expected string
	}{
		{"BadRequest", BadRequest("bad input"), "[400] bad input"},
		{"BadRequestWithDetails", BadRequestWithDetails("bad", "detail"), "[400] bad: detail"},
		{"ValidationError", ValidationError(map[string]string{"f": "r"}), "[400] Validation failed"},
		{"Unauthorized_default", Unauthorized(""), "[401] Unauthorized"},
		{"Unauthorized_custom", Unauthorized("expired"), "[401] expired"},
		{"Forbidden_default", Forbidden(""), "[403] Forbidden"},
		{"Forbidden_custom", Forbidden("no access"), "[403] no access"},
		{"NotFound", NotFound("Task"), "[404] Task not found"},
		{"Conflict", Conflict("duplicate"), "[409] duplicate"},
		{"TooManyRequests_default", TooManyRequests(""), "[429] Rate limit exceeded"},
		{"TooManyRequests_custom", TooManyRequests("slow down"), "[429] slow down"},
		{"InternalError_default", InternalError(""), "[500] Internal server error"},
		{"InternalError_custom", InternalError("oops"), "[500] oops"},
		{"Wrap", Wrap(errors.New("boom")), "[500] Internal server error: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

// ---------------------------------------------------------------------------
// JSON serialization of Error struct
// ---------------------------------------------------------------------------

func TestError_JSONSerialization_Full(t *testing.T) {
	err := &Error{
		Code:    400,
		Message: "Validation failed",
		Details: "see validation errors",
		Validation: map[string]string{
			"title": "required",
			"email": "invalid format",
		},
	}

	data, errMarshal := json.Marshal(err)
	require.NoError(t, errMarshal)

	var raw map[string]json.RawMessage
	errUnmarshal := json.Unmarshal(data, &raw)
	require.NoError(t, errUnmarshal)

	assert.Contains(t, raw, "code")
	assert.Contains(t, raw, "message")
	assert.Contains(t, raw, "details")
	assert.Contains(t, raw, "validation")
}

func TestError_JSONSerialization_OmitEmpty(t *testing.T) {
	err := &Error{
		Code:    404,
		Message: "Not found",
	}

	data, errMarshal := json.Marshal(err)
	require.NoError(t, errMarshal)

	var raw map[string]json.RawMessage
	errUnmarshal := json.Unmarshal(data, &raw)
	require.NoError(t, errUnmarshal)

	assert.Contains(t, raw, "code")
	assert.Contains(t, raw, "message")
	// Details and Validation have omitempty, so they should not appear
	assert.NotContains(t, raw, "details")
	assert.NotContains(t, raw, "validation")
}

func TestError_JSONSerialization_Roundtrip(t *testing.T) {
	original := &Error{
		Code:    400,
		Message: "Validation failed",
		Details: "check fields",
		Validation: map[string]string{
			"name": "too short",
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded Error
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.Code, decoded.Code)
	assert.Equal(t, original.Message, decoded.Message)
	assert.Equal(t, original.Details, decoded.Details)
	assert.Equal(t, original.Validation, decoded.Validation)
}

func TestError_JSONSerialization_ValidationOnly(t *testing.T) {
	err := ValidationError(map[string]string{
		"priority": "invalid value",
	})

	data, errMarshal := json.Marshal(err)
	require.NoError(t, errMarshal)

	var raw map[string]json.RawMessage
	errUnmarshal := json.Unmarshal(data, &raw)
	require.NoError(t, errUnmarshal)

	assert.Contains(t, raw, "code")
	assert.Contains(t, raw, "message")
	assert.Contains(t, raw, "validation")
	// Details is empty, so it should not appear
	assert.NotContains(t, raw, "details")
}

// ---------------------------------------------------------------------------
// Factory function status code verification (comprehensive)
// ---------------------------------------------------------------------------

func TestAllFactoryFunctions_ReturnCorrectStatusCodes(t *testing.T) {
	tests := []struct {
		name         string
		err          *Error
		expectedCode int
	}{
		{"BadRequest", BadRequest("x"), http.StatusBadRequest},
		{"BadRequestWithDetails", BadRequestWithDetails("x", "y"), http.StatusBadRequest},
		{"ValidationError", ValidationError(nil), http.StatusBadRequest},
		{"Unauthorized", Unauthorized(""), http.StatusUnauthorized},
		{"Forbidden", Forbidden(""), http.StatusForbidden},
		{"NotFound", NotFound("x"), http.StatusNotFound},
		{"Conflict", Conflict("x"), http.StatusConflict},
		{"TooManyRequests", TooManyRequests(""), http.StatusTooManyRequests},
		{"InternalError", InternalError(""), http.StatusInternalServerError},
		{"Wrap", Wrap(errors.New("x")), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedCode, tt.err.StatusCode())
			assert.Equal(t, tt.expectedCode, tt.err.Code)
		})
	}
}
