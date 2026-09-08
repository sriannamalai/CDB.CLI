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
	"strings"
	"syscall"

	"github.com/reeflective/readline"
	"github.com/sriannamalai/CDB.CLI/internal/command"
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
// parse, and that differ from the line before them, are recorded. Spec section
// 7 requires the history to be deduplicated and to exclude unparseable lines,
// and readline writes every accepted line straight through to its source.
type filteredHistory struct{ src readline.History }

func (h *filteredHistory) Write(line string) (int, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return h.src.Len(), nil
	}
	if _, err := Parse(line); err != nil {
		return h.src.Len(), nil
	}
	if n := h.src.Len(); n > 0 {
		if last, err := h.src.GetLine(n - 1); err == nil && last == line {
			return n, nil
		}
	}
	return h.src.Write(line)
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
		if runErr != nil {
			fmt.Fprintln(sh.sess.Stderr, errorText(runErr, sh.sess.Prefs.Verbose))
		}
	}
}

// errorText renders an error for the shell. Task 21 replaces the body with a
// call to render.ErrorMessage; the signature does not change.
func errorText(err error, verbose bool) string { return err.Error() }

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
	defer func() { sh.sess.Prefs.Yes, sh.sess.Prefs.Verbose = prevYes, prevVerbose }()
	if v, ferr := fs.GetBool("yes"); ferr == nil && v {
		sh.sess.Prefs.Yes = true
	}
	if v, ferr := fs.GetBool("verbose"); ferr == nil && v {
		sh.sess.Prefs.Verbose = true
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
