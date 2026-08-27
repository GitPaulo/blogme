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
	import {
		clampQuery,
		MAX_QUERY_LENGTH,
		maxOffsetFor,
		MIN_QUERY_LENGTH,
		search,
		SearchError,
		type Rank,
		type SearchResult
	} from '$lib/api';
	import { bookmarks } from '$lib/bookmarks/store.svelte';
	import { formatDate } from '$lib/date';
	import { applyFilters, emptyFilters, isFiltered } from '$lib/filters';
	import { onScreen } from '$lib/onScreen.svelte';
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

	let searchInput = $state<HTMLInputElement>();
	// One element per visible result, so children[n] is result n.
	let resultList = $state<HTMLElement>();
	// The row holding "load more" and the shortcut back to the top, watched so the shortcut
	// can return as a floating one once the row itself has scrolled out of reach.
	let controlsRow = $state<HTMLElement>();
	let searchForm = $state<HTMLElement>();

	let timer: ReturnType<typeof setTimeout> | undefined;
	let controller: AbortController | undefined;

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
	const emptyMessage = 'No results found. Try a different search.';
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

	// The page is a search box with a page around it, so the caret starts in it.
	$effect(() => {
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

	const controlsOnScreen = onScreen(() => controlsRow);
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
			syncUrl(''); // No search to describe, so the address goes back to bare.
			return;
		}

		const value = term;
		// Read inside the effect so flipping the ranking mode re-runs the current search
		// rather than only affecting the next one the user types.
		const ranking = rank;
		// The pending debounce is still work in progress, so the spinner stays up throughout.
		status = 'loading';
		clearTimeout(timer);
		timer = setTimeout(() => run(value, 0, ranking), DEBOUNCE_MS);
		return () => clearTimeout(timer);
	});

	// Leaving the page should not keep a request alive.
	$effect(() => () => cancel());

	// Submitting skips the pending debounce rather than queueing a second request.
	function onsubmit(event: SubmitEvent) {
		event.preventDefault();
		clearTimeout(timer);
		if (!searchable) return;
		run(term, 0, rank);
	}
</script>

<svelte:head>
	<title>blogme</title>
	<meta name="description" content="Search across thousands of independent tech blogs." />
</svelte:head>

<main class="mx-auto max-w-3xl px-6 py-16">
	<Heading tag="h1" class="mb-2">blogme</Heading>
	<P class="mb-8 text-gray-500 dark:text-gray-400">A search engine for tech blogs.</P>

	<form {onsubmit} role="search" bind:this={searchForm}>
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
		>
			{#snippet left()}
				<!--
					pointer-events-auto is load-bearing: Flowbite's left slot is
					pointer-events-none so the icon never eats a click meant for the field.
					type="button" keeps it from submitting the form it sits inside.
				-->
				<button
					type="button"
					onclick={() => (semanticRanking = !semanticRanking)}
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
								<P class="mt-2 line-clamp-3 break-words">{result.summary}</P>
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
				<Alert color="gray" class="mt-4">{emptyMessage}</Alert>
			{/if}
		</div>
	{/if}
</main>
