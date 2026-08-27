package discovery

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Whether a page can be framed is answered by how it is served rather than by what
// it says, so the headers have to survive the fetch that reads the body.
func TestFetcherReturnsResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		_, _ = io.WriteString(w, "<html><body>a post</body></html>")
	}))
	defer server.Close()

	body, header, err := newFetcher(server.Client()).fetch(t.Context(), server.URL, 1024)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(string(body), "a post") {
		t.Errorf("body = %q, want the page", body)
	}
	if !framingDenied(header) {
		t.Error("framingDenied(header) = false, want true for frame-ancestors 'none'")
	}
}

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

// A feed decides where its own entries point, so the crawler can be aimed at the
// function app's own network. Whatever answered would be indexed and become
// publicly searchable, which makes this the check that keeps a bad feed from
// reading the inside of the deployment out loud.
func TestIsPublicIPRejectsEverythingOffThePublicInternet(t *testing.T) {
	for _, raw := range []string{
		"127.0.0.1",       // loopback
		"::1",             // loopback, v6
		"10.1.2.3",        // private
		"172.16.5.4",      // private
		"192.168.0.1",     // private
		"169.254.169.254", // link-local, where cloud metadata lives
		"fd00::1",         // unique local, v6
		"fe80::1",         // link-local, v6
		"0.0.0.0",         // unspecified
		"224.0.0.1",       // multicast
		"100.64.0.1",      // carrier-grade NAT
		"100.127.255.255", // carrier-grade NAT, top of the range
	} {
		if ip := net.ParseIP(raw); ip == nil || isPublicIP(ip) {
			t.Errorf("isPublicIP(%s) allowed an address the crawler must never dial", raw)
		}
	}

	for _, raw := range []string{
		"93.184.216.34", // example.com
		"1.1.1.1",
		"2606:4700:4700::1111",
		"100.63.255.255", // just below the CGNAT range
		"100.128.0.1",    // just above it
	} {
		if ip := net.ParseIP(raw); ip == nil || !isPublicIP(ip) {
			t.Errorf("isPublicIP(%s) blocked an ordinary public address", raw)
		}
	}
}

// The platforms the cap exists for are the ones listed as public suffixes in their own
// right, so eTLD+1 alone reads every blog on them as its own registrable domain.
func TestLimiterKeyCollapsesSharedPlatforms(t *testing.T) {
	for host, want := range map[string]string{
		"emma.bearblog.dev":    "bearblog.dev",
		"cirro.bearblog.dev":   "bearblog.dev",
		"someone.github.io":    "github.io",
		"someone.blogspot.com": "blogspot.com",
		// An ordinary blog on its own domain still keys on the domain it registered.
		"www.example.com": "example.com",
		"example.com":     "example.com",
	} {
		if got := limiterKey(host); got != want {
			t.Errorf("limiterKey(%q) = %q, want %q", host, got, want)
		}
	}
}
