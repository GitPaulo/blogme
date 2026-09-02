import { describe, expect, it } from 'vitest';
import { ELLIPSIS, snippet, snippetBudget } from './snippet';

// This is the second of two passes over the same text: the crawler cuts to a word cap
// when it indexes an article, and this cuts again to the width the card turned out to
// have. Both follow the same rule, and the half worth testing is the one that decides a
// full stop is not the end of a sentence — an initial, an abbreviation, a lettered
// "e.g." — because getting that wrong ends a description mid-thought and looks like the
// author wrote it that way.
//
// The Go side of the rule lives in api/internal/discovery/extract.go. Anything changed
// here should be changed there too.

describe('snippetBudget', () => {
	it('reads an unmeasured width as no budget at all', () => {
		// Callers render the description whole and leave the CSS clamp holding the card's
		// height until the first measurement lands.
		expect(snippetBudget(0)).toBe(0);
		expect(snippetBudget(-1)).toBe(0);
		expect(snippetBudget(Number.NaN)).toBe(0);
	});

	it('grows with the width it is given', () => {
		expect(snippetBudget(600)).toBeGreaterThan(snippetBudget(400));
	});

	it('never returns a budget too small to say anything', () => {
		expect(snippetBudget(1)).toBe(60);
	});

	it('scales with the number of lines the card gives it', () => {
		expect(snippetBudget(600, 6)).toBeGreaterThan(snippetBudget(600, 3));
	});
});

describe('snippet', () => {
	it('returns the text unchanged while the width is not known', () => {
		expect(snippet('Anything at all.', 0)).toBe('Anything at all.');
	});

	it('has nothing to say about empty text', () => {
		expect(snippet('   ', 100)).toBe('');
	});

	it('returns text that already ends on a sentence whole and unmarked', () => {
		expect(snippet('Hello world.', 100)).toBe('Hello world.');
	});

	it('marks text that fits but still ends mid-clause', () => {
		// It fits, so nothing was cut here — it was cut upstream by the crawler, and the
		// reader is owed the same signal either way.
		expect(snippet('Hello world', 100)).toBe(`Hello world${ELLIPSIS}`);
	});

	it('ends on the last sentence that clears the floor', () => {
		const text = 'This is a full sentence here. And more text follows after it.';
		expect(snippet(text, 40)).toBe('This is a full sentence here.');
	});

	it('ends on a word when the only sentence end is too early to be worth taking', () => {
		// An article opening on a one-liner would otherwise leave the card showing four
		// words where forty were available.
		const text = 'Short. Then a much longer continuation that keeps going for a while yet';
		expect(snippet(text, 40)).toBe(`Short. Then a much longer continuation${ELLIPSIS}`);
	});

	it('never cuts mid-word when there is a boundary to cut on', () => {
		const cut = snippet('alpha beta gamma delta epsilon zeta', 20);
		expect(cut.endsWith(ELLIPSIS)).toBe(true);
		expect(cut.slice(0, -ELLIPSIS.length).split(' ').at(-1)).toBe('gamma');
	});

	it('cuts a first word longer than the whole budget where it stands', () => {
		expect(snippet('supercalifragilisticexpialidocious', 10)).toBe(`supercalif${ELLIPSIS}`);
	});
});

describe('snippet, on full stops that do not end a sentence', () => {
	it('does not stop on a known abbreviation', () => {
		expect(snippet('Redis memcached etc. plus', 25)).toBe(`Redis memcached etc. plus${ELLIPSIS}`);
	});

	it('does stop on a word of the same shape that is not one', () => {
		// The pair is the whole test: the only difference is the abbreviations table.
		expect(snippet('Redis memcached ftw. plus', 25)).toBe('Redis memcached ftw.');
	});

	it('does not stop on an initial', () => {
		const text = 'Written by J. Random Hacker over several years';
		expect(snippet(text, 30)).toBe(`Written by J. Random Hacker${ELLIPSIS}`);
	});

	it('does not stop on a lettered abbreviation', () => {
		// Stopping on "e.g." would end the description four words early, at "Many
		// languages e.g." — which reads as a sentence the author never wrote.
		const text = 'Many languages e.g. rust and go and zig';
		expect(snippet(text, 30)).toBe(`Many languages e.g. rust and${ELLIPSIS}`);
	});

	it('stops on a sentence closed inside quotation marks', () => {
		const text = 'He said "this is over." and then left the room for good';
		expect(snippet(text, 40)).toBe('He said "this is over."');
	});

	it('stops on a question or an exclamation as readily as a full stop', () => {
		// Both openers are long enough to clear the sentence floor, so what is being
		// tested here is the punctuation rather than how much of the budget it keeps.
		expect(snippet('Is this thing on? Apparently it is and more', 30)).toBe('Is this thing on?');
		expect(snippet('What a fine result! Apparently it is', 30)).toBe('What a fine result!');
	});
});
