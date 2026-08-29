// Package index is the searchable projection of the corpus, backed by Azure AI
// Search. It is rebuildable from the canonical article JSON in blob storage.
//
// Azure AI Search has no official Go data-plane SDK, so this calls the REST API
// directly: https://learn.microsoft.com/rest/api/searchservice/. In Azure the
// function app's managed identity supplies a bearer token; locally an API key is used
// instead.
package index

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/GitPaulo/blogme/api/internal/article"
)

const (
	apiVersion = "2024-07-01"
	// Azure AI Search rejects indexing requests above 1000 documents.
	// https://learn.microsoft.com/rest/api/searchservice/documents/index
	maxBatch = 1000
	// How much of a failed response is quoted back in the error.
	maxErrorBytes = 2 << 10
)

// queryTimeout is how long one search of the index may take.
//
// An execution is billed for its whole duration at the instance's memory size, so a
// query left to run out the HTTP client's 30 seconds costs some nine hundred times
// what a healthy one does, and answers a reader who left long ago. The slowest search
// seen in production is under 1.5s. Applied per request rather than to the client,
// which is shared with Upsert and its thousand-document posts.
//
// A variable only so the tests can shorten it; nothing in the service reassigns it.
var queryTimeout = 5 * time.Second

type Index struct {
	endpoint string
	name     string
	apiKey   string
	// semantic names the index's semantic configuration. Empty turns reranking off.
	semantic string
	cred     *azidentity.DefaultAzureCredential
	http     *http.Client
}

func New(endpoint, name, apiKey, semantic string) *Index {
	idx := &Index{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		name:     name,
		apiKey:   apiKey,
		semantic: semantic,
		http:     &http.Client{Timeout: 30 * time.Second},
	}

	if apiKey == "" {
		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			slog.Error("search credential unavailable", "error", err)
		} else {
			idx.cred = cred
		}
	}

	return idx
}

// Ranking modes. Semantic reranks the top keyword matches with a language model,
// which is what makes a query phrased as a sentence work. Keyword is plain relevance
// scoring: worse at intent, but it ranks the whole result set rather than a
// 50-document window, so it is the one that can page deep.
const (
	RankSemantic = "semantic"
	RankKeyword  = "keyword"
)

// QueryOptions narrows a search beyond the query text itself.
type QueryOptions struct {
	Limit  int
	Offset int
	// Fetch is how many documents to read to fill those Limit rows. The per-source cap
	// discards some of what comes back, so reading exactly Limit would hand back a
	// short page. Unset means Limit. The caller sets it, including below Limit,
	// because only the caller knows how far its ranking mode stays meaningful.
	Fetch int
	// Origin, when set to a known discovery method, restricts results to it.
	Origin string
	// Rank selects the ranking mode. Empty means semantic, which is the default.
	Rank string
	// Profile names a scoring profile to rank with. Empty uses the index's own
	// default, which is what the service always sends.
	//
	// It exists for the ranking harness: the index carries several profiles differing
	// by one variable each, and the only way to tell whether a ranking change is an
	// improvement is to run the same queries through two of them. Nothing in the
	// request path sets this, so a caller cannot choose how their results are ranked.
	Profile string
}

// Page is one response worth of results.
type Page struct {
	Results []article.Result
	// Total counts every match in the corpus, not the rows on this page.
	Total int
	// NextOffset is where the page after this one starts.
	//
	// It has to come from here because a page of N rows is not N documents wide: the
	// per-source cap discards rows after the index has chosen them, so a caller
	// advancing by its own page size would step over whatever was discarded.
	NextOffset int
	// Read is how many documents this page was chosen from. Reported separately from
	// NextOffset, which jumps to the end of the corpus once the index runs out. Read
	// against len(Results) is how hard the cap is working on a query, which is worth
	// a log line.
	Read int
	// Broadened reports that nothing matched every word of the query, so these rows
	// match any of them instead. The rows are real results, ranked as usual, but they
	// answer a looser question than the one that was asked and a caller saying so is
	// being honest with a reader who can see the words they typed.
	Broadened bool
	// Exhausted reports that this page reached the end of the index: there is nothing
	// further to fetch, whatever Total says.
	//
	// The two disagree because Total counts documents and a page holds rows, and the
	// per-source cap throws rows away after the index has ranked them. Those documents
	// are counted and unreachable both, so a caller that paged to the end still holds
	// fewer rows than Total promised. It can only tell that it has everything by being
	// told.
	Exhausted bool
}

