<script lang="ts">
	import './layout.css';
	import { API_ORIGIN } from '$lib/api';
	import favicon from '$lib/assets/favicon.svg';
	import BookmarksPanel from '$lib/components/BookmarksPanel.svelte';
	import GithubLink from '$lib/components/GithubLink.svelte';
	import LinkPreview from '$lib/components/LinkPreview.svelte';
	import ThemeToggle from '$lib/components/ThemeToggle.svelte';
	import { trackScrollbarGutter } from '$lib/scrollbarGutter';
	import { visited } from '$lib/visited/store.svelte';

	let { children } = $props();

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

`mt-auto` against the column layout.css gives the body — on a page too short to scroll it
settles at the foot of the window, and on a long one it follows the last result. `main` is
never asked to give the room up, so nothing above it is compressed either way. -->
<footer class="mt-auto flex justify-center py-8">
	<GithubLink />
</footer>
<!-- Mounted once for the whole app; every `data-preview` link on any page shares it. -->
<LinkPreview />
