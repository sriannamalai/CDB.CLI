package render

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/sriannamalai/CDB.CLI/internal/session"
	"golang.org/x/term"
)

// IsTerminal reports whether w is a terminal.
func IsTerminal(w io.Writer) bool { return isTerminalFile(w) }

// IsTerminalReader reports whether r is a terminal. The cobra front-end pairs
// it with IsTerminal to decide whether a run may prompt.
func IsTerminalReader(r io.Reader) bool { return isTerminalFile(r) }

// isTerminalFile reports whether v is an *os.File attached to a terminal.
func isTerminalFile(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// TerminalSize returns the width and height of w, or 80x24 when unknown.
func TerminalSize(w io.Writer) (int, int) {
	f, ok := w.(*os.File)
	if !ok {
		return 80, 24
	}
	width, height, err := term.GetSize(int(f.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return 80, 24
	}
	return width, height
}

// ResolveColor decides whether to emit ANSI colour. NO_COLOR always wins.
func ResolveColor(mode session.ColorMode, w io.Writer) bool {
	if v, ok := os.LookupEnv("NO_COLOR"); ok && v != "" {
		return false
	}
	switch mode {
	case session.ColorNever:
		return false
	case session.ColorAlways:
		return true
	default:
		return IsTerminal(w)
	}
}
