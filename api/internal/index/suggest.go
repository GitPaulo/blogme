package index

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

// suggesterName is the suggester defined in infra/search-index.json.
//
// A constant rather than a setting, because it is part of the index schema this
// package already mirrors — the document struct below is the same contract. Renaming
// it in the schema without renaming it here fails every autocomplete request, which
// the tests catch.
const suggesterName = "titles"

// suggestVersion marks a document whose derived text copies have been written.
//
// It exists so the backfill has a set to drain rather than a cursor to keep: a
// document leaves that set by being written, so reading the head of it repeatedly
// walks the whole corpus and then stops, the way quality scoring does. Raise it to
// make every document eligible again, which is what a change to what those copies
// hold needs. infra/backfill_suggest.py carries the same number.
//
// 1 wrote titleSuggest. 2 adds authorText, which is what lets a query be matched
// against the author without the field's missing analyzer poisoning it — see
// searchFields. Documents still at 1 are searchable by every word except the author's
// name until the backfill reaches them.
const suggestVersion = 2

// maxSuggestions is how many completions one request may return.
//
// Eight, because a completion list is scanned rather than read: past about that many
// a reader is faster typing the word out. It is fixed here rather than taken from the
// caller so that no request can ask the index for more work than another. The service
// allows up to 100.
const maxSuggestions = 8

// suggestOverFetch is how many rows to read from either source for every one returned.
//
// Both are filtered hard enough that asking for exactly what is wanted returns less.
// The suggester extends a query by one whole term with no idea which terms are worth
// extending it by — half of what it offers for the eight rows a reader sees is "rust
// and", "kubernetes on", "python a", "how to 2026" — and titles are cut for length, for
// script, and to one per blog. Reading wide is what leaves enough standing.
//
// Five costs nothing worth counting. Forty completions measured a median 157ms against
// 173ms for eight — the work is finding the prefix, not returning the rows — and the
// service allows a hundred.
const suggestOverFetch = 5

// stopWords are the words a completion is not worth making.
//
// Only ever applied to what a completion *adds*, never to what the reader typed:
// someone part-way through "how to" is completing a phrase of their own, and the list
// is there to say what could follow it. What it must not say is "how to a".
//
// English function words, and only those. Nothing here is a subject anybody searches
// for, which is the test for adding one — "go", "c" and "it" are all words a reader
// means literally, so none of them appear.
var stopWords = map[string]struct{}{
	"a": {}, "about": {}, "after": {}, "again": {}, "all": {}, "also": {}, "an": {},
	"and": {}, "any": {}, "are": {}, "as": {}, "at": {}, "be": {}, "been": {},
	"before": {}, "being": {}, "but": {}, "by": {}, "can": {}, "could": {}, "did": {},
	"do": {}, "does": {}, "down": {}, "for": {}, "from": {}, "had": {}, "has": {},
	"have": {}, "her": {}, "his": {}, "how": {}, "i": {}, "if": {}, "in": {},
	"into": {}, "is": {}, "it": {}, "its": {}, "just": {}, "may": {}, "me": {},
	"might": {}, "more": {}, "most": {}, "must": {}, "my": {}, "no": {}, "not": {},
	"of": {}, "off": {}, "on": {}, "only": {}, "or": {}, "other": {}, "our": {},
	"out": {}, "over": {}, "should": {}, "so": {}, "some": {}, "such": {}, "than": {},
	"that": {}, "the": {}, "their": {}, "them": {}, "then": {}, "there": {},
	"these": {}, "they": {}, "this": {}, "those": {}, "to": {}, "too": {}, "under": {},
	"up": {}, "us": {}, "very": {}, "was": {}, "we": {}, "were": {}, "what": {},
	"when": {}, "where": {}, "which": {}, "who": {}, "why": {}, "will": {},
	"with": {}, "would": {}, "you": {}, "your": {},
}

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

// What a suggestion is, which is not the same as how it looks. A title is a phrase
// somebody wrote and the service ranked; a completion is a phrase the suggester
// assembled and did not rank. The reader is told which is which by the icon beside it.
const (
	KindTitle = "title"
	KindQuery = "query"
)

// Suggestion is one row of the search box's dropdown.
//
// Serialised straight to the browser rather than copied into a shape of the handler's
// own, which is how article.Result reaches it from search. The projection a caller
// renders is the projection this package returns.
type Suggestion struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
}

// maxTitles is how many of the eight rows titles may take.
//
// Three. They go first because they are the ranked ones — the service scored the
// documents they came from, where completions arrive in roughly alphabetical order —
// and a first row of "Go Concurrency Patterns" beats one of "go concept art". They are
// capped because they are long and specific, and eight article titles is a worse search
// box than one that also offers "rust compiler".
const maxTitles = 3

