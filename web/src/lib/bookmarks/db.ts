/**
 * IndexedDB layer for bookmarks.
 *
 * IndexedDB over localStorage because writes are per-record rather than a
 * rewrite of the whole collection, so saving stays O(1) at thousands of
 * bookmarks and never blocks the main thread.
 */
import { database } from '$lib/idb';

export type Bookmark = {
	url: string;
	title: string;
	author?: string;
	summary?: string;
	topics?: string[];
	publishedAt?: string;
	savedAt: number;
};

const STORE = 'bookmarks';
const SAVED_AT = 'savedAt';

const db = database('blogme', 1, [{ name: STORE, keyPath: 'url', index: SAVED_AT }]);

export const put = (bookmark: Bookmark) =>
	db.request(STORE, 'readwrite', (store) => store.put(bookmark));

export const remove = (url: string) => db.request(STORE, 'readwrite', (store) => store.delete(url));

export const clear = () => db.request(STORE, 'readwrite', (store) => store.clear());

/** Keys only, so the common case never deserialises full records. */
export const keys = () =>
	db.request(STORE, 'readonly', (store) => store.getAllKeys()) as Promise<string[]>;

export async function all(): Promise<Bookmark[]> {
	const ascending = await db.request<Bookmark[]>(STORE, 'readonly', (store) =>
		store.index(SAVED_AT).getAll()
	);
	return ascending.reverse();
}
