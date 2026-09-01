<script module lang="ts">
	import { SvelteSet } from 'svelte/reactivity';
	import { faviconUrl, hueFor, monogram } from '$lib/site';

	// A site's icon, or a coloured tile with its initial where there is no icon to be had.
	// Both are the same box at the same size, so a card does not resize when one turns
	// into the other, and a list does not reflow as the icons come in.

	/**
	 * Hosts whose icon has been asked for and did not arrive, shared by every icon on the
	 * page rather than held per card.
	 *
	 * It does not save the first burst: a page of results mounts every card at once, so
	 * six posts from one blog with no `/favicon.ico` put six requests in flight before any
	 * of them has failed. What it saves is every render after that — the next page of
	 * results, a filter that rebuilds the list, the next search, and the hover preview,
	 * which asks for the icon of whichever card the pointer is resting on. Those are the
	 * repeats that add up over a session, and nothing else suppresses them: the browser's
	 * cache usually absorbs a 404, but "usually" is doing real work in that sentence, and
	 * a name that does not resolve is not cached at all.
	 *
	 * Reactive, so the other five cards swap to their tiles the moment the first request
	 * fails, rather than holding an empty box until something re-renders them.
	 *
	 * Unbounded on purpose: it holds hosts, and one session cannot meet more of those than
	 * there are blogs in the corpus.
	 */
	const failed = new SvelteSet<string>();

	/**
	 * Whether this browser has been told to spend as little as possible. An icon is
	 * decoration, so on a metered connection it is simply not fetched and every site wears
	 * its tile. Read once at module scope: it is a device setting, and re-reading it per
	 * card would touch `navigator` during prerendering, where there is no navigator at all.
	 */
	const saveData =
		typeof navigator !== 'undefined' &&
		(navigator as Navigator & { connection?: { saveData?: boolean } }).connection?.saveData ===
			true;

	// The smallest an icon can be and still be a picture rather than a smudge. Some sites
	// answer /favicon.ico with a tracking pixel or a zero-height placeholder, which loads
	// successfully and shows the reader nothing; those are treated as no icon at all.
	const MIN_PIXELS = 8;
</script>

<script lang="ts">
	let { host, class: className = '' }: { host: string; class?: string } = $props();

	const hue = $derived(hueFor(host));
	const showTile = $derived(saveData || failed.has(host));
</script>

<!-- aria-hidden throughout: the host this stands for is written out beside it in every
place this is used, so a screen reader announcing the icon would only repeat it. -->
{#if showTile}
	<span
		aria-hidden="true"
		class="tile grid shrink-0 place-items-center rounded-sm text-[0.625rem] leading-none font-semibold select-none {className}"
		style:--site-hue={hue}
	>
		{monogram(host)}
	</span>
{:else}
	<img
		src={faviconUrl(host)}
		alt=""
		aria-hidden="true"
		loading="lazy"
		decoding="async"
		fetchpriority="low"
		draggable="false"
		referrerpolicy="no-referrer"
		class="shrink-0 rounded-sm object-contain {className}"
		onerror={() => failed.add(host)}
		onload={(event) => {
			// Narrowed rather than asserted, as elsewhere: currentTarget is the img this is
			// bound to, but the DOM types do not say so.
			const img = event.currentTarget;
			if (!(img instanceof HTMLImageElement)) return;
			if (img.naturalWidth < MIN_PIXELS || img.naturalHeight < MIN_PIXELS) failed.add(host);
		}}
	/>
{/if}

<style>
	/* The hue is per site and arrives as a number, so the two colours are mixed here
	   rather than named in a class. Saturation and lightness are fixed so that every
	   tile carries the same weight on the page whatever hue it drew: colour tells two
	   blogs apart, it does not rank them. */
	.tile {
		background-color: hsl(var(--site-hue) 65% 92%);
		color: hsl(var(--site-hue) 55% 30%);
	}

	:global(.dark) .tile {
		background-color: hsl(var(--site-hue) 35% 24%);
		color: hsl(var(--site-hue) 60% 82%);
	}
</style>
