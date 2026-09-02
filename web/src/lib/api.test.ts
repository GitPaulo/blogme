import { afterEach, describe, expect, it, vi } from 'vitest';
import {
	clampQuery,
	MAX_OFFSET_KEYWORD,
	MAX_QUERY_LENGTH,
	maxOffsetFor,
	PAGE_SIZE,
	safeHttpUrl,
	search,
	SearchError,
	SEMANTIC_WINDOW,
	suggest
} from './api';

// Everything the API sends is third-party content it gathered from blogs nobody here
// wrote, so the response is untrusted input and this is the boundary that says so. A
// malformed row is dropped rather than rendered; a malformed response is an empty page
// rather than a thrown error over a search the reader can see they asked for.
//
// The numbers below mirror api/internal/httpapi. Anything changed here has to be changed
// there too — see the constants test.

/** Answers the next fetch with this body, and reports what was asked for. */
function stubFetch(body: unknown, init: { status?: number; ok?: boolean } = {}) {
	const status = init.status ?? 200;
	const calls: string[] = [];
	vi.stubGlobal('fetch', (url: string) => {
		calls.push(url);
		return Promise.resolve({
			ok: init.ok ?? status < 400,
			status,
			json: () => Promise.resolve(body)
		});
	});
	return calls;
}

const page = (over: Record<string, unknown> = {}) => ({
	total: 100,
	nextOffset: 20,
	exhausted: false,
	broadened: false,
	results: [],
	...over
});

const row = (over: Record<string, unknown> = {}) => ({
	url: 'https://example.com/post',
	title: 'A post',
	score: 1,
	...over
});

afterEach(() => vi.unstubAllGlobals());

describe('clampQuery', () => {
	it('trims what the reader typed', () => {
		expect(clampQuery('  rust ownership  ')).toBe('rust ownership');
	});

	it('holds a long query to the cap', () => {
		expect(clampQuery('a'.repeat(MAX_QUERY_LENGTH + 50))).toHaveLength(MAX_QUERY_LENGTH);
	});

	it('counts code points, so an emoji is never halved on the boundary', () => {
		// `slice` counts UTF-16 units and would leave a lone surrogate, which renders as
		// a replacement character in the search box.
		const clamped = clampQuery('👋'.repeat(MAX_QUERY_LENGTH + 10));
		expect([...clamped]).toHaveLength(MAX_QUERY_LENGTH);
		expect(clamped).not.toContain('�');
	});
});

describe('safeHttpUrl', () => {
	it('passes an ordinary web link', () => {
		expect(safeHttpUrl('https://example.com/post')).toBe('https://example.com/post');
		expect(safeHttpUrl('http://example.com/post')).toBe('http://example.com/post');
	});

	it('refuses a scheme that is not a web page', () => {
		// These end up as hrefs, so anything that could execute is not a link.
		for (const value of ['javascript:alert(1)', 'data:text/html,x', 'mailto:a@b.com', 'file:///']) {
			expect(safeHttpUrl(value), value).toBeUndefined();
		}
	});

	it('refuses what it cannot parse, and anything that is not a string', () => {
		for (const value of ['not a url', '', null, undefined, 42, {}]) {
			expect(safeHttpUrl(value), String(value)).toBeUndefined();
		}
	});
});

describe('maxOffsetFor', () => {
	it('lets keyword ranking page deep, because it scores the whole result set', () => {
		expect(maxOffsetFor('keyword')).toBe(MAX_OFFSET_KEYWORD);
	});

	it('holds semantic ranking inside the window the reranker ordered', () => {
		expect(maxOffsetFor('semantic', PAGE_SIZE)).toBe(SEMANTIC_WINDOW - PAGE_SIZE);
	});

	it('never returns a negative offset for a page wider than the window', () => {
		expect(maxOffsetFor('semantic', SEMANTIC_WINDOW + 10)).toBe(0);
	});
});

describe('search, on what it asks for', () => {
	it('sends the query and the page size, and nothing it does not have to', () => {
		const calls = stubFetch(page());
		return search('rust').then(() => {
			const url = new URL(calls[0], 'https://api.example');
			expect(url.searchParams.get('q')).toBe('rust');
			expect(url.searchParams.get('limit')).toBe(String(PAGE_SIZE));
			// Keyword is the server's default, so only a departure from it travels.
			expect(url.searchParams.has('mode')).toBe(false);
			expect(url.searchParams.has('offset')).toBe(false);
		});
	});

	it('sends the source ids and no query at all when browsing a blog', async () => {
		const calls = stubFetch(page());
		await search('', { sources: ['tonsky', 'tonsky-2'] });
		const url = new URL(calls[0], 'https://api.example');
		expect(url.searchParams.has('q')).toBe(false);
		expect(url.searchParams.get('source')).toBe('tonsky,tonsky-2');
	});

	it('holds the offset inside what the ranking mode can reach', async () => {
		const calls = stubFetch(page());
		await search('rust', { rank: 'semantic', offset: 5_000 });
		const url = new URL(calls[0], 'https://api.example');
		expect(Number(url.searchParams.get('offset'))).toBe(maxOffsetFor('semantic', PAGE_SIZE));
	});

	it('refuses a negative or fractional offset rather than passing it on', async () => {
		const calls = stubFetch(page());
		await search('rust', { offset: -5 });
		expect(new URL(calls[0], 'https://api.example').searchParams.has('offset')).toBe(false);
	});
});

