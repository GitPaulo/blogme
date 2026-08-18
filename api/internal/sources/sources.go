package sources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/GitPaulo/blogme/api/internal/blob"
)

// Source is one approved blog. The list is generated into sources/blogs.yml and
// published to blob storage, so updating it does not require redeploying the app.
type Source struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
	Site string `yaml:"site"`
	Feed string `yaml:"feed,omitempty"`
	// Kind is what sort of blog this is (personal, company), which its own pages
	// rarely say. Kept apart from Tags because it answers a different question and
	// is near-universal within a list, so as a subject it would drown the rest.
	Kind []string `yaml:"kind,omitempty"`
	Tags []string `yaml:"tags,omitempty"`
}

type file struct {
	Sources []Source `yaml:"sources"`
}

// Provider supplies the approved source list.
type Provider interface {
	Load(ctx context.Context) ([]Source, error)
}

// Parse decodes and validates a source list.
func Parse(raw []byte) ([]Source, error) {
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

// FileProvider reads the list from the local filesystem, used in development.
type FileProvider struct {
	Path string
}

func (p *FileProvider) Load(_ context.Context) ([]Source, error) {
	raw, err := os.ReadFile(p.Path)
	if err != nil {
		return nil, fmt.Errorf("read sources: %w", err)
	}
	return Parse(raw)
}

// BlobProvider reads the list from blob storage. The list is large, so the parsed
// result is cached and only re-read when the blob's ETag changes.
type BlobProvider struct {
	client    *blob.Client
	container string
	name      string

	mu     sync.Mutex
	etag   string
	cached []Source
}

func NewBlobProvider(client *blob.Client, container, name string) *BlobProvider {
	return &BlobProvider{client: client, container: container, name: name}
}

func (p *BlobProvider) Load(ctx context.Context) ([]Source, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cached != nil {
		etag, err := p.client.ETag(ctx, p.container, p.name)
		if err != nil && !errors.Is(err, blob.ErrNotFound) {
			return nil, err
		}
		if err == nil && etag == p.etag {
			return p.cached, nil
		}
	}

	raw, etag, err := p.client.Download(ctx, p.container, p.name)
	if err != nil {
		return nil, err
	}

	list, err := Parse(raw)
	if err != nil {
		return nil, err
	}

	p.cached, p.etag = list, etag
	return list, nil
}
