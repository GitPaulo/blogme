<script lang="ts">
	import { Alert, Badge, Button, Card, Heading, P, Search, Spinner } from 'flowbite-svelte';
	import { search, type SearchResult } from '$lib/api';

	let query = $state('');
	let results = $state<SearchResult[]>([]);
	let status = $state<'idle' | 'loading' | 'done' | 'error'>('idle');
	let error = $state('');

	async function onsubmit(event: SubmitEvent) {
		event.preventDefault();
		if (!query.trim()) return;

		status = 'loading';
		error = '';
		try {
			const response = await search(query);
			results = response.results;
			status = 'done';
		} catch (e) {
			error = e instanceof Error ? e.message : 'Something went wrong';
			status = 'error';
		}
	}
</script>

<svelte:head><title>blogme</title></svelte:head>

<main class="mx-auto max-w-3xl px-6 py-16">
	<Heading tag="h1" class="mb-2">blogme</Heading>
	<P class="mb-8 text-gray-500 dark:text-gray-400">
		Find human-written, long-form blog posts worth reading.
	</P>

	<form {onsubmit} class="flex gap-2">
		<Search
			bind:value={query}
			size="md"
			placeholder="problems scaling single-threaded servers"
			aria-label="Search query"
		/>
		<Button type="submit" disabled={status === 'loading'} class="shrink-0">
			{#if status === 'loading'}
				<Spinner class="me-2" size="4" color="gray" />
			{/if}
			Search
		</Button>
	</form>

	{#if status === 'error'}
		<Alert color="red" class="mt-6">
			<span class="font-medium">Search failed.</span>
			{error}
		</Alert>
	{:else if status === 'done' && results.length === 0}
		<Alert color="gray" class="mt-6">No results yet — the corpus is still empty.</Alert>
	{:else if results.length > 0}
		<div class="mt-8 space-y-4">
			{#each results as result (result.url)}
				<Card class="max-w-none p-4">
					<a
						href={result.url}
						rel="noopener noreferrer"
						class="text-lg font-semibold text-gray-900 hover:underline dark:text-white"
					>
						{result.title}
					</a>
					{#if result.author}
						<P size="sm" class="text-gray-500 dark:text-gray-400">{result.author}</P>
					{/if}
					{#if result.summary}
						<P class="mt-2">{result.summary}</P>
					{/if}
					{#if result.topics?.length}
						<div class="mt-3 flex flex-wrap gap-2">
							{#each result.topics as topic (topic)}
								<Badge>{topic}</Badge>
							{/each}
						</div>
					{/if}
				</Card>
			{/each}
		</div>
	{/if}
</main>
