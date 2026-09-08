// Package config reads and writes the cdb profile file and reaches the OS
// keyring for secrets. Secrets are never stored in the config file.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	ktoml "github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/confmap"
	kfile "github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Profile is one saved connection.
type Profile struct {
	// Name is the key in the profiles table. It is not stored inside the table.
	Name        string `koanf:"-"`
	URL         string `koanf:"url"`
	Auth        string `koanf:"auth"`
	Username    string `koanf:"username"`
	InsecureTLS bool   `koanf:"insecure_tls"`
	CAFile      string `koanf:"ca_file"`
}

// Output holds rendering preferences.
type Output struct {
	Format string `koanf:"format"`
	Color  string `koanf:"color"`
	Pager  string `koanf:"pager"`
}

// Shell holds line-editor preferences.
type Shell struct {
	Keymap string `koanf:"keymap"`
}

// Config is the whole config file.
type Config struct {
	Default  string
	Profiles map[string]Profile
	Output   Output
	Shell    Shell
}

// Defaults returns a config with no profiles and the documented defaults.
func Defaults() *Config {
	return &Config{
		Profiles: map[string]Profile{},
		Output:   Output{Format: "table", Color: "auto", Pager: "auto"},
		Shell:    Shell{Keymap: "emacs"},
	}
}

// Load reads a config file. A missing file is not an error: Load returns the
// defaults.
func Load(path string) (*Config, error) {
	c := Defaults()
	k := koanf.New(".")
	if err := k.Load(kfile.Provider(path), ktoml.Parser()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return nil, err
	}
	c.Default = k.String("default")
	if v := k.String("output.format"); v != "" {
		c.Output.Format = v
	}
	if v := k.String("output.color"); v != "" {
		c.Output.Color = v
	}
	if v := k.String("output.pager"); v != "" {
		c.Output.Pager = v
	}
	if v := k.String("shell.keymap"); v != "" {
		c.Shell.Keymap = v
	}
	for _, name := range k.MapKeys("profiles") {
		var p Profile
		if err := k.Unmarshal("profiles."+name, &p); err != nil {
			return nil, err
		}
		p.Name = name
		if p.Auth == "" {
			p.Auth = "session"
		}
		c.Profiles[name] = p
	}
	return c, nil
}

// Save writes the config file with 0600 permissions, creating its directory.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	profiles := map[string]any{}
	for name, p := range c.Profiles {
		profiles[name] = map[string]any{
			"url":          p.URL,
			"auth":         p.Auth,
			"username":     p.Username,
			"insecure_tls": p.InsecureTLS,
			"ca_file":      p.CAFile,
		}
	}
	m := map[string]any{
		"default":  c.Default,
		"profiles": profiles,
		"output": map[string]any{
			"format": c.Output.Format,
			"color":  c.Output.Color,
			"pager":  c.Output.Pager,
		},
		"shell": map[string]any{"keymap": c.Shell.Keymap},
	}
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(m, "."), nil); err != nil {
		return err
	}
	b, err := k.Marshal(ktoml.Parser())
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// Profile looks a profile up by name.
func (c *Config) Profile(name string) (Profile, bool) {
	p, ok := c.Profiles[name]
	return p, ok
}

// SetProfile adds or replaces a profile.
func (c *Config) SetProfile(p Profile) {
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	c.Profiles[p.Name] = p
}

// RemoveProfile deletes a profile and clears Default if it pointed at it.
func (c *Config) RemoveProfile(name string) bool {
	if _, ok := c.Profiles[name]; !ok {
		return false
	}
	delete(c.Profiles, name)
	if c.Default == name {
		c.Default = ""
	}
	return true
}

// ProfileNames returns every profile name, sorted.
func (c *Config) ProfileNames() []string {
	out := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ApplyOutputPrefs loads config.toml's [output] format/color/pager and
// [shell] keymap into s.Prefs. A config file that does not exist, or cannot
// be read, leaves Prefs untouched. Both front-ends call this before applying
// command-line flags or per-line overrides, so a flag still wins over the
// config file.
func ApplyOutputPrefs(s *session.Session) {
	cfgPath, err := ConfigPath()
	if err != nil {
		return
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		return
	}
	s.Prefs.Format = session.Format(cfg.Output.Format)
	s.Prefs.Color = session.ColorMode(cfg.Output.Color)
	s.Prefs.Pager = cfg.Output.Pager
	s.Prefs.Keymap = cfg.Shell.Keymap
}
