<script lang="ts">
	import { Button, Tooltip } from 'flowbite-svelte';
	import { BookmarkOutline, BookmarkSolid } from 'flowbite-svelte-icons';
	import { bookmarks } from '$lib/bookmarks/store.svelte';
	import type { SearchResult } from '$lib/api';

	let { result }: { result: SearchResult } = $props();

	const isSaved = $derived(bookmarks.has(result.url));
	const label = $derived(isSaved ? 'Remove bookmark' : 'Save bookmark');
</script>

<Button
	color="alternative"
	class="shrink-0 !p-2"
	pill
	onclick={() => bookmarks.toggle(result)}
	aria-label={label}
>
	{#if isSaved}
		<BookmarkSolid class="h-4 w-4 text-primary-600 dark:text-primary-400" />
	{:else}
		<BookmarkOutline class="h-4 w-4" />
	{/if}
</Button>
<Tooltip>{label}</Tooltip>
