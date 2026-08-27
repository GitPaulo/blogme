import { SvelteSet } from 'svelte/reactivity';
import type { SearchResult } from '$lib/api';
import * as db from './db';

/**
 * Only the keys live in memory. That keeps membership checks O(1) for the
 * results list without deserialising every saved record, which is what would
 * otherwise get expensive at thousands of bookmarks.
 */
const saved = new SvelteSet<string>();

/** Keeps a runaway script or an accidental loop from filling the user's disk. */
const MAX_BOOKMARKS = 5_000;

/** What an import does with the bookmarks already saved. */
export type ImportMode = 'merge' | 'replace';

let ready = $state(false);
let error = $state('');

function toRecord(result: SearchResult): db.Bookmark {
	return {
		url: result.url,
		title: result.title,
		author: result.author,
		summary: result.summary,
		topics: result.topics ? [...result.topics] : undefined,
		publishedAt: result.publishedAt,
		savedAt: Date.now()
	};
}

export const bookmarks = {
	get ready() {
		return ready;
	},
	get count() {
		return saved.size;
	},
	get error() {
		return error;
	},

	has(url: string) {
		return saved.has(url);
	},

	async load() {
		if (ready) return;
		try {
			for (const key of await db.keys()) saved.add(key);
		} catch {
			error = 'Bookmarks are unavailable in this browser.';
		} finally {
			ready = true;
		}
	},

	async toggle(result: SearchResult) {
		const url = result.url;
		const wasSaved = saved.has(url);

		if (!wasSaved && saved.size >= MAX_BOOKMARKS) {
			error = `You can save up to ${MAX_BOOKMARKS} bookmarks. Remove some to add more.`;
			return;
		}

		// Applied up front so the button responds on the same frame as the click.
		if (wasSaved) saved.delete(url);
		else saved.add(url);

		try {
			error = '';
			if (wasSaved) await db.remove(url);
			else await db.put(toRecord(result));
		} catch {
			if (wasSaved) saved.add(url);
			else saved.delete(url);
			error = 'Could not save your change.';
		}
	},

	async remove(url: string) {
		if (!saved.has(url)) return;
		saved.delete(url);
		try {
			error = '';
			await db.remove(url);
		} catch {
			saved.add(url);
			error = 'Could not save your change.';
		}
	},

	/**
	 * Takes in a parsed export. `merge` keeps what is already saved and adds the rest;
	 * `replace` swaps the collection for the file. Resolves true once it has landed, and
	 * false when nothing was written; the reason is on `error`.
	 *
	 * Written before the in-memory keys are touched, unlike a single toggle: an import is
	 * too large to unpick, so it is the store that has to agree it landed.
	 */
	async importAll(records: db.Bookmark[], mode: ImportMode) {
		// Deduplicated against the collection rather than overwriting it: a post saved
		// here already is the same post, and its own savedAt is the honest one.
		const incoming =
			mode === 'merge' ? records.filter((record) => !saved.has(record.url)) : records;
		const total = mode === 'merge' ? saved.size + incoming.length : incoming.length;
		if (total > MAX_BOOKMARKS) {
			error = `That would take you past ${MAX_BOOKMARKS} bookmarks. Remove some, or import a smaller file.`;
			return false;
		}

		try {
			error = '';
			if (mode === 'replace') await db.replaceAll(records);
			else await db.putAll(incoming);
		} catch {
			error = 'Could not save your change.';
			return false;
		}

		if (mode === 'replace') saved.clear();
		for (const record of records) saved.add(record.url);
		return true;
	},

	async clear() {
		if (saved.size === 0) return;
		// The keys are cheap to hold and are the only way back if the write fails.
		const previous = [...saved];
		saved.clear();
		try {
			error = '';
			await db.clear();
		} catch {
			for (const url of previous) saved.add(url);
			error = 'Could not save your change.';
		}
	},

	list: db.all
};
