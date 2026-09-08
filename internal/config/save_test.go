package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// os.WriteFile applies its mode only when it creates the file, so a
// config.toml the operator made themselves — or copied out of a dotfiles repo
// — kept whatever mode it had, and the profiles stored in it became
// world-readable. Save must end at 0600 whatever it started at.
func TestSaveTightensThePermissionsOfAnExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("default = \"old\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Defaults()
	c.SetProfile(Profile{Name: "local", URL: "http://localhost:5984", Auth: "session"})
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config file mode = %v, want 0600", fi.Mode().Perm())
	}
}

// A crash between truncating and writing used to lose every profile. Save
// writes a sibling and renames, so the file at the path is always one complete
// version or the other — and no temp file is left behind.
func TestSaveLeavesNoTemporaryFileBehind(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	c := Defaults()
	c.SetProfile(Profile{Name: "local", URL: "http://localhost:5984", Auth: "session"})
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.toml" {
			t.Errorf("Save left %q in the config directory", e.Name())
		}
	}
	back, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := back.Profile("local"); !ok {
		t.Errorf("profiles = %v, want the saved one", back.Profiles)
	}
}

// Re-saving over an existing config replaces it rather than appending to it.
func TestSaveReplacesTheWholeFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	c := Defaults()
	c.SetProfile(Profile{Name: "one", URL: "http://one:5984", Auth: "session"})
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	c.RemoveProfile("one")
	c.SetProfile(Profile{Name: "two", URL: "http://two:5984", Auth: "session"})
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "one:5984") {
		t.Errorf("the removed profile survived the rewrite:\n%s", raw)
	}
}
