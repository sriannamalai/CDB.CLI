package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
)

func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"plain", errors.New("boom"), ExitError},
		{"usage", command.Usagef("ls", "too many arguments"), ExitUsage},
		{"unreachable", couch.NewError(couch.StatusUnreachable, "connection_refused", "connection refused", "read", "server"), ExitConnection},
		{"401", couch.NewError(401, "unauthorized", "nope", "read", "server"), ExitConnection},
		{"403", couch.NewError(403, "forbidden", "nope", "read", "db"), ExitError},
		{"404", couch.NewError(404, "not_found", "missing", "read", "db"), ExitError},
		{"cancelled", context.Canceled, ExitInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestExitCodeForADeclinedConfirmation(t *testing.T) {
	if got := ExitCode(command.ErrDeclined); got != ExitError {
		t.Errorf("ExitCode(ErrDeclined) = %d, want %d", got, ExitError)
	}
}
