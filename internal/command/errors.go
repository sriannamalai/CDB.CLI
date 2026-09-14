package command

import (
	"fmt"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
)

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

// SentenceError is a failure cdb phrased itself, where the server's own words
// would be worse than useless: "service unavailable" says nothing about
// Clouseau, and "missing" says nothing about Nouveau being switched off.
//
// The cause is carried as three plain fields rather than as a wrapped error,
// and there is deliberately no Unwrap: a *couch.Error still reachable through
// errors.As would be re-rendered by internal/render's plainSentence — a 503 by
// its ">= 500" arm, a 400 by its own — and the sentence composed here would
// never be seen. Copying the fields keeps what --verbose needs, which is the
// server's own words in the trailing bracket, without keeping what would
// overwrite the sentence.
type SentenceError struct {
	Text string
	// Status, Name and Reason are the server's answer, for --verbose to append
	// as "[status N name: reason]". Status is 0 when the sentence stands on
	// nothing a server said, and then there is no bracket to print.
	Status int
	Name   string
	Reason string
}

func (e *SentenceError) Error() string { return e.Text }

// Errorf builds one, copying the status, error name and reason out of cause
// when it is a *couch.Error. It is neither a usage error nor a connection
// failure, so internal/cli.ExitCode gives it exit 1 — the code every other
// server-side refusal uses.
func Errorf(cause error, format string, args ...any) error {
	e := &SentenceError{Text: fmt.Sprintf(format, args...)}
	if ce, ok := couch.AsError(cause); ok {
		e.Status, e.Name, e.Reason = ce.Status, ce.Name, ce.Reason
	}
	return e
}
