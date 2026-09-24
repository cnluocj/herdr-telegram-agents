package herdr

import "fmt"

// codeNotFound is the server error code for a missing agent, pane or
// workspace target; the gateway maps it to domain.ErrAgentGone.
const codeNotFound = "not_found"

// codeNotIdle is returned when alternate-screen history is requested while
// an agent is working.
const codeNotIdle = "agent_not_idle"

// codeInvalidRequest is what a Herdr without a method answers when asked
// for it; the message then names the "unknown variant".
const codeInvalidRequest = "invalid_request"

// APIError is an error line returned by the Herdr server for a request.
// Codes are Herdr's snake_case identifiers such as "not_found".
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("herdr api %s: %s", e.Code, e.Message)
}
