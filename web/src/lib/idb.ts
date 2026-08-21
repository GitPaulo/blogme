/**
 * The IndexedDB plumbing the local stores share: one lazily opened connection per
 * database, and transactions that settle when they commit rather than when the requests
 * inside them succeed.
 */

export type StoreSpec = {
	name: string;
	keyPath: string;
	/** Single-field index, named after the field it orders by. */
	index?: string;
};

export type Database<Store extends string> = {
	/** One request, resolved with its result once the transaction commits. */
	request<T>(
		store: Store,
		mode: IDBTransactionMode,
		fn: (store: IDBObjectStore) => IDBRequest<T>
	): Promise<T>;
	/**
	 * Several requests in one transaction. `work` runs against the store straight away
	 * and returns the container its handlers fill; the promise hands that container back
	 * once the transaction commits, by which time every request in it has been served.
	 */
	transact<T>(
		store: Store,
		mode: IDBTransactionMode,
		work: (store: IDBObjectStore) => T
	): Promise<T>;
};

export function database<const Stores extends readonly StoreSpec[]>(
	name: string,
	version: number,
	stores: Stores
): Database<Stores[number]['name']> {
	let connection: Promise<IDBDatabase> | undefined;

	function open(): Promise<IDBDatabase> {
		connection ??= new Promise<IDBDatabase>((resolve, reject) => {
			const request = indexedDB.open(name, version);
			request.onupgradeneeded = () => {
				const db = request.result;
				// Guarded per store because this handler runs for every version bump, not only
				// the first: creating a store that is already there throws and takes the
				// upgrade — and the whole database — down with it.
				for (const spec of stores) {
					if (db.objectStoreNames.contains(spec.name)) continue;
					const store = db.createObjectStore(spec.name, { keyPath: spec.keyPath });
					if (spec.index) store.createIndex(spec.index, spec.index);
				}
			};
			request.onsuccess = () => {
				// A newer version opened in another tab stays blocked for as long as this
				// connection is held, so step aside rather than leave that tab waiting on us.
				request.result.onversionchange = () => request.result.close();
				resolve(request.result);
			};
			request.onerror = () => reject(request.error);
			// The mirror image: an older tab is holding this database at a lower version.
			// Failing beats hanging, because the caller can say so and a reload once that
			// tab is gone succeeds.
			request.onblocked = () => reject(new Error(`${name} is open in another tab`));
		}).catch((e) => {
			connection = undefined; // Let a later call retry instead of caching the failure.
			throw e;
		});
		return connection;
	}

	function transact<T>(
		store: string,
		mode: IDBTransactionMode,
		work: (store: IDBObjectStore) => T
	): Promise<T> {
		return open().then(
			(db) =>
				new Promise<T>((resolve, reject) => {
					const tx = db.transaction(store, mode);
					const result = work(tx.objectStore(store));
					// Settled on the transaction, not on the requests: a write whose request
					// succeeds can still be rolled back when the transaction fails to commit,
					// and callers undo their optimistic update from this promise.
					tx.oncomplete = () => resolve(result);
					tx.onerror = () => reject(tx.error);
					tx.onabort = () => reject(tx.error);
				})
		);
	}

	async function request<T>(
		store: string,
		mode: IDBTransactionMode,
		fn: (store: IDBObjectStore) => IDBRequest<T>
	): Promise<T> {
		let result!: T;
		await transact(store, mode, (target) => {
			const request = fn(target);
			request.onsuccess = () => (result = request.result);
		});
		return result;
	}

	return { request, transact };
}