// maxTitleLen is the longest title worth offering as a query.
//
// A title runs to a median of 30 characters across the live index, so seventy is
// generous. It is not idle, though: the harness turned up "[Redirect Magazine] #23 -
// distributed systems, testing, more distributed systems and actors on distributed
// systems" and three variants of "How to avoid accessibility issues from missing skip
// links - Accessibility how-tos - Writing - Dustin Whisman". Nobody is going to search
// for those. Dropped rather than truncated: half a title is not a phrase anybody wrote.
const maxTitleLen = 70

// maxTitlesPerSource is how many of the title rows one blog may hold.
//
// One, for the reason search caps a page at three per source and for a sharper version
// of it: there are only three title rows, and asked to complete "how to" the index
// offered all three to consecutive posts in a single accessibility series. A reader
// wants three answers, not one answer three times.
const maxTitlesPerSource = 1

// Suggest returns what to offer a reader part-way through typing, best first.
//
// Two sources, because neither is enough on its own. Titles come from documents and are
// ranked, but the service only finds them when the whole input appears in one — "why is
// my postgres" is a sentence nobody wrote in a headline, and suggest answers nothing.
// Completions are assembled from term pairs and so always answer, but nothing ordered
// them. Asked together they cover each other's failure.
//
// Asked at once rather than one after the other. In sequence the wall clock is the sum,
// and worse, a fallback would spend its second round trip exactly on the queries the
// first source could not serve — the ones already worst off.
func (i *Index) Suggest(ctx context.Context, q string) ([]Suggestion, error) {
	var (
		ranked, assembled  []string
		titleErr, queryErr error
		wg                 sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		ranked, titleErr = i.titles(ctx, q)
	}()
	go func() {
		defer wg.Done()
		assembled, queryErr = i.autocomplete(ctx, q)
	}()
	wg.Wait()

	// One source failing is a shorter list, not a failed request: the other still has
	// something to say. Both failing is the index being unreachable, which is the
	// endpoint's 500 — and both reasons are carried, because the two calls can fail
	// differently and the log line is the only place anyone will see either.
	if titleErr != nil && queryErr != nil {
		return nil, errors.Join(titleErr, queryErr)
	}

	out := make([]Suggestion, 0, maxSuggestions)
	seen := make(map[string]struct{}, maxSuggestions)
	add := func(text, kind string) {
		if len(out) == maxSuggestions {
			return
		}
		key := phraseKey(text)
		if _, repeat := seen[key]; repeat {
			return
		}
		seen[key] = struct{}{}
		out = append(out, Suggestion{Text: text, Kind: kind})
	}

	for _, text := range ranked {
		if len(out) == maxTitles {
			break
		}
		add(text, KindTitle)
	}
	// Deduped against the titles above by the same key, so a completion that only
	// repeats a title in fewer words does not take a second row.
	for _, text := range assembled {
		add(text, KindQuery)
	}
	return out, nil
}

// titles returns article titles beginning with the query, best first.
//
// The service ranks these, which is the whole reason they are here: it scores the
// documents behind them, so "go conc" answers "Go Concurrency Patterns" where the
// completions for the same prefix answer "go concept art".
//
// Only the matched text is read back. The documents themselves are not wanted — this
// puts a phrase in a search box rather than opening an article — so the smallest
// retrievable field is selected and the rest of the document stays on the server.
func (i *Index) titles(ctx context.Context, q string) ([]string, error) {
	// Every figure fixed here rather than taken from a caller, for the reason
	// rawCompletions gives at length: the endpoint in front of this is anonymous, and
	// fuzzy matching is not something anyone should be able to buy with a query string.
	// It is absent rather than set to false, which is the same thing to the service and
	// one fewer field to wonder about.
	body := map[string]any{
		"search":        q,
		"suggesterName": suggesterName,
		"top":           maxTitles * suggestOverFetch,
		// sourceId is read but never returned: it is here so that one blog cannot hold
		// every title row. See maxTitlesPerSource.
		"select": "sourceId",
	}

	ctx, cancel := context.WithTimeout(ctx, suggestTimeout)
	defer cancel()

	var resp titleResponse
	if err := i.do(ctx, http.MethodPost, "/docs/suggest", body, &resp); err != nil {
		return nil, fmt.Errorf("suggest titles: %w", err)
	}

	out := make([]string, 0, maxTitles)
	seen := make(map[string]struct{}, maxTitles)
	perSource := make(map[string]int, maxTitles)
	for _, v := range resp.Value {
		if len(out) == maxTitles {
			break
		}

		// Collapsed for the reason completions are: a title carrying a newline would
		// put one in the search box and the address bar both.
		text := strings.Join(strings.Fields(v.Text), " ")
		if text == "" || len([]rune(text)) > maxTitleLen {
			continue
		}
		// Offering back what is already in the box spends a row saying nothing.
		if strings.EqualFold(text, strings.TrimSpace(q)) {
			continue
		}
		if !readableAs(text, q) {
			continue
		}
		// One article reaches the index under several sources, and an aggregator
		// carries somebody else's post, so the same title arrives more than once.
		key := phraseKey(text)
		if _, repeat := seen[key]; repeat {
			continue
		}
		// Counted after the checks above, so a title dropped for another reason does
		// not also spend its blog's single row.
		if v.SourceID != "" {
			perSource[v.SourceID]++
			if perSource[v.SourceID] > maxTitlesPerSource {
				continue
			}
		}
		seen[key] = struct{}{}

		out = append(out, text)
	}
	return out, nil
}

