package config

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigDirUsesXDGConfigHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG variables are not used on Windows")
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgcfg")
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/tmp/xdgcfg", "cdb") {
		t.Errorf("ConfigDir() = %q, want %q", got, "/tmp/xdgcfg/cdb")
	}
}

func TestConfigDirFallsBackToDotConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG variables are not used on Windows")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/tmp/home")
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/tmp/home", ".config", "cdb") {
		t.Errorf("ConfigDir() = %q, want %q", got, "/tmp/home/.config/cdb")
	}
}

func TestStateDirFallsBackToLocalState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG variables are not used on Windows")
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/tmp/home")
	got, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("/tmp/home", ".local", "state", "cdb") {
		t.Errorf("StateDir() = %q, want %q", got, "/tmp/home/.local/state/cdb")
	}
}

func TestConfigAndHistoryPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG variables are not used on Windows")
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgcfg")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdgstate")
	cp, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if cp != "/tmp/xdgcfg/cdb/config.toml" {
		t.Errorf("ConfigPath() = %q", cp)
	}
	hp, err := HistoryPath()
	if err != nil {
		t.Fatal(err)
	}
	if hp != "/tmp/xdgstate/cdb/history" {
		t.Errorf("HistoryPath() = %q", hp)
	}
}
