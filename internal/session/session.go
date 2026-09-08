// Package session holds the state one cdb invocation or shell run carries: the
// active connection, the current virtual path, and output preferences.
package session

import (
	"bufio"
	"io"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
)

// Format selects a renderer.
type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatRaw   Format = "raw"
)

// ColorMode selects when to emit ANSI colour.
type ColorMode string

const (
	ColorAuto   ColorMode = "auto"
	ColorAlways ColorMode = "always"
	ColorNever  ColorMode = "never"
)

// Prefs are the output and input preferences for a session.
type Prefs struct {
	Format Format
	Color  ColorMode
	// Pager is "auto", "off", or an explicit command line.
	Pager string
	// Keymap is "emacs" or "vi".
	Keymap string
	// Yes skips destructive-action confirmations.
	Yes bool
	// Verbose keeps raw status codes and reasons in error output.
	Verbose bool
	// Interactive is true when stdin and stdout are both terminals.
	Interactive bool
}

// DefaultPrefs are the settings used before any config or flag is applied.
func DefaultPrefs() Prefs {
	return Prefs{Format: FormatTable, Color: ColorAuto, Pager: "auto", Keymap: "emacs"}
}

// Session is the mutable state shared by the command registry and its
// front-ends. It is not safe for concurrent use.
type Session struct {
	Client  *couch.Client
	Profile string
	Prefs   Prefs
	Stdout  io.Writer
	Stderr  io.Writer

	// stdin is unexported so that every reader of it goes through Reader and
	// shares one buffer. Two bufio.Readers over the same stream lose data: the
	// first fills its 4 KiB buffer and the second sees EOF immediately.
	stdin io.Reader
	in    *bufio.Reader

	cwd string

	// cache backs shell completion. Cache() creates it on first use.
	cache *Cache
}

// New returns a disconnected session at the server root.
func New(stdin io.Reader, stdout, stderr io.Writer) *Session {
	s := &Session{
		Prefs:  DefaultPrefs(),
		Stdout: stdout,
		Stderr: stderr,
		cwd:    "/",
	}
	s.SetStdin(stdin)
	return s
}

// Stdin is the session's raw input stream.
func (s *Session) Stdin() io.Reader { return s.stdin }

// SetStdin replaces the input stream and resets the shared line reader.
func (s *Session) SetStdin(r io.Reader) {
	s.stdin = r
	s.in = bufio.NewReader(r)
}

// Reader is the single buffered reader over the session's stdin. Every prompt
// — Confirm, ConfirmPhrase, the connect walk-through, find's guided builder,
// resolve's revision picker — must read through it, so that a second prompt in
// the same command still sees the bytes the first one buffered.
func (s *Session) Reader() *bufio.Reader { return s.in }

// Connected reports whether a client is attached.
func (s *Session) Connected() bool { return s.Client != nil }

// Attach installs a client and records the profile it came from.
func (s *Session) Attach(c *couch.Client, profile string) {
	_ = s.Detach()
	s.Client = c
	s.Profile = profile
	s.cwd = "/"
	// A new connection invalidates every cached lookup: database names and
	// sampled fields belong to the server that was just replaced.
	s.Cache().Reset()
}

// Detach closes and drops the current client and returns to the root.
func (s *Session) Detach() error {
	var err error
	if s.Client != nil {
		err = s.Client.Close()
	}
	s.Client = nil
	s.Profile = ""
	s.cwd = "/"
	s.Cache().Reset()
	return err
}

// Path is the current virtual path, always absolute and canonical.
func (s *Session) Path() string {
	if s.cwd == "" {
		return "/"
	}
	return s.cwd
}

// SetPath canonicalises and stores a new current path.
func (s *Session) SetPath(p string) {
	clean, err := path.Clean("/", p)
	if err != nil {
		s.cwd = "/"
		return
	}
	s.cwd = clean
}

// Resolve resolves input against the current path.
func (s *Session) Resolve(input string) (path.Target, error) {
	if input == "" {
		input = "."
	}
	return path.Resolve(s.Path(), input)
}

// Prompt is the shell prompt for the current state.
func (s *Session) Prompt() string {
	if !s.Connected() {
		return "cdb> "
	}
	user := s.Client.Username()
	if user == "" {
		user = "anonymous"
	}
	return user + "@" + s.Client.Host() + ":" + s.Path() + "> "
}