// readableAs reports whether a title is written in something the query could be.
//
// The corpus is multilingual by design and search returns all of it, which is right. A
// suggestion list of three is a different thing: asked to complete "python" the index
// offered "Python 对象引用与复制", "Python中光学计算相关的库" and "Python编程基础01" — every
// title row, to a reader who cannot read any of them, while the completions underneath
// were in their own alphabet.
//
// So the test is whether the two share a writing system, and it is strict: a query in
// Latin script takes titles in Latin script only. A proportion was tried first and is
// the reason this is not one — a title repeating "Python" twice around a Chinese
// sentence is more than half Latin letters and still unreadable to whoever typed it.
// Diacritics are Latin, so "Python à la française" stays.
//
// A query in any other script is left alone, because someone typing 中文 has said
// plainly what they read. Nothing is hidden from search by this: the results below the
// box are untouched, and this decides three rows of a convenience.
func readableAs(title, query string) bool {
	return !isLatin(query) || isLatin(title)
}

// isLatin reports whether every letter in the text is one a Latin alphabet writes.
func isLatin(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) && r >= unicode.MaxASCII && !unicode.Is(unicode.Latin, r) {
			return false
		}
	}
	return true
}

// suggestResponse is the wire shape of a suggested document. @search.text is the field
// text that matched, which is the title as it was written.
// https://learn.microsoft.com/rest/api/searchservice/documents/suggest-post
type titleResponse struct {
	Value []struct {
		Text     string `json:"@search.text"`
		SourceID string `json:"sourceId"`
	} `json:"value"`
}

// autocomplete completes the query a reader is part-way through typing.
//
// This is the cheap half of typeahead: it matches prefixes in the titles the
// suggester was built over and returns query text, never documents. The search
// endpoint answers what was typed; this only says what could be typed next.
//
// Every parameter that decides what the request costs is set here rather than taken
// from the caller. That is deliberate: the endpoint in front of this is anonymous, and
// a caller who could ask for 100 fuzzy completions instead of 8 exact ones would be
// choosing how much of the service to spend on themselves.
func (i *Index) autocomplete(ctx context.Context, q string) ([]string, error) {
	// Read wide and keep the best of it: see suggestOverFetch for what the service
	// offers when it is asked for exactly eight.
	raw, err := i.rawCompletions(ctx, q, maxSuggestions*suggestOverFetch)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, maxSuggestions)
	seen := make(map[string]struct{}, maxSuggestions)
	for _, term := range raw {
		if len(out) == maxSuggestions {
			break
		}
		if !worthOffering(term, q) {
			continue
		}

		key := phraseKey(term)
		if _, repeat := seen[key]; repeat {
			continue
		}
		seen[key] = struct{}{}

		out = append(out, term)
	}
	return out, nil
}

// completionMode picks how the service should finish the query.
//
// The two modes fail in opposite directions, and which one is safe depends entirely on
// how many words have been typed.
//
// "twoTerms" finishes the last word and adds another after it. That extra word is only
// ever a real pair when there is nothing in front of it: the service completes the last
// term and echoes everything before it back **unchecked**. Ask it to complete "minecraft
// world gen" and it answers "minecraft world gen z", "minecraft world generation rag" —
// and it answers "zzzqqq world gen z" to the same shape, because "minecraft world"
// constrained nothing. Those read as phrases somebody wrote and are not: "minecraft world
// gen z" finds no results at all. A suggestion that leads nowhere is worse than none.
//
// "oneTermWithContext" finishes the last word and stops. The words in front of it are
// still echoed, but they are the reader's own and nothing is invented after them — so
// "minecraft world gen" completes to "minecraft world generation" and "minecraft world
// generator", which is what a reader typing that was reaching for.
//
// So: one word means nothing to echo, and the pair is real — "zzzqqq" alone completes to
// nothing at all, which is the proof. More than one and only the word being typed may be
// finished.
func completionMode(q string) string {
	if len(strings.Fields(q)) > 1 {
		return "oneTermWithContext"
	}
	return "twoTerms"
}

