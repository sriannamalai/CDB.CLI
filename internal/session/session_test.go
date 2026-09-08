package session

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/path"
)

func newSession() *Session {
	return New(bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{})
}

func TestNewStartsDisconnectedAtRoot(t *testing.T) {
	s := newSession()
	if s.Connected() {
		t.Error("new session is connected, want disconnected")
	}
	if s.Path() != "/" {
		t.Errorf("Path() = %q, want %q", s.Path(), "/")
	}
	if s.Prompt() != "cdb> " {
		t.Errorf("Prompt() = %q, want %q", s.Prompt(), "cdb> ")
	}
}

func TestPromptWhenConnected(t *testing.T) {
	srv := couchtest.New(t)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthSession, Username: "admin", Secret: "password"})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession()
	s.Attach(c, "local")
	s.SetPath("/mydb")
	want := "admin@" + c.Host() + ":/mydb> "
	if s.Prompt() != want {
		t.Errorf("Prompt() = %q, want %q", s.Prompt(), want)
	}
	if !s.Connected() {
		t.Error("Connected() = false after Attach")
	}
	if err := s.Detach(); err != nil {
		t.Fatal(err)
	}
	if s.Connected() {
		t.Error("Connected() = true after Detach")
	}
	if s.Path() != "/" {
		t.Errorf("Path() after Detach = %q, want %q", s.Path(), "/")
	}
}

func TestResolveUsesCurrentPath(t *testing.T) {
	s := newSession()
	s.SetPath("/mydb")
	got, err := s.Resolve("doc1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != path.KindDocument || got.Database != "mydb" || got.DocID != "doc1" {
		t.Errorf("Resolve(\"doc1\") = %+v", got)
	}
}

func TestSetPathCanonicalises(t *testing.T) {
	s := newSession()
	s.SetPath("/mydb/")
	if s.Path() != "/mydb" {
		t.Errorf("Path() = %q, want %q", s.Path(), "/mydb")
	}
	s.SetPath("")
	if s.Path() != "/" {
		t.Errorf("Path() = %q, want %q", s.Path(), "/")
	}
}

func TestDefaultPrefs(t *testing.T) {
	p := DefaultPrefs()
	if p.Format != FormatTable || p.Color != ColorAuto || p.Pager != "auto" || p.Keymap != "emacs" {
		t.Errorf("DefaultPrefs() = %+v", p)
	}
}

func TestReaderIsSharedAcrossPrompts(t *testing.T) {
	s := newSession()
	s.SetStdin(strings.NewReader("2\ny\n"))
	first, err := s.Reader().ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Reader().ReadString('\n')
	if err != nil {
		t.Fatalf("second read failed: %v (a second bufio.Reader would see EOF here)", err)
	}
	if first != "2\n" || second != "y\n" {
		t.Errorf("reads = %q, %q; want %q, %q", first, second, "2\n", "y\n")
	}
}

func TestSetStdinResetsTheReader(t *testing.T) {
	s := newSession()
	s.SetStdin(strings.NewReader("first\n"))
	if _, err := s.Reader().ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	s.SetStdin(strings.NewReader("second\n"))
	line, err := s.Reader().ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "second\n" {
		t.Errorf("after SetStdin, read %q, want %q", line, "second\n")
	}
	if _, ok := s.Stdin().(*strings.Reader); !ok {
		t.Errorf("Stdin() = %T, want *strings.Reader", s.Stdin())
	}
}
