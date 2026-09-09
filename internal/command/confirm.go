package command

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// ErrDeclined means the operator answered no. Its text is a section 11
// sentence rather than the bare word "cancelled", because it is printed as it
// stands: every other operator-facing message is a sentence, and a lone
// lower-case word reads like a crash.
var ErrDeclined = errors.New("Cancelled: nothing was changed.")

// Confirm asks a yes/no question. It returns nil to proceed, ErrDeclined when
// the operator says no or the input ends, ctx.Err() when the operator
// interrupts, and a UsageError when there is no terminal to ask on.
func Confirm(ctx context.Context, s *session.Session, prompt string) error {
	if err := askable(s, prompt); err != nil {
		return err
	}
	if s.Prefs.Yes {
		return nil
	}
	fmt.Fprintf(s.Stdout, "%s [y/N] ", prompt)
	line, err := s.Reader().ReadString('\n')
	if cerr := interrupted(ctx); cerr != nil {
		return cerr
	}
	if err != nil && line == "" {
		return ErrDeclined
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	default:
		return ErrDeclined
	}
}

// ConfirmPhrase requires the operator to retype an exact phrase.
func ConfirmPhrase(ctx context.Context, s *session.Session, prompt, want string) error {
	if err := askable(s, prompt); err != nil {
		return err
	}
	if s.Prefs.Yes {
		return nil
	}
	fmt.Fprintf(s.Stdout, "%s (%s): ", prompt, want)
	line, err := s.Reader().ReadString('\n')
	if cerr := interrupted(ctx); cerr != nil {
		return cerr
	}
	if err != nil && line == "" {
		return ErrDeclined
	}
	if strings.TrimSpace(line) != want {
		return ErrDeclined
	}
	return nil
}

// interrupted reports Ctrl-C at a prompt. It is checked after the read rather
// than racing it, which is the arrangement resolve's chooser adopted in 1.1.1:
// Ctrl-C cancels the command's context and readline lets the pending read
// return, so by the time there is a line to look at the context already says
// what happened. Returning ctx.Err() rather than ErrDeclined is what makes the
// one-shot front end exit 130 in silence instead of printing a verdict — the
// operator interrupted, they were not asked and did not answer.
func interrupted(ctx context.Context) error {
	return ctx.Err()
}

// askable reports whether a prompt can be shown at all. --yes answers every
// prompt in advance; without it, a session with no terminal has nobody to ask,
// and silently proceeding with a destructive action is the one thing cdb must
// never do.
func askable(s *session.Session, prompt string) error {
	if s.Prefs.Yes || s.Prefs.Interactive {
		return nil
	}
	return Usagef("", "%s This action is destructive; re-run with --yes to confirm.", prompt)
}
