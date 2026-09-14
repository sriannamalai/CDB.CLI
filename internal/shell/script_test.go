package shell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// say is a stub command that writes its arguments to the session's stdout as
// one Message, so a script test can see which lines ran and in what order.
func say() command.Command {
	return command.Command{
		Name: "say", MinArgs: 0, MaxArgs: -1,
		Run: func(_ context.Context, _ *session.Session, inv command.Invocation) (command.Result, error) {
			return command.Message{Text: strings.Join(inv.Args, " ")}, nil
		},
	}
}

// fail is a stub command that always fails.
func fail() command.Command {
	return command.Command{
		Name: "fail", MinArgs: 0, MaxArgs: -1,
		Run: func(context.Context, *session.Session, command.Invocation) (command.Result, error) {
			return nil, command.Errorf(nil, "no")
		},
	}
}

func scriptShell(t *testing.T, out *bytes.Buffer) *Shell {
	t.Helper()
	return pipeShell(t, out, say(), fail(), command.Run(), command.Set(), command.Exit())
}

func runText(t *testing.T, sh *Shell, text string, args ...string) error {
	t.Helper()
	return sh.RunScript(context.Background(), strings.NewReader(text), "script.cdb", args, false)
}

func TestScriptSkipsCommentsAndBlankLines(t *testing.T) {
	var out bytes.Buffer
	sh := scriptShell(t, &out)
	err := runText(t, sh, "# a comment\n\n   \nsay one\n   # another\nsay two\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(out.String()); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("output = %q", out.String())
	}
}

func TestScriptContinuesALine(t *testing.T) {
	var out bytes.Buffer
	sh := scriptShell(t, &out)
	if err := runText(t, sh, "say one \\\ntwo\n"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "one two" {
		t.Errorf("output = %q", out.String())
	}
}

func TestScriptStopsAtTheFirstFailureAndNamesTheLine(t *testing.T) {
	var out bytes.Buffer
	sh := scriptShell(t, &out)
	err := runText(t, sh, "say one\nfail\nsay three\n")
	if err == nil {
		t.Fatal("the script did not fail")
	}
	if strings.Contains(out.String(), "three") {
		t.Error("the script kept going after a failure")
	}
	if !strings.Contains(out.String(), "script.cdb:2: no") {
		t.Errorf("stderr = %q, want a \"script.cdb:2: no\" line", out.String())
	}
}

func TestScriptDashPrefixReportsAndCarriesOn(t *testing.T) {
	var out bytes.Buffer
	sh := scriptShell(t, &out)
	if err := runText(t, sh, "- fail\n-fail\nsay done\n"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "done") {
		t.Errorf("output = %q; an ignored failure must not stop the script", out.String())
	}
	if n := strings.Count(out.String(), "no"); n != 2 {
		t.Errorf("reported %d ignored failures, want 2", n)
	}
}

func TestScriptPositionalArguments(t *testing.T) {
	var out bytes.Buffer
	sh := scriptShell(t, &out)
	if err := runText(t, sh, "say $1 $2 $#\n", "alpha", "beta"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "alpha beta 2" {
		t.Errorf("output = %q", out.String())
	}
	// The arguments are gone once the script is: an interactive line has none.
	if sh.args != nil {
		t.Errorf("args survived the script: %v", sh.args)
	}
}

func TestScriptExitEndsItWithSuccess(t *testing.T) {
	var out bytes.Buffer
	sh := scriptShell(t, &out)
	if err := runText(t, sh, "say one\nexit\nsay two\n"); err != nil {
		t.Fatalf("exit ended the script with %v, want success", err)
	}
	if strings.Contains(out.String(), "two") {
		t.Error("the script kept going after exit")
	}
}

func TestScriptIsNeverInteractive(t *testing.T) {
	var out bytes.Buffer
	sh := scriptShell(t, &out)
	sh.sess.Prefs.Interactive = true
	if err := runText(t, sh, "say one\n"); err != nil {
		t.Fatal(err)
	}
	if !sh.sess.Prefs.Interactive {
		t.Error("Interactive was not put back after the script")
	}
}

func TestScriptGetsItsOwnVariableScope(t *testing.T) {
	var out bytes.Buffer
	sh := scriptShell(t, &out)
	sh.sess.Vars.Set("shared", session.StringValue("outer"))
	if err := runText(t, sh, "set shared inner\nset mine local\nsay $shared\n"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "inner") {
		t.Errorf("output = %q", out.String())
	}
	if v, _ := sh.sess.Vars.Lookup("shared"); v.Text() != "outer" {
		t.Errorf("the script changed the caller's variable: %q", v.Text())
	}
	if _, ok := sh.sess.Vars.Lookup("mine"); ok {
		t.Error("the script defined a variable in the caller's scope")
	}
}

func TestScriptNestingStopsAtEight(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loop.cdb")
	if err := os.WriteFile(path, []byte("run "+path+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	sh := scriptShell(t, &out)
	err := sh.RunScript(context.Background(), strings.NewReader("run "+path+"\n"), "top.cdb", nil, false)
	if err == nil || !strings.Contains(out.String(), "scripts nest more than 8 deep") {
		t.Fatalf("err = %v, stderr = %q", err, out.String())
	}
}

func TestScriptUnfinishedLastLine(t *testing.T) {
	var out bytes.Buffer
	sh := scriptShell(t, &out)
	err := runText(t, sh, "say \"one\n")
	var ue *command.UsageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "unfinished") {
		t.Fatalf("err = %v", err)
	}
}
