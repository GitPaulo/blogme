<script lang="ts">
	import { Alert, Badge, Button, Card, Heading, Input, P, Spinner, Tooltip } from 'flowbite-svelte';
	import { SearchOutline } from 'flowbite-svelte-icons';
	import BookmarkButton from '$lib/components/BookmarkButton.svelte';
	import FilterBar from '$lib/components/FilterBar.svelte';
	import {
		MAX_OFFSET,
		MAX_QUERY_LENGTH,
		MIN_QUERY_LENGTH,
		PAGE_SIZE,
		search,
		type Origin,
		type SearchResult
	} from '$lib/api';
	import { bookmarks } from '$lib/bookmarks/store.svelte';
	import { formatDate } from '$lib/date';
	import { applyFilters, emptyFilters, isFiltered } from '$lib/filters';

	const DEBOUNCE_MS = 300;

	let query = $state('');
	let results = $state<SearchResult[]>([]);
	let filters = $state(emptyFilters());
	// Unlike the other filters this one narrows the corpus, not the loaded page, so it
	// travels with the request rather than living in Filters.
	let sitemappedOnly = $state(false);
	let total = $state(0);
	let nextOffset = $state(0);
	let status = $state<'idle' | 'loading' | 'done' | 'error'>('idle');
	let loadingMore = $state(false);
	let error = $state('');

	let timer: ReturnType<typeof setTimeout> | undefined;
	let controller: AbortController | undefined;

	const term = $derived(query.trim().slice(0, MAX_QUERY_LENGTH));
	const origin = $derived<Origin | undefined>(sitemappedOnly ? 'sitemap' : undefined);
	const searchable = $derived(term.length >= MIN_QUERY_LENGTH);
	const tooShort = $derived(term.length > 0 && !searchable);
	const hasMore = $derived(status === 'done' && nextOffset < total && nextOffset <= MAX_OFFSET);
	const filtered = $derived(applyFilters(results, filters, (url) => bookmarks.has(url)));
	const summary = $derived(
		isFiltered(filters)
			? `Showing ${filtered.length} of ${results.length} loaded ${results.length === 1 ? 'result' : 'results'}`
			: `Showing ${results.length} of ${total.toLocaleString()} ${total === 1 ? 'result' : 'results'}`
	);

	// The bookmarked filter needs the saved keys, which the drawer would otherwise only
	// load on its own schedule.
	$effect(() => {
		bookmarks.load();
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

	async function run(value: string, offset: number, only?: Origin) {
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
			const response = await search(value, { offset, origin: only, signal: current.signal });
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
		run(term, nextOffset, origin);
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
		// The pending debounce is still work in progress, so the spinner stays up throughout.
		status = 'loading';
		clearTimeout(timer);
		timer = setTimeout(() => run(value, 0, only), DEBOUNCE_MS);
		return () => clearTimeout(timer);
	});

	// Leaving the page should not keep a request alive.
	$effect(() => () => cancel());

	// Submitting skips the pending debounce rather than queueing a second request.
	function onsubmit(event: SubmitEvent) {
		event.preventDefault();
		clearTimeout(timer);
		if (!searchable) return;
		run(term, 0, origin);
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
			size="md"
			placeholder="something you want to read about..."
			class="ps-10 placeholder-gray-400"
			maxlength={MAX_QUERY_LENGTH}
			aria-label="Search query"
			aria-busy={status === 'loading'}
		>
			{#snippet left()}
				{#if status === 'loading'}
					<Spinner size="4" aria-hidden="true" />
				{:else}
					<SearchOutline class="h-4 w-4" aria-hidden="true" />
				{/if}
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
			{filtered.length === 0 && !isFiltered(filters) ? 'No results found' : summary}
		{/if}
	</p>

	{#if error}
		<Alert color="red" class="mt-6">{error}</Alert>
	{/if}

	{#if status === 'done' && results.length === 0}
		<Alert color="gray" class="mt-6">No results found. Try a different search.</Alert>
	{:else if results.length > 0}
		<P size="sm" class="mt-8 text-gray-500 tabular-nums dark:text-gray-400" aria-hidden="true">
			{summary}
		</P>

		<FilterBar {results} bind:filters bind:sitemapped={sitemappedOnly} />

		{#if filtered.length === 0}
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

		{#if hasMore}
			<div class="mt-6 flex justify-center">
				<Button color="alternative" loading={loadingMore} onclick={loadMore}>Load more</Button>
			</div>
		{/if}
	{/if}
</main>
