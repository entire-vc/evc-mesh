package apierror

// ChatRunner builds a runner-protocol refusal (agent-chat runner protocol §10):
// the HTTP status, the stable error_code the runner branches on, and the
// machine-readable context the protocol names for that code.
func ChatRunner(status int, code, message string, context map[string]any) *Error {
	return &Error{Code: status, ErrorCode: code, Message: message, Context: context}
}
