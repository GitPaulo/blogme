package quality

import (
	"strings"
	"testing"

	"github.com/GitPaulo/blogme/api/internal/index"
)

// The fixtures below are the documents a live search for "python" actually returned
// in its top ten, which is what this model was written to answer. Their text is
// written out at a realistic length rather than quoted, so that nothing here is
// separated by length alone: every one of them is comfortably past the point where
// length stops counting against a document.

// prose is ordinary technical prose: a wide vocabulary saying one thing after
// another.
const prose = `
Every Python program starts by importing modules, and the order in which that happens
decides more than most people expect. When the interpreter reaches an import statement
it looks first in its own cache of already loaded modules, then along the search path,
and only then does it read anything from disk. That cache is why a module executes
once no matter how many files ask for it, and why editing a file during a session
leaves the old version running until you restart. The gotcha arrives when two modules
import each other. Neither is finished executing when the other asks for it, so the
names defined further down the file simply are not there yet, and you get an attribute
error that points at a line which looks perfectly correct. Moving the import inside the
function that needs it defers the lookup until both modules have finished loading,
which fixes the symptom without explaining it. The better answer is usually that the
two modules want to be one module, or want a third between them holding whatever they
both reach for. Mutable default arguments cause a similar surprise for a similar
reason: the default is evaluated once, when the function is defined, so a list written
into a signature is shared by every call that omits it.`

// listing is what a documentation landing page is made of: section names, one after
// another, with almost no sentence between them.
const listing = `
Welcome! This is the official documentation for Python. Documentation sections: what's
new, tutorial, library reference, language reference, python setup and usage, python
howtos, installing python modules, distributing python modules, extending and
embedding, python runtime services, faq, glossary, about the documentation, dealing
with bugs, reporting issues, contributing to python, download the documentation,
history and license, copyright, indices and tables, global module index, general
index, python module index, search page, complete table of contents, list of sections,
frequently asked questions, tutorial start here, library reference keep this under
your pillow, language reference describes syntax and semantics, python setup and usage
how to install, python howtos in depth documents, installing python modules, third
party modules, distributing python modules, extending and embedding tutorial, python
c api reference manual, faq frequently asked questions, indices and tables, glossary,
search page, complete table of contents, meta information, reporting bugs,
contributing, about the documentation, history and license, copyright, download,
documentation sections, whats new in python, tutorial, library reference.`

// portuguese is a real post in a language this index cannot rank or present.
const portuguese = `
Grupo de Usuários Python Bahia. Atenção, eu convido todos os baianos de sangue ou de
coração que trabalham com Python a participar do nosso grupo de usuários. A ideia é
reunir pessoas interessadas em compartilhar conhecimento sobre a linguagem, organizar
encontros mensais na universidade, e produzir material em português para quem está
começando agora. Já temos uma lista de discussão aberta e um canal onde conversamos
todos os dias sobre projetos, bibliotecas novas, e oportunidades de trabalho na
região. Quem quiser apresentar alguma coisa no próximo encontro pode escrever para a
lista, e nós reservamos um espaço na programação. Também estamos procurando empresas
locais dispostas a ceder uma sala para os encontros, e qualquer ajuda nesse sentido
seria muito bem vinda pela comunidade inteira.`

// repeat lengthens a passage past the point where length affects the score, without
// touching the vocabulary of its opening, which is the only part measured.
func repeat(s string, times int) string {
	return strings.TrimSpace(strings.Repeat(s+" ", times))
}

func candidate(id, url, title, author, origin, content string) index.Candidate {
	return index.Candidate{
		ID: id, URL: url, Title: title, Author: author, Origin: origin, Content: content,
	}
}

// The whole point of the model, stated as one assertion: a real post has to finish
// above every page that was beating it.
func TestQualityPutsAnArticleAboveTheLandingPagesThatOutrankedIt(t *testing.T) {
	post := candidate("post", "https://inventwithpython.com/blog/2023/08/13/python-gotchas",
		"8 Common Python Gotchas", "Invent with Python", "feed", repeat(prose, 3))

	junk := []index.Candidate{
		candidate("docs", "https://docs.python.org/3.12/",
			"Python 3.12 documentation", "Python documentation", "sitemap", repeat(listing, 3)),
		candidate("archive", "https://pythonbytes.fm/episodes/all",
			"Python Bytes Full Episode Archive", "Python Bytes", "sitemap", repeat(listing, 3)),
		candidate("issue", "https://pythonweekly.com/archive/358",
			"Python Weekly (Issue 358 - August 2 2018)", "Python Weekly", "feed", repeat(listing, 3)),
		candidate("home", "https://inventwithpython.com/",
			"Invent with Python", "Invent with Python", "sitemap", repeat(prose, 3)),
		candidate("meetup", "https://python-blog.blogspot.com/2007/09/grupo.html",
			"Grupo de Usuários Python - Bahia", "Python Experiments", "feed", repeat(portuguese, 3)),
	}

	good := Judge(post, 0).Quality
	if good <= 0 {
		t.Fatalf("the article scored %.3f, so nothing below can mean anything", good)
	}

	for _, c := range junk {
		if got := Judge(c, 0).Quality; got >= good {
			t.Errorf("%s scored %.3f, at or above the article's %.3f", c.ID, got, good)
		}
	}
}

