import { describe, expect, it } from 'vitest';
import { visitKey } from './key';

// A visit is stored under a hash of the url, so this decides which links wear the
// "opened" mark. Two urls that name one article must land on one key, or a reader sees
// the mark come and go depending on which link they arrived through; two that name
// different articles must not, or one post wears a mark it did not earn.
//
// Nothing here asserts a particular number. The hash is an implementation detail and the
// records are disposable — what matters is which urls agree with which.

describe('visitKey, on urls that name one article', () => {
	const same = (a: string, b: string) => expect(visitKey(a)).toBe(visitKey(b));

	it('reads a blog served over both schemes as one blog', () => {
		same('http://example.com/post', 'https://example.com/post');
	});

	it('ignores a leading www.', () => {
		same('https://www.example.com/post', 'https://example.com/post');
	});

	it('ignores a trailing slash', () => {
		same('https://example.com/post/', 'https://example.com/post');
	});

	it('ignores a fragment, which picks a place inside a page rather than a page', () => {
		same('https://example.com/post#section-two', 'https://example.com/post');
	});

	it('strips the parameters that describe where a click came from', () => {
		same('https://example.com/post?utm_source=hn&fbclid=abc', 'https://example.com/post');
		same('https://example.com/post?ref=newsletter', 'https://example.com/post');
	});

	it('sorts the parameters it keeps, so one page written two ways is one page', () => {
		same('https://example.com/post?a=1&b=2', 'https://example.com/post?b=2&a=1');
	});

	it('ignores the case of the host, which is case-insensitive', () => {
		same('https://EXAMPLE.com/post', 'https://example.com/post');
	});
});

describe('visitKey, on urls that name different articles', () => {
	const differ = (a: string, b: string) => expect(visitKey(a)).not.toBe(visitKey(b));

	it('separates two paths on one blog', () => {
		differ('https://example.com/one', 'https://example.com/two');
	});

	it('separates two blogs sharing a path', () => {
		differ('https://a.example.com/post', 'https://b.example.com/post');
	});

	it('keeps the case of a path, which is not case-insensitive', () => {
		differ('https://example.com/Post', 'https://example.com/post');
	});

	it('keeps a parameter that is not a tracking one', () => {
		// A blog is free to route on anything the tracking list does not name.
		differ('https://example.com/post?page=2', 'https://example.com/post');
	});
});

describe('visitKey, on input that is not a url', () => {
	it('hashes it as it came rather than throwing on the way to rendering a row', () => {
		expect(typeof visitKey('not a url at all')).toBe('number');
		expect(visitKey('  Not A Url  ')).toBe(visitKey('not a url'));
	});

	it('always returns a finite number, which is what IndexedDB keys on', () => {
		for (const url of ['https://example.com/a', 'mailto:someone@example.com', '', '://']) {
			const key = visitKey(url);
			expect(Number.isFinite(key), url).toBe(true);
			expect(Number.isInteger(key), url).toBe(true);
		}
	});

	it('gives one url the same key every time it is asked', () => {
		// Asked once per row and again on every filter pass, so the answer is cached —
		// and a cache that ever disagreed with a fresh hash would flicker the mark.
		const url = 'https://example.com/post';
		expect(visitKey(url)).toBe(visitKey(url));
	});
});
