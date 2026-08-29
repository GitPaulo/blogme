import { MAX_SUGGEST_LENGTH, MIN_SUGGEST_LENGTH, suggest } from './api';

/**
 * One row of the search box's dropdown.
 *
 * `kind` is what the row is, not how it looks: a completion the index offered and a
 * search this browser has run before are different claims, and the reader is told which
 * is which by the icon beside it.
 */
export type Suggestion = { text: string; kind: 'recent' | 'query' };

/** One run of a suggestion, and whether it is the part the reader has already typed. */
export type Segment = { text: string; match: boolean };

/**
 * Where `query` appears inside `text`, or -1 for nowhere.
 *
 * A word boundary first, which is where a reader expects to have matched: typing "own"
 * should point at "rust ownership rules" rather than at the "own" inside "downstream".
 * Anywhere at all is the fallback, because what the index completes is not always aligned
 * to a word the reader typed.
 *
 * Searching with a space in front of both is the same trick recent.matches uses, and the
 * index it returns needs no adjusting: the space shifts the haystack right by one and the
 * match starts one character earlier, so the two cancel.
 */
function matchAt(text: string, query: string): number {
	const needle = query.trim().toLowerCase();
	if (!needle) return -1;

	const haystack = text.toLowerCase();
	const boundary = ` ${haystack}`.indexOf(` ${needle}`);
	return boundary >= 0 ? boundary : haystack.indexOf(needle);
}

/**
 * Splits a suggestion around the part of it the query matched, so the row can show which
 * of the words in front of the reader are their own.
 *
 * Returns segments rather than markup: the text is third-party — an indexed blog title,
 * or whatever was last typed into the box — and handing it back as a string to be
 * interpolated is how the highlighting of untrusted text becomes an injection. Rendered
 * through Svelte's own escaping, a suggestion of `<img onerror=...>` is those characters
 * and nothing else.
 *
 * Only the first occurrence is marked. A word appearing twice in one suggestion is not a
 * second thing to point at, and two bold runs in a short row read as emphasis rather than
 * as an answer to "why is this here".
 */
export function highlight(text: string, query: string): Segment[] {
	const at = matchAt(text, query);
	if (at < 0) return [{ text, match: false }];

	// Sliced out of the suggestion rather than taken from the query, so the row shows the
	// suggestion's own capitalisation instead of however the reader happened to type it.
	const length = query.trim().length;
	return [
		{ text: text.slice(0, at), match: false },
		{ text: text.slice(at, at + length), match: true },
		{ text: text.slice(at + length), match: false }
	].filter((segment) => segment.text !== '');
}

/**
 * The dropdown's rows: remembered searches first, then completions from the index,
 * `limit` in all.
 *
 * Recent searches go on top because they are the stronger answer — a query this reader
 * has already run beats one the corpus merely could answer — and there are never many of
 * them, so they cannot crowd out the completions underneath.
 *
 * A completion identical to a remembered search is dropped rather than repeated. Which
 * of the two survives is not arbitrary: the remembered one is the reader's own, and
 * saying so is more use than offering the same words twice.
 *
 * Completions that no longer match `query` are dropped, and that is what keeps the list
 * honest between requests. The store below holds the last answer while the next one is
 * in flight, which is what stops the list emptying and refilling on every keystroke —
 * but the answer it holds was for an earlier query, and a request that fails or times
 * out leaves it holding that one for good. Filtering here means a completion survives
 * exactly as long as it still reads as an answer to what is in the box: extending "rus"
 * to "rust" keeps every row, and a request that dies leaves nothing stale behind it.
 */
export function merge(
	recents: string[],
	completions: string[],
	query: string,
	limit: number
): Suggestion[] {
	const seen = new Set(recents.map((text) => text.toLowerCase()));
	const rows: Suggestion[] = recents.map((text) => ({ text, kind: 'recent' }));

	for (const text of completions) {
		if (rows.length === limit) break;
		if (seen.has(text.toLowerCase())) continue;
		if (matchAt(text, query) < 0) continue;
		seen.add(text.toLowerCase());
		rows.push({ text, kind: 'query' });
	}

	return rows.slice(0, limit);
}

/**
 * How long the query has to hold still before completions are asked for.
 *
 * Shorter than the search debounce, because the completion is meant to arrive while the
 * reader is still deciding what to type, and the search that follows it is the slower
 * of the two anyway. Short enough to feel immediate, long enough that a word typed at
 * speed costs one request rather than one per letter.
 */
const DEBOUNCE_MS = 120;

/**
 * How many queries are remembered.
 *
 * The cache is what makes this cheap. Typing forward through a word and backspacing
 * over it revisits the same prefixes, and a reader who searches, edits and searches
 * again walks the same ones a third time; each hit is a request that never leaves the
 * browser. A hundred entries covers a long session for a few kilobytes, and evicting
 * the oldest is enough because the prefixes worth keeping are the recent ones.
 */
const CACHE_LIMIT = 100;

/**
 * Completions for the query being typed.
 *
 * Takes a getter rather than the query itself, so it tracks whatever the caller reads
 * inside it. Like any rune, call it while the component is initialising.
 *
 * A query with nothing to complete, or one the API refuses, reads as no completions:
 * this is a convenience beside the search box, and a reader mid-word can do nothing
 * with an error about it. Failures are left to the API's own logging, which is where
 * they can be seen without a message in front of someone typing.
 */
export function suggestions(query: () => string) {
	let current = $state.raw<string[]>([]);
	const cache = new Map<string, string[]>();

	$effect(() => {
		const term = query();

		// Nothing to complete, so nothing is asked and nothing is shown. Checked here as
		// well as in `suggest` so a query outside the bounds does not even wait out the
		// debounce before clearing what is on screen.
		if (term.length < MIN_SUGGEST_LENGTH || term.length > MAX_SUGGEST_LENGTH) {
			current = [];
			return;
		}

		// Answered without a request, and without the debounce: the completions for this
		// query are already known, so waiting would only make a known answer late.
		const known = cache.get(term);
		if (known) {
			current = known;
			return;
		}

		const controller = new AbortController();
		const timer = setTimeout(async () => {
			try {
				const found = await suggest(term, { signal: controller.signal });
				remember(cache, term, found);
				current = found;
			} catch {
				// Including the abort below, which is this effect being superseded rather
				// than anything going wrong. Either way the answer is stale, and the run
				// that replaced it owns what is on screen.
			}
		}, DEBOUNCE_MS);

		return () => {
			clearTimeout(timer);
			controller.abort();
		};
	});

	return {
		get current() {
			return current;
		}
	};
}

/** Stores a query's completions, dropping the oldest once the cache is full. */
function remember(cache: Map<string, string[]>, term: string, found: string[]) {
	// An empty answer is worth keeping too: it is the one that stops a query with
	// nothing to complete being asked about on every keystroke that follows it.
	cache.set(term, found);

	if (cache.size > CACHE_LIMIT) {
		// Map iterates in insertion order, so the first key is the oldest.
		const oldest = cache.keys().next();
		if (!oldest.done) cache.delete(oldest.value);
	}
}
