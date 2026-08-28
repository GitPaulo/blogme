import { MAX_SUGGEST_LENGTH, MIN_SUGGEST_LENGTH, suggest } from './api';

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