// Ready reports whether this instance can actually read the index.
//
// It asks for a document count rather than checking that a credential exists, because
// the failures worth catching all authenticate perfectly well: a role assignment that
// was never granted still issues a token, and a misspelled index name is a valid
// request to somewhere that is not there.
//
// Counting is not semantic, so it spends nothing from the metered reranking quota.
func (i *Index) Ready(ctx context.Context) error {
	return i.do(ctx, http.MethodGet, "/docs/$count", nil, nil)
}

// searchFields is which fields a query is matched against.
//
// Named rather than left to the default, and the default is why. Omitting it searches
// every field marked searchable, and those fields are not analysed alike: title,
// summary, content and authorText declare en.microsoft, which discards English
// stopwords, while author and topics were created without an analyzer and so keep
// them. "a tour of go" reaches the first group as [tour, go] and the second as
// [a, tour, of, go].
//
// With searchMode "all" that difference is not a nuance, it is the query. Every term
// has to match somewhere, so "a" and "of" — terms only the unanalysed fields still
// hold — can be satisfied only by a document whose author name or topic slug contains
// them. Measured against the live index, "a tour of go" fell from 11,508 matches to
// 24, and all 24 were somebody's byline: "Comment on Psychopathic Manipulation", "The
// Healthy Programmer" for "the pragmatic programmer", "Fishing and football" for
// "history of the internet". Adding fields made the answer smaller and wrong.
//
// So this names only fields that analyse the query the same way. authorText is a copy
// of author under en.microsoft, which is what keeps searching by name working — text
// alone finds 3 of Daniel Mangum's 258 posts, because the other 255 never print his
// name in the body.
//
// topics is left out altogether rather than copied. It is a closed vocabulary of slugs
// — "tech", "software-engineering" — that content already carries, and across the
// query set it contributed between 21 and 95 extra documents without once changing a
// top three. Its keyword weight in the scoring profiles went with it.
//
// titleSuggest is left out because it is not a field, it is the suggester's copy of
// title. Searching it scored every title twice, once at the profile's weight of 4 and
// once unweighted, which lifted the top score for "rust ownership" from 366 to 417 and
// reordered the page.
const searchFields = "title,summary,content,authorText"

