// Package config loads and saves config.json, the defaults used by `add`.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Dryd33n/ephget/internal/fsutil"
	"github.com/Dryd33n/ephget/internal/ttl"
)

// Config holds the defaults for new apps.
type Config struct {
	TTL       string `json:"ttl"`
	KeepPrefs bool   `json:"keep_prefs"`
	// ShortcutDir is empty for the Desktop, whose location is looked up when used
	// because it can be redirected (to OneDrive, for example).
	ShortcutDir string `json:"shortcut_dir"`
	StartMenu   bool   `json:"start_menu"`
}

// Default is the configuration before the user changes anything.
func Default() Config {
	return Config{TTL: "7d", KeepPrefs: true, StartMenu: true}
}

// Load reads path. A missing file gives the defaults, as does a missing field.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if _, err := ttl.Parse(cfg.TTL); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Save writes path atomically.
func Save(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(path, append(data, '\n'))
}
