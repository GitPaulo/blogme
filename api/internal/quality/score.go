package quality

import (
	"github.com/GitPaulo/blogme/api/internal/article"
	"github.com/GitPaulo/blogme/api/internal/index"
)

// Version is the scoring model's version, stored on every article it judges.
//
// Raising it puts the whole corpus back into the unscored set, which is how a change
// to any of the figures below reaches articles that were judged under the old one.
// It is the only mechanism for that, so a change to scoring that does not raise this
// applies to new articles alone.
const Version = 1

const (
	// Penalties for the ways a document can fail to be an article. They multiply, so a
	// page that is several of these at once falls further than one that is only one of
	// them: a site root whose title is the site's name is not a borderline case.
	//
	// None of them is zero. Every one is a heuristic, and a heuristic that fires wrongly
	// on a good article should cost it rank rather than bury it.
	siteRootPenalty    = 0.10
	titleIsSitePenalty = 0.30
	archivePenalty     = 0.40
	boilerplatePenalty = 0.30

	// An article shorter than this says too little to be worth ranking; one this
	// long is not held back by its length. Between them the score rises evenly.
	//
	// Only the lower end matters, which is why the crawler's 1,000-word truncation
	// does not affect any of this: it censors long articles, and nothing here asks
	// whether an article is long.
	minWords  = 60
	fullWords = 400

	// What a post in another language keeps. Not zero: someone searching in that
	// language should still find it, and the index holds no other copy of it.
	nonEnglishFactor = 0.25

	// A feed entry is described by its author; a sitemap entry is described by
	// whatever the crawler could make of the page, and sitemap walking is what
	// pulled in the landing pages in the first place.
	feedProvenance    = 1.0
	sitemapProvenance = 0.5

	// How merit divides between how the article reads and where it came from.
	richnessWeight   = 0.7
	provenanceWeight = 0.3

	// How much of the distance to a perfect score popularity can close. It is a
	// bonus and never a penalty: most good blogs have no presence at all on the
	// sites that supply it, and their absence is not evidence against them.
	popularityWeight = 0.25
)

// Signals are the measurable properties of one article, kept apart from the
// arithmetic that weighs them so that a disagreement about the weights does not
// require re-reading any text.
type Signals struct {
	Words       int
	English     bool
	SiteRoot    bool
	TitleIsSite bool
	Archive     bool
	Boilerplate bool
	Richness    float64
	FromFeed    bool
}

// Analyse reads one article and reports what can be measured about it.
func Analyse(c index.Candidate) Signals {
	return Signals{
		Words:       words(c.Content),
		English:     english(c.Content),
		SiteRoot:    siteRoot(c.URL),
		TitleIsSite: titleIsSiteName(c.Title, c.Author),
		Archive:     archiveTitle.MatchString(c.Title),
		Boilerplate: boilerplate(c.Content),
		Richness:    richness(c.Content),
		FromFeed:    c.Origin != article.OriginSitemap,
	}
}

// Judge turns one article and the standing of the site it came from into the figures
// stored on it.
func Judge(c index.Candidate, popularity float64) index.Scores {
	sig := Analyse(c)
	content := ContentScore(sig)

	return index.Scores{
		ID:         c.ID,
		Quality:    blend(content, popularity),
		Content:    content,
		Popularity: clamp01(popularity),
		WordCount:  sig.Words,
		Version:    Version,
	}
}

// ContentScore is how good an article is on its own evidence, in [0, 1].
//
// The four terms multiply rather than add because they are conditions, not opinions: a
// documentation landing page written in flawless English is still a landing page, and
// no amount of vocabulary should argue it back up the page. Merit is the only term that
// rewards; the others can only take away.
func ContentScore(s Signals) float64 {
	return articleness(s) * lengthFactor(s.Words) * languageFactor(s.English) * merit(s)
}

// articleness is how much this looks like a piece of writing rather than a page that
// lists other pages.
func articleness(s Signals) float64 {
	factor := 1.0
	if s.SiteRoot {
		factor *= siteRootPenalty
	}
	if s.TitleIsSite {
		factor *= titleIsSitePenalty
	}
	if s.Archive {
		factor *= archivePenalty
	}
	if s.Boilerplate {
		factor *= boilerplatePenalty
	}
	return factor
}

func lengthFactor(w int) float64 {
	return normalise(float64(w), minWords, fullWords)
}

func languageFactor(isEnglish bool) float64 {
	if isEnglish {
		return 1
	}
	return nonEnglishFactor
}

// merit is what the article has going for it, as opposed to what is wrong with it.
func merit(s Signals) float64 {
	provenance := sitemapProvenance
	if s.FromFeed {
		provenance = feedProvenance
	}
	return richnessWeight*normalise(s.Richness, richnessFloor, richnessCeiling) +
		provenanceWeight*provenance
}

// blend adds popularity to an article's own score without ever subtracting from it.
//
// Written as a share of the distance still to travel rather than as a weighted average.
// An average would cap an article no one has heard of below one that has been shared,
// which ranks by fame instead of letting fame break a tie. Here a perfect article scores
// the same whether or not anyone has ever linked to it.
func blend(content, popularity float64) float64 {
	content = clamp01(content)
	return clamp01(content + (1-content)*popularityWeight*clamp01(popularity))
}
