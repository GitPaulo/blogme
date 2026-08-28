/**
 * The searches this browser has run, most recent first.
 *
 * Kept in `localStorage` rather than in IndexedDB, which is what
 * [bookmarks](bookmarks/db.ts) and [visited](visited/db.ts) use. Those hold collections
 * too large to keep in memory — a history of hundreds of thousands of urls — and pay for
 * an asynchronous read to do it. This is at most a couple of dozen short strings, and
 * reading it synchronously is what lets the first keystroke show a match rather than
 * showing nothing and correcting itself a frame later.
 *
 * Nothing here leaves the browser. Storage that is unavailable — a private window, a
 * browser set to block site data — reads as no history and writes nothing, which costs
 * the reader a convenience and never an error.
 */

const KEY = 'blogme/recent-searches';

/**
 * How many searches are kept.
 *
 * Enough that a session's worth of queries survives, small enough that the whole list is
 * read, filtered and written on every keystroke without anyone noticing. Beyond this the
 * oldest is dropped, which is also what the age limit below would eventually do.
 */
const MAX_ENTRIES = 25;

/** How long a search is remembered. A month, after which it is not recent. */
const TTL_MS = 30 * 24 * 60 * 60 * 1000;

/** A query, and when it was last searched for. */
type Entry = { q: string; at: number };

/**
 * Read at module scope rather than on first use, so a caller never writes state while
 * reading it. During prerendering there is no `localStorage`, so this is the empty list
 * and the module is evaluated again in the browser with the real one.
 */
let entries = $state.raw<Entry[]>(read());

/** The stored list, oldest entries and anything malformed dropped. */
function read(): Entry[] {
	let raw: string | null = null;
	try {
		raw = localStorage.getItem(KEY);
	} catch {
		return []; // Storage exists but refuses to be read, which is a fine answer.
	}
	if (!raw) return [];

	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return []; // Someone else's key, or a half-written value. Treated as no history.
	}
	if (!Array.isArray(parsed)) return [];

	// Written by an older version of this app, or by hand: every field is checked rather
	// than trusted, and the age limit is applied on the way in so an expired search is
	// never offered even if nothing has been written since it lapsed.
	const oldest = Date.now() - TTL_MS;
	return parsed
		.filter(
			(entry): entry is Entry =>
				typeof entry === 'object' &&
				entry !== null &&
				typeof (entry as Entry).q === 'string' &&
				typeof (entry as Entry).at === 'number' &&
				(entry as Entry).at > oldest
		)
		.slice(0, MAX_ENTRIES);
}

function write(next: Entry[]) {
	entries = next;
	try {
		localStorage.setItem(KEY, JSON.stringify(next));
	} catch {
		// Out of quota, or storage is blocked. This session keeps its own list either
		// way, and a search history is not worth telling anyone it could not be saved.
	}
}

/** Word-boundary prefix matching, which is how a search box is expected to behave. */
function matches(entry: string, query: string): boolean {
	// A space in front of both, so the query has to begin a word rather than land in the
	// middle of one: "own" finds "rust ownership rules" and "ust" does not.
	return ` ${entry.toLowerCase()}`.includes(` ${query.toLowerCase()}`);
}

export const recent = {
	/** Every remembered search, most recent first. */
	get all(): readonly string[] {
		return entries.map((entry) => entry.q);
	},

	/**
	 * Remembers a search, moving it to the front if it was already there.
	 *
	 * Called when a reader commits to a query — submitting it, picking a completion,
	 * opening a result — rather than on every keystroke the debounced search runs, which
	 * would fill the history with the prefixes typed on the way to a real query.
	 *
	 * Re-reads the stored list before writing, so two tabs searching in parallel merge
	 * rather than overwrite one another: the whole list lives under one key, and a write
	 * from memory would carry away whatever the other tab had added.
	 */
	record(query: string) {
		const q = query.trim();
		if (!q) return;

		const now = Date.now();
		const kept = read().filter((entry) => entry.q.toLowerCase() !== q.toLowerCase());
		write([{ q, at: now }, ...kept].slice(0, MAX_ENTRIES));
	},

	/**
	 * Up to `limit` remembered searches that carry on from what has been typed, most
	 * recent first.
	 *
	 * A search identical to what is already in the box is left out: it would offer the
	 * reader the line they are looking at, and spend a row of a short list doing it.
	 */
	matching(query: string, limit: number): string[] {
		const q = query.trim();
		if (!q) return [];

		const found: string[] = [];
		for (const entry of entries) {
			if (found.length === limit) break;
			if (entry.q.toLowerCase() === q.toLowerCase()) continue;
			if (matches(entry.q, q)) found.push(entry.q);
		}
		return found;
	}
};
