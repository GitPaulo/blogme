package discovery

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

// Shared platforms put thousands of blogs on distinct subdomains of one server, so
// the limiter must key on the registrable domain rather than the hostname.
func TestFetcherLimitsPerRegistrableDomain(t *testing.T) {
	f := newFetcher(&http.Client{})

	var live, peak atomic.Int64
	var wg sync.WaitGroup

	for _, host := range []string{
		"emma.bearblog.dev", "cirro.bearblog.dev", "oop.bearblog.dev",
		"alex.bearblog.dev", "bob.bearblog.dev",
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := f.acquire(context.Background(), host)
			if err != nil {
				t.Error(err)
				return
			}
			n := live.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			live.Add(-1)
			release()
		}()
	}
	wg.Wait()

	if peak.Load() > maxPerDomain {
		t.Errorf("peak concurrency %d exceeded maxPerDomain %d for one domain", peak.Load(), maxPerDomain)
	}
}

func TestFetcherSeparatesDistinctDomains(t *testing.T) {
	f := newFetcher(&http.Client{})

	// Filling one domain's slots must not block a different domain.
	for range maxPerDomain {
		if _, err := f.acquire(context.Background(), "a.bearblog.dev"); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan struct{})
	go func() {
		if _, err := f.acquire(context.Background(), "example.com"); err != nil {
			t.Error(err)
		}
		close(done)
	}()

	<-done
}

func TestFetcherAcquireRespectsContext(t *testing.T) {
	f := newFetcher(&http.Client{})

	for range maxPerDomain {
		if _, err := f.acquire(context.Background(), "busy.example"); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := f.acquire(ctx, "busy.example"); err == nil {
		t.Error("acquire() should return the context error when saturated and cancelled")
	}
}

func TestFetcherRejectsNonHTTP(t *testing.T) {
	f := newFetcher(&http.Client{})
	if _, err := f.get(context.Background(), "file:///etc/passwd", 1024); err == nil {
		t.Error("get() must reject non-HTTP schemes")
	}
}
