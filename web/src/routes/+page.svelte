<script lang="ts">
	import { Alert, Badge, Button, Card, Heading, Input, P, Spinner, Tooltip } from 'flowbite-svelte';
	import {
		ChevronDoubleUpOutline,
		SearchOutline,
		WandMagicSparklesOutline
	} from 'flowbite-svelte-icons';
	import { tick } from 'svelte';
	import { prefersReducedMotion } from 'svelte/motion';
	import { fade } from 'svelte/transition';
	import { browser } from '$app/environment';
	import { replaceState } from '$app/navigation';
	import BookmarkButton from '$lib/components/BookmarkButton.svelte';
	import FilterBar from '$lib/components/FilterBar.svelte';
	import SearchSuggestions from '$lib/components/SearchSuggestions.svelte';
	import {
		clampQuery,
		MAX_QUERY_LENGTH,
		maxOffsetFor,
		MAX_SUGGESTIONS,
		MIN_QUERY_LENGTH,
		search,
		SearchError,
		type Rank,
		type SearchResult
	} from '$lib/api';
	import { bookmarks } from '$lib/bookmarks/store.svelte';
	import { formatDate } from '$lib/date';
	import { elementWidth } from '$lib/elementWidth.svelte';
	import { applyFilters, emptyFilters, isFiltered } from '$lib/filters';
	import { onScreen } from '$lib/onScreen.svelte';
	import { recent } from '$lib/recent.svelte';
	import { snippet, snippetBudget } from '$lib/snippet';
	import { merge as mergeSuggestions, suggestions, type Suggestion } from '$lib/suggestions.svelte';
	import { visited } from '$lib/visited/store.svelte';

	const DEBOUNCE_MS = 300;
	// How many pages one "load more" may fetch while filters hide everything that arrives.
	// Twenty covers most of the deepest keyword range, so even a filter that matches a
	// handful of posts in thousands resolves in a click or two.
	const MAX_CHASE = 20;
	// Requests are sequential, so cap the wall clock too: a slow API should shorten the
	// chase rather than stretch one click into a stall.
	const CHASE_BUDGET_MS = 3_000;
	// Gap left above the row "load more" scrolls to, clearing the fixed toolbar and leaving
	// a sliver of the previous row in view so the batch reads as a continuation.
	const SCROLL_TOP_GAP = 64;
	// What counts as the reader taking the page over mid-load. Read from input rather than
	// from scrollY, because the page also moves under our own smooth scroll and a position
	// check cannot tell the two apart: it cancelled the scroll on every click that landed
	// while the previous one was still animating.
	const SCROLL_INPUT = ['wheel', 'touchmove', 'keydown'] as const;
	const SCROLL_KEYS = new Set([' ', 'ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End']);
	// What a card's padding and border take off the row width before the description
	// gets any: p-4 either side, plus the Card's own hairline.
	const CARD_TEXT_INSET = 34;
	// How many remembered searches may take the top of the dropdown. Two, because they
	// are the reader's own and belong first, and because more than two would start
	// crowding out what the index has to offer on a list this short.
	const MAX_RECENT = 2;
	// The dropdown's own id, which is what ties it to the search box for a screen reader.
	const LISTBOX_ID = 'search-suggestions';
	// Hoisted: building a formatter is the expensive half of rendering the total.
	const decimal = new Intl.NumberFormat();

	let query = $state('');
	// Raw, because a page is only ever replaced or appended as a whole and never
	// edited in place. Deep state would instead proxy every row and charge a
	// subscription for each field the filter pass touches.
	let results = $state.raw<SearchResult[]>([]);
	let filters = $state(emptyFilters());
	// Which ranking the search box asks for. Semantic understands a query phrased as a
	// sentence; keyword is literal, and is the one that can page deep. Keyword is the
	// default: it needs no reranker call and has no page-depth limit.
	let semanticRanking = $state(false);
	let total = $state(0);
	let nextOffset = $state(0);
	// Whether the index has run out. Kept apart from the numbers because it is the only
	// honest source for "that is all there is": the API drops rows that put one blog
	// over its share, so the rows on screen stop short of the total by an amount nothing
	// here can predict.
	let exhausted = $state(false);
	let status = $state<'idle' | 'loading' | 'done' | 'error'>('idle');
	let loadingMore = $state(false);
	let error = $state('');
	// Whether the document is taller than the window, which is the only thing that makes
	// a shortcut back to the top worth offering.
	let scrollable = $state(false);

	// The last suggestion the reader picked, so it is not suggested back at them. See
	// `suggested` below.
	let accepted = $state('');
	// Whether the search box has the caret, and whether the reader has dismissed the
	// dropdown for the query now in it. Both are needed: a list that reopened on the next
	// suggestion to arrive would undo the Escape that closed it.
	let searchFocused = $state(false);
	let dismissed = $state(false);
	// The highlighted row, held as the suggestion itself rather than as its position.
	//
	// Suggestions land while the reader is still typing, so the list is reordered under
	// whatever they had arrowed onto. A remembered position would then point at a
	// different row than the one they chose, and Enter would search for it. Holding the
	// text means the highlight follows its row when the list changes around it and lets
	// go when that row is gone, which is both the smoother behaviour and the safe one.
	// Empty for no selection, in which case Enter searches for what was typed.
	let selected = $state('');
	// Bumped on every sign the reader is still with the list, which restarts the countdown
	// drawn around it. A counter rather than a timestamp: the list only needs to know that
	// something happened, not when.
	let interaction = $state(0);

	let searchInput = $state<HTMLInputElement>();
	// One element per visible result, so children[n] is result n.
	let resultList = $state<HTMLElement>();
	// The row holding "load more" and the shortcut back to the top, watched so the shortcut
	// can return as a floating one once the row itself has scrolled out of reach.
	let controlsRow = $state<HTMLElement>();
	let searchForm = $state<HTMLElement>();

	let timer: ReturnType<typeof setTimeout> | undefined;
	let controller: AbortController | undefined;
	// What the first page on screen was asked for, so a request for the same thing can be
	// recognised as one that has already been made. Not state: nothing renders it.
	let requested = '';

	// Read before any effect can run, so writing the address bar back can never race
	// reading it and clear the search the reader arrived with.
	const opening = new URLSearchParams(browser ? location.search : '');

	const term = $derived(clampQuery(query));
	const searchable = $derived(term.length >= MIN_QUERY_LENGTH);
	const tooShort = $derived(term.length > 0 && !searchable);
	const rank = $derived<Rank>(semanticRanking ? 'semantic' : 'keyword');
	// Three separate ways to be out of results, and the reader meets all three. The index
	// itself runs out, which only it can report; the count runs out; and the ranking mode
	// runs out of ordering it can vouch for, which is a limit only semantic has, because
	// only it has a reranked window to reach the end of.
	const hasMore = $derived(
		status === 'done' && !exhausted && nextOffset < total && nextOffset <= maxOffsetFor(rank)
	);
	const filtered = $derived(
		applyFilters(results, filters, {
			isBookmarked: (url) => bookmarks.has(url),
			isVisited: (url) => visited.has(url)
		})
	);
	// The counts and the formatted total are their own deriveds, so the summary string is
	// rebuilt only when a number it actually shows changes. Re-running the filter pass, as
	// a bookmark toggle or a half-typed date bound does, usually leaves both counts where
	// they were, and the total moves only on a new page.
	const loaded = $derived(results.length);
	const shown = $derived(filtered.length);
	const totalLabel = $derived(decimal.format(total));
	const partial = $derived(isFiltered(filters));
	// "about", until the index says there is nothing left. The figure it reports counts
	// documents, and rows are dropped from them after ranking, so it is an upper bound on
	// what paging can reach rather than a number the reader will ever see reached. Written
	// as a flat "of 27" it reads as a promise the last page always breaks.
	const summary = $derived(
		partial
			? `Showing ${shown} of ${loaded} loaded ${loaded === 1 ? 'result' : 'results'}`
			: exhausted
				? `Showing all ${totalLabel} ${total === 1 ? 'result' : 'results'}`
				: `Showing ${loaded} of about ${totalLabel} ${total === 1 ? 'result' : 'results'}`
	);
	// These filters narrow the rows already fetched rather than the query behind them, so
	// the figure they are counted against climbs every time another page arrives. Said out
	// loud because a total that grows while you page through it otherwise reads as a bug.
	const partialNote =
		'Filters apply to the results loaded so far, not the whole index, so both numbers grow each time you load more.';
	const rankLabel = $derived(
		semanticRanking
			? 'Semantic ranking: finds posts about the idea. Switch to keyword ranking.'
			: 'Keyword ranking: matches the words you typed. Switch to semantic ranking.'
	);
	const emptyMessage = $derived(
		`No results found. Try a different search or ${semanticRanking ? 'disable' : 'enable'} semantic search.`
	);
	// Filters narrow the rows already fetched rather than the query behind them, so an
	// empty list with pages still to come is a prompt rather than a dead end.
	const noMatchMessage = $derived(
		hasMore
			? 'No loaded results match these filters. Try loading more.'
			: 'No loaded results match these filters.'
	);

	// The bookmarked filter needs the saved keys, which the drawer would otherwise only
	// load on its own schedule.
	$effect(() => {
		bookmarks.load();
	});

	// The page is a search box with a page around it, so the caret starts in it — unless
	// the link already carries a search.
	//
	// Someone opening a shared or bookmarked result set came to read it, and focusing the
	// box for them opens the suggestion list on top of the results the moment they arrive:
	// suggestions for a query they did not type, over the answer they followed the link
	// for. Leaving the caret alone means the list appears when they go to the box, which
	// is when they have asked for it.
	$effect(() => {
		if (opening.get('q')) return;
		searchInput?.focus();
	});

	// Reopens the search a shared or reloaded link describes. Done in an effect rather
	// than in the initial state because the page is prerendered holding an empty box,
	// and hydrating a different one would not match the markup already on screen.
	$effect(() => {
		query = clampQuery(opening.get('q') ?? '');
		semanticRanking = opening.get('mode') === 'semantic';
	});

	// Watching the document rather than recomputing per render: every filter, page and
	// window resize changes the answer, and the observer already fires on all of them.
	$effect(() => {
		const measure = () => {
			scrollable = document.documentElement.scrollHeight > window.innerHeight;
		};
		const observer = new ResizeObserver(measure);
		observer.observe(document.documentElement);
		window.addEventListener('resize', measure);
		return () => {
			observer.disconnect();
			window.removeEventListener('resize', measure);
		};
	});

	// Completions for what is in the box. Against `term` rather than the raw query so
	// that the cache behind it is keyed by what the API would be asked anyway: a
	// trailing space is not a different question.
	//
	// A query the reader just picked is asked about as an empty one, which is to say not
	// at all. Left to itself the box would complete what it had only now completed: the
	// list reopens under the cursor showing the one line already in the box, and a
	// request is spent to fetch it. Editing the query again makes it a different one,
	// and suggestions resume on their own.
	const suggested = suggestions(() => (term === accepted ? '' : term));

	// Remembered searches are matched against whatever is in the box, including the one
	// and two characters the index is never asked about: they are held locally and cost
	// nothing to look through, so there is no reason to make the reader type a third
	// letter before showing them their own history.
	const options = $derived(
		mergeSuggestions(recent.matching(term, MAX_RECENT), suggested.current, term, MAX_SUGGESTIONS)
	);
	const suggestionsOpen = $derived(searchFocused && !dismissed && options.length > 0);
	// Where the highlighted suggestion currently sits, or -1 once it is no longer offered.
	// Derived rather than stored, so nothing has to be kept in step with the list.
	const active = $derived(selected ? options.findIndex((o) => o.text === selected) : -1);

	const controlsOnScreen = onScreen(() => controlsRow);
	// Every card is the same width, so one row measurement sizes all of their descriptions.
	const listWidth = elementWidth(() => resultList);
	const summaryChars = $derived(snippetBudget(listWidth.current - CARD_TEXT_INSET));
	// The shortcut exists to get back to the search box, so it has nothing left to offer
	// once the box is in view.
	const searchOnScreen = onScreen(() => searchForm);
	const floatingTop = $derived(loaded > 0 && !controlsOnScreen.current && !searchOnScreen.current);

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
	function syncUrl(value: string, ranking?: Rank) {
		const params = new URLSearchParams();
		if (value) {
			params.set('q', value);
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

	/** Resolves true when this call is the one that landed a page. */
	async function run(value: string, offset: number, ranking: Rank) {
		cancel();
		const current = new AbortController();
		controller = current;

		if (offset === 0) {
			status = 'loading';
			// Recorded before the request rather than after it, so that a second ask
			// arriving while this one is still in flight is recognised too. See onsubmit.
			requested = requestKey(value, ranking);
			// Filters describe the result set on screen, so a fresh search starts clean.
			filters = emptyFilters();
			syncUrl(value, ranking);
		}
		error = '';
		try {
			const response = await search(value, {
				offset,
				rank: ranking,
				signal: current.signal
			});
			// A newer search (or a cleared query) owns the UI now, so drop this answer.
			if (controller !== current) return false;
			const merged = offset === 0 ? response.results : merge(results, response.results);
			results = merged;
			// Reaching the end settles the count. Until then it is the index's figure,
			// which counts documents rather than rows and so overstates what paging can
			// reach: the rows dropped for putting one blog over its share are counted
			// there and unreachable both. Once there is nothing left to fetch, what is on
			// screen is the whole answer.
			total = response.exhausted ? merged.length : response.total;
			exhausted = response.exhausted;
			// The API's figure rather than a stride of our own: it drops rows that put
			// one blog over its share of a page, so a page is wider than the rows it
			// returns, and counting by page size would step over whatever it dropped.
			nextOffset = response.nextOffset;
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
				results = [];
				total = 0;
				nextOffset = 0;
				exhausted = false;
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

	// Filters narrow the page rather than the query behind it, so a page can arrive
	// holding nothing they let through and the button appears to do nothing. Keep paging
	// until something shows, within a bounded number of requests and a time budget so a
	// filter matching almost nothing cannot turn one click into a walk to the end.
	async function loadMore() {
		if (!hasMore || loadingMore) return;
		// Pinned for the whole chase: reading these per request would pair a query the
		// user has since edited with an offset counted against the previous one.
		const value = term;
		const ranking = rank;
		const before = shown;
		const readerTookOver = watchReaderScroll();
		loadingMore = true;
		try {
			const deadline = Date.now() + CHASE_BUDGET_MS;
			for (let page = 0; page < MAX_CHASE; page++) {
				if (!(await run(value, nextOffset, ranking))) break;
				if (shown > before || !hasMore || Date.now() >= deadline) break;
				// The search this chase belongs to is no longer the one on screen. Leaving
				// now also spares the debounce the next run() would cancel out from under.
				if (term !== value || rank !== ranking) break;
			}
		} finally {
			loadingMore = false;
		}

		// Detaches the watcher and reports whether the reader moved the page themselves.
		// After the chase settles rather than per page, so several pages move the reader once.
		if (!readerTookOver() && shown > before && term === value) await revealNewResults(before);
	}

	/** Reports, once detached, whether the reader scrolled the page while it was called. */
	function watchReaderScroll(): () => boolean {
		let taken = false;
		const mark = (event: Event) => {
			// Every wheel and touch counts; only the keys that actually scroll do.
			if (!(event instanceof KeyboardEvent) || SCROLL_KEYS.has(event.key)) taken = true;
		};
		const options = { passive: true, capture: true } as const;
		for (const type of SCROLL_INPUT) window.addEventListener(type, mark, options);
		return () => {
			for (const type of SCROLL_INPUT) window.removeEventListener(type, mark, options);
			return taken;
		};
	}

	// New rows land below the fold, so without this a click leaves the reader on the same
	// screen with the button they pressed now buried under everything that arrived.
	async function revealNewResults(firstNew: number) {
		await tick(); // The rows are in state but not yet on the page.

		const target = resultList?.children[firstNew];
		if (!target) return;

		// Clamped to what the document can actually offer, so a final short page scrolls to
		// the bottom rather than asking for a position past it. Computed rather than left to
		// scrollIntoView so the destination is a number this code chose and can check.
		const furthest = document.documentElement.scrollHeight - window.innerHeight;
		const top = window.scrollY + target.getBoundingClientRect().top - SCROLL_TOP_GAP;

		window.scrollTo({
			top: Math.max(0, Math.min(top, furthest)),
			behavior: prefersReducedMotion.current ? 'auto' : 'smooth'
		});
	}

	function toTop() {
		window.scrollTo({ top: 0, behavior: prefersReducedMotion.current ? 'auto' : 'smooth' });
	}

	function toggleRanking() {
		semanticRanking = !semanticRanking;
	}

	$effect(() => {
		if (!searchable) {
			cancel();
			results = [];
			filters = emptyFilters();
			total = 0;
			nextOffset = 0;
			exhausted = false;
			error = '';
			status = 'idle';
			// The completion that was accepted belonged to the search being cleared, so
			// typing it again is a new question and gets completed like any other.
			accepted = '';
			// And nothing has been asked for any more, so typing that query again searches.
			requested = '';
			syncUrl(''); // No search to describe, so the address goes back to bare.
			return;
		}

		const value = term;
		// Read inside the effect so flipping the ranking mode re-runs the current search
		// rather than only affecting the next one the user types.
		const ranking = rank;

		// Already on screen, so there is nothing to fetch. This runs whenever the query or
		// the ranking changes, and both can change back: flipping the ranking on and off
		// again, or typing a letter and deleting it before the debounce elapsed, each end
		// where they started and would otherwise re-request the page already showing.
		//
		// `requested` is a plain variable rather than state, which matters here — reading
		// the status instead would make this effect depend on something it sets, and it
		// would re-run itself. A failed search clears it, so a retry is never suppressed.
		if (requested === requestKey(value, ranking)) return;

		// The pending debounce is still work in progress, so the spinner stays up throughout.
		status = 'loading';
		clearTimeout(timer);
		timer = setTimeout(() => run(value, 0, ranking), DEBOUNCE_MS);
		return () => clearTimeout(timer);
	});

	// Leaving the page should not keep a request alive.
	$effect(() => () => cancel());

	// Taking a suggestion is choosing a whole query, not completing the word being typed.
	//
	// The list holds whole queries because it has to read as the thing that will be
	// searched for: a dropdown of "query", "queue", "quantum" under a box saying
	// "postgres qu" does not tell a reader what they are about to get. So this replaces
	// what is in the box rather than appending to it. The search needs no prompting —
	// the effect below already watches the query.
	function acceptSuggestion(option: Suggestion) {
		accepted = option.text;
		query = option.text;
		dismissed = true;
		selected = '';
		// Taking a suggestion is committing to it, whichever list it came from. A
		// remembered search moves back to the front for having been used again.
		recent.record(option.text);
		// The press was prevented from moving focus, but a click on a row still leaves
		// the caret where the reader last put it. Ending on the box means they can keep
		// typing.
		searchInput?.focus();
	}

	// The combobox keyboard contract. Only the keys that mean something to an open list
	// are taken; everything else, Home and End included, belongs to the text field.
	function onSearchKeydown(event: KeyboardEvent) {
		// Every key, before any of them is read: a reader still typing is a reader still
		// using the list, whether or not the key meant anything to it.
		interaction++;

		if (event.key === 'Escape') {
			// With the list closed, Escape belongs to the browser, which clears a search
			// field with it.
			if (!suggestionsOpen) return;
			event.preventDefault();
			dismissed = true;
			selected = '';
			return;
		}

		if (!suggestionsOpen) return;

		switch (event.key) {
			case 'ArrowDown':
				// Wrapping, and starting from the top on the first press.
				event.preventDefault();
				selected = options[(active + 1) % options.length].text;
				break;
			case 'ArrowUp':
				// Up from nothing is the last row, which is what makes the two symmetrical.
				event.preventDefault();
				selected = options[active <= 0 ? options.length - 1 : active - 1].text;
				break;
			case 'Enter':
				// Only when a row is chosen. Otherwise this is an ordinary submit of
				// whatever was typed, which is what the form below does with it.
				if (active >= 0) {
					event.preventDefault();
					acceptSuggestion(options[active]);
				}
				break;
			case 'Tab':
				// Focus is leaving, so the list has nothing left to be open for.
				dismissed = true;
				break;
		}
	}

	/** What a first page is asked for: two searches are the same one when these match. */
	function requestKey(value: string, ranking: Rank) {
		// A separator no query survives being trimmed to, so a rank and a query cannot run
		// together into the same string as some other pair.
		return `${ranking} ${value}`;
	}

	// Submitting skips the pending debounce rather than queueing a second request.
	function onsubmit(event: SubmitEvent) {
		event.preventDefault();
		clearTimeout(timer);
		if (!searchable) return;
		dismissed = true;
		// Pressing Enter is the plainest way a reader says this query was the one they
		// meant, which is what makes it worth remembering.
		recent.record(term);

		// Enter is a way to have the results now rather than a way to ask for them again.
		// By the time it is pressed the debounced search has usually already run, so
		// running it again fetches a page that is on screen — and because a fresh search
		// aborts whatever is in flight, holding Enter down turned into a run of requests
		// each cancelling the one before it. Nothing to skip means nothing to do.
		//
		// A failed search clears `requested`, so this never suppresses a retry: there the
		// query has been asked for and has nothing to show, and Enter means try again.
		if (requested === requestKey(term, rank)) return;

		run(term, 0, rank);
	}

	// Opening a result is the other way of saying it, and the more common one: most
	// searches here are typed, read and clicked without Enter ever being pressed, so a
	// history that only recorded submissions would stay empty for most readers.
	//
	// Delegated to the list rather than bound per row, and listened for rather than
	// written into the markup, which is how the visited tracker does the same job: a
	// middle click opens a tab without ever firing `click` and arrives as `auxclick`
	// instead, and a handler on the container element is a click handler on something
	// that is not itself interactive. The anchors are.
	$effect(() => {
		const list = resultList;
		if (!list) return;

		const onopen = (event: MouseEvent) => {
			// Something up the tree cancelled the navigation, so nothing was opened.
			if (event.defaultPrevented) return;
			// Left and middle both open the article; a right click only offers to.
			if (event.button !== 0 && event.button !== 1) return;
			const node = event.target instanceof Element ? event.target : undefined;
			// The same `data-visit` anchors the visited tracker matches, so a link that
			// is not an article does not count as one.
			if (node?.closest('a[data-visit]')) recent.record(term);
		};

		list.addEventListener('click', onopen);
		list.addEventListener('auxclick', onopen);
		return () => {
			list.removeEventListener('click', onopen);
			list.removeEventListener('auxclick', onopen);
		};
	});
</script>

<svelte:head>
	<title>blogme</title>
	<meta name="description" content="Search across thousands of independent tech blogs." />
</svelte:head>

<main class="mx-auto max-w-3xl px-6 py-16">
	<Heading tag="h1" class="mb-2">blogme</Heading>
	<P class="mb-8 text-gray-500 dark:text-gray-400">A search engine for tech blogs.</P>

	<!-- The positioning context for the suggestion list, which hangs below the box without
	taking any room in the layout. -->
	<form {onsubmit} role="search" bind:this={searchForm} class="relative">
		<Input
			type="search"
			bind:value={query}
			bind:elementRef={searchInput}
			size="md"
			placeholder="something you want to read about..."
			class="ps-10 placeholder-gray-400"
			maxlength={MAX_QUERY_LENGTH}
			aria-label="Search query"
			aria-busy={status === 'loading'}
			role="combobox"
			aria-expanded={suggestionsOpen}
			aria-controls={suggestionsOpen ? LISTBOX_ID : undefined}
			aria-activedescendant={active >= 0 ? `${LISTBOX_ID}-${active}` : undefined}
			aria-autocomplete="list"
			autocomplete="off"
			onkeydown={onSearchKeydown}
			onfocus={() => (searchFocused = true)}
			onblur={() => (searchFocused = false)}
			oninput={() => (dismissed = false)}
		>
			{#snippet left()}
				<!--
					pointer-events-auto is load-bearing: Flowbite's left slot is
					pointer-events-none so the icon never eats a click meant for the field.
					type="button" keeps it from submitting the form it sits inside.
				-->
				<button
					type="button"
					onclick={toggleRanking}
					aria-pressed={semanticRanking}
					aria-label={rankLabel}
					class="pointer-events-auto -m-1 rounded-sm p-1 transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 {semanticRanking
						? 'text-primary-600 dark:text-primary-400'
						: 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'}"
				>
					{#if status === 'loading'}
						<Spinner size="4" aria-hidden="true" />
					{:else if semanticRanking}
						<WandMagicSparklesOutline class="h-4 w-4" aria-hidden="true" />
					{:else}
						<SearchOutline class="h-4 w-4" aria-hidden="true" />
					{/if}
				</button>
				<Tooltip>{rankLabel}</Tooltip>
			{/snippet}
		</Input>

		{#if suggestionsOpen}
			<SearchSuggestions
				id={LISTBOX_ID}
				{options}
				{active}
				query={term}
				restart={interaction}
				onselect={acceptSuggestion}
				onhover={(index) => {
					// A pointer on the list is the other way of still being with it, and the
					// one that matters most: without this the list can close under a hand on
					// its way to the row it was reaching for.
					interaction++;
					selected = options[index].text;
				}}
				onexpire={() => {
					dismissed = true;
					selected = '';
				}}
			/>
		{/if}
	</form>

	{#if tooShort}
		<P size="sm" class="mt-2 text-gray-500 dark:text-gray-400">
			Type at least {MIN_QUERY_LENGTH} characters to search.
		</P>
	{/if}

	<p class="sr-only" role="status">
		{#if status === 'loading'}
			Searching
		{:else if loadingMore}
			Loading more results
		{:else if status === 'done'}
			{loaded === 0 ? emptyMessage : summary}
		{/if}
	</p>

	{#if error}
		<Alert color="red" class="mt-6">{error}</Alert>
	{/if}

	{#if searchable}
		<div class="mt-8">
			<!-- One guard for the whole result view: the summary, the filters, the rows and
			the controls each describe a set of loaded results, and none of them mean anything
			without one. Gated on what was loaded rather than on what survives the filters, so
			a filter narrow enough to match nothing still leaves the bar that undoes it. -->
			{#if loaded > 0}
				<!-- The sentence is aria-hidden because the live region above already reads it.
				The note is not: it sits outside that region so the explanation is reachable by
				keyboard and screen reader without being re-announced on every filter change,
				and it carries the whole sentence as its label rather than pointing at a
				tooltip a screen reader has no way to open. -->
				<div class="flex items-baseline">
					<P size="sm" class="text-gray-500 tabular-nums dark:text-gray-400" aria-hidden="true">
						{summary}
					</P>
					{#if partial}
						<button
							type="button"
							class="cursor-help rounded-sm px-1 text-sm leading-none text-gray-500 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:text-gray-400"
							aria-label={partialNote}
						>
							*
						</button>
						<Tooltip class="max-w-64 text-center">{partialNote}</Tooltip>
					{/if}
				</div>

				<FilterBar {results} bind:filters />

				{#if shown === 0}
					<Alert color="gray" class="mt-4">{noMatchMessage}</Alert>
				{/if}

				<div class="mt-3 space-y-4" bind:this={resultList}>
					{#each filtered as result (result.url)}
						{@const published = formatDate(result.publishedAt)}
						{@const opened = visited.has(result.url)}
						<Card class="max-w-none p-4">
							<div class="flex items-start gap-3">
								<div class="min-w-0 flex-1">
									<Heading tag="h2" class="text-lg font-semibold">
										<!-- data-preview opens the shared hover panel, and carries what the crawler
										found out about framing so the panel knows whether to try; data-visit tells the
										shared tracker that following this link counts as reading the article.

										An opened post takes the theme's blue, a step darker than the accent the
										buttons wear: at eighteen pixels of semibold that accent shouts, and this
										is a note about the post rather than the thing to look at. -->
										<a
											href={result.url}
											target="_blank"
											rel="noopener noreferrer"
											data-preview={result.framingDenied === undefined
												? ''
												: result.framingDenied
													? 'denied'
													: 'allowed'}
											data-visit
											class="line-clamp-2 rounded-sm break-words hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 {opened
												? 'text-primary-700 dark:text-primary-400'
												: 'text-gray-900 dark:text-white'}"
										>
											{result.title}
										</a>
									</Heading>
									{#if result.author || published}
										<div class="flex items-center gap-1.5 text-sm text-gray-500 dark:text-gray-400">
											{#if result.author}
												<span class="truncate">{result.author}</span>
											{/if}
											{#if result.author && published}
												<span aria-hidden="true">&middot;</span>
											{/if}
											{#if published}
												<time datetime={result.publishedAt} class="shrink-0 tabular-nums">
													{published}
												</time>
											{/if}
										</div>
									{/if}
								</div>
								<BookmarkButton {result} />
							</div>
							{#if result.summary}
								<!-- Cut to the width measured above, so the description ends on a sentence
								or says that it did not. The clamp stays as the backstop for the frame
								before the first measurement lands, and for anything the character
								estimate underrates — a description of nothing but long words. -->
								<P class="mt-2 line-clamp-3 break-words">
									{snippet(result.summary, summaryChars)}
								</P>
							{/if}
							{#if result.origin === 'sitemap' || result.topics?.length}
								<div class="mt-3 flex flex-wrap items-center gap-2">
									{#if result.origin === 'sitemap'}
										<Badge color="purple">Sitemapped</Badge>
										<Tooltip class="max-w-64 text-center">
											Found through the site's page list, not a feed, so details may be less exact.
										</Tooltip>
									{/if}
									{#each result.topics ?? [] as topic (topic)}
										<Badge class="max-w-full truncate">{topic}</Badge>
									{/each}
								</div>
							{/if}
						</Card>
					{/each}
				</div>

				<div class="mt-6 flex items-center justify-center gap-2" bind:this={controlsRow}>
					<!-- Present for as long as there are results, so the end of the list is a
					disabled button rather than a control that vanishes from under the pointer. -->
					<Button color="alternative" loading={loadingMore} disabled={!hasMore} onclick={loadMore}>
						Load more
					</Button>
					<!-- Stood down while the floating copy below has it, so the shortcut is never
					two tab stops, one of them off screen and scrolling the page when focused. -->
					{#if scrollable && !floatingTop}
						<!-- Same button, squared off around the icon: a shortcut back is a peer of
						the way forward, not a different kind of control. The icon is sized to the
						text line box beside it so both buttons come out the same height. -->
						<Button
							color="alternative"
							class="shrink-0 !px-3"
							onclick={toTop}
							aria-label="Back to top"
						>
							<ChevronDoubleUpOutline class="h-5 w-5" />
						</Button>
						<Tooltip>Back to top</Tooltip>
					{/if}
				</div>

				<!-- The same shortcut, floated once its row has scrolled away and the search box
				with it. Bottom end rather than bottom centre: beside the results, within reach of
				a thumb, clear of the toolbar at the top. Sized to a 44px touch target, which the
				inline one does not need because a pointer is already on the row it sits in. -->
				{#if floatingTop}
					<div
						class="fixed end-4 bottom-4 z-40"
						transition:fade={{ duration: prefersReducedMotion.current ? 0 : 150 }}
					>
						<Button
							color="alternative"
							class="size-11 shrink-0 !p-0 shadow-lg"
							onclick={toTop}
							aria-label="Back to top"
						>
							<ChevronDoubleUpOutline class="h-5 w-5" />
						</Button>
						<Tooltip>Back to top</Tooltip>
					</div>
				{/if}
			{:else if status === 'done'}
				<Alert color="gray" class="mt-4">
					No results found. Try a different search or
					<button
						type="button"
						class="underline hover:no-underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600"
						onclick={toggleRanking}
					>
						{semanticRanking ? 'disable' : 'enable'} semantic search
					</button>.
				</Alert>
			{/if}
		</div>
	{/if}
</main>
