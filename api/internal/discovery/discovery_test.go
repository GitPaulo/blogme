package discovery

import (
	"testing"

	"github.com/GitPaulo/blogme/api/internal/sources"
)

func list(ids ...string) []sources.Source {
	out := make([]sources.Source, len(ids))
	for i, id := range ids {
		out[i] = sources.Source{ID: id}
	}
	return out
}

func ids(in []sources.Source) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = s.ID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestResumeIndex(t *testing.T) {
	l := list("a", "b", "c")

	tests := []struct {
		name   string
		cursor string
		want   int
	}{
		{"no cursor starts at the beginning", "", 0},
		{"resumes after the recorded source", "a", 1},
		{"wraps after the last source", "c", 0},
		{"unknown cursor restarts", "removed", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resumeIndex(l, tt.cursor); got != tt.want {
				t.Errorf("resumeIndex(%q) = %d, want %d", tt.cursor, got, tt.want)
			}
		})
	}
}

func TestSliceWrapsAround(t *testing.T) {
	l := list("a", "b", "c", "d")

	batch, next := slice(l, 2, 3)
	if want := []string{"c", "d", "a"}; !equal(ids(batch), want) {
		t.Errorf("slice() = %v, want %v", ids(batch), want)
	}
	if next != "a" {
		t.Errorf("next cursor = %q, want %q", next, "a")
	}
}

func TestSliceCapsAtListLength(t *testing.T) {
	l := list("a", "b")

	batch, next := slice(l, 0, 10)
	if len(batch) != 2 {
		t.Errorf("slice() returned %d sources, want 2", len(batch))
	}
	if next != "b" {
		t.Errorf("next cursor = %q, want %q", next, "b")
	}
}

// Successive batches must eventually cover every source.
func TestSliceCoversAllSourcesOverMultipleRuns(t *testing.T) {
	l := list("a", "b", "c", "d", "e")
	seen := map[string]bool{}

	cursor := ""
	for range 3 {
		batch, next := slice(l, resumeIndex(l, cursor), 2)
		for _, s := range batch {
			seen[s.ID] = true
		}
		cursor = next
	}

	for _, s := range l {
		if !seen[s.ID] {
			t.Errorf("source %q was never processed", s.ID)
		}
	}
}
