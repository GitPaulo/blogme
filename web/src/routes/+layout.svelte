<script lang="ts">
	import './layout.css';
	import favicon from '$lib/assets/favicon.svg';
	import BookmarksPanel from '$lib/components/BookmarksPanel.svelte';
	import GithubLink from '$lib/components/GithubLink.svelte';
	import LinkPreview from '$lib/components/LinkPreview.svelte';
	import ThemeToggle from '$lib/components/ThemeToggle.svelte';

	let { children } = $props();
</script>

<svelte:head><link rel="icon" href={favicon} /></svelte:head>
<div class="fixed end-4 top-4 z-50 flex items-center gap-2">
	<BookmarksPanel />
	<GithubLink />
	<ThemeToggle />
</div>
{@render children()}
<!-- Mounted once for the whole app; every `data-preview` link on any page shares it. -->
<LinkPreview />
