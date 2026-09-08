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
