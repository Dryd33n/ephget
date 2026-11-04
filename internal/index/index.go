// Package index parses and searches the catalog. It does no network access,
// so ephgetw can't pull in an HTTP client through it.
package index

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// SchemaVersion is the only index format this client understands.
const SchemaVersion = 1

// Installer is one architecture's download.
type Installer struct {
	Arch       string   `json:"arch"`
	Type       string   `json:"type"`
	URL        string   `json:"url"`
	SHA256     string   `json:"sha256"`
	Exe        string   `json:"exe"`
	ExtractDir string   `json:"extract_dir"`
	Rename     []string `json:"rename"`
}

// Package is one app.
type Package struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Bucket      string      `json:"bucket"`
	Version     string      `json:"version"`
	Description string      `json:"description"`
	Persist     []string    `json:"persist"`
	Installers  []Installer `json:"installers"`
}

// Index is the whole catalog.
type Index struct {
	Schema    int       `json:"schema"`
	Generated string    `json:"generated"`
	Packages  []Package `json:"packages"`
}

// Parse decodes a gzipped index and refuses any other schema version.
func Parse(gz []byte) (*Index, error) {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("index is not gzip data: %w", err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("index is damaged: %w", err)
	}
	// Check the version first: a newer format may not fit the fields below.
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("index is damaged: %w", err)
	}
	if head.Schema != SchemaVersion {
		return nil, fmt.Errorf("index uses format %d but this ephget only reads format %d; update ephget", head.Schema, SchemaVersion)
	}
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("index is damaged: %w", err)
	}
	return &idx, nil
}

// Load reads and parses a local index file.
func Load(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Find returns the app with this ID, ignoring case, or nil.
func (idx *Index) Find(id string) *Package {
	for i := range idx.Packages {
		if strings.EqualFold(idx.Packages[i].ID, id) {
			return &idx.Packages[i]
		}
	}
	return nil
}

// ErrNoInstaller means an app has no build this machine can run.
var ErrNoInstaller = errors.New("no build for this machine")

// SelectInstaller picks the build for a machine, "x64" or "arm64". arm64 prefers
// a native build, then an architecture-independent one, then x64 under emulation.
func (p *Package) SelectInstaller(machine string) (*Installer, error) {
	order := []string{"x64", "any"}
	if machine == "arm64" {
		order = []string{"arm64", "any", "x64"}
	}
	for _, arch := range order {
		if i := slices.IndexFunc(p.Installers, func(in Installer) bool { return in.Arch == arch }); i >= 0 {
			return &p.Installers[i], nil
		}
	}
	return nil, fmt.Errorf("%s: %w (%s)", p.ID, ErrNoInstaller, machine)
}
