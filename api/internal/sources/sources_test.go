package sources

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRepositorySources(t *testing.T) {
	p := &FileProvider{Path: filepath.Join("..", "..", "..", "sources", "blogs.yml")}
	list, err := p.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(list) == 0 {
		t.Fatal("Load() returned no sources")
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	p := &FileProvider{Path: filepath.Join(t.TempDir(), "absent.yml")}
	if _, err := p.Load(context.Background()); err == nil {
		t.Fatal("Load() expected an error for a missing file")
	}
}

func TestParseRejectsDuplicateID(t *testing.T) {
	_, err := Parse([]byte("sources:\n  - id: a\n    site: https://a.example\n  - id: a\n    site: https://b.example\n"))
	if err == nil {
		t.Fatal("Parse() expected an error for a duplicate id")
	}
}

func TestParseRejectsMissingSite(t *testing.T) {
	if _, err := Parse([]byte("sources:\n  - id: a\n")); err == nil {
		t.Fatal("Parse() expected an error for a missing site")
	}
}

func TestFileProviderReadsTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blogs.yml")
	if err := os.WriteFile(path, []byte("sources:\n  - id: a\n    site: https://a.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := (&FileProvider{Path: path}).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != "a" {
		t.Fatalf("Load() = %+v, want one source with id 'a'", list)
	}
}
