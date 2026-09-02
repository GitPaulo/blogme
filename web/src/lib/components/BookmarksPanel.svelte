<script lang="ts">
	import {
		Alert,
		Button,
		Drawer,
		Drawerhead,
		Heading,
		Input,
		Modal,
		P,
		Tooltip,
		VirtualList
	} from 'flowbite-svelte';
	import {
		BookmarkSolid,
		DownloadOutline,
		SearchOutline,
		TrashBinOutline,
		UploadOutline
	} from 'flowbite-svelte-icons';
	import { bookmarks, type ImportMode } from '$lib/bookmarks/store.svelte';
	import { download } from '$lib/bookmarks/export';
	import { readFile } from '$lib/bookmarks/import';
	import { safeHttpUrl } from '$lib/api';
	import { hostOf } from '$lib/site';
	import SiteIcon from '$lib/components/SiteIcon.svelte';
	import { formatDate } from '$lib/date';
	import type { Bookmark } from '$lib/bookmarks/db';

	const ROW_HEIGHT = 88;

	let open = $state(false);
	let items = $state<Bookmark[]>([]);
	let filter = $state('');
	let confirming = $state(false);
	let loading = $state(false);
	let listHeight = $state(0);
	let reads = 0;

	/** A file that has been read and understood, waiting on how to apply it. */
	let pending = $state.raw<{ name: string; records: Bookmark[] } | undefined>();
	/** Only ever a failure: everything that works shows in the list behind it. */
	let notice = $state('');
	let picker = $state<HTMLInputElement>();

	$effect(() => {
		bookmarks.load();
	});

	// Full records are only read when the drawer is actually opened.
	$effect(() => {
		if (!open) return;
		filter = ''; // A filter left over from last time would hide the list on arrival.
		notice = ''; // As would a complaint about a file picked two visits ago.
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

	// The store rolls a failed delete back and reports it through bookmarks.error, so
	// the row only leaves the list once the store agrees it is gone. Dropping it either
	// way would leave the panel showing a collection the store does not have.
	async function removeOne(url: string) {
		await bookmarks.remove(url);
		if (!bookmarks.has(url)) items = items.filter((item) => item.url !== url);
	}

	async function removeAll() {
		confirming = false;
		await bookmarks.clear();
		if (bookmarks.count === 0) items = [];
	}

	async function exportAll() {
		try {
			// Read back rather than handed the loaded list: another tab may have saved
			// something since this drawer opened, and an export that quietly leaves it out
			// is worse than one that takes a moment.
			download(await bookmarks.list());
		} catch {
			notice = 'Could not export your bookmarks.';
		}
	}

	async function pickFile(event: Event) {
		// Narrowed rather than asserted, as elsewhere: currentTarget is the picker this is
		// bound to, but the DOM types do not say so.
		const input = event.currentTarget;
		if (!(input instanceof HTMLInputElement)) return;

		const file = input.files?.[0];
		// Cleared straight away, so picking the same file twice still counts as a change.
		input.value = '';
		if (!file) return;

		notice = '';
		try {
			// Capped because the name is the reader's, and a long one would push the dialog wide.
			pending = { name: file.name.slice(0, 80), records: await readFile(file) };
		} catch (e) {
			notice = e instanceof Error ? e.message : 'Could not read that file.';
		}
	}

	async function runImport(mode: ImportMode) {
		const file = pending;
		pending = undefined;
		if (!file) return;

		// A refusal is reported through bookmarks.error, which the drawer already shows.
		// Anything else shows in the list below, which is the only report worth making.
		if (!(await bookmarks.importAll(file.records, mode))) return;

		items = await bookmarks.list().catch(() => items);
		filter = '';
	}

	const plural = (n: number) => `${n} bookmark${n === 1 ? '' : 's'}`;

	// The shared derivation, with the raw url kept as the label for anything it refuses to
	// name. A saved row is the reader's own and predates whatever the index holds today, so
	// a drawer that showed nothing for one would be hiding a bookmark rather than tidying.
	const host = (url: string) => hostOf(url) ?? url;

	const label = $derived(
		bookmarks.count === 1 ? 'Bookmarks, 1 saved' : `Bookmarks, ${bookmarks.count} saved`
	);
	// Split out so an untouched filter keeps handing the list the same array, and the
	// virtual list has nothing to recompute.
	const needle = $derived(filter.trim().toLowerCase());
	// What each row is matched against, built once per list rather than once per
	// keystroke. host() parses a url, and doing that for every saved post on every
	// character typed is most of the cost of filtering a full collection.
	const haystacks = $derived(items.map((item) => `${item.title} ${host(item.url)}`.toLowerCase()));
	const visible = $derived(needle ? items.filter((_, i) => haystacks[i].includes(needle)) : items);
	const savedLabel = $derived(
		needle
			? `${visible.length} of ${items.length} shown`
			: items.length === 1
				? '1 saved'
				: `${items.length} saved`
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

<!-- Flush to the inline end of the viewport: the auto margin on the other side is what
puts it there, and the edges it shares with the screen carry neither border nor corner,
so the panel reads as the side of the window rather than as a card floating near it.
Full-width below `sm`, where a 24rem panel would leave a strip of page too narrow to use
but wide enough to invite a tap that only closes it. -->
<Drawer
	bind:open
	placement="right"
	dismissable={false}
	aria-labelledby="bookmarks-drawer-title"
	class="ms-auto me-0 flex h-full w-full max-w-full flex-col gap-3 rounded-none border-y-0 border-e-0 sm:w-96"
>
	<!-- The close button is pulled out by its own optical inset, so the glyph inside it
	lines up with the drawer's gutter rather than the button's edge doing so — an icon
	button flush to a padded container always reads as further in than the text above it.
	40px square, because it is the one control here a thumb has to find.

	The focus ring is spelled out because Flowbite's drawer head styles none, which left the
	button wearing the browser's own gold ring instead of the outline every other focusable
	thing on the page uses. -->
	<Drawerhead
		onclick={() => (open = false)}
		class="shrink-0"
		classes={{
			button:
				'-me-3 h-10 w-10 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600'
		}}
	>
		<Heading id="bookmarks-drawer-title" tag="h2" class="text-lg font-semibold">Bookmarks</Heading>
	</Drawerhead>

	{#if bookmarks.error}
		<Alert color="red" class="shrink-0">{bookmarks.error}</Alert>
	{/if}

	{#if notice}
		<Alert color="red" class="shrink-0">{notice}</Alert>
	{/if}

	<!-- flex-1 on the two states that have no list to grow: it is what holds the action
	bar below on the bottom edge of the drawer rather than up under the message. -->
	{#if loading}
		<P role="status" class="flex-1 text-sm text-gray-500 dark:text-gray-400">
			Loading your bookmarks.
		</P>
	{:else if items.length === 0}
		<P class="flex-1 text-sm text-gray-500 dark:text-gray-400">
			No bookmarks yet. Save a result to find it here later, or import an export.
		</P>
	{:else}
		<!-- Nothing to narrow until something is saved, so the field only exists alongside a list. -->
		<div class="shrink-0">
			<Input
				type="search"
				bind:value={filter}
				size="md"
				placeholder="Filter saved posts..."
				class="ps-10 placeholder-gray-400"
				aria-label="Filter bookmarks"
			>
				{#snippet left()}
					<SearchOutline class="h-4 w-4" aria-hidden="true" />
				{/snippet}
			</Input>
		</div>

		<!-- Not announced here: the counter in the bar below is already live, and reports
		"0 of N shown" for the same keystroke. -->
		{#if visible.length === 0}
			<P class="flex-1 text-sm text-gray-500 dark:text-gray-400">
				No saved posts match that filter.
			</P>
		{:else}
			<!-- The list takes the leftover space so the action bar can sit on the bottom edge. -->
			<div class="min-h-0 flex-1" bind:clientHeight={listHeight}>
				<VirtualList items={visible} height={listHeight} minItemHeight={ROW_HEIGHT} contained>
					{#snippet children(item: Bookmark)}
						{@const published = formatDate(item.publishedAt)}
						{@const site = hostOf(item.url)}
						<div class="flex h-22 items-center gap-2 border-b border-gray-200 dark:border-gray-700">
							<div class="min-w-0 flex-1">
								<!-- Opened from here or from a result, it is the same article and the same
								visit; the drawer just has nowhere to show the mark. -->
								<a
									href={safeHttpUrl(item.url) ?? '#'}
									target="_blank"
									rel="noopener noreferrer"
									data-visit
									class="line-clamp-2 rounded-sm text-sm font-medium break-words text-gray-900 hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:text-white"
								>
									{item.title}
								</a>
								<span
									class="mt-1 flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400"
								>
									<!-- Same icon and the same failure registry as a result card, so a blog
									whose favicon is missing is asked for it once per session rather than
									once per surface. -->
									{#if site}
										<SiteIcon host={site} class="h-3.5 w-3.5" />
									{/if}
									<span class="truncate">{site ?? item.url}</span>
									{#if published}
										<span aria-hidden="true">&middot;</span>
										<time datetime={item.publishedAt} class="shrink-0">{published}</time>
									{/if}
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
		{/if}
	{/if}

	<!-- Outside the branch above, because importing is how a browser with none of its own
	gets any: the bar is the one part of the drawer that has to be there when the list is
	not. What it acts on is disabled instead.

	Wraps, because the drawer is as narrow as the screen below `sm` and three controls plus
	a count do not always fit one line there. -->
	<div
		class="flex shrink-0 flex-wrap items-center gap-2 border-t border-gray-200 pt-3 dark:border-gray-700"
	>
		<span class="text-xs text-gray-500 tabular-nums dark:text-gray-400" aria-live="polite">
			{savedLabel}
		</span>
		<Button
			color="alternative"
			size="xs"
			class="ms-auto gap-2"
			disabled={items.length === 0}
			onclick={exportAll}
		>
			<DownloadOutline class="h-4 w-4" />
			Export
		</Button>
		<Tooltip>Download your bookmarks as JSON</Tooltip>
		<Button color="alternative" size="xs" class="gap-2" onclick={() => picker?.click()}>
			<UploadOutline class="h-4 w-4" />
			Import
		</Button>
		<Tooltip>Read bookmarks back from a file you exported</Tooltip>
		<!-- Last, and the one control here with no word beside it: three labels do not fit
		the drawer, and a bin that opens a confirmation is the one that reads without one. -->
		<Button
			color="red"
			outline
			size="xs"
			class="!px-2"
			disabled={items.length === 0}
			aria-label="Remove all bookmarks"
			onclick={() => (confirming = true)}
		>
			<TrashBinOutline class="h-4 w-4" />
		</Button>
		<Tooltip>Remove all bookmarks</Tooltip>
	</div>

	<!-- The button above stands in for this, which no styling makes presentable. -->
	<input
		type="file"
		accept="application/json,.json"
		class="hidden"
		bind:this={picker}
		onchange={pickFile}
	/>
</Drawer>

<!-- sm rather than xs, because three choices crowd the narrower foot. The file is read
and understood before this opens, so the counts below are the ones that would apply. -->
<Modal
	title="Import bookmarks"
	size="sm"
	bind:open={() => pending !== undefined, (shown) => (pending = shown ? pending : undefined)}
>
	{#if pending}
		<P class="text-sm text-gray-500 dark:text-gray-400">
			<span class="font-medium break-all text-gray-900 dark:text-white">{pending.name}</span>
			holds {plural(pending.records.length)}. You have {items.length} saved.
		</P>
		<div class="flex flex-wrap justify-end gap-2 pt-2">
			<Button color="alternative" size="sm" onclick={() => (pending = undefined)}>Cancel</Button>
			<Button color="red" size="sm" onclick={() => runImport('replace')}>Replace all</Button>
			<Button size="sm" onclick={() => runImport('merge')}>Add to mine</Button>
		</div>
	{/if}
</Modal>

<!-- Emptying the store is the one action here that cannot be undone from the panel. -->
<Modal title="Remove all bookmarks?" bind:open={confirming} size="xs">
	<P class="text-sm text-gray-500 dark:text-gray-400">
		This deletes the {items.length} posts saved in this browser and cannot be undone.
	</P>
	<div class="flex justify-end gap-2 pt-2">
		<Button color="alternative" size="sm" onclick={() => (confirming = false)}>Cancel</Button>
		<Button color="red" size="sm" onclick={removeAll}>Remove all</Button>
	</div>
</Modal>
