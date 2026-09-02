package discovery

import (
	"context"
	"errors"
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

const (
	// maxRedirects bounds a redirect chain. Go's default of ten is more hops than any
	// blog needs, and every extra hop is another chance to be walked somewhere
	// unintended.
	maxRedirects = 5

	// Shared blogging platforms host thousands of the sources: bearblog.dev alone
	// accounts for over a thousand, plus github.io and blogspot.com. Limiting by
	// hostname would not help, because each blog is its own subdomain of one server, so
	// concurrency is capped per registrable domain instead.
	maxPerDomain = 2
)

// newCrawlClient builds the HTTP client every crawler fetch goes through.
//
// The crawler follows links handed to it by third parties, since a feed decides where
// its own entries point, so without a guard it can be aimed at whatever the function
// app can reach on its own network and up to a thousand words of the answer would
// become publicly searchable. refuseNonPublicAddress is that guard.
func newCrawlClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   refuseNonPublicAddress,
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

// refuseNonPublicAddress is the dialer's Control hook. It runs after DNS resolution
// against the address actually being connected to, so a hostname resolving to a private
// address is caught however it was spelled, on the first request and on every redirect
// alike. https://pkg.go.dev/net#Dialer
func refuseNonPublicAddress(_, address string, _ syscall.RawConn) error {
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
}

// isPublicIP reports whether ip is routable on the public internet, which is the only
// place a blog can be.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}

	// Carrier-grade NAT (RFC 6598, 100.64.0.0/10) is not private by Go's definition,
	// but nothing inside it is reachable from outside its operator either.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}

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
	body, _, err := f.fetch(ctx, rawURL, limit)
	return body, err
}

// fetch is get with the response headers kept, for the callers that care how a page is
// served as well as what it says.
func (f *fetcher) fetch(ctx context.Context, rawURL string, limit int64) ([]byte, http.Header, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	if !isHTTP(u) {
		return nil, nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}

	release, err := f.acquire(ctx, u.Hostname())
	if err != nil {
		return nil, nil, err
	}
	defer release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	// Deliberately no Accept-Encoding: setting it manually disables the transport's
	// transparent gzip decompression, handing back compressed bytes.
	// https://pkg.go.dev/net/http#Transport

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		return nil, nil, &statusError{code: resp.StatusCode, status: resp.Status}
	}

	read, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return read, resp.Header, err
}

// statusError is a response that arrived intact but was not a success.
//
// Typed, where this used to be a bare string, because what a crawl should do next
// depends on which refusal it was: a 403 or a 429 is the operator saying stop, and the
// rest of their archive is behind the same door, where a 500 is a bad minute on their
// side and the next page may well be fine. The server's own wording is kept alongside
// the code because it is what the logs have always shown.
type statusError struct {
	code   int
	status string
}

func (e *statusError) Error() string { return "status " + e.status }

// refusesCrawler reports whether err is a server turning the crawler away rather than
// failing at it. Reading past one of these is what turns a crawl into a hammering.
func refusesCrawler(err error) bool {
	var se *statusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

// limiterKey is the domain a host's concurrency is counted against.
//
// Not simply eTLD+1: the platforms this exists to be polite to are exactly the ones
// registered in the public suffix list's private section, so bearblog.dev and github.io
// are themselves suffixes and eTLD+1 hands back the per-blog subdomain unchanged — one
// slot each, and the cap stops capping the operator it was written for. A private
// suffix is the platform, so it is the key; under an ICANN suffix the registered domain
// below it is.
func limiterKey(host string) string {
	if suffix, icann := publicsuffix.PublicSuffix(host); !icann && suffix != "" {
		return suffix
	}
	key, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil || key == "" {
		return host
	}
	return key
}

// acquire takes one of the domain's slots, returning the function that gives it back.
func (f *fetcher) acquire(ctx context.Context, host string) (func(), error) {
	key := limiterKey(host)

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
