package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/reeflective/readline"
	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/render"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Config configures the shell.
type Config struct {
	// HistoryFile is the history path, or "" to keep history in memory only.
	HistoryFile string
	// Keymap is "emacs" or "vi".
	Keymap string
}

// Shell is the interactive front-end.
type Shell struct {
	reg  *command.Registry
	sess *session.Session
	rl   *readline.Shell
	cfg  Config
	hist readline.History
}

// filteredHistory wraps a readline history source so that only lines cdb could
// parse, and that differ from the line before them, are recorded, and so that
// no line it records carries a credential. Spec section 7 requires the history
// to be deduplicated and to exclude unparseable lines, and readline writes
// every accepted line straight through to its source.
type filteredHistory struct{ src readline.History }

func (h *filteredHistory) Write(line string) (int, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return h.src.Len(), nil
	}
	if _, err := Parse(line); err != nil {
		return h.src.Len(), nil
	}
	// Redact before the dedup check, so what is compared is what is stored.
	line = redactLine(line)
	if n := h.src.Len(); n > 0 {
		if last, err := h.src.GetLine(n - 1); err == nil && last == line {
			return n, nil
		}
	}
	return h.src.Write(line)
}

// redactLine rewrites any credential a typed line carries. The history file
// outlives the session and the "history" command prints every stored line to
// stdout, so `connect https://admin:pw@host` would otherwise put a password in
// both — and the global rule is that a secret never reaches stdout, stderr, a
// log or a file. The line is rewritten rather than dropped so that what is
// stored is still a command the operator can re-run, and only the tokens that
// are recognisably a server URL are touched: a document id that merely
// contains an "@" must survive intact.
func redactLine(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	start := -1
	for i := 0; i <= len(line); i++ {
		if i < len(line) && !isLineSpace(line[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			b.WriteString(redactToken(line[start:i]))
			start = -1
		}
		if i < len(line) {
			b.WriteByte(line[i])
		}
	}
	return b.String()
}

func isLineSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// redactToken strips the userinfo from one word of a command line, leaving
// anything that is not a credential-carrying URL exactly as it was.
func redactToken(tok string) string {
	// Shell quoting is part of the raw line. Analyse and rewrite the quoted
	// text, then put the quotes back, so 'https://admin:pw@host' is treated
	// exactly like the bare form and a quoted JSON argument is judged on its
	// JSON rather than on a stray trailing quote.
	open, body, closing := splitQuotes(tok)
	if !carriesUserinfo(body) {
		return tok
	}
	return open + couch.RedactURL(body) + closing
}

// splitQuotes peels one matching pair of surrounding shell quotes off a token.
func splitQuotes(tok string) (open, body, closing string) {
	if len(tok) >= 2 {
		q := tok[0]
		if (q == '\'' || q == '"') && tok[len(tok)-1] == q {
			return string(q), tok[1 : len(tok)-1], string(q)
		}
	}
	return "", tok, ""
}

// hostPattern is what the host half of an authority has to look like: a host
// name or address, optionally with a port. It is the guard that keeps a JSON
// argument out of the redactor — `{"email":{"$eq":"a@b.com"}}` has an "@" with
// a ":" before it and would otherwise be cut at the "@", silently rewriting
// the operator's query into a parse error.
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+(:[0-9]+)?$`)

// userPattern is what the user-name half of a schemeless credential has to
// look like. JSON punctuation and "$" are what a Mango selector brings and a
// user name does not.
var userPattern = regexp.MustCompile(`^[A-Za-z0-9._~%+-]+$`)

// carriesUserinfo reports whether tok is a URL, or a schemeless
// "user:pass@host", whose authority holds userinfo. Both forms require the
// host half to look like a host: everything else is an argument that merely
// contains an "@".
func carriesUserinfo(tok string) bool {
	if i := strings.Index(tok, "://"); i >= 0 {
		authority := tok[i+3:]
		if j := strings.IndexAny(authority, "/?#"); j >= 0 {
			authority = authority[:j]
		}
		at := strings.LastIndex(authority, "@")
		return at > 0 && hostPattern.MatchString(authority[at+1:])
	}
	return schemelessCredential(tok)
}

// schemelessCredential recognises "user:pass@host", which url.Parse reads as a
// scheme plus an opaque part rather than as an authority. A password is what
// makes such a token worth rewriting: a bare "name@host" is an email address or
// a document id far more often than it is a credential, and rewriting those
// would corrupt the stored command. Both halves must also look like an
// authority — see hostPattern.
func schemelessCredential(tok string) bool {
	at := strings.LastIndex(tok, "@")
	if at <= 0 || at == len(tok)-1 {
		return false
	}
	userinfo, host := tok[:at], tok[at+1:]
	colon := strings.Index(userinfo, ":")
	if colon <= 0 {
		return false
	}
	return userPattern.MatchString(userinfo[:colon]) && hostPattern.MatchString(host)
}

func (h *filteredHistory) GetLine(pos int) (string, error) { return h.src.GetLine(pos) }
func (h *filteredHistory) Len() int                        { return h.src.Len() }
func (h *filteredHistory) Dump() interface{}               { return h.src.Dump() }

// historyLines reads a history source out in order, oldest first.
func historyLines(src readline.History) []string {
	if src == nil {
		return nil
	}
	out := make([]string, 0, src.Len())
	for i := 0; i < src.Len(); i++ {
		line, err := src.GetLine(i)
		if err != nil {
			continue
		}
		out = append(out, line)
	}
	return out
}

// New builds a shell. It does not touch the terminal until Run is called.
func New(reg *command.Registry, s *session.Session, cfg Config) (*Shell, error) {
	sh := &Shell{reg: reg, sess: s, cfg: cfg}
	if err := sh.initHistory(); err != nil {
		return nil, err
	}
	return sh, nil
}

// initHistory builds the filtered history source and swaps the registry's
// placeholder history command for one that reads it. It touches no terminal,
// so tests that drive RunLine get a working "history" command too.
func (sh *Shell) initHistory() error {
	src := readline.NewInMemoryHistory()
	if sh.cfg.HistoryFile != "" {
		if err := os.MkdirAll(filepath.Dir(sh.cfg.HistoryFile), 0o700); err != nil {
			return err
		}
		// NewHistoryFromFile reports an error when the file does not exist
		// yet; the source it returns is still usable and creates the file on
		// its first write.
		fileSrc, ferr := readline.NewHistoryFromFile(sh.cfg.HistoryFile)
		if fileSrc != nil {
			src = fileSrc
		} else if ferr != nil {
			return ferr
		}
	}
	sh.hist = &filteredHistory{src: src}
	sh.reg.Replace(command.HistoryFrom(func() []string { return historyLines(sh.hist) }))
	return nil
}

// initReadline creates the line editor. It is separate from New so tests can
// drive RunLine without a terminal.
func (sh *Shell) initReadline() error {
	rl := readline.NewShell()
	rl.Prompt.Primary(func() string { return sh.sess.Prompt() })
	rl.Prompt.Secondary(func() string { return "...> " })
	// readline only paints a continuation marker when a multiline column is
	// asked for; without this the secondary prompt above is never displayed
	// and a continued line looks like a stray second line of input.
	_ = rl.Config.Set("multiline-column", true)
	rl.AcceptMultiline = func(line []rune) bool { return !NeedsMore(string(line)) }
	rl.Completer = sh.complete
	if sh.cfg.Keymap == "vi" {
		_ = rl.Config.Set("editing-mode", "vi")
		rl.Keymap.SetMain("vi-insert")
	} else {
		_ = rl.Config.Set("editing-mode", "emacs")
		rl.Keymap.SetMain("emacs")
	}
	// Add replaces readline's default in-memory source, so every accepted line
	// goes through the filter above.
	rl.History.Add("cdb", sh.hist)
	sh.rl = rl
	return nil
}

// Run reads and executes lines until the operator exits.
func (sh *Shell) Run(ctx context.Context) error {
	if err := sh.initReadline(); err != nil {
		return err
	}
	// runShell has already set this; repeat it so Shell.Run is correct on its
	// own, whoever calls it.
	sh.sess.Prefs.Interactive = true
	for {
		line, err := sh.rl.Readline()
		switch {
		case errors.Is(err, readline.ErrInterrupt):
			continue
		case errors.Is(err, io.EOF):
			fmt.Fprintln(sh.sess.Stdout)
			return nil
		case err != nil:
			return err
		}
		trimmed := strings.TrimSpace(strings.ReplaceAll(line, "\\\n", " "))
		if trimmed == "" {
			continue
		}
		cmdCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		runErr := sh.RunLine(cmdCtx, trimmed)
		stop()
		if errors.Is(runErr, command.ErrExit) {
			return nil
		}
		sh.reportError(runErr)
	}
}

// reportError prints a command's error to the shell's stderr, unless it is a
// cancelled context: Ctrl-C during a command, or replications --watch
// interrupted, wraps context.Canceled, and that means the operator asked to
// stop, so nothing is printed and the shell just returns to the prompt.
func (sh *Shell) reportError(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	fmt.Fprintln(sh.sess.Stderr, errorText(err, sh.sess.Prefs.Verbose))
}

// errorText renders an error for the shell.
func errorText(err error, verbose bool) string { return render.ErrorMessage(err, verbose) }

// RunLine parses and runs one line, rendering the result.
func (sh *Shell) RunLine(ctx context.Context, input string) error {
	line, err := Parse(input)
	if err != nil {
		return err
	}
	if len(line.Argv) == 0 {
		return nil
	}
	name := line.Argv[0]
	c, ok := sh.reg.Lookup(name)
	if !ok {
		return command.Usagef(name, "unknown command. Type \"help\" to see the command list.")
	}
	fs := sh.reg.NewFlagSet(c)
	if err := fs.Parse(line.Argv[1:]); err != nil {
		return command.Usagef(c.Name, "%v\nusage: %s %s", err, c.Name, c.Usage)
	}
	args := fs.Args()
	if err := c.CheckArgsErr(args); err != nil {
		return err
	}
	// --yes and --verbose are per-line in the shell: they apply to this
	// invocation only, and the session's own settings come back afterwards.
	// (--json is not a preference; it reaches the renderer as forceJSON below.)
	prevYes, prevVerbose := sh.sess.Prefs.Yes, sh.sess.Prefs.Verbose
	prevAnon := sh.sess.Prefs.Anonymous
	defer func() {
		sh.sess.Prefs.Yes, sh.sess.Prefs.Verbose = prevYes, prevVerbose
		sh.sess.Prefs.Anonymous = prevAnon
	}()
	if v, ferr := fs.GetBool("yes"); ferr == nil && v {
		sh.sess.Prefs.Yes = true
	}
	if v, ferr := fs.GetBool("verbose"); ferr == nil && v {
		sh.sess.Prefs.Verbose = true
	}
	// --anonymous has to be on the session before the auto-connect below, and
	// before "connect" runs: openProfile is what acts on it.
	if v, ferr := fs.GetBool("anonymous"); ferr == nil && v {
		sh.sess.Prefs.Anonymous = true
	}
	if c.NeedsClient && !sh.sess.Connected() {
		if err := command.Open(ctx, sh.sess, ""); err != nil {
			return err
		}
	}
	res, err := c.Run(ctx, sh.sess, command.Invocation{
		Args:   args,
		Flags:  fs,
		Stdin:  sh.sess.Stdin(),
		Stdout: sh.sess.Stdout,
		Stderr: sh.sess.Stderr,
		Shell:  true,
	})
	if err != nil {
		return err
	}
	forceJSON, _ := fs.GetBool("json")
	if line.Filter != "" {
		res, err = applyFilterToResult(line.Filter, res)
		if err != nil {
			return err
		}
		forceJSON = true
	}
	return render.New(sh.sess.Stdout, render.OptionsFor(sh.sess.Prefs, sh.sess.Stdout, forceJSON)).Render(res)
}

// applyFilterToResult runs a gojq filter over the JSON side of a result.
func applyFilterToResult(expr string, res command.Result) (command.Result, error) {
	docs, err := resultJSON(res)
	if err != nil {
		return nil, err
	}
	out, err := ApplyFilter(expr, docs)
	if err != nil {
		return nil, err
	}
	rows := command.Rows{Columns: []command.Column{{Title: "value"}}}
	for _, v := range out {
		rows.Items = append(rows.Items, command.Row{Cells: []string{string(v)}, JSON: v})
	}
	return rows, nil
}

// resultJSON extracts the JSON documents a result carries.
func resultJSON(res command.Result) ([]json.RawMessage, error) {
	switch v := res.(type) {
	case command.Document:
		return []json.RawMessage{v.JSON}, nil
	case command.Rows:
		out := make([]json.RawMessage, 0, len(v.Items))
		for _, item := range v.Items {
			if item.JSON != nil {
				out = append(out, item.JSON)
			}
		}
		return out, nil
	case command.Stream:
		var out []json.RawMessage
		for {
			row, ok, err := v.Next()
			if err != nil {
				return nil, err
			}
			if !ok {
				return out, nil
			}
			if row.JSON != nil {
				out = append(out, row.JSON)
			}
		}
	case command.Message:
		b, err := json.Marshal(v.Text)
		return []json.RawMessage{b}, err
	case command.Empty:
		return nil, nil
	default:
		return nil, fmt.Errorf("a %s result cannot be filtered", res.ResultKind())
	}
}
