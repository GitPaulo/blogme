import { browser } from '$app/environment';
import { replaceState } from '$app/navigation';
import {
	clampQuery,
	maxOffsetFor,
	MIN_QUERY_LENGTH,
	search as fetchSearch,
	SearchError,
	type Rank,
	type SearchResult
} from './api';
import { bookmarks } from './bookmarks/store.svelte';
import { applyFilters, emptyFilters, isFiltered, type Filters } from './filters';
import { visited } from './visited/store.svelte';

/**
 * The state of a search: what was asked, what came back, and what is still in flight.
 *
 * This was a dozen loose variables in the page, mutated together in five places — a fresh
 * search, a failed one, a cleared box, a chase for more, and a submit — so that adding a
 * field to a response meant finding all five. Here the five are one file and the clear is
 * one assignment.
 *
 * Takes no arguments and owns its own debounce, so a caller sets `query` and reads
 * `results`. Everything about how the page looks stays in the page: what has focus,
 * which suggestion is highlighted, where the reader has scrolled to.
 */

/** How long the query holds still before a search goes out. */
const DEBOUNCE_MS = 300;
// How many pages one "load more" may fetch while filters hide everything that arrives.
// Twenty covers most of the deepest keyword range, so even a filter that matches a
// handful of posts in thousands resolves in a click or two.
const MAX_CHASE = 20;
// Requests are sequential, so cap the wall clock too: a slow API should shorten the
// chase rather than stretch one click into a stall.
const CHASE_BUDGET_MS = 3_000;

/**
 * Everything one page of answers says, held together because it is only ever replaced as
 * a whole. Raw for the same reason: deep state would proxy every row and charge a
 * subscription for each field the filter pass touches.
 */
type Answer = {
	results: SearchResult[];
	/**
	 * What the index reports, except once it says there is nothing left — then it is the
	 * rows in hand. The index counts documents, and rows are dropped after ranking for
	 * putting one blog over its share, so its figure is an upper bound on what paging can
	 * reach rather than a number the reader will ever see reached.
	 */
	total: number;
	/**
	 * Where the next page starts, as the API reported it. Not a stride of our own: a page
	 * is wider than the rows it hands back, and counting by page size would step over
	 * whatever it dropped.
	 */
	nextOffset: number;
	/**
	 * Whether the index has run out. The only honest source for "that is all there is",
	 * because the rows on screen stop short of the total by an amount nothing here can
	 * predict.
	 */
	exhausted: boolean;
	/** Whether nothing matched every word, so these rows match any of them. */
	broadened: boolean;
};

const NOTHING: Answer = {
	results: [],
	total: 0,
	nextOffset: 0,
	exhausted: false,
	broadened: false
};

/** Where a search has got to. `done` covers an empty result set, which is still an answer. */
export type Status = 'idle' | 'loading' | 'done' | 'error';

