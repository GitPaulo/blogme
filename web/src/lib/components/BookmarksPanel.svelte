<script lang="ts">
	import {
		Alert,
		Button,
		Drawer,
		Drawerhead,
		Heading,
		P,
		Tooltip,
		VirtualList
	} from 'flowbite-svelte';
	import { BookmarkSolid, DownloadOutline, TrashBinOutline } from 'flowbite-svelte-icons';
	import { bookmarks } from '$lib/bookmarks/store.svelte';
	import { download } from '$lib/bookmarks/export';
	import { safeHttpUrl } from '$lib/api';
	import type { Bookmark } from '$lib/bookmarks/db';

	const ROW_HEIGHT = 88;

	let open = $state(false);
	let items = $state<Bookmark[]>([]);
	let loading = $state(false);
	let listHeight = $state(0);
	let reads = 0;

	$effect(() => {
		bookmarks.load();
	});

	// Full records are only read when the drawer is actually opened.
	$effect(() => {
		if (!open) return;
		const read = ++reads; // A reopen while a read is in flight must win.
		loading = true;
		bookmarks
			.list()
			.then((records) => {
				if (read === reads) items = records;
			})
			.catch(() => {
				if (read === reads) items = [];
			})
			.finally(() => {
				if (read === reads) loading = false;
			});
	});

	async function removeOne(url: string) {
		await bookmarks.remove(url);
		items = items.filter((item) => item.url !== url);
	}

	function host(url: string) {
		try {
			return new URL(url).hostname.replace(/^www\./, '');
		} catch {
			return url;
		}
	}

	const label = $derived(
		bookmarks.count === 1 ? 'Bookmarks, 1 saved' : `Bookmarks, ${bookmarks.count} saved`
	);
</script>

<Button
	color="alternative"
	class="gap-2 !px-3 !py-2.5"
	pill
	onclick={() => (open = true)}
	aria-label={label}
>
	<BookmarkSolid class="h-4 w-4" />
	<span class="text-xs font-medium tabular-nums">{bookmarks.count}</span>
</Button>
<Tooltip>Bookmarks</Tooltip>

<Drawer bind:open placement="right" dismissable={false} class="w-full sm:w-96">
	<Drawerhead onclick={() => (open = false)} class="shrink-0">
		<Heading tag="h2" class="text-lg font-semibold">Bookmarks</Heading>
	</Drawerhead>

	{#if bookmarks.error}
		<Alert color="red" class="mt-2 shrink-0">{bookmarks.error}</Alert>
	{/if}

	{#if loading}
		<P class="mt-4 text-sm text-gray-500 dark:text-gray-400">Loading your bookmarks.</P>
	{:else if items.length === 0}
		<P class="mt-4 text-sm text-gray-500 dark:text-gray-400">
			No bookmarks yet. Save a result to find it here later.
		</P>
	{:else}
		<!-- The list takes the leftover space so the action bar can sit on the bottom edge. -->
		<div class="mt-2 min-h-0 flex-1" bind:clientHeight={listHeight}>
			<VirtualList {items} height={listHeight} minItemHeight={ROW_HEIGHT} contained>
				{#snippet children(item: Bookmark)}
					<div class="flex h-22 items-center gap-2 border-b border-gray-200 dark:border-gray-700">
						<div class="min-w-0 flex-1">
							<a
								href={safeHttpUrl(item.url) ?? '#'}
								target="_blank"
								rel="noopener noreferrer"
								class="line-clamp-2 rounded-sm text-sm font-medium break-words text-gray-900 hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:text-white"
							>
								{item.title}
							</a>
							<span class="mt-1 block truncate text-xs text-gray-500 dark:text-gray-400">
								{host(item.url)}
							</span>
						</div>
						<Button
							color="alternative"
							class="shrink-0 !p-2"
							pill
							onclick={() => removeOne(item.url)}
							aria-label="Remove {item.title} from bookmarks"
						>
							<TrashBinOutline class="h-4 w-4" />
						</Button>
					</div>
				{/snippet}
			</VirtualList>
		</div>

		<div
			class="mt-3 flex shrink-0 items-center justify-between gap-2 border-t border-gray-200 pt-3 dark:border-gray-700"
		>
			<span class="text-xs text-gray-500 tabular-nums dark:text-gray-400">
				{items.length === 1 ? '1 saved' : `${items.length} saved`}
			</span>
			<Button color="alternative" size="xs" class="gap-2" onclick={() => download(items)}>
				<DownloadOutline class="h-4 w-4" />
				Export
			</Button>
			<Tooltip>Download your bookmarks as JSON</Tooltip>
		</div>
	{/if}
</Drawer>
