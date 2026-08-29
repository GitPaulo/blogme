/**
 * What shape a query is in, which is the only thing the search box can know about it
 * without asking the index.
 */

/**
 * The words a question opens with, and the words that have to follow one.
 *
 * Both halves are required, and that is the whole design. "how" alone opens "how I built
 * my blog", which is a title somebody wrote and a keyword search finds exactly. "how do"
 * opens nothing else. Requiring a pair turns a signal that fires on ordinary searches
 * into one that fires on questions.
 */
const OPENERS = new Set(['why', 'how', 'what', 'when', 'where', 'who', 'whom', 'whose', 'which']);

/** What an opener has to be followed by for the phrase to be a question rather than a title. */
const FOLLOWERS = new Set([
	'is',
	"isn't",
	'are',
	"aren't",
	'was',
	'were',
	'do',
	"don't",
	'does',
	"doesn't",
	'did',
	'can',
	"can't",
	'could',
	'should',
	'would',
	'will',
	"won't",
	'has',
	'have',
	'had',
	'am',
	'to'
]);

/**
 * A question asked of the reader's own machine — "can I", "should we" — which opens with
 * the auxiliary rather than an interrogative.
 */
const SUBJECTS = new Set(['i', 'we', 'you', 'it', 'my', 'this', 'that', 'there']);

/**
 * Whether a query reads as a question rather than as words to look up.
 *
 * Deliberately hard to trigger. It exists to offer the reader semantic ranking, and an
 * offer that appears over ordinary searches is worse than no offer at all — it teaches
 * people to ignore it. So it fires on two things only: a query that ends in a question
 * mark, and one that opens with a pair of words no title begins with.
 *
 * What it must not fire on is the reason it is written this way. "the rust book", "a tour
 * of go" and "the art of computer programming" are full of function words and are not
 * questions; "how i built my blog" and "what the hell happened to css" are titles. None
 * of them open with an interrogative followed by an auxiliary.
 */
export function looksLikeAQuestion(query: string): boolean {
	const trimmed = query.trim();
	if (!trimmed) return false;

	// Nobody types one of these by accident, and it is the only signal that needs no
	// vocabulary at all.
	if (trimmed.endsWith('?')) return true;

	const words = trimmed.toLowerCase().split(/\s+/);
	// A question needs something to ask about: "how do" on its own is half a thought,
	// and offering to re-rank two words helps nobody.
	if (words.length < 3) return false;

	const [first, second] = words;
	if (OPENERS.has(first) && FOLLOWERS.has(second)) return true;
	// "can i run postgres locally", "should we use kubernetes".
	if (FOLLOWERS.has(first) && SUBJECTS.has(second)) return true;

	return false;
}
