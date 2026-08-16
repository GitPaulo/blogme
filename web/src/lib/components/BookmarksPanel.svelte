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
	import type { Bookmark } from '$lib/bookmarks/db';

	const ROW_HEIGHT = 88;

	let open = $state(false);
	let items = $state<Bookmark[]>([]);
	let loading = $state(false);
	let innerHeight = $state(800);

	$effect(() => {
		bookmarks.load();
	});

	// Full records are only read when the drawer is actually opened.
	$effect(() => {
		if (!open) return;
		loading = true;
		bookmarks.list().then((records) => {
			items = records;
			loading = false;
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

<svelte:window bind:innerHeight />

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
	<Drawerhead onclick={() => (open = false)}>
		<Heading tag="h2" class="text-lg font-semibold">Bookmarks</Heading>
	</Drawerhead>

	{#if bookmarks.error}
		<Alert color="red" class="mt-2">{bookmarks.error}</Alert>
	{/if}

	{#if loading}
		<P class="mt-4 text-sm text-gray-500 dark:text-gray-400">Loading your bookmarks.</P>
	{:else if items.length === 0}
		<P class="mt-4 text-sm text-gray-500 dark:text-gray-400">
			No bookmarks yet. Save a result to find it here later.
		</P>
	{:else}
		<div class="mt-2 mb-3 flex justify-end">
			<Button color="alternative" size="xs" class="gap-2" onclick={() => download(items)}>
				<DownloadOutline class="h-4 w-4" />
				Export
			</Button>
			<Tooltip>Download your bookmarks as JSON</Tooltip>
		</div>

		<VirtualList {items} height={innerHeight - 170} minItemHeight={ROW_HEIGHT} contained>
			{#snippet children(item: Bookmark)}
				<div class="flex h-22 items-center gap-2 border-b border-gray-200 dark:border-gray-700">
					<div class="min-w-0 flex-1">
						<a
							href={item.url}
							target="_blank"
							rel="noopener noreferrer"
							class="line-clamp-2 rounded-sm text-sm font-medium text-gray-900 hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:text-white"
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
	{/if}
</Drawer>
