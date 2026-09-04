<script lang="ts">
	import './layout.css';
	import { API_ORIGIN } from '$lib/api';
	import favicon from '$lib/assets/favicon.svg';
	import BookmarksPanel from '$lib/components/BookmarksPanel.svelte';
	import GithubLink from '$lib/components/GithubLink.svelte';
	import LinkPreview from '$lib/components/LinkPreview.svelte';
	import ThemeToggle from '$lib/components/ThemeToggle.svelte';
	import { overlapsContent } from '$lib/overlapsContent.svelte';
	import { trackScrollbarGutter } from '$lib/scrollbarGutter';
	import { visited } from '$lib/visited/store.svelte';

	let { children } = $props();

	let mark: HTMLElement | undefined = $state();
	const markCovered = overlapsContent(() => mark);

	// Mounted here rather than beside the preview panel, which only installs itself on
	// devices that can hover: an article opened by tap counts the same as one opened by
	// click, whether or not this device will ever draw the mark.
	$effect(() => visited.track());

	// Measured for the whole app rather than by the drawer, because every modal dialog on
	// the site is laid out against the gutter this reports. See lib/scrollbarGutter.ts.
	$effect(() => trackScrollbarGutter());
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<!--
		Opened while the page is still loading, so the first search does not begin with a
		handshake. crossorigin because the searches that will use it are cross-origin
		fetches: a connection opened without it is opened in a different mode and the
		fetch makes its own anyway, which is the whole cost this is here to avoid.
	-->
	{#if API_ORIGIN}
		<link rel="preconnect" href={API_ORIGIN} crossorigin="anonymous" />
	{/if}
</svelte:head>
<!-- A landmark rather than a bare div. These controls sit outside `main`, so without a
region of their own they are the one thing on the site a reader moving by landmark walks
straight past. A `header` that is not inside a sectioning element is the page's banner,
which is what this is. -->
<header class="fixed end-4 top-4 z-50 flex items-center gap-2">
	<BookmarksPanel />
	<ThemeToggle />
</header>
{@render children()}
<!-- The repository link keeps company with nothing, so it is given the foot of the page
rather than a third seat in the corner: it is the one thing up there that was not about
the search in front of you.

It is fixed to the window, not to the end of the document, because it is a fact about the
site rather than the last item of the page: three results and three hundred should not
put it in two different places. In flow it rode up the screen as a search narrowed, which
is movement the reader has to account for and learn to ignore.

Centred on the window rather than `inset-x-0`, so there is no full-width strip lying
across the bottom of the page catching clicks meant for what is under it.

`flex` so the link is a flex item and not an inline box: inline, its `p-2` shapes nothing
and the line's leading sits under the glyph, which lifted the mark off the bottom of the
window by half again the inset asked for.

The four is the header's four at the other end of the page, and the same four the
back-to-top button keeps, so the two things fixed to the bottom of the window rest on one
line.

The lowest layer anything on this site sits on: under the preview panel's z-60, the
header's z-50 and the floating back-to-top's z-40, and it yields to all three rather than
being arranged against any of them. It cannot go lower than nought and stay a link —
below the page it would be behind `main`'s box, which spans the window, and every click
meant for it would land there instead.

So it gets out of the way by leaving rather than by stacking: it fades out for as long as
anything is painted under it, which is what a mark with no row of its own can do instead
of holding a row open. See lib/overlapsContent.svelte.ts for what counts as under it.

`main`'s bottom padding still leaves it a clear strip at the end of a scrolled page, so
what it fades for is the content on the way past, not the foot of the page.

Focus overrides all of it. A keyboard reaching a link it cannot see is worse than a mark
crossing a line of text, and the fade is decoration either way — `pointer-events` follow
the opacity so an invisible mark never takes a click meant for what is behind it. -->
<footer
	bind:this={mark}
	class="fixed bottom-4 left-1/2 z-0 flex -translate-x-1/2 transition-opacity duration-200 focus-within:pointer-events-auto focus-within:opacity-100 motion-reduce:transition-none"
	class:pointer-events-none={markCovered.current}
	class:opacity-0={markCovered.current}
>
	<GithubLink />
</footer>
<!-- Mounted once for the whole app; every `data-preview` link on any page shares it. -->
<LinkPreview />
