import type { SearchResult } from './api';

export type Filters = {
	/** A result matches when it carries any of the selected tags. */
	tags: string[];
	/** Quick window in days back from now; 0 when unused. */
	days: number;
	/** Inclusive yyyy-mm-dd bounds of a precise range, empty when unused. */
	from: string;
	to: string;
	bookmarkedOnly: boolean;
};

export const PERIODS = [
	{ days: 0, name: 'Any time' },
	{ days: 30, name: 'Past month' },
	{ days: 365, name: 'Past year' },
	{ days: 1825, name: 'Past 5 years' }
];

export const emptyFilters = (): Filters => ({
	tags: [],
	days: 0,
	from: '',
	to: '',
	bookmarkedOnly: false
});

export const hasDateFilter = (filters: Filters) =>
	filters.days > 0 || filters.from !== '' || filters.to !== '';

export const isFiltered = (filters: Filters) =>
	filters.tags.length > 0 || hasDateFilter(filters) || filters.bookmarkedOnly;

/** Tags present in the loaded results, most common first. */
export function filterTags(results: SearchResult[]): string[] {
	const counts = new Map<string, number>();
	for (const result of results) {
		for (const tag of result.topics ?? []) counts.set(tag, (counts.get(tag) ?? 0) + 1);
	}
	return [...counts]
		.sort(([aTag, aCount], [bTag, bCount]) => bCount - aCount || aTag.localeCompare(bTag))
		.map(([tag]) => tag);
}

const DAY_MS = 86_400_000;

// Day bounds are UTC to match how publication dates are rendered. A half-typed
// bound parses as NaN and is treated as unset.
const dayStart = (day: string) => (day ? Date.parse(`${day}T00:00:00Z`) : NaN);
const dayEnd = (day: string) => (day ? Date.parse(`${day}T23:59:59.999Z`) : NaN);

export function applyFilters(
	results: SearchResult[],
	filters: Filters,
	isBookmarked: (url: string) => boolean
): SearchResult[] {
	if (!isFiltered(filters)) return results;

	const tags = new Set(filters.tags);
	// A quick window and a precise range never apply at the same time.
	const from = filters.days > 0 ? Date.now() - filters.days * DAY_MS : dayStart(filters.from);
	const to = filters.days > 0 ? NaN : dayEnd(filters.to);
	const dated = !Number.isNaN(from) || !Number.isNaN(to);

	return results.filter((result) => {
		if (filters.bookmarkedOnly && !isBookmarked(result.url)) return false;
		if (tags.size > 0 && !result.topics?.some((tag) => tags.has(tag))) return false;
		if (dated) {
			// An undated post cannot be shown to fall inside the window.
			const published = result.publishedAt ? Date.parse(result.publishedAt) : NaN;
			if (Number.isNaN(published)) return false;
			if (!Number.isNaN(from) && published < from) return false;
			if (!Number.isNaN(to) && published > to) return false;
		}
		return true;
	});
}
