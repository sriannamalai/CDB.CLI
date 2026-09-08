package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func TestCompletionScriptsAreGenerated(t *testing.T) {
	for _, shellName := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shellName, func(t *testing.T) {
			var out, errOut bytes.Buffer
			s := session.New(strings.NewReader(""), &out, &errOut)
			code := Execute(context.Background(), command.Default(), s, BuildInfo{}, []string{"completion", shellName})
			if code != ExitOK {
				t.Fatalf("exit code = %d (stderr: %s)", code, errOut.String())
			}
			if out.Len() < 100 {
				t.Errorf("%s completion script is %d bytes, want a real script", shellName, out.Len())
			}
		})
	}
}

func TestCompletionCommandIsPresent(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	root := NewRoot(command.Default(), s, BuildInfo{})
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "completion" {
			found = true
		}
	}
	if !found {
		t.Error("the cobra tree has no completion command")
	}
}
