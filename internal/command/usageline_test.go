package command

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Issue #53.5: put, rm and cat take an optional path only in a pipeline. Run
// with no argument and no pipeline they are the one-shot commands they have
// always been, and the usage line they print says so: "<path>", not
// "[<path>]". The bracketed form belongs to the pipeline help, which explains
// what leaving the path out means.
func TestTheOneShotUsageLineRequiresAPath(t *testing.T) {
	for _, tc := range []struct {
		cmd  Command
		want string
	}{
		{Cat(), "cat: expected at least 1 argument(s), got 0\nusage: cat <path>"},
		{Put(), "put: expected at least 1 argument(s), got 0\nusage: put <path> [file]"},
		{Rm(), "rm: expected at least 1 argument(s), got 0\nusage: rm <path>"},
	} {
		s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
		_, err := invoke(t, tc.cmd, s)
		if err == nil {
			t.Errorf("%s with no argument was accepted", tc.cmd.Name)
			continue
		}
		var ue *UsageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: error is %T, want *UsageError (exit 2)", tc.cmd.Name, err)
			continue
		}
		if err.Error() != tc.want {
			t.Errorf("%s: error = %q, want %q", tc.cmd.Name, err.Error(), tc.want)
		}
	}
}
