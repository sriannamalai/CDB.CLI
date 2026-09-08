package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// appName is the directory and keyring service name.
const appName = "cdb"

// ConfigDir returns the directory holding config.toml.
//
// Windows uses %APPDATA%\cdb. Everywhere else, including macOS, uses
// $XDG_CONFIG_HOME/cdb and falls back to ~/.config/cdb. This deliberately
// differs from Go's os.UserConfigDir and from github.com/adrg/xdg, both of
// which put macOS configuration under ~/Library/Application Support.
func ConfigDir() (string, error) {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, appName), nil
		}
		return "", errors.New("APPDATA is not set")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", appName), nil
}

// StateDir returns the directory holding mutable state such as history.
func StateDir() (string, error) {
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, appName), nil
		}
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, appName), nil
		}
		return "", errors.New("LOCALAPPDATA is not set")
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", appName), nil
}

// ConfigPath is the full path of config.toml.
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// HistoryPath is the full path of the shell history file.
func HistoryPath() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "history"), nil
}
