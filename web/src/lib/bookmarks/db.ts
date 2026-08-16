/**
 * IndexedDB layer for bookmarks.
 *
 * IndexedDB over localStorage because writes are per-record rather than a
 * rewrite of the whole collection, so saving stays O(1) at thousands of
 * bookmarks and never blocks the main thread.
 */
export type Bookmark = {
	url: string;
	title: string;
	author?: string;
	summary?: string;
	topics?: string[];
	publishedAt?: string;
	savedAt: number;
};

const DB_NAME = 'blogme';
const DB_VERSION = 1;
const STORE = 'bookmarks';
const SAVED_AT = 'savedAt';

let connection: Promise<IDBDatabase> | undefined;

function open(): Promise<IDBDatabase> {
	connection ??= new Promise<IDBDatabase>((resolve, reject) => {
		const request = indexedDB.open(DB_NAME, DB_VERSION);
		request.onupgradeneeded = () => {
			const store = request.result.createObjectStore(STORE, { keyPath: 'url' });
			store.createIndex(SAVED_AT, SAVED_AT);
		};
		request.onsuccess = () => resolve(request.result);
		request.onerror = () => reject(request.error);
	}).catch((e) => {
		connection = undefined; // Let a later call retry instead of caching the failure.
		throw e;
	});
	return connection;
}

async function run<T>(
	mode: IDBTransactionMode,
	fn: (store: IDBObjectStore) => IDBRequest<T>
): Promise<T> {
	const db = await open();
	return new Promise<T>((resolve, reject) => {
		const tx = db.transaction(STORE, mode);
		const request = fn(tx.objectStore(STORE));
		request.onsuccess = () => resolve(request.result);
		tx.onerror = () => reject(tx.error);
		tx.onabort = () => reject(tx.error);
	});
}

export const put = (bookmark: Bookmark) => run('readwrite', (store) => store.put(bookmark));

export const remove = (url: string) => run('readwrite', (store) => store.delete(url));

export const clear = () => run('readwrite', (store) => store.clear());

/** Keys only, so the common case never deserialises full records. */
export const keys = () => run('readonly', (store) => store.getAllKeys()) as Promise<string[]>;

export async function all(): Promise<Bookmark[]> {
	const ascending = await run<Bookmark[]>('readonly', (store) => store.index(SAVED_AT).getAll());
	return ascending.reverse();
}
