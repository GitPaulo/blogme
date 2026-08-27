/**
 * How a result card's description is cut down to the space it has.
 *
 * The rule is the one search engines settle on: end on a sentence where a sentence end
 * is close enough to the budget to be worth ending on, otherwise end on a word and say
 * so with an ellipsis. Ending mid-clause with no mark at all — which is what a bare
 * word cut gives you — reads as prose that simply stops, and a reader cannot tell a
 * truncated description from a badly written one.
 *
 * The API applies the same rule at a word cap when it indexes an article: see
 * truncateSentences in api/internal/discovery/extract.go. This is the second pass, and
 * the one that knows the card's width. Neither the crawler nor this appends anything
 * when it cuts; the ellipsis is added once, here, at the end.
 */

/** Lines a card gives the description. Mirrors the line-clamp on the rendered element. */
export const SNIPPET_LINES = 3;

/**
 * What a cut description ends with. A space before the ellipsis keeps it from reading
 * as a full stop the author wrote, which is the whole point of the mark.
 */
export const ELLIPSIS = ' …';

/**
 * How much of the budget a sentence-boundary cut has to keep to be worth taking. Below
 * this, an article opening on a one-liner — "Let's talk about locks." — would leave the
 * card showing four words where forty were available. Mirrors sentenceFloor in
 * api/internal/discovery/extract.go.
 */
const SENTENCE_FLOOR = 0.6;

/**
 * Average glyph width at the card's 16px body text, in pixels. Measured at 7.7 on the
 * card's own font and rounded up from there: the estimate has to land under what three
 * lines really hold, because the CSS clamp behind it is a backstop that should never be
 * the thing a reader sees fire. The margin costs a few characters of the last line.
 */
const AVG_CHAR_PX = 8.2;

/** Room lost at each line end to the word that did not fit. */
const WRAP_SLACK_CHARS = 4;

/** Narrow enough that a smaller budget would say nothing at all. */
const MIN_BUDGET_CHARS = 60;

/**
 * Words that end in a full stop without ending a sentence. Mirrors the abbreviations
 * table in api/internal/discovery/extract.go.
 */
const ABBREVIATIONS = new Set([
	'al',
	'approx',
	'cf',
	'dr',
	'eg',
	'etc',
	'fig',
	'ie',
	'inc',
	'ltd',
	'mr',
	'mrs',
	'ms',
	'no',
	'prof',
	'st',
	'vs'
]);

const CLOSING = /["'”’)\]}»]+$/;
const OPENING = /^["'“”‘’([{«]+/;

/**
 * Whether a word closes a sentence rather than merely ending in a full stop. Quotes and
 * brackets come off first, so a sentence inside quotation marks still counts.
 */
function endsSentence(word: string): boolean {
	const trimmed = word.replace(CLOSING, '');
	const last = trimmed.slice(-1);
	if (last === '!' || last === '?' || last === '…') return true;
	if (last !== '.') return false;

	const stem = trimmed.slice(0, -1).replace(OPENING, '').replace(CLOSING, '');
	// A single letter is an initial: "J. Random Hacker".
	if ([...stem].length <= 1) return false;
	// An interior dot before a letter is the shape of a lettered abbreviation: "e.g.",
	// "U.S.", "Ph.D.".
	if (/\.\p{L}/u.test(stem)) return false;
	return !ABBREVIATIONS.has(stem.toLowerCase());
}

/** Every word in the text with the offset just past it, in order. */
function wordEnds(text: string): { word: string; end: number }[] {
	return [...text.matchAll(/\S+/gu)].map((match) => ({
		word: match[0],
		end: match.index + match[0].length
	}));
}

/**
 * How many characters of description fit, given the width the text has to run in.
 *
 * Returns 0 for a width that has not been measured yet, which callers read as "no
 * budget known" and render the description whole, leaving the CSS clamp to hold the
 * card's height until the first measurement lands.
 */
export function snippetBudget(textWidth: number, lines: number = SNIPPET_LINES): number {
	if (!(textWidth > 0)) return 0;
	const perLine = textWidth / AVG_CHAR_PX - WRAP_SLACK_CHARS;
	return Math.max(Math.floor(perLine * lines), MIN_BUDGET_CHARS);
}

/**
 * Cuts text to roughly maxChars, preferring to stop on a sentence.
 *
 * An unmarked tail is the signal that something was dropped, whether it was dropped
 * here or by the crawler before the text ever arrived: a description that fits the
 * budget and still ends mid-clause was cut upstream, and gets the ellipsis just the
 * same. A budget of 0 means the width is not known yet and returns the text unchanged.
 */
export function snippet(text: string, maxChars: number): string {
	const trimmed = text.trim();
	if (maxChars <= 0 || trimmed === '') return trimmed;

	const words = wordEnds(trimmed);
	let clipped = trimmed;
	if (trimmed.length > maxChars) {
		const fits = words.filter((entry) => entry.end <= maxChars);
		// A first word longer than the whole budget has no boundary to cut on, so it is
		// cut where it stands rather than dropped.
		clipped = fits.length
			? trimmed.slice(0, fits[fits.length - 1].end)
			: trimmed.slice(0, maxChars);
	}

	// Walked back from the cut, so the last sentence that still clears the floor wins.
	// Text ending on a sentence of its own is caught by the first step of this walk,
	// and returned whole and unmarked.
	const floor = clipped.length * SENTENCE_FLOOR;
	for (let i = words.length - 1; i >= 0; i--) {
		const { word, end } = words[i];
		if (end > clipped.length) continue;
		if (end < floor) break;
		if (endsSentence(word)) return trimmed.slice(0, end);
	}

	return clipped + ELLIPSIS;
}