// rawCompletions asks the service for completions and returns them as they came, so
// that what the filter above does can be measured against what it was given. See
// TestSuggestionHarness.
func (i *Index) rawCompletions(ctx context.Context, q string, top int) ([]string, error) {
	// fuzzy stays off, and is not a caller's decision. The service documents it as
	// slower and more resource-hungry, and it measured 323 ms against 86 ms for the
	// same query. A typo is worth correcting in a search, where the reader has
	// finished typing and is waiting for an answer; it is not worth quadrupling the
	// cost of a keystroke.
	body := map[string]any{
		"search":           q,
		"suggesterName":    suggesterName,
		"autocompleteMode": completionMode(q),
		"top":              top,
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

// phraseKey identifies a completion for the purpose of spotting one already offered.
//
// Case is folded because the suggester reaches the same phrase through titles that
// capitalise it differently. A plural is folded with it because the two are the same
// suggestion: a list of eight that spends four rows on "docker image", "docker images",
// "docker container" and "docker containers" has offered a reader two things.
//
// Only a trailing "s", and only where something recognisable is left without it, so
// that short words which merely end in one — "aws", "js", "css" — are left alone.
func phraseKey(completion string) string {
	key := strings.ToLower(completion)
	if last := strings.LastIndexByte(key, ' '); last >= 0 {
		word := key[last+1:]
		if len(word) > 3 && strings.HasSuffix(word, "s") {
			return key[:last+1] + strings.TrimSuffix(word, "s")
		}
	}
	return key
}

// worthOffering reports whether a completion says anything the query did not.
//
// It judges only the words the completion adds. The words the reader typed are theirs,
// and the last of them is the one the suggester was asked to finish — "kubernet"
// becoming "kubernetes" is the whole point, and is never in question here.
func worthOffering(completion, query string) bool {
	// A row that puts back what is already in the box spends a place in a list of eight
	// to say nothing. Compared as text rather than by counting words, because finishing
	// the word being typed adds no word at all: "minecraft world gen" becoming
	// "minecraft world generation" is the whole point of it.
	if strings.EqualFold(strings.TrimSpace(completion), strings.TrimSpace(query)) {
		return false
	}

	typed := strings.Fields(query)
	words := strings.Fields(completion)
	added := words[min(len(typed), len(words)):]

	// Nothing whole was added, so this finished the word being typed — and finishing a
	// function word is not a completion anybody is reaching for. "how to" came back as
	// "how tokyo", "how topaz" and "how toxic"; "why is" as "why island" and "why iso".
	// Somebody who typed "to" meant "to". A word with meaning of its own is the
	// opposite case and the reason the mode exists: "minecraft world gen" becoming
	// "minecraft world generation" is exactly right.
	if len(added) == 0 && len(typed) > 0 {
		if _, stop := stopWords[bareWord(typed[len(typed)-1])]; stop {
			return false
		}
	}

	// Only whole words beyond what was typed are judged.
	for _, word := range added {
		bare := bareWord(word)
		if bare == "" {
			return false
		}
		if _, stop := stopWords[bare]; stop {
			return false
		}
		// A bare number carries nothing on its own. Every title with a year or a list
		// in it offers one, which is how "how to" came back as "how to 1", "how to 2",
		// "how to 10", "how to 100", "how to 2020" and "how to 2026" in a single
		// eight-row list. Version numbers keep their dot and so are not caught by this,
		// which is right: "python 3.14" is a subject.
		if isNumber(bare) {
			return false
		}
	}
	return true
}

// bareWord is a word stripped of what a title wraps it in, ready to be looked up.
// The suggester returns a term as the title wrote it, and titles end sentences: "and,"
// is the same word as "and".
func bareWord(word string) string {
	return strings.ToLower(strings.Trim(word, `.,:;!?()[]{}"'“”‘’`))
}

func isNumber(word string) bool {
	for _, r := range word {
		if r < '0' || r > '9' {
			return false
		}
	}
	return word != ""
}