// Popularity settles ties. It must never be able to create one, or the ranking is by
// fame rather than by writing.
func TestPopularityOnlyEverAdds(t *testing.T) {
	// Found by sitemap rather than by feed, which is the commonest reason a sound
	// article scores short of the top and so has room for popularity to matter.
	c := candidate("post", "https://example.com/post", "A post", "Someone", "sitemap", repeat(prose, 3))

	alone := Judge(c, 0).Quality
	famous := Judge(c, 1).Quality

	switch {
	case famous < alone:
		t.Errorf("popularity lowered the score: %.3f without, %.3f with", alone, famous)
	case famous == alone:
		t.Errorf("popularity changed nothing: %.3f", famous)
	case famous > 1:
		t.Errorf("score left the range: %.3f", famous)
	}
}

// The scale is a floor, not a gradient. Anything that is plainly an article reaches
// the top of it, and this is deliberate: the failure being corrected is landing pages
// outranking writing, not one good post outranking another. Sorting the good ones
// among themselves is what the query is for, and BM25 already does it.
func TestASoundArticleReachesTheTopOfTheScale(t *testing.T) {
	sound := candidate("post", "https://example.com/blog/post",
		"A post about importing modules", "Someone", "feed", repeat(prose, 3))

	if got := Judge(sound, 0).Quality; got != 1 {
		t.Errorf("a sound article scored %.3f, want the full 1: the scale is a floor", got)
	}
}

// A perfect article must not be held below one that has been shared, or every blog
// nobody has heard of is capped beneath every blog that has.
func TestPopularityCannotCapAPerfectArticle(t *testing.T) {
	if got := blend(1, 0); got != 1 {
		t.Errorf("blend(1, 0) = %.3f, want 1: an unknown blog can still be perfect", got)
	}
}

// The failures compound rather than replace one another: a site root whose title is
// the site's own name is not the same borderline case as either on its own.
func TestFailingTwoChecksCostsMoreThanFailingOne(t *testing.T) {
	base := Signals{Words: 800, English: true, Richness: 0.6, FromFeed: true}

	sound := ContentScore(base)

	rooted := base
	rooted.SiteRoot = true

	named := base
	named.TitleIsSite = true

	both := base
	both.SiteRoot, both.TitleIsSite = true, true

	if !(ContentScore(both) < ContentScore(rooted) && ContentScore(both) < ContentScore(named)) {
		t.Errorf("two failures (%.4f) did not cost more than one (%.4f / %.4f)",
			ContentScore(both), ContentScore(rooted), ContentScore(named))
	}
	if ContentScore(rooted) >= sound {
		t.Errorf("a site root scored %.4f against a sound article's %.4f", ContentScore(rooted), sound)
	}
}

// Another language is a reason to rank below an English post, not a reason to be
// unfindable: this index holds no other copy of it.
func TestAnotherLanguageKeepsSomeCredit(t *testing.T) {
	base := Signals{Words: 800, English: true, Richness: 0.6, FromFeed: true}
	foreign := base
	foreign.English = false

	got, want := ContentScore(foreign), ContentScore(base)
	if got <= 0 {
		t.Errorf("a post in another language scored %.4f, which is unfindable", got)
	}
	if got >= want {
		t.Errorf("a post in another language scored %.4f, at or above English's %.4f", got, want)
	}
}

// Length only ever counts against the short. The crawler truncates content at a
// thousand words, so anything that read length at the top would be measuring the
// truncation rather than the article.
func TestLengthCountsOnlyAtTheShortEnd(t *testing.T) {
	if got := lengthFactor(minWords); got != 0 {
		t.Errorf("lengthFactor(%d) = %.3f, want 0", minWords, got)
	}
	if got := lengthFactor(fullWords); got != 1 {
		t.Errorf("lengthFactor(%d) = %.3f, want 1", fullWords, got)
	}
	if lengthFactor(fullWords) != lengthFactor(10*fullWords) {
		t.Error("length still counted past the point it stops meaning anything")
	}
	if !(lengthFactor(200) > lengthFactor(100)) {
		t.Error("the ramp between the two ends does not rise")
	}
}

// Every score carries the version of the model that produced it, or nothing can ever
// be re-judged.
func TestJudgeStampsTheModelVersion(t *testing.T) {
	got := Judge(candidate("a", "https://example.com/a", "A", "B", "feed", repeat(prose, 3)), 0)

	if got.Version != Version {
		t.Errorf("version = %d, want %d", got.Version, Version)
	}
	if got.ID != "a" {
		t.Errorf("id = %q, want the article's", got.ID)
	}
	if got.WordCount == 0 {
		t.Error("word count was not recorded, so no score can ever be explained")
	}
}
