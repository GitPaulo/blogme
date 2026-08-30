import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const KEY = 'blogme/recent-searches';

/** A stand-in for the browser's, with the failures that matter made reachable. */
function storage(seed?: string) {
	const map = new Map<string, string>();
	if (seed !== undefined) map.set(KEY, seed);
	return {
		map,
		throwOnRead: false,
		throwOnWrite: false,
		getItem(k: string) {
			if (this.throwOnRead) throw new Error('blocked');
			return this.map.get(k) ?? null;
		},
		setItem(k: string, v: string) {
			if (this.throwOnWrite) throw new Error('quota');
			this.map.set(k, v);
		},
		removeItem(k: string) {
			this.map.delete(k);
		}
	};
}

/**
 * The module reads storage once, at import, so every test needs its own copy of it.
 * That is the behaviour under test as much as a testing detail: reading at module scope
 * is what lets the first keystroke show a match instead of showing nothing and
 * correcting itself a frame later.
 */
async function load(seed?: string) {
	vi.resetModules();
	const store = storage(seed);
	vi.stubGlobal('localStorage', store);
	const { recent } = await import('./recent.svelte');
	return { recent, store };
}

const stored = (store: ReturnType<typeof storage>) => JSON.parse(store.map.get(KEY) ?? '[]');
const day = 24 * 60 * 60 * 1000;

beforeEach(() => vi.useRealTimers());
afterEach(() => vi.unstubAllGlobals());

describe('recent.matching', () => {
	it('offers a remembered search that carries on from what was typed', async () => {
		const { recent } = await load(JSON.stringify([{ q: 'rust ownership', at: Date.now() }]));
		expect(recent.matching('rust', 2)).toEqual(['rust ownership']);
	});

	it('matches on a word boundary, not inside a word', async () => {
		const { recent } = await load(JSON.stringify([{ q: 'rust ownership', at: Date.now() }]));
		expect(recent.matching('own', 2)).toEqual(['rust ownership']);
		expect(recent.matching('ust', 2)).toEqual([]);
	});

	it('leaves out the query already in the box', async () => {
		// Offering the reader the line they are looking at spends a row saying nothing.
		const { recent } = await load(JSON.stringify([{ q: 'rust', at: Date.now() }]));
		expect(recent.matching('rust', 2)).toEqual([]);
	});

	it('honours the limit and returns most recent first', async () => {
		const now = Date.now();
		const { recent } = await load(
			JSON.stringify([
				{ q: 'rust c', at: now },
				{ q: 'rust b', at: now - 1000 },
				{ q: 'rust a', at: now - 2000 }
			])
		);
		expect(recent.matching('rust', 2)).toEqual(['rust c', 'rust b']);
	});

	it('has nothing to say about an empty query', async () => {
		const { recent } = await load(JSON.stringify([{ q: 'rust', at: Date.now() }]));
		expect(recent.matching('   ', 2)).toEqual([]);
	});
});

describe('recent.record', () => {
	it('remembers a search and writes it through', async () => {
		const { recent, store } = await load();
		recent.record('rust ownership');
		expect(stored(store).map((e: { q: string }) => e.q)).toEqual(['rust ownership']);
	});

	it('moves a repeated search back to the front rather than duplicating it', async () => {
		const { recent, store } = await load();
		recent.record('rust');
		recent.record('kubernetes');
		recent.record('rust');
		expect(stored(store).map((e: { q: string }) => e.q)).toEqual(['rust', 'kubernetes']);
	});

	it('ignores an empty query', async () => {
		const { recent, store } = await load();
		recent.record('   ');
		expect(store.map.has(KEY)).toBe(false);
	});

	it('merges with another tab rather than overwriting it', async () => {
		// The whole list lives under one key, so a write from memory would carry away
		// whatever the other tab had added since this one loaded.
		const { recent, store } = await load(JSON.stringify([{ q: 'first', at: Date.now() }]));
		store.map.set(KEY, JSON.stringify([{ q: 'from another tab', at: Date.now() }]));
		recent.record('mine');
		expect(stored(store).map((e: { q: string }) => e.q)).toEqual(['mine', 'from another tab']);
	});

	it('keeps at most 25 searches', async () => {
		const { recent, store } = await load();
		for (let i = 0; i < 30; i++) recent.record(`query ${i}`);
		expect(stored(store)).toHaveLength(25);
		expect(stored(store)[0].q).toBe('query 29');
	});
});

describe('recent, when storage misbehaves', () => {
	it('drops entries older than a month on the way in', async () => {
		const { recent } = await load(
			JSON.stringify([
				{ q: 'recent enough', at: Date.now() - 29 * day },
				{ q: 'too old', at: Date.now() - 31 * day }
			])
		);
		expect(recent.matching('too', 2)).toEqual([]);
		expect(recent.matching('recent', 2)).toEqual(['recent enough']);
	});

	it('treats malformed storage as no history', async () => {
		for (const seed of ['not json', '{"not":"an array"}', '[{"q":1,"at":"nope"}]', '[null]']) {
			const { recent } = await load(seed);
			expect(recent.matching('rust', 2), seed).toEqual([]);
		}
	});

	it('reads as no history when storage refuses to be read', async () => {
		// A private window, or a browser set to block site data. Costs the reader a
		// convenience and never an error.
		vi.resetModules();
		const store = storage();
		store.throwOnRead = true;
		vi.stubGlobal('localStorage', store);
		const { recent } = await import('./recent.svelte');
		expect(recent.matching('rust', 2)).toEqual([]);
	});

	it('keeps this session going when a write is refused', async () => {
		const { recent, store } = await load();
		store.throwOnWrite = true;
		expect(() => recent.record('rust ownership')).not.toThrow();
		// Still offered in this session, even though nothing reached storage.
		expect(recent.matching('rust', 2)).toEqual(['rust ownership']);
	});
});
