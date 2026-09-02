import { describe, expect, it } from 'vitest';
import type { SearchResult } from './api';
import {
	applyFilters,
	emptyFilters,
	filterTags,
	hasDateFilter,
	isFiltered,
	type Filters,
	type Lookups
} from './filters';

// These narrow the rows already fetched rather than the query behind them, so every one
// of them runs again on each page that arrives and on every bookmark toggled. The cases
// that matter are the ones where a filter would quietly drop a row it should have kept:
// a post with no date under a date window, a half-typed bound, an empty tag list.

const post = (url: string, extra: Partial<SearchResult> = {}): SearchResult => ({
	url,
	title: url,
	score: 1,
	...extra
});

/** Nothing is bookmarked or visited unless a test says so. */
const lookups = (bookmarked: string[] = [], visited: string[] = []): Lookups => ({
	isBookmarked: (url) => bookmarked.includes(url),
	isVisited: (url) => visited.includes(url)
});

const withFilters = (over: Partial<Filters>): Filters => ({ ...emptyFilters(), ...over });

describe('isFiltered and hasDateFilter', () => {
	it('reads a fresh set of filters as narrowing nothing', () => {
		expect(isFiltered(emptyFilters())).toBe(false);
		expect(hasDateFilter(emptyFilters())).toBe(false);
	});

	it('notices each way of narrowing on its own', () => {
		expect(isFiltered(withFilters({ tags: ['rust'] }))).toBe(true);
		expect(isFiltered(withFilters({ days: 30 }))).toBe(true);
		expect(isFiltered(withFilters({ from: '2026-01-01' }))).toBe(true);
		expect(isFiltered(withFilters({ to: '2026-01-01' }))).toBe(true);
		expect(isFiltered(withFilters({ bookmarkedOnly: true }))).toBe(true);
		expect(isFiltered(withFilters({ visitedOnly: true }))).toBe(true);
	});

	it('counts only the date filters as date filters', () => {
		expect(hasDateFilter(withFilters({ tags: ['rust'] }))).toBe(false);
		expect(hasDateFilter(withFilters({ days: 30 }))).toBe(true);
	});
});

describe('filterTags', () => {
	it('offers the tags present in the loaded results, most common first', () => {
		const rows = [
			post('https://a.example/1', { topics: ['rust', 'wasm'] }),
			post('https://a.example/2', { topics: ['rust'] }),
			post('https://a.example/3', { topics: ['go'] })
		];
		expect(filterTags(rows)).toEqual(['rust', 'go', 'wasm']);
	});

	it('breaks a tie alphabetically, so the list does not reshuffle between pages', () => {
		const rows = [
			post('https://a.example/1', { topics: ['zig'] }),
			post('https://a.example/2', { topics: ['ada'] })
		];
		expect(filterTags(rows)).toEqual(['ada', 'zig']);
	});

	it('has nothing to offer for results carrying no topics', () => {
		expect(filterTags([post('https://a.example/1')])).toEqual([]);
	});
});

describe('applyFilters', () => {
	const rows = [
		post('https://a.example/rust', { topics: ['rust'], publishedAt: '2026-01-15' }),
		post('https://a.example/go', { topics: ['go', 'rust'], publishedAt: '2020-06-01' }),
		post('https://a.example/undated', { topics: ['go'] })
	];

	it('hands back the very same array when nothing is narrowing', () => {
		// Not merely an equal one: an unfiltered page must not look a single url up, and
		// a fresh array would make every consumer downstream recompute for nothing.
		expect(applyFilters(rows, emptyFilters(), lookups())).toBe(rows);
	});

	it('keeps a result carrying any of the selected tags', () => {
		const kept = applyFilters(rows, withFilters({ tags: ['rust'] }), lookups());
		expect(kept.map((r) => r.url)).toEqual(['https://a.example/rust', 'https://a.example/go']);
	});

	it('keeps only what the reader saved', () => {
		const kept = applyFilters(
			rows,
			withFilters({ bookmarkedOnly: true }),
			lookups(['https://a.example/go'])
		);
		expect(kept.map((r) => r.url)).toEqual(['https://a.example/go']);
	});

	it('keeps only what the reader has opened', () => {
		const kept = applyFilters(
			rows,
			withFilters({ visitedOnly: true }),
			lookups([], ['https://a.example/rust'])
		);
		expect(kept.map((r) => r.url)).toEqual(['https://a.example/rust']);
	});

	it('drops an undated post from any date window', () => {
		// It cannot be shown to fall inside one, and guessing would be worse.
		const kept = applyFilters(rows, withFilters({ from: '2000-01-01' }), lookups());
		expect(kept.map((r) => r.url)).not.toContain('https://a.example/undated');
	});

	it('takes both bounds of an exact range inclusively', () => {
		const kept = applyFilters(
			rows,
			withFilters({ from: '2026-01-15', to: '2026-01-15' }),
			lookups()
		);
		expect(kept.map((r) => r.url)).toEqual(['https://a.example/rust']);
	});

	it('treats a half-typed bound as unset rather than as a window matching nothing', () => {
		// The date input reports every keystroke, so this is the common state, not a rare
		// one. It narrows nothing at all — undated posts included, which is what stops the
		// list emptying out halfway through typing a year.
		const kept = applyFilters(rows, withFilters({ from: '20' }), lookups());
		expect(kept).toEqual(rows);
	});

	it('lets a quick window replace an exact range rather than intersecting it', () => {
		// The bar clears one when the other is set; this is the guarantee behind that.
		const kept = applyFilters(
			rows,
			withFilters({ days: 365_000, from: '2026-01-15', to: '2026-01-15' }),
			lookups()
		);
		expect(kept.map((r) => r.url)).toEqual(['https://a.example/rust', 'https://a.example/go']);
	});

	it('applies every active filter, not just the first', () => {
		const kept = applyFilters(
			rows,
			withFilters({ tags: ['rust'], bookmarkedOnly: true }),
			lookups(['https://a.example/go'])
		);
		expect(kept.map((r) => r.url)).toEqual(['https://a.example/go']);
	});
});
