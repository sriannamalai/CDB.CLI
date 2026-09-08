package command

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// ErrDeclined means the operator answered no.
var ErrDeclined = errors.New("cancelled")

// Confirm asks a yes/no question. It returns nil to proceed, ErrDeclined when
// the operator says no, and a UsageError when there is no terminal to ask on.
func Confirm(s *session.Session, prompt string) error {
	if err := askable(s, prompt); err != nil {
		return err
	}
	if s.Prefs.Yes {
		return nil
	}
	fmt.Fprintf(s.Stdout, "%s [y/N] ", prompt)
	line, err := s.Reader().ReadString('\n')
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
func ConfirmPhrase(s *session.Session, prompt, want string) error {
	if err := askable(s, prompt); err != nil {
		return err
	}
	if s.Prefs.Yes {
		return nil
	}
	fmt.Fprintf(s.Stdout, "%s (%s): ", prompt, want)
	line, err := s.Reader().ReadString('\n')
	if err != nil && line == "" {
		return ErrDeclined
	}
	if strings.TrimSpace(line) != want {
		return ErrDeclined
	}
	return nil
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
