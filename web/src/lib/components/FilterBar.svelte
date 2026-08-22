<script lang="ts">
	import { Badge, Button, Input, Label, Modal, MultiSelect, Tooltip } from 'flowbite-svelte';
	import {
		BookmarkSolid,
		CalendarMonthOutline,
		CloseOutline,
		EyeSolid,
		GlobeOutline
	} from 'flowbite-svelte-icons';
	import type { SearchResult } from '$lib/api';
	import { formatDate } from '$lib/date';
	import {
		emptyFilters,
		filterTags,
		hasDateFilter,
		isFiltered,
		PERIODS,
		type Filters
	} from '$lib/filters';

	// Sitemapped is separate from the rest: the others narrow what is already loaded,
	// whereas this one is answered by the search itself.
	let {
		results,
		filters = $bindable(),
		sitemapped = $bindable()
	}: { results: SearchResult[]; filters: Filters; sitemapped: boolean } = $props();

	let dateOpen = $state(false);

	// Nothing is published in the future, so a stray year cannot empty the list.
	const today = new Date().toISOString().slice(0, 10);
	const tagItems = $derived(filterTags(results).map((tag) => ({ value: tag, name: tag })));
	const active = $derived(isFiltered(filters) || sitemapped);
	const ranged = $derived(filters.from !== '' || filters.to !== '');
	const dateLabel = $derived(
		ranged
			? `${formatDate(filters.from) ?? 'Earliest'} – ${formatDate(filters.to) ?? 'Today'}`
			: (PERIODS.find((option) => option.days === filters.days)?.name ?? 'Any time')
	);

	function pickPeriod(days: number) {
		filters.days = days;
		filters.from = '';
		filters.to = '';
		dateOpen = false;
	}

	function setBound(bound: 'from' | 'to', event: Event) {
		filters.days = 0; // A precise range replaces the quick window.
		filters[bound] = (event.target as HTMLInputElement).value;
	}
</script>

<!-- Clear sits in the same wrapping row as the rest rather than in a column beside it:
outside, it anchored to the top of a group whose own items are centred, so every tag
that made the select taller pulled it further out of line. -->
<div class="mt-3 flex flex-wrap items-center gap-2">
	{#if tagItems.length > 0}
		<!-- Takes the width the buttons leave rather than a fixed one: a wrapping row
			breaks on each item's natural size, so any width wide enough to push the last
			button onto a second line would do so before the select ever gave any back. -->
		<MultiSelect
			size="sm"
			class="w-full min-w-0 sm:w-auto sm:flex-1"
			items={tagItems}
			bind:value={filters.tags}
			placeholder="All tags"
			aria-label="Filter by tag"
		>
			{#snippet children({ item, clear })}
				<Badge color="gray" dismissable onclose={clear} class="mx-0.5 px-2 py-0">
					<span class="block max-w-32 truncate">{item.name}</span>
				</Badge>
			{/snippet}
		</MultiSelect>
	{/if}

	<Button
		size="sm"
		color={hasDateFilter(filters) ? 'primary' : 'alternative'}
		class="max-w-64 shrink-0 gap-2"
		aria-haspopup="dialog"
		aria-expanded={dateOpen}
		onclick={() => (dateOpen = true)}
	>
		<CalendarMonthOutline class="h-4 w-4 shrink-0" />
		<span class="truncate">{dateLabel}</span>
	</Button>

	<Button
		size="sm"
		color={filters.bookmarkedOnly ? 'primary' : 'alternative'}
		class="shrink-0 gap-2"
		aria-pressed={filters.bookmarkedOnly}
		aria-label="Bookmarked"
		onclick={() => (filters.bookmarkedOnly = !filters.bookmarkedOnly)}
	>
		<BookmarkSolid class="h-4 w-4" />
		<span class="hidden sm:inline">Bookmarked</span>
	</Button>

	<Button
		size="sm"
		color={filters.visitedOnly ? 'primary' : 'alternative'}
		class="shrink-0 gap-2"
		aria-pressed={filters.visitedOnly}
		aria-label="Visited"
		onclick={() => (filters.visitedOnly = !filters.visitedOnly)}
	>
		<EyeSolid class="h-4 w-4" />
		<span class="hidden sm:inline">Visited</span>
	</Button>
	<!-- Bookmarked needs no explanation because the reader pressed a button to make one.
	Nothing is pressed to make a visit, so this one says where they come from. -->
	<Tooltip class="max-w-64 text-center">Show only posts you have already opened from here.</Tooltip>

	<Button
		size="sm"
		color={sitemapped ? 'primary' : 'alternative'}
		class="shrink-0 gap-2"
		aria-pressed={sitemapped}
		aria-label="Sitemapped"
		onclick={() => (sitemapped = !sitemapped)}
	>
		<!-- A globe, not a branch: these came off the open site, and the branch read as
			version control. -->
		<GlobeOutline class="h-4 w-4" />
		<span class="hidden sm:inline">Sitemapped</span>
	</Button>
	<Tooltip class="max-w-64 text-center">
		Show only posts found through a site's page list instead of its feed.
	</Tooltip>

	<Button
		size="sm"
		color="red"
		outline
		class="shrink-0 gap-1.5"
		disabled={!active}
		aria-label="Clear filters"
		onclick={() => {
			filters = emptyFilters();
			sitemapped = false;
		}}
	>
		<CloseOutline class="h-4 w-4" />
		<span class="hidden sm:inline">Clear</span>
	</Button>
</div>

<!-- sm rather than xs: two date inputs plus their picker icons do not fit the
narrower body without crowding. -->
<Modal title="Select a time window" bind:open={dateOpen} size="sm">
	<div class="grid grid-cols-2 gap-2">
		{#each PERIODS as option (option.days)}
			{@const selected = !ranged && filters.days === option.days}
			<Button
				size="sm"
				color={selected ? 'primary' : 'alternative'}
				aria-pressed={selected}
				onclick={() => pickPeriod(option.days)}
			>
				{option.name}
			</Button>
		{/each}
	</div>

	<!-- The heading carries the split between the two ways of picking rather than a
	rule, which only works if the break between them is the widest gap in the body:
	padding, because the body's own spacing owns the margin. -->
	<div class="pt-2">
		<p class="mb-2 text-sm text-gray-500 dark:text-gray-400">Or pick an exact range</p>
		<div class="grid grid-cols-2 gap-2">
			<Label class="space-y-1 text-xs font-normal text-gray-500 dark:text-gray-400">
				From
				<Input
					type="date"
					size="sm"
					value={filters.from}
					max={filters.to || today}
					oninput={(event) => setBound('from', event)}
				/>
			</Label>
			<Label class="space-y-1 text-xs font-normal text-gray-500 dark:text-gray-400">
				To
				<Input
					type="date"
					size="sm"
					value={filters.to}
					min={filters.from || undefined}
					max={today}
					oninput={(event) => setBound('to', event)}
				/>
			</Label>
		</div>
	</div>
</Modal>
