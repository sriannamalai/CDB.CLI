package command

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func TestConfirmAcceptsYes(t *testing.T) {
	var out bytes.Buffer
	s := session.New(strings.NewReader("y\n"), &out, &out)
	s.Prefs.Interactive = true
	if err := Confirm(s, "Delete doc1?"); err != nil {
		t.Fatalf("Confirm = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "Delete doc1?") {
		t.Errorf("prompt was not printed: %q", out.String())
	}
}

func TestConfirmRejectsAnythingElse(t *testing.T) {
	s := session.New(strings.NewReader("n\n"), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = true
	if err := Confirm(s, "Delete doc1?"); !errors.Is(err, ErrDeclined) {
		t.Fatalf("Confirm = %v, want ErrDeclined", err)
	}
}

func TestConfirmSkippedByYes(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Yes = true
	if err := Confirm(s, "Delete doc1?"); err != nil {
		t.Fatalf("Confirm with --yes = %v, want nil", err)
	}
}

func TestConfirmWithYesPrintsNothing(t *testing.T) {
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Yes = true
	if err := Confirm(s, "Delete doc1?"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("Confirm with --yes printed %q", out.String())
	}
}

func TestConfirmOnANonTerminalWithoutYesIsAUsageError(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = false
	err := Confirm(s, "Delete doc1?")
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
	if err := Confirm(s, "Really?"); err != nil {
		t.Fatalf("first Confirm = %v", err)
	}
	if err := ConfirmPhrase(s, "Type the database name", "mydb"); err != nil {
		t.Fatalf("second prompt = %v, want nil", err)
	}
}

func TestConfirmPhraseRequiresAnExactMatch(t *testing.T) {
	s := session.New(strings.NewReader("wrong\n"), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = true
	if err := ConfirmPhrase(s, "Type the database name", "mydb"); !errors.Is(err, ErrDeclined) {
		t.Fatalf("ConfirmPhrase = %v, want ErrDeclined", err)
	}
	s2 := session.New(strings.NewReader("mydb\n"), &bytes.Buffer{}, &bytes.Buffer{})
	s2.Prefs.Interactive = true
	if err := ConfirmPhrase(s2, "Type the database name", "mydb"); err != nil {
		t.Fatalf("ConfirmPhrase = %v, want nil", err)
	}
}

func TestConfirmPhraseOnANonTerminalWithoutYesIsAUsageError(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = false
	if _, ok := ConfirmPhrase(s, "Type the database name", "mydb").(*UsageError); !ok {
		t.Fatal("ConfirmPhrase did not return a *UsageError")
	}
}

func TestConfirmOnClosedInputDeclines(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Interactive = true
	if err := Confirm(s, "Delete doc1?"); !errors.Is(err, ErrDeclined) {
		t.Fatalf("Confirm at EOF = %v, want ErrDeclined", err)
	}
}
