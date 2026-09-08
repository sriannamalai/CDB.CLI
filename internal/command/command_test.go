package command

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func TestRegistryLookupByNameAndAlias(t *testing.T) {
	r := NewRegistry()
	r.Register(Command{
		Name:    "list",
		Aliases: []string{"ls", "dir"},
		Summary: "list things",
		Run: func(context.Context, *session.Session, Invocation) (Result, error) {
			return Message{Text: "ok"}, nil
		},
	})
	for _, n := range []string{"list", "ls", "dir"} {
		if _, ok := r.Lookup(n); !ok {
			t.Errorf("Lookup(%q) = not found", n)
		}
	}
	if _, ok := r.Lookup("nope"); ok {
		t.Error("Lookup(\"nope\") found a command")
	}
	names := r.Names()
	if len(names) != 1 || names[0] != "list" {
		t.Errorf("Names() = %v, want [list]", names)
	}
}

func TestRegistryNamesAreSorted(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"zoo", "alpha", "mid"} {
		r.Register(Command{Name: n})
	}
	got := r.Names()
	want := []string{"alpha", "mid", "zoo"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register did not panic on a duplicate name")
		}
	}()
	r := NewRegistry()
	r.Register(Command{Name: "dup"})
	r.Register(Command{Name: "dup"})
}

func TestCheckArgs(t *testing.T) {
	c := Command{Name: "cat", MinArgs: 1, MaxArgs: 2}
	if err := c.CheckArgsErr(nil); err == nil {
		t.Error("CheckArgsErr(nil) = nil, want a UsageError")
	}
	if err := c.CheckArgsErr([]string{"a"}); err != nil {
		t.Errorf("CheckArgsErr([a]) = %v, want nil", err)
	}
	if err := c.CheckArgsErr([]string{"a", "b", "c"}); err == nil {
		t.Error("CheckArgsErr([a b c]) = nil, want a UsageError")
	}
	unbounded := Command{Name: "x", MinArgs: 0, MaxArgs: -1}
	if err := unbounded.CheckArgsErr([]string{"a", "b", "c", "d"}); err != nil {
		t.Errorf("unbounded CheckArgsErr = %v, want nil", err)
	}
}

func TestNewFlagSetAppliesFlagsFunc(t *testing.T) {
	r := NewRegistry()
	c := Command{
		Name: "ls",
		Flags: func(fs *pflag.FlagSet) {
			fs.Int("limit", 20, "how many rows")
			fs.Bool("all", false, "stream everything")
		},
	}
	r.Register(c)
	fs := r.NewFlagSet(c)
	if err := fs.Parse([]string{"--limit", "5", "/mydb"}); err != nil {
		t.Fatal(err)
	}
	inv := Invocation{Args: fs.Args(), Flags: fs}
	if inv.Int("limit") != 5 {
		t.Errorf("Int(\"limit\") = %d, want 5", inv.Int("limit"))
	}
	if inv.Bool("all") {
		t.Error("Bool(\"all\") = true, want false")
	}
	if !inv.Changed("limit") {
		t.Error("Changed(\"limit\") = false, want true")
	}
	if inv.Changed("all") {
		t.Error("Changed(\"all\") = true, want false")
	}
	if inv.Arg(0) != "/mydb" {
		t.Errorf("Arg(0) = %q, want %q", inv.Arg(0), "/mydb")
	}
	if inv.Arg(9) != "" {
		t.Errorf("Arg(9) = %q, want empty", inv.Arg(9))
	}
}

func TestResultKinds(t *testing.T) {
	for _, tc := range []struct {
		res  Result
		want string
	}{
		{Empty{}, "empty"},
		{Message{Text: "hi"}, "message"},
		{Document{}, "document"},
		{Rows{}, "rows"},
		{Stream{}, "stream"},
		{Raw{}, "raw"},
	} {
		if got := tc.res.ResultKind(); got != tc.want {
			t.Errorf("%T.ResultKind() = %q, want %q", tc.res, got, tc.want)
		}
	}
}

func TestReplaceSwapsACommandInPlace(t *testing.T) {
	r := NewRegistry()
	r.Register(Command{Name: "history", Summary: "old"})
	r.Register(Command{Name: "pwd"})
	r.Replace(Command{Name: "history", Summary: "new"})
	c, ok := r.Lookup("history")
	if !ok || c.Summary != "new" {
		t.Errorf("Lookup(\"history\") = %+v, ok=%v; want the new command", c, ok)
	}
	names := r.Names()
	if len(names) != 2 || names[0] != "history" || names[1] != "pwd" {
		t.Errorf("Names() = %v, want [history pwd]", names)
	}
}

func TestReplacePanicsWhenNothingIsRegistered(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Replace did not panic for an unregistered name")
		}
	}()
	NewRegistry().Replace(Command{Name: "nope"})
}

func TestJSONObjectUsesLowerCaseKeys(t *testing.T) {
	got := string(jsonObject("name", "mydb", "docs", "42"))
	want := `{"docs":"42","name":"mydb"}`
	if got != want {
		t.Errorf("jsonObject = %s, want %s", got, want)
	}
}

func TestDefaultRegistryHasPwd(t *testing.T) {
	if _, ok := Default().Lookup("pwd"); !ok {
		t.Error("Default() has no pwd command")
	}
}

// TestEveryCommandHasSummaryAndExample keeps the generated reference pages and
// the two help front-ends honest: a command with no summary or no worked
// example documents itself as a blank line.
func TestEveryCommandHasSummaryAndExample(t *testing.T) {
	for _, c := range Default().All() {
		if c.Summary == "" {
			t.Errorf("command %q has no Summary", c.Name)
		}
		if c.ShellOnly {
			continue
		}
		if c.Example == "" {
			t.Errorf("command %q has no Example", c.Name)
			continue
		}
		if strings.TrimSpace(c.Example) != c.Example {
			t.Errorf("command %q: Example has leading or trailing whitespace", c.Name)
		}
	}
}
