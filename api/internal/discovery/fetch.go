package discovery

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/publicsuffix"
)

// maxRedirects bounds a redirect chain. Go's default of ten is more hops than
// any blog needs, and every extra hop is another chance to be walked somewhere
// unintended.
const maxRedirects = 5

// newCrawlClient builds the HTTP client every crawler fetch goes through.
//
// The crawler follows links handed to it by third parties — a feed decides where
// its own entries point — so without a guard it can be aimed at whatever the
// function app can reach on its own network, and up to a thousand words of
// whatever answers would be indexed and become publicly searchable. The check
// lives in the dialer's Control hook, which runs after DNS resolution against
// the address actually being connected to, so a hostname that resolves to a
// private address is caught however it was spelled, on the first request and on
// every redirect alike.
func newCrawlClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("refusing malformed address %q", address)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("refusing unresolved address %q", host)
			}
			if !isPublicIP(ip) {
				return fmt.Errorf("refusing non-public address %s", ip)
			}
			return nil
		},
	}).DialContext

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
}

// isPublicIP reports whether ip is routable on the public internet, which is the
// only place a blog can be.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}

	// Carrier-grade NAT is not private by Go's definition, but nothing inside it
	// is reachable from outside its operator either.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}

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
