package cli

import (
	"errors"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/render"
)

// A connection that could not be built because the keyring refused is a
// connection failure, not a command failure: spec section 11 puts it on exit 3
// alongside a refused socket, and its message is already a plain sentence, so
// the renderer must pass it through unchanged.
func TestConnectionErrorExitsThree(t *testing.T) {
	err := command.Connectionf(errors.New("keychain access denied"),
		"Could not read the password for profile %q from the system keyring: %v. Set CDB_PASSWORD to bypass it.",
		"local", errors.New("keychain access denied"))
	if got := ExitCode(err); got != ExitConnection {
		t.Errorf("ExitCode = %d, want %d", got, ExitConnection)
	}
	if msg := render.ErrorMessage(err, false); msg != err.Error() {
		t.Errorf("ErrorMessage = %q, want the sentence unchanged (%q)", msg, err.Error())
	}
}
