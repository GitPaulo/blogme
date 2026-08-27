package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GitPaulo/blogme/api/internal/blob"
)

// hnEndpoint is where site standing is read from.
//
// Popularity is the one thing about an article its own text cannot say: whether anyone
// found it worth passing on. There is no public source of per-article traffic and the
// paid estimates are per-domain anyway, so this uses the Hacker News search API, which
// is free, unauthenticated, and where this corpus's readers circulate. It asks by site
// rather than by article, which turns 600,000 lookups into 46,000.
// https://hn.algolia.com/api
//
// A variable only so the tests can point it at a local server; nothing in the service
// reassigns it.
var hnEndpoint = "https://hn.algolia.com/api/v1/search"

const (
	userAgent = "blogme/0.1 (+https://github.com/GitPaulo/blogme)"

	// Stories read per site. A blog's standing is set by whether it lands at all and
	// roughly how often; reading further down its tail does not change that.
	hnStories = 50

	// Sites asked about at once. Hacker News publishes no rate limit but does refuse
	// bursts, and there is no hurry, since nothing here is on a reader's path.
	sweepConcurrency = 8

	// The points total at which a site counts as fully established. Logarithmic below
	// it, because the difference between 0 and 200 points says far more than the
	// difference between 800 and 1,000.
	popularityCeiling = 500

	// How much of a response is read before giving up on it.
	maxHNBytes = 1 << 20
)

// Entry is what is known about one site.
type Entry struct {
	Points  int `json:"points"`
	Stories int `json:"stories"`
	// CheckedAt is set whether or not the lookup found anything, including when it
	// failed. A site left unmarked would stay at the head of the queue, be asked
	// about again every run, and starve every site behind it.
	CheckedAt time.Time `json:"checkedAt"`
}

// blobStore is what the popularity map needs of storage: one blob to read and write.
//
// An interface rather than *blob.Client so the map can be exercised without an Azure
// account behind it, matching how discovery takes its article store and how scoring
// takes its index.
type blobStore interface {
	DownloadString(ctx context.Context, container, name string) (string, error)
	Upload(ctx context.Context, container, name string, data []byte) error
}

// Store holds what is known about every site, backed by a single blob.
//
// One blob rather than one per site: the whole thing is a few megabytes, read once and
// written once per run, where a per-site layout would turn that into tens of thousands
// of requests to save nothing.
//
// What it produces can only add to a score. Most good blogs have never appeared on
// Hacker News at all, and reading that absence as a verdict would rank by fame.
type Store struct {
	client    blobStore
	container string
	name      string

	// Guards entries, which the sweep writes to from several goroutines at once.
	mu      sync.Mutex
	entries map[string]Entry
}

func NewStore(client blobStore, container, name string) *Store {
	return &Store{
		client:    client,
		container: container,
		name:      name,
		entries:   make(map[string]Entry),
	}
}

// Load reads what is already known.
//
// A store that has never been written is empty rather than an error: on the first run
// there is nothing to read, and since popularity only ever adds, knowing nothing is a
// valid state to score from.
func (s *Store) Load(ctx context.Context) error {
	raw, err := s.client.DownloadString(ctx, s.container, s.name)
	if errors.Is(err, blob.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read popularity: %w", err)
	}

	entries := make(map[string]Entry)
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return fmt.Errorf("parse popularity: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = entries
	return nil
}

func (s *Store) Save(ctx context.Context) error {
	s.mu.Lock()
	data, err := json.Marshal(s.entries)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal popularity: %w", err)
	}

	return s.client.Upload(ctx, s.container, s.name, data)
}

// Score is how well known the site behind a URL is, in [0, 1].
//
// A nil store scores everything zero, which is what the scorer sees when popularity
// has been switched off or never gathered. Since the figure only adds, that leaves
// every article judged on its own text alone.
func (s *Store) Score(rawURL string) float64 {
	if s == nil {
		return 0
	}

	site := siteOf(rawURL)
	if site == "" {
		return 0
	}

	s.mu.Lock()
	entry, ok := s.entries[site]
	s.mu.Unlock()
	if !ok || entry.Points <= 0 {
		return 0
	}

	return clamp01(math.Log1p(float64(entry.Points)) / math.Log1p(popularityCeiling))
}

// Stale returns up to limit sites that have gone longest without being asked about,
// those never asked about first.
func (s *Store) Stale(sites []string, limit int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	ordered := slices.Clone(sites)
	slices.SortFunc(ordered, func(a, b string) int {
		return s.entries[a].CheckedAt.Compare(s.entries[b].CheckedAt)
	})
	return ordered[:min(limit, len(ordered))]
}

// Sweep asks Hacker News about the sites given and records what it learns, reporting
// how many were read successfully.
func (s *Store) Sweep(ctx context.Context, client *http.Client, sites []string) int {
	var (
		wg   sync.WaitGroup
		sem  = make(chan struct{}, sweepConcurrency)
		read atomic.Int64
	)

	for _, site := range sites {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			entry := Entry{CheckedAt: time.Now().UTC()}
			points, stories, err := hackerNews(ctx, client, site)
			if err != nil {
				// Debug rather than warn: a sweep of thousands of third-party sites
				// will always have some failures, and a site that could not be read
				// this time simply keeps the score it had.
				slog.DebugContext(ctx, "popularity lookup failed", "site", site, "error", err)
			} else {
				entry.Points, entry.Stories = points, stories
				read.Add(1)
			}

			s.mu.Lock()
			s.entries[site] = entry
			s.mu.Unlock()
		}()
	}

	wg.Wait()
	return int(read.Load())
}

type hnResponse struct {
	Hits []struct {
		URL    string `json:"url"`
		Points int    `json:"points"`
	} `json:"hits"`
}

// hackerNews returns the points a site's stories have earned, and how many there are.
func hackerNews(ctx context.Context, client *http.Client, site string) (int, int, error) {
	params := url.Values{
		"query":                        {site},
		"restrictSearchableAttributes": {"url"},
		"tags":                         {"story"},
		"hitsPerPage":                  {strconv.Itoa(hnStories)},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hnEndpoint+"?"+params.Encode(), nil)
	if err != nil {
		return 0, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("call hacker news: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		return 0, 0, fmt.Errorf("hacker news returned %s", resp.Status)
	}

	var body hnResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxHNBytes)).Decode(&body); err != nil {
		return 0, 0, fmt.Errorf("decode response: %w", err)
	}

	// The search matches URLs loosely, so a query for one site also returns stories
	// from others that merely share a word with it. Only exact hosts are counted, or
	// a blog on a shared platform would inherit the whole platform's standing.
	points, stories := 0, 0
	for _, hit := range body.Hits {
		if siteOf(hit.URL) != site {
			continue
		}
		points += max(hit.Points, 0)
		stories++
	}
	return points, stories, nil
}

// siteOf reduces a URL to the host its writing lives under.
//
// The full host rather than the registrable domain, because thousands of sources in this
// corpus are subdomains of a handful of blogging platforms. Folding them together would
// hand every blog on bearblog.dev the standing of the most popular one on it.
func siteOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}
