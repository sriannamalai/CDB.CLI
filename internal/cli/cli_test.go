package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func testRegistry() *command.Registry {
	r := command.NewRegistry()
	r.Register(command.Pwd())
	r.Register(command.Command{
		Name:    "echo",
		Summary: "Echo the first argument",
		Usage:   "<text>",
		MinArgs: 1,
		MaxArgs: 1,
		Flags:   func(fs *pflag.FlagSet) { fs.Bool("shout", false, "upper-case the text") },
		Run: func(_ context.Context, _ *session.Session, inv command.Invocation) (command.Result, error) {
			text := inv.Arg(0)
			if inv.Bool("shout") {
				text = strings.ToUpper(text)
			}
			return command.Message{Text: text}, nil
		},
	})
	r.Register(command.Command{
		Name:      "help-only",
		Summary:   "Only in the shell",
		ShellOnly: true,
		Run: func(context.Context, *session.Session, command.Invocation) (command.Result, error) {
			return command.Empty{}, nil
		},
	})
	return r
}

func TestExecutePwd(t *testing.T) {
	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), testRegistry(), s, BuildInfo{Version: "0.0.1"}, []string{"pwd"})
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, errOut.String())
	}
	if out.String() != "/\n" {
		t.Errorf("stdout = %q, want %q", out.String(), "/\n")
	}
}

func TestExecutePassesFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), testRegistry(), s, BuildInfo{}, []string{"echo", "--shout", "hi"})
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, errOut.String())
	}
	if out.String() != "HI\n" {
		t.Errorf("stdout = %q, want %q", out.String(), "HI\n")
	}
}

func TestExecuteUsageErrorExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), testRegistry(), s, BuildInfo{}, []string{"echo"})
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut.String(), "echo") {
		t.Errorf("stderr = %q, want it to name the command", errOut.String())
	}
}

func TestShellOnlyCommandsAreNotInTheCobraTree(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	root := NewRoot(testRegistry(), s, BuildInfo{})
	for _, c := range root.Commands() {
		if c.Name() == "help-only" {
			t.Fatal("a ShellOnly command reached the cobra tree")
		}
	}
}

func TestVersionCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), testRegistry(), s, BuildInfo{Version: "1.2.3", Commit: "abc1234", Date: "2026-09-08"}, []string{"version"})
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, errOut.String())
	}
	for _, want := range []string{"1.2.3", "abc1234", "2026-09-08"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("version output %q is missing %q", out.String(), want)
		}
	}
}

func TestGlobalFlagsReachPrefs(t *testing.T) {
	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), testRegistry(), s, BuildInfo{}, []string{"--yes", "--verbose", "pwd"})
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, errOut.String())
	}
	if !s.Prefs.Yes || !s.Prefs.Verbose {
		t.Errorf("Prefs = %+v, want Yes and Verbose true", s.Prefs)
	}
}

func TestExecuteMarksANonTerminalRunNonInteractive(t *testing.T) {
	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	s.Prefs.Interactive = true // must be overwritten by Execute
	if code := Execute(context.Background(), testRegistry(), s, BuildInfo{}, []string{"pwd"}); code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, errOut.String())
	}
	if s.Prefs.Interactive {
		t.Error("Prefs.Interactive = true with buffers for stdin and stdout")
	}
}

func TestExecuteUnknownFlagExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), testRegistry(), s, BuildInfo{}, []string{"pwd", "--bogus-flag"})
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitUsage, errOut.String())
	}
	if errOut.String() == "" {
		t.Error("stderr is empty, want the flag error message")
	}
}

func TestExecuteUnknownCommandExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), testRegistry(), s, BuildInfo{}, []string{"no-such-command"})
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no-such-command") {
		t.Errorf("stderr = %q, want it to name the unknown command", errOut.String())
	}
}

func TestExecuteVersionExtraArgExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), testRegistry(), s, BuildInfo{}, []string{"version", "extra-arg"})
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitUsage, errOut.String())
	}
	if errOut.String() == "" {
		t.Error("stderr is empty, want the unexpected-argument message")
	}
}