// Query runs a full-text search and returns one page of ranked results.
func (i *Index) Query(ctx context.Context, q string, opts QueryOptions) (Page, error) {
	// The caller's figure wins when it gives one, including when it is below Limit:
	// only the caller knows how far its ranking mode stays meaningful, and a short
	// page there is better than a wrong one.
	fetch := opts.Fetch
	if fetch <= 0 {
		fetch = opts.Limit
	}

	// sourceId is selected but never returned to the caller: it is there so one blog
	// can be stopped from filling the page. See maxPerSource.
	//
	// searchMode "all" requires every word of the query, where "any" needs only one.
	// "any" was the wrong trade for keyword ranking: "ai text watermarks" reported
	// 185,796 matches, of which 265 contained all three words. Requiring all of them
	// put "How AI text watermarking works" first, moved "sean goedecke" from rank 39 to
	// 14 among his own posts, and left "github actions" unchanged.
	//
	// Set on the shared body but true only of keyword ranking: the semantic branch
	// below overrides it, and says why.
	body := map[string]any{
		"search":       q,
		"top":          fetch,
		"skip":         opts.Offset,
		"count":        true,
		"queryType":    "simple",
		"searchMode":   "all",
		"searchFields": searchFields,
		"select":       "url,title,author,origin,summary,topics,publishedAt,sourceId,framingDenied",
	}

	// Built from a fixed set rather than from the caller's string, so a filter can
	// never be injected through the query parameter.
	switch opts.Origin {
	case article.OriginSitemap:
		body["filter"] = "origin eq 'sitemap'"
	case article.OriginFeed:
		// Documents indexed before origin existed came from feeds.
		body["filter"] = "origin eq 'feed' or origin eq null"
	}

	// Set before the semantic clone below, so both rankings are measured under the
	// same profile.
	if opts.Profile != "" {
		body["scoringProfile"] = opts.Profile
	}

	if i.semantic != "" && opts.Rank != RankKeyword {
		semantic := maps.Clone(body)
		semantic["queryType"] = "semantic"
		semantic["semanticConfiguration"] = i.semantic
		// Back to "any", because requiring every word settles the query before the
		// reranker sees it. Here keyword matching only picks the candidates, and a
		// question phrased as a sentence is the whole reason this mode exists: "why is
		// my postgres query slow" and "essays about burnout and leaving big tech"
		// matched nothing at all under "all".
		//
		// It costs nothing on the queries that chose "all" above: for "ai text
		// watermarks", "github actions" and "sean goedecke" the reranked top ten came
		// back identical under either setting.
		semantic["searchMode"] = "any"

		var resp searchResponse
		err := i.search(ctx, semantic, &resp)
		if err == nil {
			return selectPage(resp, opts.Offset, opts.Limit, fetch), nil
		}
		// Reranking is metered and throttled: the free plan stops at 1,000 queries a
		// month and the service sheds load above roughly ten concurrent queries. Worse
		// ranking is a disappointment, no search at all is an outage, so fall through
		// to the plain keyword query.
		slog.WarnContext(ctx, "semantic ranking unavailable, using keyword ranking", "error", err)
	}

	var resp searchResponse
	if err := i.search(ctx, body, &resp); err != nil {
		return Page{}, err
	}

	// Nothing matched every word, so ask again for any of them.
	//
	// Requiring all of them is right and stays the first thing tried — it is what puts
	// "How AI text watermarking works" above the 185,000 documents that merely say
	// "text". What is left over is the query where every word is a real one and no
	// single article happens to carry them all: a long phrase, or a spelling the corpus
	// writes another way. Answering it with the articles that carry most of the words
	// beats answering it with nothing.
	//
	// This used to catch far more than that, and the reason it did was a fault rather
	// than a feature: unanalysed fields kept the stopwords the rest of the index drops,
	// so an ordinary sentence asked for an author whose name contained "the". See
	// searchFields, which is where that is now fixed. Broadening is the floor under the
	// remainder, and only ever when the strict query found nothing at all.
	if worthBroadening(q, resp) {
		broad := maps.Clone(body)
		broad["searchMode"] = "any"

		var second searchResponse
		if err := i.search(ctx, broad, &second); err != nil {
			// The strict answer is still an answer, empty as it is. Failing the request
			// over a retry would turn "no results" into "search is broken".
			slog.WarnContext(ctx, "broadened search failed, reporting the strict result",
				"error", err)
			return selectPage(resp, opts.Offset, opts.Limit, fetch), nil
		}

		page := selectPage(second, opts.Offset, opts.Limit, fetch)
		page.Broadened = true
		return page, nil
	}

	return selectPage(resp, opts.Offset, opts.Limit, fetch), nil
}

// worthBroadening reports whether a second, looser query is worth making.
//
// Only when the first found nothing, so a search that works is never touched, and only
// when the query has more than one word: with a single term "all" and "any" are the same
// question, and asking it twice spends an execution to be told the same thing.
//
// Semantic ranking never reaches here — it searches for any of the words already, and a
// semantic run that failed has fallen back to a keyword one that this then applies to.
func worthBroadening(q string, resp searchResponse) bool {
	return resp.Total == 0 && len(resp.Value) == 0 && len(strings.Fields(q)) > 1
}

// search runs one query under its own time budget.
//
// Per call rather than around the pair of them, so a semantic attempt that burns the
// whole budget still leaves the keyword fallback a full one. Sharing a deadline would
// turn the slow case into the failing case, which the fallback exists to prevent.
func (i *Index) search(ctx context.Context, body map[string]any, out *searchResponse) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	return i.do(ctx, http.MethodPost, "/docs/search", body, out)
}

type searchResponse struct {
	Total int `json:"@odata.count"`
	Value []struct {
		Score float64 `json:"@search.score"`
		// Present only on a semantic query, and then it is the score the order
		// actually follows. See selectPage.
		RerankerScore *float64 `json:"@search.rerankerScore"`
		URL           string   `json:"url"`
		Title         string   `json:"title"`
		Author        string   `json:"author"`
		Origin        string   `json:"origin"`
		Summary       string   `json:"summary"`
		Topics        []string `json:"topics"`
		PublishedAt   string   `json:"publishedAt"`
		SourceID      string   `json:"sourceId"`
		// Null on everything indexed before the crawler looked, which reads as nil.
		FramingDenied *bool `json:"framingDenied"`
	} `json:"value"`
}

// maxPerSource caps how much of one page a single blog may occupy.
//
// Three posts from one site is rarely what a reader wanted, and it stops a source that
// stuffs its posts with popular terms from taking the whole page.
//
// The cost is that a page is thinned after the index has already chosen it, which is
// why QueryOptions.Fetch and Page.NextOffset exist. Read exactly one page's worth and
// a dominated query comes back nearly empty: "claude" returned three rows out of
// twenty, its first twenty-nine matches all being the same blog.
const maxPerSource = 3

