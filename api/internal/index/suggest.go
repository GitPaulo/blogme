package index

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// suggesterName is the suggester defined in infra/search-index.json.
//
// A constant rather than a setting, because it is part of the index schema this
// package already mirrors — the document struct below is the same contract. Renaming
// it in the schema without renaming it here fails every autocomplete request, which
// the tests catch.
const suggesterName = "titles"

// suggestVersion marks a document whose titleSuggest has been written.
//
// It exists so the backfill has a set to drain rather than a cursor to keep: a
// document leaves that set by being written, so reading the head of it repeatedly
// walks the whole corpus and then stops, the way quality scoring does. Raise it to
// make every document eligible again, which is what a change to what titleSuggest
// holds would need. infra/backfill_suggest.py carries the same number.
const suggestVersion = 1

// maxSuggestions is how many completions one request may return.
//
// Eight, because a completion list is scanned rather than read: past about that many
// a reader is faster typing the word out. It is fixed here rather than taken from the
// caller so that no request can ask the index for more work than another. The service
// allows up to 100.
const maxSuggestions = 8

// suggestTimeout is how long one autocomplete may take.
//
// Far shorter than queryTimeout, because the two fail differently. A slow search is
// still the answer the reader asked for; a slow completion is for a word they have
// already finished typing, so waiting on it only spends billed instance time to
// arrive too late to be used. Measured at 75–93 ms against the live service.
//
// A variable only so the tests can shorten it; nothing in the service reassigns it.
var suggestTimeout = 1500 * time.Millisecond

// autocompleteResponse is the wire shape of a completion.
//
// Text holds only the completed term ("watermarking"); QueryPlusText holds the whole
// query with that term completed ("ai text watermarking"), which is what a search box
// needs to put in front of a reader.
// https://learn.microsoft.com/rest/api/searchservice/documents/autocomplete-post
type autocompleteResponse struct {
	Value []struct {
		Text          string `json:"text"`
		QueryPlusText string `json:"queryPlusText"`
	} `json:"value"`
}

// Autocomplete completes the query a reader is part-way through typing.
//
// This is the cheap half of typeahead: it matches prefixes in the titles the
// suggester was built over and returns query text, never documents. The search
// endpoint answers what was typed; this only says what could be typed next.
//
// Every parameter that decides what the request costs is set here rather than taken
// from the caller. That is deliberate: the endpoint in front of this is anonymous, and
// a caller who could ask for 100 fuzzy completions instead of 8 exact ones would be
// choosing how much of the service to spend on themselves.
func (i *Index) Autocomplete(ctx context.Context, q string) ([]string, error) {
	// twoTerms completes to a phrase rather than a word, which is what makes the list
	// worth reading: "kubernet" comes back as "kubernetes networking" and "kubernetes
	// job" rather than as "kubernetes" alone, so a reader learns what the corpus holds
	// instead of only how the word ends.
	//
	// fuzzy stays off, and is not a caller's decision. The service documents it as
	// slower and more resource-hungry, and it measured 323 ms against 86 ms for the
	// same query. A typo is worth correcting in a search, where the reader has
	// finished typing and is waiting for an answer; it is not worth quadrupling the
	// cost of a keystroke.
	body := map[string]any{
		"search":           q,
		"suggesterName":    suggesterName,
		"autocompleteMode": "twoTerms",
		"top":              maxSuggestions,
	}

	// Per call, like search's, so a request that spends the whole budget cannot eat
	// into the next one.
	ctx, cancel := context.WithTimeout(ctx, suggestTimeout)
	defer cancel()

	var resp autocompleteResponse
	if err := i.do(ctx, http.MethodPost, "/docs/autocomplete", body, &resp); err != nil {
		return nil, fmt.Errorf("autocomplete: %w", err)
	}

	out := make([]string, 0, len(resp.Value))
	for _, v := range resp.Value {
		// The index is built from third-party titles, so a completion is third-party
		// text. Whitespace is collapsed for the same reason it is in repeatKey: a
		// completion is displayed and typed back into a search box, and a title
		// carrying a newline would put one in both.
		if term := strings.Join(strings.Fields(v.QueryPlusText), " "); term != "" {
			out = append(out, term)
		}
	}
	return out, nil
}
