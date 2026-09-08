package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sample = `default = "local"

[profiles.local]
url = "http://localhost:5984"
auth = "session"
username = "admin"
insecure_tls = false
ca_file = ""

[profiles.staging]
url = "https://couch.example.com"
auth = "jwt"

[output]
format = "json"
color = "never"
pager = "off"

[shell]
keymap = "vi"
`

func TestLoadParsesEverything(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Default != "local" {
		t.Errorf("Default = %q", c.Default)
	}
	local, ok := c.Profile("local")
	if !ok {
		t.Fatal("profile local is missing")
	}
	if local.Name != "local" || local.URL != "http://localhost:5984" || local.Auth != "session" || local.Username != "admin" {
		t.Errorf("local = %+v", local)
	}
	staging, _ := c.Profile("staging")
	if staging.Auth != "jwt" {
		t.Errorf("staging.Auth = %q", staging.Auth)
	}
	if c.Output.Format != "json" || c.Output.Color != "never" || c.Output.Pager != "off" {
		t.Errorf("Output = %+v", c.Output)
	}
	if c.Shell.Keymap != "vi" {
		t.Errorf("Shell.Keymap = %q", c.Shell.Keymap)
	}
	names := c.ProfileNames()
	if len(names) != 2 || names[0] != "local" || names[1] != "staging" {
		t.Errorf("ProfileNames() = %v, want sorted [local staging]", names)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load of a missing file returned %v, want defaults and nil", err)
	}
	if c.Output.Format != "table" || c.Output.Color != "auto" || c.Output.Pager != "auto" || c.Shell.Keymap != "emacs" {
		t.Errorf("defaults = %+v / %+v", c.Output, c.Shell)
	}
	if len(c.Profiles) != 0 {
		t.Errorf("Profiles = %v, want empty", c.Profiles)
	}
}

func TestSaveRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "config.toml")
	c := Defaults()
	c.Default = "prod"
	c.SetProfile(Profile{Name: "prod", URL: "https://couch.example.com", Auth: "session", Username: "svc"})
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("config file mode = %v, want 0600", fi.Mode().Perm())
	}
	back, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := back.Profile("prod")
	if !ok || got.URL != "https://couch.example.com" || got.Username != "svc" {
		t.Errorf("round trip = %+v, ok=%v", got, ok)
	}
	body, _ := os.ReadFile(p)
	if strings.Contains(string(body), "password") || strings.Contains(string(body), "secret") || strings.Contains(string(body), "token") {
		t.Errorf("config file contains a secret-looking key:\n%s", body)
	}
}

func TestRemoveProfile(t *testing.T) {
	c := Defaults()
	c.SetProfile(Profile{Name: "a", URL: "http://a"})
	if !c.RemoveProfile("a") {
		t.Error("RemoveProfile(\"a\") = false, want true")
	}
	if c.RemoveProfile("a") {
		t.Error("RemoveProfile on a missing profile = true, want false")
	}
}

func TestRemoveProfileClearsDefault(t *testing.T) {
	c := Defaults()
	c.SetProfile(Profile{Name: "a", URL: "http://a"})
	c.Default = "a"
	c.RemoveProfile("a")
	if c.Default != "" {
		t.Errorf("Default = %q after removing the default profile, want empty", c.Default)
	}
}