// selectPage turns the documents that came back into one page of results, and reports
// where the next page starts.
//
// The two go together on purpose. Rows are dropped here, after the index has ranked
// them, so the number of rows returned says nothing about how far into the results
// they reached, and the next page has to start where this one stopped reading.
func selectPage(resp searchResponse, offset, limit, fetch int) Page {
	out := make([]article.Result, 0, limit)
	perSource := make(map[string]int, limit)
	seen := make(map[string]struct{}, limit)

	read := 0
	for _, v := range resp.Value {
		if len(out) == limit {
			break
		}
		read++

		// One article can be indexed more than once, because a document's key is its
		// source and its URL together: a site listed twice, or an aggregator carrying
		// someone else's post, produces the same URL under two source ids. The browser
		// drops them again on arrival, which only means the reader gets a short page
		// and the count on it is a lie.
		if _, repeat := seen[v.URL]; repeat {
			continue
		}
		seen[v.URL] = struct{}{}

		// A site can also carry one article under many urls, which the check above
		// cannot see: a tutorial published in five languages is five paths holding the
		// same page. Searching for "opengl tutorial" returned "Tutorial 12 : OpenGL
		// Extensions" three times, its source's whole allowance spent on one article
		// under /hu/, /ru/ and no prefix at all.
		//
		// Matched within a source rather than across the corpus. Two blogs posting
		// under one title have written two different articles, and titles as plain as
		// "Shaders" or "Security" are common enough that collapsing them everywhere
		// would hide real writing.
		if title := repeatKey(v.SourceID, v.Title); title != "" {
			if _, repeat := seen[title]; repeat {
				continue
			}
			seen[title] = struct{}{}
		}

		// Counted after the repeat checks, so a duplicate does not also spend one of its
		// source's three rows. Documents indexed before sourceId was selected carry
		// none, and an unknown source cannot be shown to be over its share.
		if v.SourceID != "" {
			perSource[v.SourceID]++
			if perSource[v.SourceID] > maxPerSource {
				continue
			}
		}

		// The reranker's score whenever there is one, because that is the order these
		// rows arrived in. On a semantic query @search.score is still the keyword
		// score, and it runs 146.7, 59.7, 79.5, 68.9 down a list the reranker had
		// already sorted. The two are on different scales, keyword unbounded against
		// the reranker's 0 to 4.
		score := v.Score
		if v.RerankerScore != nil {
			score = *v.RerankerScore
		}

		r := article.Result{
			URL:           v.URL,
			Title:         v.Title,
			Author:        v.Author,
			Origin:        v.Origin,
			Summary:       v.Summary,
			Topics:        v.Topics,
			Score:         score,
			FramingDenied: v.FramingDenied,
		}
		if v.PublishedAt != "" {
			if t, err := time.Parse(time.RFC3339, v.PublishedAt); err == nil {
				r.PublishedAt = t
			}
		}
		out = append(out, r)
	}

	// Reaching the end of a window that was already short of what was asked for means
	// the index has nothing further, whatever the count says. Both halves matter: a
	// short window the page filled from before running out still has documents left.
	exhausted := read == len(resp.Value) && len(resp.Value) < fetch

	next := offset + read
	// Otherwise a caller paging by NextOffset keeps coming back for the same empty
	// window until its own guard rail stops it.
	if exhausted {
		next = max(next, resp.Total)
	}

	return Page{Results: out, Total: resp.Total, NextOffset: next, Read: read, Exhausted: exhausted}
}

