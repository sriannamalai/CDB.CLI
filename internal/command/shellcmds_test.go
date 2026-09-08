package command

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// runHelp runs the help command from a default registry and returns its result.
func runHelp(t *testing.T, args ...string) Result {
	t.Helper()
	reg := Default()
	c, ok := reg.Lookup("help")
	if !ok {
		t.Fatal("the default registry has no help command")
	}
	s := session.New(strings.NewReader(""), &strings.Builder{}, &strings.Builder{})
	res, err := c.Run(context.Background(), s, Invocation{Args: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// helpRow finds the table row help produced for one command.
func helpRow(t *testing.T, rows Rows, name string) Row {
	t.Helper()
	for _, item := range rows.Items {
		if len(item.Cells) > 0 && item.Cells[0] == name {
			return item
		}
	}
	t.Fatalf("help listed no row for %q", name)
	return Row{}
}

func TestHelpMarksDestructiveCommands(t *testing.T) {
	rows, ok := runHelp(t).(Rows)
	if !ok {
		t.Fatal("help did not return a table")
	}
	for _, name := range []string{"rm", "rmdir", "resolve"} {
		row := helpRow(t, rows, name)
		if !strings.Contains(strings.Join(row.Cells, " "), destructiveMarker) {
			t.Errorf("help row for %q is missing the destructive marker: %v", name, row.Cells)
		}
		var payload struct {
			Destructive bool `json:"destructive"`
		}
		if err := json.Unmarshal(row.JSON, &payload); err != nil {
			t.Fatalf("help row for %q has unreadable JSON: %v", name, err)
		}
		if !payload.Destructive {
			t.Errorf("help JSON for %q has destructive=false: %s", name, row.JSON)
		}
	}

	row := helpRow(t, rows, "pwd")
	if strings.Contains(strings.Join(row.Cells, " "), destructiveMarker) {
		t.Errorf("help marked the harmless pwd command as destructive: %v", row.Cells)
	}
	var payload struct {
		Command     string `json:"command"`
		Summary     string `json:"summary"`
		Destructive bool   `json:"destructive"`
	}
	if err := json.Unmarshal(row.JSON, &payload); err != nil {
		t.Fatalf("help row for pwd has unreadable JSON: %v", err)
	}
	if payload.Destructive {
		t.Errorf("help JSON marked pwd destructive: %s", row.JSON)
	}
	if payload.Command != "pwd" || payload.Summary == "" {
		t.Errorf("help JSON for pwd lost its other fields: %s", row.JSON)
	}
}

func TestHelpExplainsThatACommandIsDestructive(t *testing.T) {
	msg, ok := runHelp(t, "rm").(Message)
	if !ok {
		t.Fatal("help rm did not return a message")
	}
	if !strings.Contains(msg.Text, "destructive") {
		t.Errorf("help rm does not say the command is destructive:\n%s", msg.Text)
	}
	if !strings.Contains(msg.Text, "--yes") {
		t.Errorf("help rm does not mention --yes:\n%s", msg.Text)
	}

	msg, ok = runHelp(t, "pwd").(Message)
	if !ok {
		t.Fatal("help pwd did not return a message")
	}
	if strings.Contains(msg.Text, "destructive") {
		t.Errorf("help pwd calls a harmless command destructive:\n%s", msg.Text)
	}
}
