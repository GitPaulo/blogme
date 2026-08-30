package index

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The bug, as the reader met it. Every count here was measured against the live index.
func TestEscapeQueryRescuesNamesTheParserAteToday(t *testing.T) {
	for _, tc := range []struct {
		query, want, was string
	}{
		{"c++", `c\+\+`, "0 results, against 21,562 documents that are there"},
		{"c++ templates", `c\+\+ templates`, `50,337 results led by "Free Templates and Themes by WrapPixel"`},
		{"modern c++", `modern c\+\+`, `94,512 results led by "Modern Mythology"`},
		{"google+", `google\+`, "128,138 results about Google rather than 2,353 about Google+"},
		{"notepad++", `notepad\+\+`, "the plus signs read as operators"},
	} {
		if got := escapeQuery(tc.query); got != tc.want {
			t.Errorf("escapeQuery(%q) = %q, want %q\n  unescaped this was %s", tc.query, got, tc.want, tc.was)
		}
	}
}

// Escaping is only safe to apply to everything if it leaves everything else alone. Each
// of these carries punctuation and each returned an identical count escaped or not.
func TestEscapeQueryLeavesOrdinaryPunctuationAlone(t *testing.T) {
	for _, q := range []string{
		"c#", ".net", "node.js", "python 3.14", "tls 1.3",
		"127.0.0.1", "don't repeat yourself", "developer's guide", "rust ownership",
		"sean goedecke", "中文 编程", "café résumé", "🦀 rust",
	} {
		if got := escapeQuery(q); got != q {
			t.Errorf("escapeQuery(%q) = %q, want it untouched", q, got)
		}
	}
}

// Hyphens and slashes sit inside the names of real things far more often than they are
// typed as operators, so they are escaped and the names survive.
func TestEscapeQueryKeepsHyphenatedAndSlashedNamesWhole(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{"objective-c", `objective\-c`},
		{"x86-64 assembly", `x86\-64 assembly`},
		{"utf-8 encoding", `utf\-8 encoding`},
		{"ci/cd pipelines", `ci\/cd pipelines`},
		{"e-mail", `e\-mail`},
		{"http/2", `http\/2`},
	} {
		if got := escapeQuery(tc.query); got != tc.want {
			t.Errorf("escapeQuery(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// A quoted phrase is the one piece of grammar a reader arrives already knowing, and the
// service honours it: "rust ownership" quoted returns 161 results against 1,192 loose.
func TestEscapeQueryKeepsABalancedPhrase(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{`"rust ownership"`, `"rust ownership"`},
		{`"a b" "c d"`, `"a b" "c d"`},
		// Quoting protects operators from the parser on its own; escaping them again
		// measured identical, so the rule stays "operators are always text".
		{`"c++"`, `"c\+\+"`},
		{`c++ "exact phrase"`, `c\+\+ "exact phrase"`},
	} {
		if got := escapeQuery(tc.query); got != tc.want {
			t.Errorf("escapeQuery(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// A lone quote is an apostrophe or a typo, never an instruction — and passing it through
// would leave the rest of the query inside a phrase nobody opened.
func TestEscapeQueryTakesAnUnbalancedQuoteAsText(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{`rust "ownership`, `rust \"ownership`},
		{`rust ownership"`, `rust ownership\"`},
		{`"`, `\"`},
		{`say "hello" and "goodbye`, `say \"hello\" and \"goodbye`},
	} {
		if got := escapeQuery(tc.query); got != tc.want {
			t.Errorf("escapeQuery(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// The undocumented operators go too. A leading "-" excludes a word and "c*" matches
// every term beginning with c -- 1,244,794 documents against 0 for the literal string.
// Neither is something this search box offers or explains.
func TestEscapeQueryDropsUnadvertisedOperators(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{"rust -ownership", `rust \-ownership`},
		{"c*", `c\*`},
		{"rust | python", `rust \| python`},
		{"(rust)", `\(rust\)`},
		{"rust AND ownership", "rust AND ownership"}, // a word, not an operator
	} {
		if got := escapeQuery(tc.query); got != tc.want {
			t.Errorf("escapeQuery(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// The escape character itself has to be escaped, or a trailing one would escape the
// quote the service appends around nothing and the query would not parse.
func TestEscapeQueryEscapesItsOwnEscapeCharacter(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		// The colon goes with it: fielded search in the full syntax this does not use
		// yet, and escaping it costs nothing today.
		{`C:\Windows`, `C\:\\Windows`},
		{`rust\`, `rust\\`},
		{`\\`, `\\\\`},
	} {
		if got := escapeQuery(tc.query); got != tc.want {
			t.Errorf("escapeQuery(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

func TestEscapeQueryHandlesTheEmptyString(t *testing.T) {
	if got := escapeQuery(""); got != "" {
		t.Errorf("escapeQuery(%q) = %q, want empty", "", got)
	}
}

// Escaping has to reach the request, and every request: the semantic branch and the
// broadened retry both clone the body, so a query escaped in one and not the others
// would answer from a different question depending on which path ran.
func TestQuerySendsTheEscapedTextOnEveryRequest(t *testing.T) {
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		search, _ := body["search"].(string)
		sent = append(sent, search)
		// Empty, so the keyword branch also makes its broadened second ask.
		_, _ = io.WriteString(w, `{"@odata.count":0,"value":[]}`)
	}))
	defer srv.Close()

	idx := New(srv.URL, "articles", "test-key", "blogme-semantic")
	if _, err := idx.Query(context.Background(), "c++ templates", QueryOptions{Limit: 20}); err != nil {
		t.Fatalf("semantic query: %v", err)
	}
	if _, err := idx.Query(context.Background(), "c++ templates",
		QueryOptions{Limit: 20, Rank: RankKeyword}); err != nil {
		t.Fatalf("keyword query: %v", err)
	}

	if len(sent) != 3 {
		t.Fatalf("sent %d requests, want 3 (semantic, keyword, broadened)", len(sent))
	}
	for i, got := range sent {
		if got != `c\+\+ templates` {
			t.Errorf("request %d searched for %q, want the escaped text", i, got)
		}
		if strings.Contains(got, "++") {
			t.Errorf("request %d carried a bare operator: %q", i, got)
		}
	}
}
