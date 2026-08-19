<script lang="ts">
	import { Alert, Badge, Button, Card, Heading, Input, P, Spinner, Tooltip } from 'flowbite-svelte';
	import {
		ChevronDoubleUpOutline,
		SearchOutline,
		WandMagicSparklesOutline
	} from 'flowbite-svelte-icons';
	import { prefersReducedMotion } from 'svelte/motion';
	import BookmarkButton from '$lib/components/BookmarkButton.svelte';
	import FilterBar from '$lib/components/FilterBar.svelte';
	import {
		clampQuery,
		MAX_QUERY_LENGTH,
		maxOffsetFor,
		MIN_QUERY_LENGTH,
		PAGE_SIZE,
		search,
		type Origin,
		type Rank,
		type SearchResult
	} from '$lib/api';
	import { bookmarks } from '$lib/bookmarks/store.svelte';
	import { formatDate } from '$lib/date';
	import { applyFilters, emptyFilters, isFiltered } from '$lib/filters';

	const DEBOUNCE_MS = 300;
	// Hoisted: building a formatter is the expensive half of rendering the total.
	const decimal = new Intl.NumberFormat();

	let query = $state('');
	// Raw, because a page is only ever replaced or appended as a whole and never
	// edited in place. Deep state would instead proxy every row and charge a
	// subscription for each field the filter pass touches.
	let results = $state.raw<SearchResult[]>([]);
	let filters = $state(emptyFilters());
	// Unlike the other filters this one narrows the corpus, not the loaded page, so it
	// travels with the request rather than living in Filters.
	let sitemappedOnly = $state(false);
	// Which ranking the search box asks for. Semantic understands a query phrased as a
	// sentence; keyword is literal, and is the one that can page deep. Keyword is the
	// default: it needs no reranker call and has no page-depth limit.
	let semanticRanking = $state(false);
	let total = $state(0);
	let nextOffset = $state(0);
	let status = $state<'idle' | 'loading' | 'done' | 'error'>('idle');
	let loadingMore = $state(false);
	let error = $state('');
	// Whether the document is taller than the window, which is the only thing that makes
	// a shortcut back to the top worth offering.
	let scrollable = $state(false);

	let searchInput = $state<HTMLInputElement>();

	let timer: ReturnType<typeof setTimeout> | undefined;
	let controller: AbortController | undefined;

	const term = $derived(clampQuery(query));
	const origin = $derived<Origin | undefined>(sitemappedOnly ? 'sitemap' : undefined);
	const searchable = $derived(term.length >= MIN_QUERY_LENGTH);
	const tooShort = $derived(term.length > 0 && !searchable);
	const rank = $derived<Rank>(semanticRanking ? 'semantic' : 'keyword');
	// How deep "load more" may go depends on the mode, because only semantic ranking
	// has a reranked window to run out of.
	const hasMore = $derived(
		status === 'done' && nextOffset < total && nextOffset <= maxOffsetFor(rank)
	);
	const filtered = $derived(applyFilters(results, filters, (url) => bookmarks.has(url)));
	// The counts and the formatted total are their own deriveds so the summary string
	// is rebuilt only when a number it actually shows changes. Re-running the filter
	// pass — which a bookmark toggle or a half-typed date bound does — usually leaves
	// both counts where they were, and the total moves only on a new page.
	const loaded = $derived(results.length);
	const shown = $derived(filtered.length);
	const totalLabel = $derived(decimal.format(total));
	const partial = $derived(isFiltered(filters));
	const summary = $derived(
		partial
			? `Showing ${shown} of ${loaded} loaded ${loaded === 1 ? 'result' : 'results'}`
			: `Showing ${loaded} of ${totalLabel} ${total === 1 ? 'result' : 'results'}`
	);
	// These filters narrow the rows already fetched rather than the query behind them,
	// so the figure they are counted against climbs every time another page arrives.
	// Worth saying out loud: a total that grows while you page through it reads as a
	// bug, and the honest explanation is shorter than the guess.
	const partialNote =
		'Filters apply to the results loaded so far, not the whole index, so both numbers grow each time you load more.';
	const rankLabel = $derived(
		semanticRanking
			? 'Semantic ranking: finds posts about the idea. Switch to keyword ranking.'
			: 'Keyword ranking: matches the words you typed. Switch to semantic ranking.'
	);
	// An empty corpus-narrowing filter reads as an empty index unless we say otherwise.
	const emptyMessage = $derived(
		sitemappedOnly
			? 'No sitemapped results found. Turn off Sitemapped, or try a different search.'
			: 'No results found. Try a different search.'
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

	function cancel() {
		clearTimeout(timer);
		timer = undefined;
		controller?.abort();
		controller = undefined;
	}

	// Offset paging can repeat a row if the index changes between pages, and the keyed
	// each block would throw on the duplicate.
	function merge(existing: SearchResult[], incoming: SearchResult[]) {
		const seen = new Set(existing.map((result) => result.url));
		return [...existing, ...incoming.filter((result) => !seen.has(result.url))];
	}

	async function run(value: string, offset: number, only: Origin | undefined, ranking: Rank) {
		cancel();
		const current = new AbortController();
		controller = current;

		if (offset === 0) {
			status = 'loading';
			// Filters describe the result set on screen, so a fresh search starts clean.
			filters = emptyFilters();
		} else loadingMore = true;
		error = '';
		try {
			const response = await search(value, {
				offset,
				origin: only,
				rank: ranking,
				signal: current.signal
			});
			// A newer search (or a cleared query) owns the UI now, so drop this answer.
			if (controller !== current) return;
			results = offset === 0 ? response.results : merge(results, response.results);
			total = response.total;
			nextOffset = offset + PAGE_SIZE;
			status = 'done';
		} catch (e) {
			if (controller !== current) return;
			error = e instanceof Error ? e.message : 'Something went wrong.';
			// A failed page keeps the pages already on screen; only a failed first page
			// has nothing left to show.
			if (offset === 0) {
				results = [];
				total = 0;
				nextOffset = 0;
				status = 'error';
			}
		} finally {
			if (controller === current) {
				controller = undefined;
				loadingMore = false;
			}
		}
	}

	function loadMore() {
		if (!hasMore || loadingMore) return;
		run(term, nextOffset, origin, rank);
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
			error = '';
			status = 'idle';
			return;
		}

		const value = term;
		const only = origin;
		// Read inside the effect so flipping the ranking mode re-runs the current search
		// rather than only affecting the next one the user types.
		const ranking = rank;
		// The pending debounce is still work in progress, so the spinner stays up throughout.
		status = 'loading';
		clearTimeout(timer);
		timer = setTimeout(() => run(value, 0, only, ranking), DEBOUNCE_MS);
		return () => clearTimeout(timer);
	});

	// Leaving the page should not keep a request alive.
	$effect(() => () => cancel());

	// Submitting skips the pending debounce rather than queueing a second request.
	function onsubmit(event: SubmitEvent) {
		event.preventDefault();
		clearTimeout(timer);
		if (!searchable) return;
		run(term, 0, origin, rank);
	}
