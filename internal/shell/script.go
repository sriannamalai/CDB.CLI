package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/render"
)

// maxScriptDepth is how deeply "run" may nest. A script that runs itself is a
// mistake, and eight is deep enough that no honest arrangement of scripts
// reaches it.
const maxScriptDepth = 8

// scriptScanBuffer is the longest single line a script may hold. A line can
// carry a whole document, and 1 MiB is well past any document worth typing
// into a file of commands while still being the bound bufio.Scanner needs.
const scriptScanBuffer = 1 << 20

// errScriptExit is how an "exit" line ends its own script: with success, and
// without leaving the shell that ran it.
var errScriptExit = errors.New("script exit")

// RunScript runs a file of shell lines. Comments (a "#" beginning a line),
// blank lines and continuation follow the shell's own rules; execution stops at
// the first failing line, whose sentence is written to stderr as
// "<name>:<line>: <sentence>" with the line number of its first physical line.
// A line beginning with "-" has its failure reported and ignored.
//
// The script is never interactive, whatever terminals the process has, so no
// prompt and no guided builder is reachable for its duration; yes, or a --yes
// on a line, is what lets it past a confirmation. It runs with a child
// variable scope and with args as $1 to $9, and everything it changed about
// the session's preferences is put back when it returns.
func (sh *Shell) RunScript(ctx context.Context, r io.Reader, name string, args []string, yes bool) error {
	if sh.depth >= maxScriptDepth {
		return command.Usagef("run", "scripts nest more than %d deep", maxScriptDepth)
	}
	prevInteractive, prevYes := sh.sess.Prefs.Interactive, sh.sess.Prefs.Yes
	prevArgs, prevVars := sh.args, sh.sess.Vars
	sh.sess.Prefs.Interactive = false
	if yes {
		sh.sess.Prefs.Yes = true
	}
	sh.args = args
	sh.sess.Vars = sh.sess.Vars.Child()
	sh.depth++
	defer func() {
		sh.depth--
		sh.sess.Prefs.Interactive, sh.sess.Prefs.Yes = prevInteractive, prevYes
		sh.args, sh.sess.Vars = prevArgs, prevVars
	}()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), scriptScanBuffer)
	var (
		buf    strings.Builder
		start  int
		lineNo int
	)
	for sc.Scan() {
		lineNo++
		text := sc.Text()
		if buf.Len() == 0 {
			trimmed := strings.TrimSpace(text)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			start = lineNo
		}
		buf.WriteString(text)
		if NeedsMore(buf.String()) {
			buf.WriteString("\n")
			continue
		}
		stmt := buf.String()
		buf.Reset()
		err := sh.runScriptLine(ctx, stmt, name, start)
		if errors.Is(err, errScriptExit) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(buf.String()) != "" {
		return command.Usagef("run", "%s:%d: the last line is unfinished", name, start)
	}
	return nil
}

// runScriptLine runs one logical line. A leading "-" — but not a leading "--",
// which is a flag — means the line's failure is reported and then ignored.
func (sh *Shell) runScriptLine(ctx context.Context, stmt, name string, at int) error {
	// A continued line reaches RunLine joined exactly the way the interactive
	// loop joins one, so the two front-ends cannot disagree about what the
	// operator wrote.
	stmt = strings.TrimSpace(strings.ReplaceAll(stmt, "\\\n", " "))
	ignore := false
	if strings.HasPrefix(stmt, "-") && !strings.HasPrefix(stmt, "--") {
		ignore = true
		stmt = strings.TrimLeft(stmt[1:], " \t")
	}
	if stmt == "" {
		return nil
	}
	err := sh.RunLine(ctx, stmt)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, command.ErrExit):
		return errScriptExit
	case errors.Is(err, context.Canceled):
		// Ctrl-C: the operator asked to stop, and nothing is printed.
		return err
	}
	fmt.Fprintf(sh.sess.Stderr, "%s:%d: %s\n", name, at, render.ErrorMessage(err, sh.sess.Prefs.Verbose))
	if ignore {
		return nil
	}
	return err
}
