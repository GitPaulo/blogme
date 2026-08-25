package quality

import "testing"

func TestSiteRootRecognisesSitesAndSectionsButNotPosts(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://inventwithpython.com/", true},
		{"https://inventwithpython.com", true},
		// A documentation root, and the shape that put three of them in one top ten.
		{"https://docs.python.org/3.12/", true},
		{"https://docs.python.org/3/", true},
		{"https://example.com/v2", true},
		// A one-word path is how a great many blogs publish real posts, and reading
		// those as landing pages would bury exactly the sites this index is for.
		{"https://example.com/my-first-post", false},
		{"https://example.com/blog/2024/01/a-post", false},
		{"https://pythonbytes.fm/ai-integration", false},
		{"not a url at all", false},
	}

	for _, tc := range cases {
		if got := siteRoot(tc.url); got != tc.want {
			t.Errorf("siteRoot(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestEnglishSeparatesLanguagesAndAbstainsWhenItCannotTell(t *testing.T) {
	if !english(repeat(prose, 1)) {
		t.Error("English prose was not read as English")
	}
	if english(repeat(portuguese, 1)) {
		t.Error("Portuguese was read as English")
	}
	// No evidence is not evidence against: a title-length text keeps its credit.
	if !english("Short and inconclusive") {
		t.Error("a text too short to judge was held against")
	}
}

// Vocabulary is what stands in for prose quality here, so it has to tell writing from
// a list of section names.
func TestRichnessSeparatesWritingFromListings(t *testing.T) {
	writing, list := richness(repeat(prose, 3)), richness(repeat(listing, 3))

	if writing <= list {
		t.Errorf("prose scored %.3f against a listing's %.3f", writing, list)
	}
	if writing < richnessCeiling {
		t.Errorf("ordinary prose scored %.3f, below the ceiling the model treats as full marks", writing)
	}
}

// Too little text to measure must not read as repetitive, or every short post is
// judged as filler.
func TestRichnessAbstainsOnShortText(t *testing.T) {
	if got := richness("only a handful of words here"); got != neutralRichness {
		t.Errorf("richness = %.3f, want the neutral %.3f", got, neutralRichness)
	}
}

func TestTitleIsSiteNameMatchesOnlyTheWholeName(t *testing.T) {
	if !titleIsSiteName("Invent with Python", "invent  with python") {
		t.Error("a homepage title was not recognised as its site's name")
	}
	if titleIsSiteName("8 Common Python Gotchas", "Invent with Python") {
		t.Error("a post on the site was mistaken for the site itself")
	}
	if titleIsSiteName("", "") {
		t.Error("two empty fields were called a match")
	}
}

func TestArchiveTitleMatchesListingsNotPosts(t *testing.T) {
	listings := []string{
		"Python Weekly (Issue 358 - August 2 2018)",
		"Python Bytes Full Episode Archive",
		"Archives",
		"Index of /posts",
		"Blog page 4",
	}
	for _, title := range listings {
		if !archiveTitle.MatchString(title) {
			t.Errorf("%q was not recognised as a listing", title)
		}
	}

	posts := []string{
		"8 Common Python Gotchas",
		"What I learned archiving ten years of email",
		"Rust ownership explained",
	}
	for _, title := range posts {
		if archiveTitle.MatchString(title) {
			t.Errorf("%q was mistaken for a listing", title)
		}
	}
}

func TestBoilerplateMatchesOnlyTheOpening(t *testing.T) {
	if !boilerplate(repeat(listing, 1)) {
		t.Error("a page introducing a site was not recognised")
	}
	if boilerplate(repeat(prose, 1)) {
		t.Error("an article was mistaken for a site introduction")
	}
	// Far enough in to be the article talking about something, not the page
	// introducing itself.
	late := repeat(prose, 1) + " welcome to the second half of this post"
	if boilerplate(late) {
		t.Error("a phrase deep in the body was read as an opening")
	}
}

func TestNormaliseFlattensOutsideItsRange(t *testing.T) {
	cases := []struct {
		v, want float64
	}{
		{0.2, 0}, {0.35, 0}, {0.5, 0.5}, {0.65, 1}, {0.9, 1},
	}
	for _, tc := range cases {
		if got := normalise(tc.v, 0.35, 0.65); got != tc.want {
			t.Errorf("normalise(%.2f) = %.3f, want %.3f", tc.v, got, tc.want)
		}
	}
}
