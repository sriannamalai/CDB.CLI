package command

import "fmt"

// UsageError means the caller wrote the command wrong. The front-ends map it
// to exit code 2 and print the command's usage line.
type UsageError struct {
	Command string
	Reason  string
}

func (e *UsageError) Error() string {
	if e.Command == "" {
		return e.Reason
	}
	return fmt.Sprintf("%s: %s", e.Command, e.Reason)
}

// Usagef builds a UsageError.
func Usagef(cmd, format string, args ...any) *UsageError {
	return &UsageError{Command: cmd, Reason: fmt.Sprintf(format, args...)}
}

// ConnectionError means cdb could not open a connection for a reason that is
// not the server's: an OS keyring that will not open, or one that will not
// hand over a stored password. The front-ends map it to exit code 3, the same
// as a refused socket, because the operator is in the same position — nothing
// ran, and the fix is in the connection, not the command.
//
// Its Message is already a plain sentence in the shape spec section 11 asks
// for, so the renderer prints it as it stands.
type ConnectionError struct {
	Message string
	Err     error
}

func (e *ConnectionError) Error() string { return e.Message }

func (e *ConnectionError) Unwrap() error { return e.Err }

// Connectionf builds a ConnectionError wrapping cause.
func Connectionf(cause error, format string, args ...any) *ConnectionError {
	return &ConnectionError{Message: fmt.Sprintf(format, args...), Err: cause}
}
