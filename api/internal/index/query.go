package index

import "strings"

// Turning what a reader typed into what the service is asked.
//
// Azure AI Search parses the search string before any analyzer sees it, and the
// punctuation people put in the names of things collides with that grammar. "c++" is
// the letter c followed by two AND operators, and c on its own analyses to nothing, so
// the query asked for nothing and the service obliged.
//
// What made it worth fixing is that it did not look broken. "c++ templates" dropped the
// c++ and searched for templates, coming back with 50,337 results led by "Free Templates
// and Themes by WrapPixel"; "modern c++" led with "Modern Mythology". A count that large
// reads as a healthy search. Measured against the live index, escaping turns those into
// 1,906 and 3,162 results led by "C++ Templates: How to Iterate through std::tuple" and
// "A long article about modern C++", and finds the 21,562 documents that "c++" alone
// could not reach at all.

// queryOperators are the characters the query parser reads as grammar rather than text.
//
// Wider than the simple query type strictly needs. Simple treats + | " ( ) ' \ as
// special and - and * as operators; the rest of these belong to the full Lucene syntax,
// which this package does not use today. They are here because escaping a character the
// parser does not care about costs nothing — measured across 28 queries carrying
// punctuation, from "c#" and "node.js" to "127.0.0.1" and "ci/cd pipelines", escaping
// the wider set returned byte-identical counts to escaping the narrow one — and because
// the day someone sets queryType to "full" to get fielded search, this is already right.
// A rule that only holds for the current setting is a trap for whoever changes it.
//
// The quote is absent and handled separately below.
const queryOperators = `\+-&|!(){}[]^~*?:/`

// escapeQuery makes a reader's words literal, so the search box searches for what is in
// it rather than for what the query grammar reads it as.
//
// Double quotes are the exception, and the only one. Wrapping words in them to mean
// "these words, in this order" is a convention people arrive already knowing, it is
// unambiguous, and the service honours it — "rust ownership" quoted returns 161 results
// against 1,192 unquoted. So a balanced pair is passed through as the reader's own.
//
// Balanced, because a lone quote is a typo or an apostrophe rather than an instruction,
// and passing it through would leave the rest of the query inside a phrase nobody opened.
// Operators inside a quoted phrase are escaped like any others: quoting already protects
// them from the parser, and escaping them again measured identical either way, so the
// rule stays "operators are always text" rather than gaining a second case.
//
// Nothing else is preserved. A leading "-" would exclude a word and "c*" would match
// every term beginning with c — 1,244,794 documents, against 0 for the literal string —
// and neither is something this search box offers or explains. An operator nobody was
// told about is a way for a query to mean something its author did not.
// searchText is what goes in the request's search field.
//
// Azure reads "*" as match-all, which is the right reading of an empty query and the
// wrong one of a query a reader actually typed. escapeQuery would turn a typed
// asterisk into a literal one, so the two cases are separated before it runs.
func searchText(q string) string {
	if q == "" {
		return "*"
	}
	return escapeQuery(q)
}

func escapeQuery(q string) string {
	// Odd means one of them opened a phrase that was never closed, so none of them can
	// be taken as the reader's punctuation.
	quotesBalanced := strings.Count(q, `"`)%2 == 0

	// Most queries carry no operator at all, and the ones that do carry one or two.
	var b strings.Builder
	b.Grow(len(q) + 8)

	for _, r := range q {
		switch {
		case r == '"':
			if !quotesBalanced {
				b.WriteByte('\\')
			}
		case strings.ContainsRune(queryOperators, r):
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
