package cli

import (
	"context"
	"errors"
	"net/http"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
)

// Process exit codes.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitUsage       = 2
	ExitConnection  = 3
	ExitInterrupted = 130
)

// ExitCode maps an error to the process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, context.Canceled) {
		return ExitInterrupted
	}
	var ue *command.UsageError
	if errors.As(err, &ue) {
		return ExitUsage
	}
	if ce, ok := couch.AsError(err); ok {
		if ce.Status == couch.StatusUnreachable || ce.Status == http.StatusUnauthorized {
			return ExitConnection
		}
	}
	return ExitError
}
