package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GitPaulo/blogme/api/internal/index"
)

const (
	// The shortest and longest prefix worth completing.
	//
	// The minimum matches MIN_QUERY_LENGTH in the web app and the three characters the
	// service documents as its own floor: below it a prefix matches most of the corpus
	// and completes to nothing a reader can use. The maximum is far below the search
	// endpoint's, because this only ever sees a query in progress — a completion for
	// half a page of text is not a thing anyone is waiting for, and refusing it early
	// is one fewer way to spend an execution on nonsense. Runes, not bytes, for the
	// reason maxQueryLen counts them.
	minSuggestLen = 3
	maxSuggestLen = 64
	// How long a browser may reuse a list of completions.
	//
	// An hour, where a page of results gets two minutes, because the two go stale at
	// different rates. Results move whenever discovery indexes anything; the vocabulary
	// of a million titles does not change in an hour, and prefixes are short and shared,
	// so one reader's cached "kuber" is the same answer as everyone else's. This is the
	// cheapest limit on the endpoint: a request answered from a cache is one that never
	// reached an instance.
	suggestMaxAge = 3600
)

// Suggest handles GET /api/suggest?q=...
//
// It completes the query a reader is typing. Deliberately narrower than Search: the
// only thing it reads from the request is q, so there is no parameter through which a
// caller can make their own request cost more than anyone else's. Everything that
// decides the work — how many rows, which suggester, whether to match fuzzily — is
// fixed in index.Suggest and the two halves behind it.
func (h *Handlers) Suggest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	started := time.Now()
	caller := clientKey(r)

	// Before any work, including validation, for the reason Search does the same: an
	// execution costs money whatever the request turns out to say.
	if ok, wait := h.suggestClient.allow(caller, started); !ok {
		h.logThrottled(ctx, "suggest-caller", caller, wait, started)
		writeRateLimited(ctx, w, h.limits.SuggestPerMinute, wait)
		return
	}
	if ok, wait := h.suggestAll.allow(globalKey, started); !ok {
		h.logThrottled(ctx, "suggest-service", caller, wait, started)
		writeRateLimited(ctx, w, h.limits.SuggestAllPerMinute, wait)
		return
	}

	q, err := parseSuggest(r.URL.Query())
	if err != nil {
		writeError(ctx, w, http.StatusBadRequest, err.Error())
		return
	}

	found, err := h.index.Suggest(ctx, q)
	if err != nil {
		h.logSuggestFailure(ctx, q, err)
		writeError(ctx, w, http.StatusInternalServerError, "suggestions unavailable")
		return
	}

	// Nothing is logged on the way through, unlike Search. One line per keystroke would
	// make the cheapest path in the service the loudest, and telemetry is billed by
	// volume. Nothing is lost by it: the platform already counts invocations per
	// function, so how much this endpoint is used is visible without paying to say so.
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(suggestMaxAge))

	writeJSON(ctx, w, http.StatusOK, suggestResponse{Query: q, Suggestions: found})
}

type suggestResponse struct {
	Query string `json:"query"`
	// Written straight out rather than copied into a shape of this package's own, as
	// the search response does with article.Result. Never null: index.Suggest returns
	// an empty slice rather than none, so a client rendering a list gets an empty one
	// instead of having to tell "no suggestions" from "no field".
	Suggestions []index.Suggestion `json:"suggestions"`
}

// parseSuggest validates the one parameter this endpoint has. Its error is answered
// verbatim with a 400, so it is written for the caller rather than for a log.
//
// Anything else in the query string is ignored rather than rejected: a caller sending
// fuzzy=true or top=100 is asking for something this endpoint does not offer, and the
// answer is the ordinary one rather than an error that would tell them the parameter
// was recognised.
func parseSuggest(values url.Values) (string, error) {
	q := values.Get("q")
	if strings.TrimSpace(q) == "" {
		return "", errors.New("query parameter 'q' is required")
	}

	switch n := utf8.RuneCountInString(q); {
	case n < minSuggestLen:
		return "", errors.New("query parameter 'q' is too short to complete")
	case n > maxSuggestLen:
		return "", errors.New("query parameter 'q' is too long")
	}

	return q, nil
}

// logSuggestFailure reports that suggestions could not be fetched, without letting the
// reporting become the flood.
//
// Bounded for the same reason logThrottled is, and more sharply needed here: the
// failure this reports is the search index being unreachable, which fails every
// request, and this endpoint receives several per search. failed_total is cumulative
// for the life of the instance, so nothing is lost to the lines that never happened.
//
// Its own limiter rather than the one logThrottled uses, so that an outage failing
// every request cannot starve the record of the throttling happening beside it, or
// the other way round.
func (h *Handlers) logSuggestFailure(ctx context.Context, q string, err error) {
	failed := h.suggestFailed.Add(1)

	if ok, _ := h.suggestLog.allow(globalKey, time.Now()); !ok {
		return
	}

	slog.WarnContext(ctx, "suggest failed",
		"query", logQuery(q),
		"failed_total", failed,
		"error", err)
}
