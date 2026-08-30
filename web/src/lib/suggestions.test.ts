import { describe, expect, it } from 'vitest';
import { highlight, merge } from './suggestions.svelte';
import type { ApiSuggestion } from './api';

const api = (text: string, kind: ApiSuggestion['kind'] = 'query'): ApiSuggestion => ({
	text,
	kind
});

describe('highlight', () => {
	it('splits a suggestion around the words already typed', () => {
		expect(highlight('rust ownership', 'rust')).toEqual([
			{ text: 'rust', match: true },
			{ text: ' ownership', match: false }
		]);
	});

	it('prefers a word boundary over a match inside a word', () => {
		// "own" should point at "ownership", not at the "own" inside "downstream".
		expect(highlight('downstream ownership', 'own')).toEqual([
			{ text: 'downstream ', match: false },
			{ text: 'own', match: true },
			{ text: 'ership', match: false }
		]);
	});

	it('falls back to a match anywhere when no word starts with the query', () => {
		expect(highlight('downstream', 'own')).toEqual([
			{ text: 'd', match: false },
			{ text: 'own', match: true },
			{ text: 'stream', match: false }
		]);
	});

	it("keeps the suggestion's own capitalisation rather than the reader's", () => {
		const [first] = highlight('Rust Ownership', 'rust');
		expect(first).toEqual({ text: 'Rust', match: true });
	});

	it('marks only the first occurrence', () => {
		// Two bold runs in a short row read as emphasis rather than as an answer to
		// "why is this here".
		expect(highlight('rust and more rust', 'rust').filter((s) => s.match)).toHaveLength(1);
	});

	it('returns the text whole when nothing matches', () => {
		expect(highlight('kubernetes', 'zzz')).toEqual([{ text: 'kubernetes', match: false }]);
		expect(highlight('kubernetes', '   ')).toEqual([{ text: 'kubernetes', match: false }]);
	});

	it('never emits empty segments', () => {
		for (const segments of [highlight('rust', 'rust'), highlight('rust book', 'rust')]) {
			expect(segments.every((s) => s.text !== '')).toBe(true);
		}
	});

	it('returns markup as text, so a hostile title cannot be injected', () => {
		// The corpus is third-party titles. Segments rather than a marked-up string is
		// what keeps highlighting from becoming an injection: the angle brackets have
		// to survive as characters for Svelte to escape them.
		const hostile = '<img src=x onerror=alert(1)> rust';
		expect(
			highlight(hostile, 'rust')
				.map((s) => s.text)
				.join('')
		).toBe(hostile);
	});
});

describe('merge', () => {
	it('puts remembered searches first and keeps the API order after them', () => {
		const rows = merge(['rust ownership'], [api('rust async'), api('rust book')], 'rust', 8);
		expect(rows).toEqual([
			{ text: 'rust ownership', kind: 'recent' },
			{ text: 'rust async', kind: 'query' },
			{ text: 'rust book', kind: 'query' }
		]);
	});

	it('keeps the reader’s own row when the API offers the same words', () => {
		// Saying "you searched this before" is more use than offering it twice.
		const rows = merge(['rust book'], [api('Rust Book')], 'rust', 8);
		expect(rows).toEqual([{ text: 'rust book', kind: 'recent' }]);
	});

	it('drops completions that no longer answer what is in the box', () => {
		// The store holds the last answer while the next is in flight, and a request
		// that fails leaves it holding a stale one for good. This is what expires it.
		const rows = merge([], [api('kubernetes pods'), api('rust ownership')], 'rust', 8);
		expect(rows.map((r) => r.text)).toEqual(['rust ownership']);
	});

	it('honours the limit across both sources', () => {
		const rows = merge(
			['rust a', 'rust b'],
			[api('rust c'), api('rust d'), api('rust e')],
			'rust',
			3
		);
		expect(rows).toHaveLength(3);
		expect(rows.map((r) => r.kind)).toEqual(['recent', 'recent', 'query']);
	});

	it('carries the kind through, which is what picks the icon', () => {
		const rows = merge([], [api('Rust Atomics and Locks', 'title'), api('rust async')], 'rust', 8);
		expect(rows.map((r) => r.kind)).toEqual(['title', 'query']);
	});

	it('returns nothing when there is nothing to offer', () => {
		expect(merge([], [], 'rust', 8)).toEqual([]);
	});
});