// document is the wire shape of an indexed article, matching infra/search-index.json.
type document struct {
	Action string `json:"@search.action"`
	ID     string `json:"id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	// TitleSuggest is the title again, under the field the suggester was built over.
	//
	// Duplicated rather than shared because Azure AI Search refuses to add a field
	// that already exists to a suggester: "only new fields added in the same index
	// update operation are allowed". Putting the suggester on title itself would have
	// meant dropping and rebuilding the index, which also empties every quality score.
	// The copy costs about 295 bytes a document and no downtime at all.
	TitleSuggest string `json:"titleSuggest,omitempty"`
	// SuggestVersion records which of the derived copies below have been written, so
	// the backfill in infra/backfill_suggest.py can find the documents that predate
	// them. Same idea as qualityVersion, and the script mirrors this number.
	SuggestVersion int    `json:"suggestVersion,omitempty"`
	Author         string `json:"author,omitempty"`
	// AuthorText is the author again, analysed as English rather than as a keyword.
	//
	// Duplicated for the same reason TitleSuggest is: author was created without an
	// analyzer and an existing field cannot be given one — the service answers
	// "Existing field 'author' cannot be changed." Its terms therefore keep the
	// stopwords every other searchable field discards, which is what made a query with
	// "the" in it ask for an article by an author called "The" something. See
	// searchFields. About 22 bytes of text a document, against title's 77.
	AuthorText  string   `json:"authorText,omitempty"`
	SourceID    string   `json:"sourceId"`
	Origin      string   `json:"origin,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Content     string   `json:"content,omitempty"`
	Topics      []string `json:"topics,omitempty"`
	Kind        []string `json:"kind,omitempty"`
	PublishedAt *string  `json:"publishedAt,omitempty"`
	// A pointer because false and unknown are different answers, and only the first is
	// worth writing. See article.Article.FramingDenied.
	FramingDenied *bool `json:"framingDenied,omitempty"`
}

// Upsert adds or replaces articles in the index.
func (i *Index) Upsert(ctx context.Context, articles []article.Article) error {
	for start := 0; start < len(articles); start += maxBatch {
		end := min(start+maxBatch, len(articles))

		docs := make([]document, 0, end-start)
		for _, a := range articles[start:end] {
			d := document{
				Action: "mergeOrUpload",
				ID:     a.ID,
				URL:    a.URL,
				Title:  a.Title,
				// Written here so every article discovered from now on arrives
				// suggestable and searchable by name, leaving the backfill only the
				// documents indexed before the fields existed. See document.TitleSuggest
				// and document.AuthorText.
				TitleSuggest:   a.Title,
				AuthorText:     a.Author,
				SuggestVersion: suggestVersion,
				Author:         a.Author,
				SourceID:       a.SourceID,
				Origin:         a.Origin,
				Summary:        a.Summary,
				Content:        a.Content,
				Topics:         a.Topics,
				Kind:           a.Kind,

				FramingDenied: a.FramingDenied,
			}
			if !a.PublishedAt.IsZero() {
				ts := a.PublishedAt.UTC().Format(time.RFC3339)
				d.PublishedAt = &ts
			}
			docs = append(docs, d)
		}

		if err := i.do(ctx, http.MethodPost, "/docs/index", map[string]any{"value": docs}, nil); err != nil {
			return fmt.Errorf("index %d documents: %w", len(docs), err)
		}
	}

	return nil
}

func (i *Index) do(ctx context.Context, method, path string, body, out any) error {
	// A nil body means a GET, which carries none: sending "null" with a JSON content
	// type would be a request the service is entitled to refuse.
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		payload = bytes.NewReader(raw)
	}

	endpoint := fmt.Sprintf("%s/indexes/%s%s?api-version=%s",
		i.endpoint, url.PathEscape(i.name), path, apiVersion)

	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if err := i.authorize(ctx, req); err != nil {
		return err
	}

	resp, err := i.http.Do(req)
	if err != nil {
		return fmt.Errorf("call search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		// Bounded: this error text ends up in a log record, and an upstream failure is
		// entitled to return a response body of any size it likes.
		var msg bytes.Buffer
		_, _ = msg.ReadFrom(io.LimitReader(resp.Body, maxErrorBytes))
		return fmt.Errorf("search returned %s: %s", resp.Status, strings.TrimSpace(msg.String()))
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (i *Index) authorize(ctx context.Context, req *http.Request) error {
	if i.apiKey != "" {
		req.Header.Set("api-key", i.apiKey)
		return nil
	}
	if i.cred == nil {
		return errors.New("no search credential configured")
	}

	token, err := i.cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://search.azure.com/.default"},
	})
	if err != nil {
		return fmt.Errorf("acquire search token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	return nil
}

// repeatKey identifies one source's title for the purpose of spotting a repeat, or
// returns empty where there is nothing dependable to match on.
//
// Kept apart from the url keys in the same map by a separator no url contains, so the
// two cannot collide. Case and spacing are normalised because the same page translated
// or re-rendered rarely reproduces either exactly.
func repeatKey(sourceID, title string) string {
	if sourceID == "" || title == "" {
		return ""
	}
	return sourceID + "\x00" + strings.ToLower(strings.Join(strings.Fields(title), " "))
}
