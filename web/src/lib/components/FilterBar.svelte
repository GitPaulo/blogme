<script lang="ts">
	import { Badge, Button, Input, Label, Modal, MultiSelect } from 'flowbite-svelte';
	import { BookmarkSolid, CalendarMonthOutline, CloseOutline } from 'flowbite-svelte-icons';
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

	let { results, filters = $bindable() }: { results: SearchResult[]; filters: Filters } = $props();

	let dateOpen = $state(false);

	// Nothing is published in the future, so a stray year cannot empty the list.
	const today = new Date().toISOString().slice(0, 10);
	const tagItems = $derived(filterTags(results).map((tag) => ({ value: tag, name: tag })));
	const active = $derived(isFiltered(filters));
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

<div class="mt-3 flex flex-wrap items-center gap-2">
	{#if tagItems.length > 0}
		<MultiSelect
			size="sm"
			class="w-full min-w-0 sm:w-64"
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
		onclick={() => (filters.bookmarkedOnly = !filters.bookmarkedOnly)}
	>
		<BookmarkSolid class="h-4 w-4" />
		Bookmarked
	</Button>

	<Button
		size="sm"
		color="red"
		outline
		class="ms-auto shrink-0 gap-1.5"
		disabled={!active}
		aria-label="Clear filters"
		onclick={() => (filters = emptyFilters())}
	>
		<CloseOutline class="h-4 w-4" />
		<span class="hidden sm:inline">Clear</span>
	</Button>
</div>

<Modal title="Published" bind:open={dateOpen} size="xs">
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

	<div class="mt-4 border-t border-gray-200 pt-4 dark:border-gray-700">
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
