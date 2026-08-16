<script lang="ts">
	import { Alert, Badge, Button, Card, Heading, P, Search } from 'flowbite-svelte';
	import BookmarkButton from '$lib/components/BookmarkButton.svelte';
	import { MAX_QUERY_LENGTH, MIN_QUERY_LENGTH, search, type SearchResult } from '$lib/api';

	const DEBOUNCE_MS = 300;

	let query = $state('');
	let results = $state<SearchResult[]>([]);
	let status = $state<'idle' | 'loading' | 'done' | 'error'>('idle');
	let error = $state('');

	let timer: ReturnType<typeof setTimeout> | undefined;
	let controller: AbortController | undefined;

	const term = $derived(query.trim().slice(0, MAX_QUERY_LENGTH));
	const searchable = $derived(term.length >= MIN_QUERY_LENGTH);
	const tooShort = $derived(term.length > 0 && !searchable);

	function cancel() {
		clearTimeout(timer);
		timer = undefined;
		controller?.abort();
		controller = undefined;
	}

	async function run(value: string) {
		cancel();
		const current = new AbortController();
		controller = current;

		status = 'loading';
		error = '';
		try {
			const response = await search(value, current.signal);
			// A newer search (or a cleared query) owns the UI now, so drop this answer.
			if (controller !== current) return;
			results = response.results;
			status = 'done';
		} catch (e) {
			if (controller !== current) return;
			results = [];
			error = e instanceof Error ? e.message : 'Something went wrong.';
			status = 'error';
		} finally {
			if (controller === current) controller = undefined;
		}
	}

	$effect(() => {
		if (!searchable) {
			cancel();
			results = [];
			error = '';
			status = 'idle';
			return;
		}

		const value = term;
		// The pending debounce is still work in progress, so the button stays busy throughout.
		status = 'loading';
		clearTimeout(timer);
		timer = setTimeout(() => run(value), DEBOUNCE_MS);
		return () => clearTimeout(timer);
	});

	// Leaving the page should not keep a request alive.
	$effect(() => () => cancel());

	// Submitting skips the pending debounce rather than queueing a second request.
	function onsubmit(event: SubmitEvent) {
		event.preventDefault();
		clearTimeout(timer);
		if (!searchable) return;
		run(term);
	}
</script>

<svelte:head><title>blogme</title></svelte:head>

<main class="mx-auto max-w-3xl px-6 py-16">
	<Heading tag="h1" class="mb-2">blogme</Heading>
	<P class="mb-8 text-gray-500 dark:text-gray-400">
		Find human-written, long-form blog posts worth reading.
	</P>

	<form {onsubmit} role="search" class="flex gap-2">
		<Search
			bind:value={query}
			size="md"
			placeholder="something you want to read about..."
			classes={{ input: 'placeholder-gray-400' }}
			maxlength={MAX_QUERY_LENGTH}
			aria-label="Search query"
		/>
		<Button type="submit" loading={status === 'loading'} disabled={!searchable} class="shrink-0">
			Search
		</Button>
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
			{results.length === 1 ? '1 result found' : `${results.length} results found`}
		{/if}
	</p>

	{#if status === 'error'}
		<Alert color="red" class="mt-6">
			<span class="font-medium">Search failed.</span>
			{error}
		</Alert>
	{:else if status === 'done' && results.length === 0}
		<Alert color="gray" class="mt-6">No results found. Try a different search.</Alert>
	{:else if results.length > 0}
		<div class="mt-8 space-y-4">
			{#each results as result (result.url)}
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
							{#if result.author}
								<P size="sm" class="truncate text-gray-500 dark:text-gray-400">{result.author}</P>
							{/if}
						</div>
						<BookmarkButton {result} />
					</div>
					{#if result.summary}
						<P class="mt-2 line-clamp-3 break-words">{result.summary}</P>
					{/if}
					{#if result.topics?.length}
						<div class="mt-3 flex flex-wrap gap-2">
							{#each result.topics as topic (topic)}
								<Badge class="max-w-full truncate">{topic}</Badge>
							{/each}
						</div>
					{/if}
				</Card>
			{/each}
		</div>
	{/if}
</main>
