<script lang="ts">
	import Button from 'flowbite-svelte/Button.svelte';
	import Tooltip from 'flowbite-svelte/Tooltip.svelte';
	import BookmarkSolid from 'flowbite-svelte-icons/BookmarkSolid.svelte';
	import { tick } from 'svelte';
	import { bookmarks } from '$lib/bookmarks/store.svelte';
	import { lazy } from '$lib/lazy.svelte';

	/**
	 * The button in the corner, and nothing else.
	 *
	 * What it opens — the drawer, its two dialogs, the virtual list, the file reader —
	 * lives in BookmarksDrawer.svelte and is fetched the first time anyone reaches for it.
	 * None of it can be seen before that, and all of it was in the chunk every reader
	 * downloaded to look at the landing page.
	 *
	 * The count stays here, because the button wears it whether or not the panel has ever
	 * been opened, and it comes from the store rather than from the panel's list.
	 */

	let open = $state(false);

	const drawer = lazy(() => import('$lib/components/BookmarksDrawer.svelte'));

	$effect(() => {
		bookmarks.load();
	});

	/**
	 * The drawer slides in, and a Svelte transition does not play on the frame its
	 * component mounts — so a panel handed `open` at the moment it appears would arrive
	 * fully drawn rather than arriving at all. The module is fetched, the panel is given a
	 * frame to render itself closed, and only then is it opened.
	 *
	 * The `tick` is not redundant with the preloading below. A pointer warms this on its
	 * way to the button, but a touch has no way to say it is coming, and a phone is where
	 * the drawer is the full width of the screen and the slide is doing the most work.
	 */
	async function openBookmarks() {
		await drawer.load();
		await tick();
		open = true;
	}

	const label = $derived(
		bookmarks.count === 1 ? 'Bookmarks, 1 saved' : `Bookmarks, ${bookmarks.count} saved`
	);
</script>

<!-- Fetched on the way to the click rather than on the click: a pointer resting on this
button, or a keyboard arriving at it, is as clear a statement of intent as pressing it,
and it buys the whole trip. Both handlers cost one request between them however many
times they fire. -->
<Button
	color="alternative"
	class="gap-2 !px-3 !py-2.5"
	pill
	onpointerenter={() => drawer.load()}
	onfocus={() => drawer.load()}
	onclick={openBookmarks}
	aria-label={label}
>
	<BookmarkSolid class="h-4 w-4" />
	<span class="text-xs font-medium tabular-nums">{bookmarks.count}</span>
</Button>
<Tooltip>Bookmarks</Tooltip>

{#if drawer.current}
	{@const BookmarksDrawer = drawer.current}
	<BookmarksDrawer bind:open />
{/if}
