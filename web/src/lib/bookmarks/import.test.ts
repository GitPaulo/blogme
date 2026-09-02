import { describe, expect, it } from 'vitest';
import { toExport } from './export';
import { readFile } from './import';
import type { Bookmark } from './db';

// A file the reader picked is third-party data even when this app wrote it: it may have
// been hand-edited, truncated, or produced by another version. So every record is rebuilt
// rather than trusted, and one that cannot be rebuilt is dropped instead of taking the
// whole file down with it. The round trip at the bottom is the one that matters most —
// an export this app cannot read back is a collection the reader has lost.

const saved = (over: Partial<Bookmark> = {}): Bookmark => ({
	url: 'https://example.com/post',
	title: 'A post',
	savedAt: Date.parse('2026-01-15T12:00:00Z'),
	...over
});

/** `readFile` reads `size` and `text()`, which a real File gives it. */
const file = (body: unknown) =>
	new File([typeof body === 'string' ? body : JSON.stringify(body)], 'export.json', {
		type: 'application/json'
	});

const envelope = (bookmarks: unknown[]) => ({
	format: 'blogme/bookmarks',
	version: 1,
	exportedAt: '2026-01-15T12:00:00Z',
	count: bookmarks.length,
	bookmarks
});

describe('readFile, on a file it can use', () => {
	it('reads back what this app exported, unchanged', async () => {
		const items = [
			saved({ url: 'https://a.example/1', title: 'One', topics: ['rust'] }),
			saved({ url: 'https://a.example/2', title: 'Two', author: 'Someone' })
		];
		expect(await readFile(file(toExport(items)))).toEqual(items);
	});

	it('drops a record with no usable url rather than the whole file', async () => {
		const records = await readFile(
			file(
				envelope([
					{ url: 'https://good.example/a', title: 'Kept' },
					{ url: 'javascript:alert(1)', title: 'Dropped' },
					{ title: 'No url at all' },
					'not an object'
				])
			)
		);
		expect(records.map((r) => r.url)).toEqual(['https://good.example/a']);
	});

	it('falls back to the url when a record has no title', async () => {
		const records = await readFile(file(envelope([{ url: 'https://a.example/1' }])));
		expect(records[0].title).toBe('https://a.example/1');
	});

	it('keeps the later save when one url appears twice', async () => {
		const records = await readFile(
			file(
				envelope([
					{ url: 'https://a.example/1', title: 'Older', savedAt: '2026-01-01T00:00:00Z' },
					{ url: 'https://a.example/1', title: 'Newer', savedAt: '2026-02-01T00:00:00Z' }
				])
			)
		);
		expect(records).toHaveLength(1);
		expect(records[0].title).toBe('Newer');
	});

	it('treats an unreadable savedAt as saved just now', async () => {
		const before = Date.now();
		const records = await readFile(file(envelope([{ url: 'https://a.example/1', savedAt: 'x' }])));
		expect(records[0].savedAt).toBeGreaterThanOrEqual(before);
	});

	it('refuses a date so far out that the next export would throw on it', async () => {
		const records = await readFile(file(envelope([{ url: 'https://a.example/1', savedAt: 1e18 }])));
		expect(() => new Date(records[0].savedAt).toISOString()).not.toThrow();
	});

	it('holds an imported record to the same caps a searched one gets', async () => {
		const records = await readFile(
			file(
				envelope([
					{
						url: 'https://a.example/1',
						title: 'x'.repeat(1_000),
						topics: Array.from({ length: 40 }, (_, i) => `topic-${i}`)
					}
				])
			)
		);
		expect(records[0].title).toHaveLength(300);
		expect(records[0].topics).toHaveLength(12);
	});

	it('ignores fields a later version added rather than refusing the file', async () => {
		const records = await readFile(
			file(envelope([{ url: 'https://a.example/1', somethingNew: true }]))
		);
		expect(records).toHaveLength(1);
	});
});

describe('readFile, on a file it cannot use', () => {
	const rejects = (body: unknown, message: RegExp) =>
		expect(readFile(file(body))).rejects.toThrow(message);

	it('says so when the file is not JSON', () => rejects('not json at all', /not JSON/i));

	it('says so when the file is not one of ours', () =>
		rejects({ format: 'someone/else', bookmarks: [] }, /not a blogme/i));

	it('says so when the file came from a newer version', () =>
		rejects({ ...envelope([]), version: 99 }, /newer version/i));

	it('says so when nothing in it can be read', () =>
		rejects(envelope([{ url: 'javascript:alert(1)' }]), /no bookmarks/i));

	it('refuses a file too large to be an export before loading it', async () => {
		const huge = new File(['x'], 'huge.json');
		Object.defineProperty(huge, 'size', { value: 9_000_000 });
		await expect(readFile(huge)).rejects.toThrow(/too large/i);
	});
});