export function createSearch() {
	let query = $state('');
	// The blogs a search is narrowed to, empty for an ordinary search.
	//
	// Set by picking a blog off the landing page, and cleared by typing. While it is set
	// the box holds the blog's name so the reader can see whose posts these are, but the
	// name is not what is searched for: the request sends no query at all and the API
	// filters on these ids. Searching for the name instead does not work, and cannot be
	// made to — a blog's name is not reliably in its own posts, and the per-source cap
	// would hold it to three rows even when it is. See docs/plans/popular-blogs-landing-plan.md.
	let sources = $state.raw<string[]>([]);
	// Which ranking the search box asks for. Semantic understands a query phrased as a
	// sentence; keyword is literal, and is the one that can page deep. Keyword is the
	// default: it needs no reranker call and has no page-depth limit.
	let semanticRanking = $state(false);
	let filters = $state(emptyFilters());

	let answer = $state.raw<Answer>(NOTHING);
	let status = $state<Status>('idle');
	let loadingMore = $state(false);
	let error = $state('');

	let timer: ReturnType<typeof setTimeout> | undefined;
	let controller: AbortController | undefined;
	// What the first page on screen was asked for, so a request for the same thing can be
	// recognised as one that has already been made. Not state, and that matters: reading
	// the status instead would make the debounce effect depend on something it sets, and
	// it would re-run itself.
	let requested = '';

	// Read before any effect can run, so writing the address bar back can never race
	// reading it and clear the search the reader arrived with.
	const opening = new URLSearchParams(browser ? location.search : '');

	const term = $derived(clampQuery(query));
	// Browsing a blog needs no words, so it is searchable whatever the box says.
	const browsing = $derived(sources.length > 0);
	const searchable = $derived(browsing || term.length >= MIN_QUERY_LENGTH);
	// What the request actually asks for: nothing, when the ids are doing the asking.
	const asked = $derived(browsing ? '' : term);
	const rank = $derived<Rank>(semanticRanking ? 'semantic' : 'keyword');

	const filtered = $derived(
		applyFilters(answer.results, filters, {
			isBookmarked: (url) => bookmarks.has(url),
			isVisited: (url) => visited.has(url)
		})
	);
	const loaded = $derived(answer.results.length);
	const shown = $derived(filtered.length);
	// Derived rather than recomputed per read: the summary and the note beside it both ask,
	// and a filter pass runs on every bookmark toggled.
	const partial = $derived(isFiltered(filters));
	const tooShort = $derived(term.length > 0 && !searchable);
	// A constant, not a getter: what the reader arrived with cannot change.
	const openedWithSearch = Boolean(opening.get('q') || opening.get('source'));
	// Three separate ways to be out of results, and the reader meets all three. The index
	// itself runs out, which only it can report; the count runs out; and the ranking mode
	// runs out of ordering it can vouch for, which is a limit only semantic has, because
	// only it has a reranked window to reach the end of.
	const hasMore = $derived(
		status === 'done' &&
			!answer.exhausted &&
			answer.nextOffset < answer.total &&
			answer.nextOffset <= maxOffsetFor(rank)
	);

	/** What a first page is asked for: two searches are the same one when these match. */
	function requestKey(value: string, ranking: Rank, srcs: string[]) {
		// A separator no query survives being trimmed to, so a rank and a query cannot run
		// together into the same string as some other pair. Written as an escape rather
		// than the byte itself, which would make the whole file binary to git and grep.
		return `${ranking}\0${srcs.join(',')}\0${value}`;
	}

	function cancel() {
		clearTimeout(timer);
		timer = undefined;
		controller?.abort();
		controller = undefined;
	}

	// Puts the search on screen in the address bar, so it can be shared, reloaded or
	// returned to. Carries what the server was asked for and nothing else: the rest of
	// the filters narrow the rows already fetched, and a fresh search clears them.
	//
	// Called once per search rather than once per keystroke. Both because a URL should
	// describe results that exist, and because browsers throttle history writes: Safari
	// stops at a hundred in thirty seconds, which is a fast typist.
	function syncUrl(value: string, ranking?: Rank, srcs: string[] = []) {
		const params = new URLSearchParams();
		// The blog's name rides along even though it is not what was searched for: it is
		// what the box shows, and a link restoring the results without the name would
		// leave the reader looking at one blog's posts with an empty box above them.
		if (srcs.length > 0) params.set('source', srcs.join(','));
		if (value || srcs.length > 0) {
			if (value) params.set('q', value);
			if (ranking === 'semantic') params.set('mode', ranking);
		}

		const next = params.toString();
		if (next === location.search.slice(1)) return;
		replaceState(next ? `?${next}` : location.pathname, {});
	}

	// Offset paging can repeat a row if the index changes between pages, and the keyed
	// each block would throw on the duplicate.
	function merge(existing: SearchResult[], incoming: SearchResult[]) {
		const seen = new Set(existing.map((result) => result.url));
		return [...existing, ...incoming.filter((result) => !seen.has(result.url))];
	}

	/**
	 * Leaves a blog and goes back to searching.
	 *
	 * Guarded rather than assigned outright: a fresh array every call is a new value to
	 * the effect that reads it, which would re-trigger the clear branch forever.
	 */
	function leaveBlog() {
		if (sources.length > 0) sources = [];
	}

	/** Resolves true when this call is the one that landed a page. */
	async function run(value: string, offset: number, ranking: Rank, srcs: string[]) {
		cancel();
		const current = new AbortController();
		controller = current;

		if (offset === 0) {
			status = 'loading';
			// Recorded before the request rather than after it, so that a second ask
			// arriving while this one is still in flight is recognised too. See submit.
			requested = requestKey(value, ranking, srcs);
			// Filters describe the result set on screen, so a fresh search starts clean.
			filters = emptyFilters();
			// `term`, not `value`: browsing a blog searches for nothing, and an address
			// holding the ids alone would restore the results above an empty box. What
			// goes in the address is what the box shows.
			syncUrl(term, ranking, srcs);
		}
		error = '';

		try {
			const response = await fetchSearch(value, {
				offset,
				rank: ranking,
				sources: srcs,
				signal: current.signal
			});
			// A newer search (or a cleared query) owns the UI now, so drop this answer.
			if (controller !== current) return false;

			const results = offset === 0 ? response.results : merge(answer.results, response.results);
			answer = {
				results,
				// Reaching the end settles the count: what is on screen is the whole answer.
				total: response.exhausted ? results.length : response.total,
				nextOffset: response.nextOffset,
				exhausted: response.exhausted,
				broadened: response.broadened
			};
			status = 'done';
			return true;
		} catch (e) {
			if (controller !== current) return false;
			// A chase can outrun the API's own rate limit, which is this code's doing
			// rather than a fault the reader should be shown: the pages already on screen
			// stay, the button stays live, and the next click a moment later works. Only a
			// first page reports it, because there the reader asked and got nothing.
			if (offset > 0 && e instanceof SearchError && e.status === 429) return false;

			error = e instanceof Error ? e.message : 'Something went wrong.';
			// A failed page keeps the pages already on screen; only a failed first page
			// has nothing left to show.
			if (offset === 0) {
				answer = NOTHING;
				status = 'error';
				// Nothing was loaded, so nothing has been asked for as far as the next
				// submit is concerned: pressing Enter again is a retry and must go out.
				requested = '';
			}
			return false;
		} finally {
			if (controller === current) controller = undefined;
		}
	}

	// Reopens the search a shared or reloaded link describes. Done in an effect rather
	// than in the initial state because the page is prerendered holding an empty box,
	// and hydrating a different one would not match the markup already on screen.
	$effect(() => {
		query = clampQuery(opening.get('q') ?? '');
		// Ids are checked by the API, which refuses a malformed one rather than
		// widening the search, so nothing here has to validate what the address bar says.
		sources = (opening.get('source') ?? '').split(',').filter(Boolean);
		semanticRanking = opening.get('mode') === 'semantic';
	});

	$effect(() => {
		if (!searchable) {
			cancel();
			answer = NOTHING;
			filters = emptyFilters();
			error = '';
			status = 'idle';
			// Nothing has been asked for any more, so typing that query again searches.
			requested = '';
			leaveBlog(); // An empty box is not a blog either.
			syncUrl(''); // No search to describe, so the address goes back to bare.
			return;
		}

		const value = asked;
		// Read inside the effect so flipping the ranking mode re-runs the current search
		// rather than only affecting the next one the user types.
		const ranking = rank;
		const srcs = sources;

		// Already on screen, so there is nothing to fetch. This runs whenever the query or
		// the ranking changes, and both can change back: flipping the ranking on and off
		// again, or typing a letter and deleting it before the debounce elapsed, each end
		// where they started and would otherwise re-request the page already showing. A
		// failed search clears `requested`, so a retry is never suppressed.
		if (requested === requestKey(value, ranking, srcs)) return;

		// The pending debounce is still work in progress, so the spinner stays up throughout.
		status = 'loading';
		clearTimeout(timer);
		timer = setTimeout(() => run(value, 0, ranking, srcs), DEBOUNCE_MS);
		return () => clearTimeout(timer);
	});

	// Leaving the page should not keep a request alive.
	$effect(() => () => cancel());

	return {
		get query() {
			return query;
		},
		set query(value: string) {
			query = value;
		},
		get sources() {
			return sources;
		},
		get semanticRanking() {
			return semanticRanking;
		},
		get filters() {
			return filters;
		},
		set filters(value: Filters) {
			filters = value;
		},

		/** The query as the API would be asked it: trimmed, and held to the length cap. */
		get term() {
			return term;
		},
		get rank() {
			return rank;
		},
		/** Whether a blog is being browsed rather than a query searched for. */
		get browsing() {
			return browsing;
		},
		get searchable() {
			return searchable;
		},
		/** Something was typed, but not yet enough of it to be worth a round trip. */
		get tooShort() {
			return tooShort;
		},
		/** Whether the link the reader arrived on already described a search. */
		openedWithSearch,

		get results() {
			return answer.results;
		},
		/** The rows that survive the filters, which is what the page renders. */
		get filtered() {
			return filtered;
		},
		get total() {
			return answer.total;
		},
		get exhausted() {
			return answer.exhausted;
		},
		get broadened() {
			return answer.broadened;
		},
		/** How many rows are in hand, before the filters narrow them. */
		get loaded() {
			return loaded;
		},
		/** How many survive the filters. */
		get shown() {
			return shown;
		},
		get status() {
			return status;
		},
		get loadingMore() {
			return loadingMore;
		},
		get error() {
			return error;
		},
		get hasMore() {
			return hasMore;
		},
		/** Whether any filter is narrowing the rows on screen. */
		get partial() {
			return partial;
		},

		/**
		 * Skips the pending debounce rather than queueing a second request.
		 *
		 * Enter is a way to have the results now rather than a way to ask for them again.
		 * By the time it is pressed the debounced search has usually already run, so
		 * running it again fetches a page that is on screen — and because a fresh search
		 * aborts whatever is in flight, holding Enter down turned into a run of requests
		 * each cancelling the one before it. Nothing to skip means nothing to do.
		 *
		 * A failed search clears `requested`, so this never suppresses a retry: there the
		 * query has been asked for and has nothing to show, and Enter means try again.
		 */
		submit() {
			clearTimeout(timer);
			if (!searchable) return;
			if (requested === requestKey(asked, rank, sources)) return;
			run(asked, 0, rank, sources);
		},

		/**
		 * Fetches until something new is on screen, and reports whether anything was.
		 *
		 * Filters narrow the page rather than the query behind it, so a page can arrive
		 * holding nothing they let through and the button appears to do nothing. Keep
		 * paging until something shows, within a bounded number of requests and a time
		 * budget so a filter matching almost nothing cannot turn one click into a walk to
		 * the end.
		 */
		async loadMore() {
			if (!hasMore || loadingMore) return false;

			// Pinned for the whole chase: reading these per request would pair a query the
			// user has since edited with an offset counted against the previous one.
			const value = asked;
			const ranking = rank;
			const srcs = sources;
			const before = shown;

			loadingMore = true;
			try {
				const deadline = Date.now() + CHASE_BUDGET_MS;
				for (let page = 0; page < MAX_CHASE; page++) {
					if (!(await run(value, answer.nextOffset, ranking, srcs))) break;
					if (shown > before || !hasMore || Date.now() >= deadline) break;
					// The search this chase belongs to is no longer the one on screen. Leaving
					// now also spares the debounce the next run() would cancel out from under.
					if (asked !== value || rank !== ranking || sources !== srcs) break;
				}
			} finally {
				loadingMore = false;
			}

			return shown > before && asked === value;
		},

		toggleRanking() {
			semanticRanking = !semanticRanking;
		},

		/** Turns semantic ranking on, which the effect above re-runs the search for. */
		useSemanticRanking() {
			semanticRanking = true;
		},

		/**
		 * Shows one blog's posts. The box gets the blog's name so the reader can see whose
		 * posts these are; the ids are what the request actually filters on.
		 */
		browseBlog(name: string, ids: string[]) {
			query = name;
			sources = ids;
		},

		/**
		 * Called from the input event rather than watched, because browsing puts the blog's
		 * name in the box itself: a watcher could not tell the two apart, and would close
		 * the view the instant it opened.
		 */
		leaveBlog,

		/**
		 * Empties the box. Everything else — the results, the filters, the error, the
		 * address bar — follows from the effect above, which is watching the query.
		 *
		 * Emptying the box is not enough once a blog is being browsed: the ids alone keep
		 * the page searchable, so clearing only the query would leave the reader on that
		 * blog's posts. The ranking mode is left as it was, because emptying the box by
		 * hand does not turn it off either.
		 */
		clear() {
			query = '';
			leaveBlog();
		}
	};
}
