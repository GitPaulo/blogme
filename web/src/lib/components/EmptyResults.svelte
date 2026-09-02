<script lang="ts">
	import { Button, Card, Tooltip } from 'flowbite-svelte';
	import { SearchOutline, WandMagicSparklesOutline } from 'flowbite-svelte-icons';

	/**
	 * What the page says when a search came back with nothing.
	 *
	 * The card a result comes in: same border, same radius, same column, same width. An
	 * empty search is still the page answering, so it answers in the shape the reader has
	 * been reading all along. Red is reserved for the error card; this state has nothing
	 * wrong to say, so its type stays grey.
	 *
	 * The one thing not carried over is the fill. Grey where a result is white, the page's
	 * own gray-900 where a result is the lifted gray-800, and no shadow at all, so the
	 * outline says a row belongs here and the flat recess inside it says none came.
	 *
	 * No role of its own: the page's live region already reads a sentence naming this same
	 * button, and a box that announced itself would say it twice.
	 */
	let { semanticRanking, ontoggle }: { semanticRanking: boolean; ontoggle: () => void } = $props();

	// What each mode does, said wherever its name appears in prose rather than only on the
	// icon that switches between them.
	const KEYWORD_NOTE = 'Every word has to appear. Best for names, libraries and exact phrases.';
	const SEMANTIC_NOTE =
		'Reads the query as a sentence, so posts that never use your exact words can still come back.';
</script>

<!--
	The mode names in the sentence below are glossary terms, not controls.

	They were buttons, so a Flowbite tooltip had something to hang off, and pressing one
	did nothing — three dead tab stops in the middle of a paragraph, each announced as a
	button. The control is the real one underneath, which switches the mode and says so.

	So they are spans now, and the explanation a pointer gets from the tooltip is read
	inline by a screen reader instead.
-->
{#snippet mode(name: string, note: string)}
	<span class="cursor-help font-medium text-primary-600 dark:text-primary-400">
		{name}
		<span class="sr-only">({note})</span>
	</span>
	<Tooltip class="max-w-64 text-center">{note}</Tooltip>
{/snippet}

<Card
	class="mt-4 max-w-none flex-col items-start gap-3 bg-gray-50 p-4 shadow-none dark:bg-gray-900"
>
	<div>
		<p class="font-medium text-gray-900 dark:text-white">No results found</p>
		<p class="mt-1 text-sm text-gray-600 dark:text-gray-400">
			{#if semanticRanking}
				Nothing came back close enough.
				{@render mode('Keyword search', KEYWORD_NOTE)}
				matches your words exactly, and pages deeper.
			{:else}
				{@render mode('Keyword search', KEYWORD_NOTE)}
				needs every word to appear.
				{@render mode('Semantic search', SEMANTIC_NOTE)}
				reads the query as a sentence instead.
			{/if}
		</p>
	</div>
	<!-- An ordinary `alternative` button at the size Load more uses, so it hovers, focuses
	and depresses like every other button here and is a 42px target under a thumb. It was a
	filled pill set into the middle of the sentence, which is the shape this page uses for
	tags: a label, not a control.

	The icon is the mode being offered, not the mode in use — the inverse of the toggle in
	the search box. Both carry a word beside the picture, so the wand is never left to mean
	two things a few centimetres apart. -->
	<Button color="alternative" class="gap-2" onclick={ontoggle}>
		{#if semanticRanking}
			<SearchOutline class="h-4 w-4" aria-hidden="true" />
			Try keyword search
		{:else}
			<WandMagicSparklesOutline class="h-4 w-4" aria-hidden="true" />
			Try semantic search
		{/if}
	</Button>
</Card>
