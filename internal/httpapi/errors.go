package httpapi

import "net/http"

// ErrorCode identifies an error that is part of the public HTTP API contract.
type ErrorCode string

const (
	CodeInvalidRequest               ErrorCode = "INVALID_REQUEST"
	CodeUnauthorized                 ErrorCode = "UNAUTHORIZED"
	CodeForbidden                    ErrorCode = "FORBIDDEN"
	CodeSegregationOfDutiesViolation ErrorCode = "SEGREGATION_OF_DUTIES_VIOLATION"
	CodeUserNotFound                 ErrorCode = "USER_NOT_FOUND"
	CodeCaseNotFound                 ErrorCode = "CASE_NOT_FOUND"
	CodePolicyNotFound               ErrorCode = "POLICY_NOT_FOUND"
	CodeAnalysisNotFound             ErrorCode = "ANALYSIS_NOT_FOUND"
	CodeConflict                     ErrorCode = "CONFLICT"
	CodeInvalidStateTransition       ErrorCode = "INVALID_STATE_TRANSITION"
	CodeStaleAnalysis                ErrorCode = "STALE_ANALYSIS"
	CodePolicyIndexingFailed         ErrorCode = "POLICY_INDEXING_FAILED"
	CodeInternalError                ErrorCode = "INTERNAL_ERROR"
)

// APIError carries client-safe error information and an optional private cause.
// Message and Details are part of the public response and must not contain
// provider payloads, credentials, stack traces, or other internal information.
type APIError struct {
	Code    ErrorCode
	Message string
	Details map[string]any

	cause error
}

// NewError creates a public API error. The message and details must be safe to
// return to a client.
func NewError(code ErrorCode, message string, details map[string]any) *APIError {
	return &APIError{
		Code:    code,
		Message: message,
		Details: details,
	}
}

// WrapError creates a public API error while retaining a private cause for
// errors.Is/errors.As and server-side diagnostics. The cause is never written
// to the HTTP response.
func WrapError(code ErrorCode, message string, details map[string]any, cause error) *APIError {
	return &APIError{
		Code:    code,
		Message: message,
		Details: details,
		cause:   cause,
	}
}

func (e *APIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap exposes the private cause to the standard errors package. Response
// writers deliberately never serialize it.
func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func statusForCode(code ErrorCode) (int, bool) {
	switch code {
	case CodeInvalidRequest:
		return http.StatusBadRequest, true
	case CodeUnauthorized:
		return http.StatusUnauthorized, true
	case CodeForbidden, CodeSegregationOfDutiesViolation:
		return http.StatusForbidden, true
	case CodeUserNotFound, CodeCaseNotFound, CodePolicyNotFound, CodeAnalysisNotFound:
		return http.StatusNotFound, true
	case CodeConflict, CodeInvalidStateTransition, CodeStaleAnalysis:
		return http.StatusConflict, true
	case CodePolicyIndexingFailed:
		return http.StatusBadGateway, true
	case CodeInternalError:
		return http.StatusInternalServerError, true
	default:
		return 0, false
	}
}

func defaultMessage(code ErrorCode) string {
	switch code {
	case CodeInvalidRequest:
		return "Invalid request."
	case CodeUnauthorized:
		return "Authentication is required."
	case CodeForbidden:
		return "You do not have permission to perform this action."
	case CodeSegregationOfDutiesViolation:
		return "This action violates segregation of duties."
	case CodeUserNotFound:
		return "User not found."
	case CodeCaseNotFound:
		return "Case not found."
	case CodePolicyNotFound:
		return "Policy not found."
	case CodeAnalysisNotFound:
		return "Analysis not found."
	case CodeConflict:
		return "The request conflicts with the current resource state."
	case CodeInvalidStateTransition:
		return "The requested state transition is not allowed."
	case CodeStaleAnalysis:
		return "Analysis version is no longer current."
	case CodePolicyIndexingFailed:
		return "Policy indexing failed."
	default:
		return "An internal error occurred."
	}
}
