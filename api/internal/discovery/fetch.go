package discovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"golang.org/x/net/publicsuffix"
)

// Shared blogging platforms host thousands of the sources: bearblog.dev alone
// accounts for over a thousand, plus github.io and blogspot.com. Limiting by
// hostname would not help, because each blog is its own subdomain of one server, so
// concurrency is capped per registrable domain instead.
const maxPerDomain = 2

// fetcher performs bounded HTTP GETs while keeping concurrent load on any single
// operator low.
type fetcher struct {
	client *http.Client

	mu      sync.Mutex
	domains map[string]chan struct{}
}

func newFetcher(client *http.Client) *fetcher {
	return &fetcher{client: client, domains: make(map[string]chan struct{})}
}

// get retrieves rawURL, reading at most limit bytes.
func (f *fetcher) get(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if !isHTTP(u) {
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}

	release, err := f.acquire(ctx, u.Hostname())
	if err != nil {
		return nil, err
	}
	defer release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	// Deliberately no Accept-Encoding: setting it manually disables the transport's
	// transparent gzip decompression, handing back compressed bytes.

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %s", resp.Status)
	}

	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (f *fetcher) acquire(ctx context.Context, host string) (func(), error) {
	key, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil || key == "" {
		key = host
	}

	f.mu.Lock()
	slot, ok := f.domains[key]
	if !ok {
		slot = make(chan struct{}, maxPerDomain)
		f.domains[key] = slot
	}
	f.mu.Unlock()

	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
