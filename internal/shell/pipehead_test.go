package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Issue #53.4: help, history and clear mutate nothing, so they may head a line
// of several stages — "history | .[]" is a useful thing to type.
func TestAReadOnlyShellCommandMayHeadAPipeline(t *testing.T) {
	var out bytes.Buffer
	sh := pipeShell(t, &out)
	for _, name := range []string{"help", "history", "clear"} {
		line := Line{Stages: []Stage{{Argv: []string{name}}, {Expr: ".line"}}}
		if err := sh.checkPipelineHead(line); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The commands that change the connection, the directory or the variables
// every stage shares still may not: a session is not safe for concurrent use.
func TestASessionCommandStillMayNotHeadAPipeline(t *testing.T) {
	var out bytes.Buffer
	sh := pipeShell(t, &out)
	for _, name := range []string{"connect", "profiles", "cd", "exit", "run", "set", "unset"} {
		line := Line{Stages: []Stage{{Argv: []string{name}}, {Expr: ".line"}}}
		err := sh.checkPipelineHead(line)
		if err == nil {
			t.Errorf("%s headed a pipeline", name)
			continue
		}
		if want := name + " cannot start a pipeline."; err.Error() != want {
			t.Errorf("%s: error = %q, want %q", name, err.Error(), want)
		}
	}
}

// And the whole line runs: the rows of such a command reach the stage below
// it. The stub stands in for the real "help", whose rows a test registry of
// two stub commands would not have.
func TestAReadOnlyShellCommandFeedsAPipeline(t *testing.T) {
	var out bytes.Buffer
	rows := command.Command{
		Name: "help", ShellOnly: true, MinArgs: 0, MaxArgs: 1,
		Run: func(context.Context, *session.Session, command.Invocation) (command.Result, error) {
			return command.Rows{
				Columns: []command.Column{{Title: "line"}},
				Items: []command.Row{
					{Cells: []string{"cd"}, JSON: json.RawMessage(`{"line":"cd"}`)},
					{Cells: []string{"ls"}, JSON: json.RawMessage(`{"line":"ls"}`)},
				},
			}, nil
		},
	}
	sh := pipeShell(t, &out, rows)
	if err := sh.RunLine(context.Background(), "help | .line"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, `"cd"`) || !strings.Contains(got, `"ls"`) {
		t.Errorf("output = %q, want both rows", got)
	}
}
