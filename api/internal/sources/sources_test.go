package sources

import (
	"path/filepath"
	"testing"
)

func TestLoadRepositorySources(t *testing.T) {
	list, err := Load(filepath.Join("..", "..", "..", "sources", "blogs.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(list) == 0 {
		t.Fatal("Load() returned no sources")
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yml")); err == nil {
		t.Fatal("Load() expected an error for a missing file")
	}
}
