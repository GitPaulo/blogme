<script lang="ts">
	import { Badge, Card, Heading, P, Tooltip } from 'flowbite-svelte';
	import type { SearchResult } from '$lib/api';
	import BookmarkButton from '$lib/components/BookmarkButton.svelte';
	import SiteIcon from '$lib/components/SiteIcon.svelte';
	import { formatDate } from '$lib/date';
	import { hostOf } from '$lib/site';
	import { snippet } from '$lib/snippet';
	import { visited } from '$lib/visited/store.svelte';

	// One row of results. Everything it shows is either on the row the index returned or
	// derived from its url in the browser — see lib/site.ts for why the site line costs
	// the index nothing.

	let {
		result,
		/** Characters the description has room for, measured once for the whole list. */
		summaryChars
	}: { result: SearchResult; summaryChars: number } = $props();

	const published = $derived(formatDate(result.publishedAt));
	const host = $derived(hostOf(result.url));
	const opened = $derived(visited.has(result.url));
	/**
	 * What the crawler found out about framing, passed to the shared preview panel through
	 * the attribute that opts this link into it. Empty where nobody has looked, which the
	 * panel reads as unknown rather than as permission.
	 */
	const framing = $derived(
		result.framingDenied === undefined ? '' : result.framingDenied ? 'denied' : 'allowed'
	);
</script>

<!-- One step of elevation for everything in this column, empty states included:
	a list of twenty cards at Flowbite's default reads as twenty things lifted off
	the page rather than as one list on it. -->
<Card class="max-w-none p-4" shadow="sm">
	<div class="flex items-start gap-3">
		<div class="min-w-0 flex-1">
			<!-- Where the post came from leads, above the title rather than under it.
				This is a search engine for blogs, so which blog wrote a thing is half of
				what the reader is choosing between: on a page of twenty results the site
				is what tells "another framework post" apart from "another framework post,
				by the people who wrote the framework".

				The site gets this line to itself. It shared it with the author and the
				date at first, and three facts of equal weight separated by dots read as a
				list to be parsed rather than as one thing to be recognised — and the two
				that truncate were competing for the same width.

				Everything here is derived from the url and the row the index already
				returns. Nothing costs the index a byte. See lib/site.ts.

				Undefined host is a url that is not an http page, which the index should
				not hold and which is not worth a broken row if it ever does: the line is
				simply absent, and the title takes the top of the card. -->
			{#if host}
				<div class="flex items-center gap-1.5 text-sm text-gray-500 dark:text-gray-400">
					<SiteIcon {host} class="h-4 w-4" />
					<span class="truncate">{host}</span>
				</div>
			{/if}
			<!-- Twice the gap above the title that the byline gets below it. Proximity
				is what says the byline belongs to the title and the site line does not:
				both rows are the same size, weight and colour, so spacing is carrying the
				grouping on its own, and it cannot do that if it is equal on both sides. -->
			<Heading tag="h2" class="text-lg font-semibold {host ? 'mt-2' : ''}">
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
					data-preview={framing}
					data-visit
					class="line-clamp-2 rounded-sm break-words hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 {opened
						? 'text-primary-700 dark:text-primary-400'
						: 'text-gray-900 dark:text-white'}"
				>
					{result.title}
				</a>
			</Heading>
			<!-- Who wrote it and when, under the title where a byline goes.

				Set in the page's own typeface, at the size the site line above it uses
				and a shade heavier.

				The card runs on three steps — 18 for the title, 16 for the description, 14
				for everything that describes the post rather than being it — and both
				metadata rows belong to that last step. Dropping this one to 12 gave the
				card four sizes for three jobs, and put the reader's name a step below the
				host, which is not the order of interest.

				So position separates the two rows rather than any change of face, and it
				is the only thing that needs to: they are two halves of the same rank, and
				a card that dressed them differently would be claiming an order between
				them that does not exist. Everything tried in place of this was too much —
				a serif, then small caps, then a heavier weight, then a lighter one.
				Changing the letterforms reads as lifted from another document; changing
				the weight reads as emphasis this row is not asking for.

				Four pixels below the title and eight below the site line, both on the same
				grid as the rest of the card. Grey 500 rather than the 400 that would sit
				quieter: 400 on white is 3:1, which is under AA for text this size, and a
				byline is text.

				The date is set with the byline, tabular figures aside: those pick
				different numerals of the same face, so a column of dates still lines up
				down the page. -->
			{#if result.author || published}
				<div class="mt-1 flex items-baseline gap-1.5 text-sm text-gray-500 dark:text-gray-400">
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
