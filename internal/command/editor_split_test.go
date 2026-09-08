package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// $EDITOR is a command line, not a file name: it routinely carries flags, and
// on macOS the editor's own path has a space in it. Splitting on whitespace
// alone turned `"/Applications/My Editor/bin/ed" -w` into a hunt for a program
// called `"/Applications/My`.
func TestEditorArgsUnderstandsQuoting(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`vim`, []string{"vim"}},
		{`code --wait`, []string{"code", "--wait"}},
		{`  code   --wait  `, []string{"code", "--wait"}},
		{`"/Applications/My Editor/bin/ed" -w`, []string{"/Applications/My Editor/bin/ed", "-w"}},
		{`'/Applications/My Editor/bin/ed' -w`, []string{"/Applications/My Editor/bin/ed", "-w"}},
		{`/opt/ed --arg="a b"`, []string{"/opt/ed", "--arg=a b"}},
		{`emacsclient -a "" -c`, []string{"emacsclient", "-a", "", "-c"}},
	} {
		got, err := editorArgs(tc.in)
		if err != nil {
			t.Errorf("editorArgs(%q): %v", tc.in, err)
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("editorArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := editorArgs(`"/opt/my ed`); err == nil {
		t.Error("an unbalanced quote was accepted")
	}
}

// And end to end: an editor whose path holds a space, invoked through a quoted
// $EDITOR, has to be found and run.
func TestEditRunsAnEditorWhosePathHasASpace(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a","n":1}`)
	srv.JSON("PUT", "/mydb/doc1", 201, `{"ok":true,"id":"doc1","rev":"2-b"}`)

	dir := filepath.Join(t.TempDir(), "My Editor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "ed.sh")
	// The stub only rewrites the file when it was given the -w flag, so the
	// test also proves the flag survived the split.
	script := "#!/bin/sh\n[ \"$1\" = -w ] || exit 9\nprintf '{\"_id\":\"doc1\",\"n\":2}' > \"$2\"\n"
	if err := os.WriteFile(name, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", `"`+name+`" -w`)

	s := connected(t, srv)
	s.SetPath("/mydb")
	if _, err := invoke(t, Edit(), s, "doc1"); err != nil {
		t.Fatal(err)
	}
}