</script>

<svelte:head><title>blogme</title></svelte:head>

<main class="mx-auto max-w-3xl px-6 py-16">
	<Heading tag="h1" class="mb-2">blogme</Heading>
	<P class="mb-8 text-gray-500 dark:text-gray-400">
		Find human-written, long-form blog posts worth reading.
	</P>

	<form {onsubmit} role="search">
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
		{:else if status === 'done'}
			{loaded === 0 ? emptyMessage : summary}
		{/if}
	</p>

	{#if error}
		<Alert color="red" class="mt-6">{error}</Alert>
	{/if}

	<!-- Tied to there being a search rather than to there being results: a filter
	narrow enough to return nothing would otherwise unmount the bar that holds the
	control for undoing it. -->
	{#if searchable}
		<div class="mt-8">
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
			{/if}

			<FilterBar {results} bind:filters bind:sitemapped={sitemappedOnly} />

			{#if status === 'done' && loaded === 0}
				<Alert color="gray" class="mt-4">{emptyMessage}</Alert>
			{:else if loaded > 0 && shown === 0}
				<Alert color="gray" class="mt-4">No loaded results match these filters.</Alert>
			{/if}

			<div class="mt-3 space-y-4">
				{#each filtered as result (result.url)}
					{@const published = formatDate(result.publishedAt)}
					<Card class="max-w-none p-4">
						<div class="flex items-start gap-3">
							<div class="min-w-0 flex-1">
								<Heading tag="h2" class="text-lg font-semibold">
									<a
										href={result.url}
										target="_blank"
										rel="noopener noreferrer"
										data-preview
										class="line-clamp-2 rounded-sm break-words text-gray-900 hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:text-white"
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

			{#if loaded > 0}
				<div class="mt-6 flex items-center justify-center gap-2">
					<!-- Present for as long as there are results, so the end of the list is a
					disabled button rather than a control that vanishes from under the pointer. -->
					<Button color="alternative" loading={loadingMore} disabled={!hasMore} onclick={loadMore}>
						Load more
					</Button>
					{#if scrollable}
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
			{/if}
		</div>
	{/if}
</main>
