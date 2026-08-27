/**
 * IndexedDB layer for visited articles.
 *
 * A separate database from bookmarks, not another store inside it: a history is
 * disposable where a bookmark is not, so it evicts, clears and fails on its own without
 * a version bump that would have to be agreed with the collection worth keeping.
 */
import { database } from '$lib/idb';

/** A key and the moment that article was last opened. No url, no title: see key.ts. */
type Visit = { key: number; at: number };

const STORE = 'visited';
const AT = 'at';
/** Deleted in chunks, so a long write never sits in the queue in front of a lookup. */
const TRIM_CHUNK = 5_000;

const db = database('blogme-visited', 1, [{ name: STORE, keyPath: 'key', index: AT }]);

export const mark = (key: number) =>
	db.request(STORE, 'readwrite', (store) => store.put({ key, at: Date.now() } satisfies Visit));

/**
 * The subset of `keys` already on record. One transaction for the whole batch, because
 * the round trip is the expensive part and the lookups themselves are B-tree descents:
 * a page of links costs one transaction whether the history holds a hundred records or
 * half a million.
 */
export const known = (keys: number[]) =>
	db.transact(STORE, 'readonly', (store) => {
		const hits: number[] = [];
		for (const key of keys) {
			// getKey rather than get: presence is the whole answer, and it saves
			// deserialising a record we would throw away.
			const lookup = store.getKey(key);
			lookup.onsuccess = () => {
				if (lookup.result !== undefined) hits.push(key);
			};
		}
		return hits;
	});

export const count = () => db.request(STORE, 'readonly', (store) => store.count());

/** Drops the `excess` least recently opened records. */
export async function trim(excess: number): Promise<void> {
	while (excess > 0) {
		const limit = Math.min(excess, TRIM_CHUNK);
		const chunk = await db.transact(STORE, 'readwrite', (store) => {
			const progress = { deleted: 0 };
			// Oldest first, and keys only: deleting a record never needs to read it.
			const cursor = store.index(AT).openKeyCursor();
			cursor.onsuccess = () => {
				const position = cursor.result;
				if (!position || progress.deleted >= limit) return;
				store.delete(position.primaryKey);
				progress.deleted++;
				position.continue();
			};
			return progress;
		});
		if (chunk.deleted === 0) return; // Emptied out from under us.
		excess -= chunk.deleted;
	}
}
