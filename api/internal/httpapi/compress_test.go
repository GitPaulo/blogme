package httpapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// body of the given size, compressible enough to stand in for a page of JSON.
func body(n int) string {
	return strings.Repeat(`{"url":"https://example.com/post","title":"a post"},`, n)
}

// getCompressed runs a handler through Compress the way the worker does, asking for
// gzip unless encoding says otherwise.
func getCompressed(t *testing.T, encoding string, next http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=go", nil)
	if encoding != "" {
		req.Header.Set("Accept-Encoding", encoding)
	}
	Compress(next)(rec, req)
	return rec
}

// unzip reads a recorded body back, failing the test if it is not the gzip stream the
// headers claim. A truncated stream is the failure worth catching: it means the footer
// was never written, and a browser rejects the response outright.
func unzip(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	r, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("open gzip stream: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read gzip stream: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close gzip stream: %v", err)
	}
	return string(out)
}

// The point of the whole thing: a page of results goes out smaller than it came in,
// and comes back out the other side byte for byte.
func TestCompressShrinksAPageAndPreservesIt(t *testing.T) {
	want := body(400)

	rec := getCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, want)
	})

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("got Content-Encoding %q, want gzip", got)
	}
	if got := unzip(t, rec); got != want {
		t.Errorf("body did not survive: got %d bytes, want %d", len(got), len(want))
	}
	if sent, raw := rec.Body.Len(), len(want); sent >= raw {
		t.Errorf("compressed body is %d bytes against %d uncompressed", sent, raw)
	}
}

// A refusal is one sentence. Gzip's header and footer alone would make it longer, so
// the threshold is what keeps compression from being a cost on the error path.
func TestCompressLeavesAShortBodyAlone(t *testing.T) {
	const want = `{"error":"query parameter 'q' is required"}`

	rec := getCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, want)
	})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("a %d-byte body was sent as %q", len(want), got)
	}
	if got := rec.Body.String(); got != want {
		t.Errorf("got body %q, want %q", got, want)
	}
}

// The header is what a shared cache reads to tell the two forms apart, and it has to
// be there on both of them: a search is cacheable and public, and the compressed and
// uncompressed answers share a URL.
func TestCompressVariesWhetherOrNotItCompressed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		encoding string
		size     int
	}{
		{"compressed", "gzip", 400},
		{"too short to compress", "gzip", 1},
		{"caller cannot read gzip", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := getCompressed(t, tc.encoding, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body(tc.size))
			})

			if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Errorf("got Vary %q, want Accept-Encoding", got)
			}
		})
	}
}

// Whatever the handler set has to survive being held back, because the headers are
// sent from here rather than by the handler that wrote them.
func TestCompressKeepsTheHandlersHeadersAndStatus(t *testing.T) {
	rec := getCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=120")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body(400))
	})

	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", rec.Code)
	}
	for header, want := range map[string]string{
		"Content-Type":  "application/json",
		"Cache-Control": "public, max-age=120",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("got %s %q, want %q", header, got, want)
		}
	}
	// It described the body before compression, so sending it on would describe the
	// compressed one by the wrong number.
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("got Content-Length %q on a compressed body, want none", got)
	}
}

// A body written a piece at a time is one body: the threshold is about how much has
// arrived in total, not about any single write.
func TestCompressCountsTheWholeBodyNotOneWrite(t *testing.T) {
	const chunks = 100
	chunk := body(8)

	rec := getCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
		for range chunks {
			_, _ = io.WriteString(w, chunk)
		}
	})

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("got Content-Encoding %q, want gzip", got)
	}
	if got, want := unzip(t, rec), strings.Repeat(chunk, chunks); got != want {
		t.Errorf("got %d bytes, want %d", len(got), len(want))
	}
}

// A handler that writes nothing still has to answer. Held-back bytes are sent from
// close, and an empty body must not leave the status unsent with it.
func TestCompressAnswersAHandlerThatWroteNothing(t *testing.T) {
	rec := getCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("got status %d, want 503", rec.Code)
	}
	if got := rec.Body.Len(); got != 0 {
		t.Errorf("got %d bytes of body, want none", got)
	}
}

func TestAcceptsGzip(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   bool
	}{
		{"gzip", true},
		{"gzip, deflate, br", true},
		{"deflate, gzip", true},
		{"GZIP", true},
		{" gzip ", true},
		{"gzip;q=0.5", true},
		{"br;q=1.0, gzip;q=0.8", true},
		// A quality of zero is the one way a client says it would rather not.
		{"gzip;q=0", false},
		{"gzip;q=0.0", false},
		{"deflate, br", false},
		{"identity", false},
		{"", false},
		// Named nowhere, so nothing was asked for: a wildcard is a preference among
		// encodings the caller did name, not a claim to read one it did not.
		{"*", false},
	} {
		t.Run(tc.header, func(t *testing.T) {
			if got := acceptsGzip(tc.header); got != tc.want {
				t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}
