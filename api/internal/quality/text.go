package quality

import (
	"math"
	"net/url"
	"regexp"
	"strings"
)

const (
	// How much of a text is read for the language and vocabulary measures.
	//
	// Both are ratios, and a ratio taken over a whole document is not comparable
	// between documents of different lengths: distinct words rise more slowly than
	// total words, so a long article looks repetitive next to a short one purely
	// because it is long. A fixed window makes the two figures mean the same thing
	// everywhere.
	sampleWords = 200

	// Below these, a ratio is noise rather than a measurement, and the article is
	// given the benefit of the doubt.
	minLanguageWords = 25
	minRichnessWords = 40

	// The share of an English text made of the commonest English words. Ordinary
	// prose runs far above this even when technical; the threshold is set low
	// because the cost of calling English text foreign is much higher than the cost
	// of missing a foreign one.
	englishStopwordShare = 0.06

	// A ratio of distinct words to total words. Prose sits around the upper figure;
	// keyword-stuffed or boilerplate text sits below the lower one.
	richnessFloor   = 0.35
	richnessCeiling = 0.65

	// Used where there is too little text to measure vocabulary, so that a short
	// article is neither rewarded nor punished for it.
	neutralRichness = 0.5

	// How much of the opening is read for a site-introduction phrase. These phrases
	// come first or not at all.
	openingWords = 20
)

// englishStopwords is a deliberately small set: the words that are hard to write
// English without. A larger list would not make the test more accurate, only slower.
var englishStopwords = map[string]bool{
	"the": true, "and": true, "of": true, "to": true, "a": true, "in": true,
	"is": true, "it": true, "that": true, "for": true, "with": true, "as": true,
	"was": true, "on": true, "are": true, "this": true, "be": true, "by": true,
	"from": true, "or": true, "an": true, "at": true, "not": true, "but": true,
	"have": true, "has": true, "you": true, "we": true, "can": true, "if": true,
}

// words counts the words in a text.
func words(s string) int {
	return len(strings.Fields(s))
}

// sample yields the first sampleWords words of a text, lowercased and stripped of
// surrounding punctuation, and reports how many there were.
func sample(s string) ([]string, int) {
	out := make([]string, 0, sampleWords)
	for _, f := range strings.Fields(strings.ToLower(s)) {
		if len(out) == sampleWords {
			break
		}
		if w := strings.Trim(f, ".,:;!?()[]{}\"'`“”‘’—–…"); w != "" {
			out = append(out, w)
		}
	}
	return out, len(out)
}

// english reports whether a text reads as English.
//
// The index is analysed with an English analyser and the interface is in English, so
// a post in another language is one this search can neither rank nor present well.
// Too short to judge counts as English: no evidence is not evidence against.
func english(s string) bool {
	ws, n := sample(s)
	if n < minLanguageWords {
		return true
	}

	hits := 0
	for _, w := range ws {
		if englishStopwords[w] {
			hits++
		}
	}
	return float64(hits)/float64(n) >= englishStopwordShare
}

// richness is the share of distinct words in a text's opening.
//
// It stands in for prose quality, which is the thing here that cannot be measured
// directly without reading the article. Keyword stuffing, generated filler and
// navigation boilerplate all say the same few words over and over, and all three
// score low; writing that develops an idea uses a wider vocabulary and scores high.
func richness(s string) float64 {
	ws, n := sample(s)
	if n < minRichnessWords {
		return neutralRichness
	}

	seen := make(map[string]struct{}, n)
	for _, w := range ws {
		seen[w] = struct{}{}
	}
	return float64(len(seen)) / float64(n)
}

// versionPath matches a path segment that names a release rather than a piece of
// writing, such as the "/3.12/" of docs.python.org.
var versionPath = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+)*$`)

// siteRoot reports whether a URL addresses a site or a section of one rather than a
// single article.
//
// Only two shapes count: an empty path is a homepage, and a lone version number is a
// documentation root. Both were live in the top ten for "python". A one-word path is
// not one of them, because that is how a great many blogs publish real posts.
func siteRoot(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	path := strings.Trim(u.Path, "/")
	if path == "" {
		return true
	}
	return !strings.Contains(path, "/") && versionPath.MatchString(path)
}

// titleIsSiteName reports whether an article's title is just the name of the blog it
// came from, which is what a homepage indexed as an article looks like. The author
// field carries the blog's name rather than a person's.
func titleIsSiteName(title, author string) bool {
	t, a := fold(title), fold(author)
	return t != "" && a != "" && t == a
}

// archiveTitle matches titles that announce a list of other posts rather than a post:
// newsletter issues, episode archives, and paginated indexes.
var archiveTitle = regexp.MustCompile(`(?i)\b(issue|episode|edition)\s*#?\s*[0-9]+\b|\barchives?\b|\bindex of\b|\bpage\s+[0-9]+\b`)

// openers are how a page introduces a site rather than saying something. Short by
// design: each entry is a phrase found at the top of a page that actually ranked.
var openers = []string{
	"welcome to",
	"welcome!",
	"this is the official documentation",
	"official documentation for",
	"documentation sections",
}

// boilerplate reports whether a text opens by introducing a site.
func boilerplate(content string) bool {
	ws, n := sample(content)
	if n == 0 {
		return false
	}

	opening := strings.Join(ws[:min(n, openingWords)], " ")
	for _, o := range openers {
		if strings.Contains(opening, o) {
			return true
		}
	}
	return false
}

// fold reduces a string to what two of them must share to be called equal: case and
// spacing carry no meaning in these comparisons.
func fold(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// normalise maps a value from [low, high] onto [0, 1], flattening everything outside.
func normalise(v, low, high float64) float64 {
	if high <= low {
		return 0
	}
	return clamp01((v - low) / (high - low))
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return max(0, min(1, v))
}
