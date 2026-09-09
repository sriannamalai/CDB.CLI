package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func TestConfirmAcceptsYes(t *testing.T) {
	var out bytes.Buffer
	s := session.New(strings.NewReader("y\n"), &out, &out)
	s.Prefs.Interactive = true
	if err := Confirm(context.Background(), s, "Delete doc1?"); err != nil {
		t.Fatalf("Confirm = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "Delete doc1?") {
		t.Errorf("prompt was not printed: %q", out.String())
	}
}

func TestConfirmRejectsAnythingElse(t *testing.T) {
	s := session.New(strings.NewReader("n\n"), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = true
	if err := Confirm(context.Background(), s, "Delete doc1?"); !errors.Is(err, ErrDeclined) {
		t.Fatalf("Confirm = %v, want ErrDeclined", err)
	}
}

func TestConfirmSkippedByYes(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Yes = true
	if err := Confirm(context.Background(), s, "Delete doc1?"); err != nil {
		t.Fatalf("Confirm with --yes = %v, want nil", err)
	}
}

func TestConfirmWithYesPrintsNothing(t *testing.T) {
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Yes = true
	if err := Confirm(context.Background(), s, "Delete doc1?"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("Confirm with --yes printed %q", out.String())
	}
}

func TestConfirmOnANonTerminalWithoutYesIsAUsageError(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = false
	err := Confirm(context.Background(), s, "Delete doc1?")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("Confirm = %#v, want *UsageError", err)
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error = %v, want it to name --yes", err)
	}
}

func TestConfirmReadsThroughTheSharedReader(t *testing.T) {
	// Two prompts in one command must not lose input: the first prompt's
	// bufio.Reader buffers both lines, so the second must read the same one.
	s := session.New(strings.NewReader("y\nmydb\n"), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = true
	if err := Confirm(context.Background(), s, "Really?"); err != nil {
		t.Fatalf("first Confirm = %v", err)
	}
	if err := ConfirmPhrase(context.Background(), s, "Type the database name", "mydb"); err != nil {
		t.Fatalf("second prompt = %v, want nil", err)
	}
}

func TestConfirmPhraseRequiresAnExactMatch(t *testing.T) {
	s := session.New(strings.NewReader("wrong\n"), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = true
	if err := ConfirmPhrase(context.Background(), s, "Type the database name", "mydb"); !errors.Is(err, ErrDeclined) {
		t.Fatalf("ConfirmPhrase = %v, want ErrDeclined", err)
	}
	s2 := session.New(strings.NewReader("mydb\n"), &bytes.Buffer{}, &bytes.Buffer{})
	s2.Prefs.Interactive = true
	if err := ConfirmPhrase(context.Background(), s2, "Type the database name", "mydb"); err != nil {
		t.Fatalf("ConfirmPhrase = %v, want nil", err)
	}
}

func TestConfirmPhraseOnANonTerminalWithoutYesIsAUsageError(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = false
	if _, ok := ConfirmPhrase(context.Background(), s, "Type the database name", "mydb").(*UsageError); !ok {
		t.Fatal("ConfirmPhrase did not return a *UsageError")
	}
}

func TestConfirmOnClosedInputDeclines(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = true
	if err := Confirm(context.Background(), s, "Delete doc1?"); !errors.Is(err, ErrDeclined) {
		t.Fatalf("Confirm at EOF = %v, want ErrDeclined", err)
	}
}

// confirmCase is one row of the table in the design: what the operator did at
// the prompt, and what the helper must return for it.
type confirmCase struct {
	name    string
	input   string
	cancel  bool
	wantErr error // nil, ErrDeclined, or context.Canceled
}

func confirmCases() []confirmCase {
	return []confirmCase{
		{name: "accepted", input: "y\n", wantErr: nil},
		{name: "accepted in full", input: "yes\n", wantErr: nil},
		{name: "declined", input: "n\n", wantErr: ErrDeclined},
		{name: "end of file", input: "", wantErr: ErrDeclined},
		{name: "interrupted", input: "\n", cancel: true, wantErr: context.Canceled},
	}
}

func TestConfirm(t *testing.T) {
	for _, tc := range confirmCases() {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			s := session.New(strings.NewReader(tc.input), &out, &out)
			s.Prefs.Interactive = true
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err := Confirm(ctx, s, "Delete it?")
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestConfirmPhrase(t *testing.T) {
	// The accepted answer is the phrase itself, not "y".
	cases := []confirmCase{
		{name: "phrase typed", input: "mydb\n", wantErr: nil},
		{name: "wrong phrase", input: "mydc\n", wantErr: ErrDeclined},
		{name: "yes is not the phrase", input: "y\n", wantErr: ErrDeclined},
		{name: "end of file", input: "", wantErr: ErrDeclined},
		{name: "interrupted", input: "\n", cancel: true, wantErr: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			s := session.New(strings.NewReader(tc.input), &out, &out)
			s.Prefs.Interactive = true
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err := ConfirmPhrase(ctx, s, "Type the database name to destroy it", "mydb")
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestConfirmInterruptedSaysNothing pins the half of the behaviour the exit
// code cannot express: an interrupted prompt prints no verdict, because the
// operator already knows what they did.
func TestConfirmInterruptedSaysNothing(t *testing.T) {
	var out bytes.Buffer
	s := session.New(strings.NewReader("\n"), &out, &out)
	s.Prefs.Interactive = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Confirm(ctx, s, "Delete it?"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if strings.Contains(out.String(), "Cancelled") {
		t.Errorf("an interrupted prompt printed a verdict: %q", out.String())
	}
}

// TestConfirmYesFlagSkipsTheRead keeps --yes ahead of everything, including a
// cancelled context: there is no prompt to interrupt.
func TestConfirmYesFlagSkipsTheRead(t *testing.T) {
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Yes = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Confirm(ctx, s, "Delete it?"); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

// cancelAtPrompt is a stdin that cancels the command's context as the prompt
// reads it. That is what Ctrl-C at a confirmation looks like from inside the
// helpers: the read is already blocked when the signal lands, so it comes back
// with whatever the terminal had rather than with an error, and the cancelled
// context is the only record that the operator asked to stop. Cancelling
// before the command runs would not do — the request the command makes on the
// way to the prompt would fail first, and the test would pass without ever
// reaching a prompt.
type cancelAtPrompt struct {
	cancel context.CancelFunc
	rest   io.Reader
}

func (c *cancelAtPrompt) Read(p []byte) (int, error) {
	c.cancel()
	return c.rest.Read(p)
}
