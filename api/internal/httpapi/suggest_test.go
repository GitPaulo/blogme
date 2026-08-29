package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/GitPaulo/blogme/api/internal/index"
)

const twoCompletions = `{"value": [
	{"text": "networking", "queryPlusText": "kubernetes networking"},
	{"text": "job", "queryPlusText": "kubernetes job"}
]}`

func suggest(t *testing.T, h *Handlers, target string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	h.Suggest(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// The rows a response carries, as text. Kind is checked where it is the point; most
// of these tests are about which rows come back rather than what they are.
func completions(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()

	text := make([]string, 0, len(rows(t, rec)))
	for _, row := range rows(t, rec) {
		text = append(text, row.Text)
	}
	return text
}

func rows(t *testing.T, rec *httptest.ResponseRecorder) []index.Suggestion {
	t.Helper()

	var body struct {
		Query       string             `json:"query"`
		Suggestions []index.Suggestion `json:"suggestions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body.Suggestions
}

func TestSuggestReturnsCompletions(t *testing.T) {
	rec := suggest(t, newTestHandlers(t, twoCompletions), "/api/suggest?q=kubernet")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if want := []string{"kubernetes networking", "kubernetes job"}; !slices.Equal(completions(t, rec), want) {
		t.Errorf("got %q, want %q", completions(t, rec), want)
	}
}

// A prefix shorter than this matches most of the corpus and completes to nothing
// useful, so refusing it is one fewer execution spent on a request nobody wanted.
func TestSuggestRejectsPrefixesOutsideTheBounds(t *testing.T) {
	h := newTestHandlers(t, twoCompletions)

	for _, tc := range []struct {
		name   string
		target string
	}{
		{"missing", "/api/suggest"},
		{"empty", "/api/suggest?q="},
		{"too short", "/api/suggest?q=ku"},
		{"too long", "/api/suggest?q=" + strings.Repeat("a", maxSuggestLen+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := suggest(t, h, tc.target); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// The length bound counts characters rather than bytes, matching what the browser's
// search box counts. In bytes the same number would refuse a Japanese prefix three
// times sooner than an English one.
func TestSuggestPrefixLengthCountsRunes(t *testing.T) {
	h := newTestHandlers(t, twoCompletions)

	if rec := suggest(t, h, "/api/suggest?q="+strings.Repeat("あ", maxSuggestLen)); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for a prefix of exactly the maximum length", rec.Code)
	}
}

// The endpoint is anonymous, so nothing a caller sends may decide how much work the
// service does on their behalf. Everything that costs is fixed in index.Autocomplete,
// and a caller asking for more gets the ordinary answer rather than what they asked
// for.
func TestSuggestIgnoresParametersThatWouldCostMore(t *testing.T) {
	h, sent := newCapturingHandlers(t, "", twoCompletions, DefaultLimits())

	rec := suggest(t, h, "/api/suggest?q=kubernet&fuzzy=true&top=100&limit=100&filter=quality+gt+0&suggesterName=other")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// Two, because a suggestion is drawn from two sources at once: ranked titles and
	// assembled completions. Both are checked, because a caller's parameters must reach
	// neither.
	requests := sent()
	if len(requests) != 2 {
		t.Fatalf("sent %d requests, want 2 — titles and completions", len(requests))
	}
	for _, request := range requests {
		if request["suggesterName"] != "titles" {
			t.Errorf("suggesterName = %v, want the index's own", request["suggesterName"])
		}
		if request["fuzzy"] != nil {
			t.Errorf("fuzzy = %v; a caller must not be able to buy the expensive match", request["fuzzy"])
		}
		if request["filter"] != nil {
			t.Errorf("filter = %v; nothing a caller sends may reach one", request["filter"])
		}
		if request["top"] == float64(100) {
			t.Errorf("top = %v; the caller chose how much work to ask for", request["top"])
		}
		if got, ok := request["top"].(float64); !ok || got <= 0 {
			t.Errorf("top = %v, want a figure the index package set", request["top"])
		}
	}
}

// Completions go stale far more slowly than results do, and prefixes are short and
// shared between readers, so this is the cheapest limit on the endpoint: an answer
// served from a cache is one that never reached an instance.
func TestSuggestCachesAnAnswerAndNothingElse(t *testing.T) {
	answer := suggest(t, newTestHandlers(t, twoCompletions), "/api/suggest?q=kubernet")
	if got := answer.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want an hour", got)
	}

	refused := suggest(t, newTestHandlers(t, twoCompletions), "/api/suggest?q=ku")
	if got := refused.Header().Get("Cache-Control"); got != "" {
		t.Errorf("a refusal carried Cache-Control %q; it describes this moment, not the query", got)
	}

	failed := suggest(t, newHandlersReturning(t, http.StatusInternalServerError), "/api/suggest?q=kubernet")
	if got := failed.Header().Get("Cache-Control"); got != "" {
		t.Errorf("a failure carried Cache-Control %q; caching one keeps serving an outage already over", got)
	}
}

// Typeahead fires several times per search by design, so it cannot draw on the search
// allowance: one reader typing one query would trip their own limit for searching.
func TestSuggestDoesNotSpendTheSearchAllowance(t *testing.T) {
	limits := DefaultLimits()
	limits.PerMinute, limits.Burst = 1, 1

	h, _ := newCapturingHandlers(t, "", twoCompletions, limits)

	for i := range 5 {
		if rec := suggest(t, h, "/api/suggest?q=kubernet"); rec.Code != http.StatusOK {
			t.Fatalf("suggestion %d: status = %d, want 200", i, rec.Code)
		}
	}
	if rec := get(t, h, "/api/search?q=kubernetes"); rec.Code != http.StatusOK {
		t.Errorf("the search after them = %d, want 200; typeahead spent its allowance", rec.Code)
	}
}

// Its own bucket is still a bucket. A caller past it is refused, and told for how long.
func TestSuggestThrottlesAndSaysHowLongToWait(t *testing.T) {
	limits := DefaultLimits()
	limits.SuggestPerMinute, limits.SuggestBurst = 60, 2

	h, _ := newCapturingHandlers(t, "", twoCompletions, limits)

	for i := range 2 {
		if rec := suggest(t, h, "/api/suggest?q=kubernet"); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
	}

	rec := suggest(t, h, "/api/suggest?q=kubernet")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After on a refusal")
	}
}

// Nothing about typeahead is metered, so it must never touch the budget that is.
func TestSuggestLeavesTheRerankingBudgetAlone(t *testing.T) {
	limits := DefaultLimits()
	limits.SemanticPerMinute, limits.SemanticBurst = 60, 1

	h, sent := newCapturingHandlers(t, "blogme-semantic", twoCompletions, limits)

	for range 5 {
		suggest(t, h, "/api/suggest?q=kubernet")
	}
	if rec := get(t, h, "/api/search?q=kubernetes&mode=semantic"); rec.Code != http.StatusOK {
		t.Fatalf("search status = %d, want 200", rec.Code)
	}

	requests := sent()
	if last := requests[len(requests)-1]; last["queryType"] != "semantic" {
		t.Errorf("the search ranked with %v; typeahead spent the reranking budget", last["queryType"])
	}
}

// A reader is mid-word, so a failure is answered plainly and the client shows no
// completions. What it must not do is pretend it succeeded.
func TestSuggestReportsAnUnreadableIndex(t *testing.T) {
	rec := suggest(t, newHandlersReturning(t, http.StatusInternalServerError), "/api/suggest?q=kubernet")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// A client renders a list from this, so "nothing to suggest" has to arrive as an empty
// list rather than as null — otherwise every caller has to tell the two apart itself.
func TestSuggestAnswersAnEmptyListRatherThanNull(t *testing.T) {
	rec := suggest(t, newTestHandlers(t, `{"value": []}`), "/api/suggest?q=kubernet")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"suggestions":[]`) {
		t.Errorf("body = %s, want an empty suggestions list", strings.TrimSpace(body))
	}
}

// Whitespace passes a length check and buys two index queries that can only come back
// empty, so it is refused with everything else that cannot be completed.
func TestSuggestRejectsAQueryOfNothingButSpaces(t *testing.T) {
	h, sent := newCapturingHandlers(t, "", twoCompletions, DefaultLimits())

	if rec := suggest(t, h, "/api/suggest?q=%20%20%20%20"); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if n := len(sent()); n != 0 {
		t.Errorf("sent %d requests to the index for a blank query, want 0", n)
	}
}
