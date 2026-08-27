package httpapi

import (
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// minCompressed is the smallest body worth compressing.
//
// A gzip stream costs eighteen bytes of header and footer before a single byte of
// content, so a short body comes back longer than it went in. Every refusal this API
// returns is one sentence, and sending a sixty-byte error as eighty would be the whole
// of what compression achieved on that path.
const minCompressed = 1 << 10

// gzipPool keeps compressors alive between requests.
//
// A gzip.Writer carries a 32 KB window and the tables that go with it, which is a
// large allocation to make and discard once per search on an instance whose memory is
// what it is billed for.
var gzipPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

// Compress gzips a handler's response when the caller can read one and the body is
// large enough to be worth it.
//
// A page of results is 10–18 KB of JSON that gzips to 36–45% of that, measured against
// the live index, for about 0.3 ms of an instance's time on a search whose median is
// 31 ms. Nearly all of what a reader waits for is the body crossing the network rather
// than the search itself, so that is the trade: a little compute for most of the bytes
// on the wire.
//
// It has to happen here. The Functions host forwards the request to this worker and
// proxies the answer back untouched, so a response left uncompressed stays that way.
//
// gzip.NewWriter's default level rather than BestSpeed: on the same payloads BestSpeed
// returned 39–48% for 0.06 ms less, which is a worse trade at these sizes.
func Compress(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Said whether or not this particular answer was compressed. A search is
		// cacheable and public, and the compressed and uncompressed forms share a URL:
		// without this a shared cache is entitled to hand gzip to a client that never
		// asked for it.
		w.Header().Add("Vary", "Accept-Encoding")

		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next(w, r)
			return
		}

		c := &compressed{ResponseWriter: w, status: http.StatusOK}
		// Deferred rather than called after next, because a body held back below the
		// threshold is only sent from here: a handler that panics part-way through
		// would otherwise return a set of headers and nothing else.
		defer func() {
			if err := c.close(); err != nil {
				// The usual cause is a caller that hung up mid-response, which is
				// their business rather than a fault of ours. Same reading as the
				// failed write in writeJSON.
				slog.WarnContext(r.Context(), "compress response failed", "error", err)
			}
		}()

		next(c, r)
	}
}

// compressed is a ResponseWriter that gzips the body once enough of it has arrived to
// be worth compressing.
//
// The decision cannot be taken up front, because nothing here knows how large the
// answer will be until the handler has written it: a page of results and a one-line
// refusal arrive through the same call. So the opening bytes are held back, and
// whichever the body turns out to be, the headers are still unsent by the time the
// answer is known.
type compressed struct {
	http.ResponseWriter

	status int
	// held is the body so far, kept back while it is still small enough that
	// compressing it would cost bytes rather than save them.
	held []byte
	// gz is non-nil once the body has passed the threshold and the response has been
	// committed to being compressed.
	gz *gzip.Writer
	// flushed says the headers have gone, after which nothing above can be changed and
	// every write goes straight out.
	flushed bool
}

// WriteHeader records the status rather than sending it, because the headers that go
// with it are not settled until the size of the body is known.
func (c *compressed) WriteHeader(status int) {
	if !c.flushed {
		c.status = status
	}
}

func (c *compressed) Write(p []byte) (int, error) {
	switch {
	case c.gz != nil:
		return c.gz.Write(p)
	case c.flushed:
		return c.ResponseWriter.Write(p)
	}

	c.held = append(c.held, p...)
	if len(c.held) < minCompressed {
		return len(p), nil
	}
	if err := c.start(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// start commits the response to gzip: it sends the headers that say so, and hands the
// compressor everything held back up to now.
func (c *compressed) start() error {
	header := c.Header()
	header.Set("Content-Encoding", "gzip")
	// Any length already set describes the body before compression, and would be sent
	// as though it described the body after it.
	header.Del("Content-Length")

	c.ResponseWriter.WriteHeader(c.status)
	c.flushed = true

	c.gz = gzipPool.Get().(*gzip.Writer)
	c.gz.Reset(c.ResponseWriter)

	held := c.held
	c.held = nil
	_, err := c.gz.Write(held)
	return err
}

// close finishes the response, and is what sends a body that never reached the
// threshold. It runs on every path: a gzip stream is only valid once its footer is
// written, and a short body has not been written at all until this point.
func (c *compressed) close() error {
	if c.gz != nil {
		err := c.gz.Close()
		// Reset before returning it, so a pooled compressor does not hold the finished
		// request's ResponseWriter alive until something else happens to need it.
		c.gz.Reset(io.Discard)
		gzipPool.Put(c.gz)
		c.gz = nil
		return err
	}

	if c.flushed {
		return nil
	}
	c.ResponseWriter.WriteHeader(c.status)
	c.flushed = true

	held := c.held
	c.held = nil
	_, err := c.ResponseWriter.Write(held)
	return err
}

// acceptsGzip reports whether the caller said it could read a gzipped body.
//
// Nothing beyond the name is read except the quality value, and that only for the one
// thing it can say which matters here: q=0 is a refusal rather than a preference, so a
// client sending "gzip;q=0" gets its bytes raw. Preference order between encodings is
// not honoured, because gzip is the only one on offer.
// https://developer.mozilla.org/docs/Web/HTTP/Reference/Headers/Accept-Encoding
func acceptsGzip(header string) bool {
	for _, encoding := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(encoding, ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}

		_, quality, ok := strings.Cut(params, "q=")
		if !ok {
			return true
		}
		// An unreadable quality is treated as no quality at all, which is the reading
		// that leaves a malformed header working rather than silently uncompressed.
		q, err := strconv.ParseFloat(strings.TrimSpace(quality), 64)
		return err != nil || q > 0
	}

	return false
}
