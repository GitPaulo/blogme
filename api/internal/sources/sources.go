package sources

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Source is one approved blog, as declared in sources/blogs.yml. The list is kept
// in Git so changes go through normal review rather than an admin service.
type Source struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Site  string `yaml:"site"`
	Feed  string `yaml:"feed,omitempty"`
	Notes string `yaml:"notes,omitempty"`
}

type file struct {
	Sources []Source `yaml:"sources"`
}

// Load reads and validates the source list at path.
func Load(path string) ([]Source, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sources: %w", err)
	}

	var parsed file
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse sources: %w", err)
	}

	seen := make(map[string]bool, len(parsed.Sources))
	for i, s := range parsed.Sources {
		switch {
		case s.ID == "":
			return nil, fmt.Errorf("source %d: id is required", i)
		case s.Site == "":
			return nil, fmt.Errorf("source %q: site is required", s.ID)
		case seen[s.ID]:
			return nil, fmt.Errorf("source %q: duplicate id", s.ID)
		}
		seen[s.ID] = true
	}

	return parsed.Sources, nil
}