describe('search, on what it accepts back', () => {
	it('keeps only rows that carry a web link', async () => {
		stubFetch(
			page({
				results: [
					row({ url: 'https://good.example/a' }),
					row({ url: 'javascript:alert(1)' }),
					row({ url: 42 }),
					'not an object'
				]
			})
		);
		const response = await search('rust');
		expect(response.results.map((r) => r.url)).toEqual(['https://good.example/a']);
	});

	it('falls back to the url when a row has no title to show', async () => {
		stubFetch(page({ results: [row({ title: '   ' })] }));
		const response = await search('rust');
		expect(response.results[0].title).toBe('https://example.com/post');
	});

	it('drops a repeated url, because rows are keyed by it in the markup', async () => {
		// The same article can be indexed twice when a blog serves it under two paths.
		stubFetch(page({ results: [row(), row()] }));
		const response = await search('rust');
		expect(response.results).toHaveLength(1);
	});

	it('dedupes the topics on a row, which are keyed in the markup too', async () => {
		stubFetch(page({ results: [row({ topics: ['rust', 'rust', 'wasm'] })] }));
		const response = await search('rust');
		expect(response.results[0].topics).toEqual(['rust', 'wasm']);
	});

	it('reads only a real boolean as an answer about framing', async () => {
		// Null, missing, or anything else is the API saying it does not know, which is
		// not the same as permission.
		stubFetch(
			page({
				results: [
					row({ url: 'https://a.example/1', framingDenied: true }),
					row({ url: 'https://a.example/2', framingDenied: 'yes' }),
					row({ url: 'https://a.example/3' })
				]
			})
		);
		const response = await search('rust');
		expect(response.results.map((r) => r.framingDenied)).toEqual([true, undefined, undefined]);
	});

	it('only accepts an origin the index actually uses', async () => {
		stubFetch(page({ results: [row({ origin: 'invented' })] }));
		const response = await search('rust');
		expect(response.results[0].origin).toBeUndefined();
	});

	it('never reports a total below what it just handed back', async () => {
		// A total that contradicted the rows on screen would make "load more" argue with
		// the summary above it.
		stubFetch(page({ total: 0, results: [row()] }));
		const response = await search('rust');
		expect(response.total).toBeGreaterThanOrEqual(1);
	});

	it('always advances the offset, so load more cannot ask for this page again', async () => {
		stubFetch(page({ nextOffset: 0 }));
		const response = await search('rust');
		expect(response.nextOffset).toBeGreaterThanOrEqual(PAGE_SIZE);
	});

	it('reads a missing exhausted or broadened flag as the quiet answer', async () => {
		// An older API that sends neither has not reached the end and has not broadened
		// anything; claiming the end early would strand every result past this page.
		stubFetch({ results: [row()] });
		const response = await search('rust');
		expect(response.exhausted).toBe(false);
		expect(response.broadened).toBe(false);
	});

	it('reads a body that is not an object as an empty page', async () => {
		stubFetch('not a response at all');
		const response = await search('rust');
		expect(response.results).toEqual([]);
	});
});

describe('search, when it fails', () => {
	it('carries the status, so being throttled can be told from being broken', async () => {
		stubFetch({ error: 'too many requests' }, { status: 429 });
		await expect(search('rust')).rejects.toMatchObject({ status: 429 });
		await expect(search('rust')).rejects.toBeInstanceOf(SearchError);
	});

	it('shows the API its own sentence for a refusal the reader can act on', async () => {
		stubFetch({ error: 'query is too long' }, { status: 400 });
		await expect(search('rust')).rejects.toThrow('Query is too long');
	});

	it('replaces a 5xx body, which only restates the failure', async () => {
		stubFetch({ error: 'internal server error' }, { status: 500 });
		await expect(search('rust')).rejects.toThrow(/unavailable/i);
	});

	it('reports an unreachable service as one, not as an empty result set', async () => {
		vi.stubGlobal('fetch', () => Promise.reject(new TypeError('failed to fetch')));
		await expect(search('rust')).rejects.toThrow(/could not reach/i);
	});
});

describe('suggest', () => {
	it('does not spend a round trip on a query the API would refuse', async () => {
		const calls = stubFetch({ suggestions: [] });
		expect(await suggest('ru')).toEqual([]);
		expect(await suggest('x'.repeat(100))).toEqual([]);
		expect(calls).toHaveLength(0);
	});

	it('keeps the order the API ranked, and drops what it cannot read', async () => {
		stubFetch({
			suggestions: [
				{ text: 'rust ownership', kind: 'title' },
				{ text: '   ', kind: 'title' },
				'not an object',
				{ text: 'rust lifetimes', kind: 'query' }
			]
		});
		expect(await suggest('rust')).toEqual([
			{ text: 'rust ownership', kind: 'title' },
			{ text: 'rust lifetimes', kind: 'query' }
		]);
	});

	it('drops a repeated suggestion, which the list keys by', async () => {
		stubFetch({
			suggestions: [
				{ text: 'rust', kind: 'title' },
				{ text: 'rust', kind: 'query' }
			]
		});
		expect(await suggest('rust')).toHaveLength(1);
	});

	it('reads an unknown kind as the ordinary one', async () => {
		// A kind added later is a suggestion whose provenance we do not know.
		stubFetch({ suggestions: [{ text: 'rust', kind: 'invented' }] });
		expect((await suggest('rust'))[0].kind).toBe('query');
	});

	it('reads a body with no suggestions in it as none', async () => {
		stubFetch({ suggestions: 'not an array' });
		expect(await suggest('rust')).toEqual([]);
	});
});
