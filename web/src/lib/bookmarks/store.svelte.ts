import { SvelteSet } from 'svelte/reactivity';
import type { SearchResult } from '$lib/api';
import * as db from './db';

/**
 * Only the keys live in memory. That keeps membership checks O(1) for the
 * results list without deserialising every saved record, which is what would
 * otherwise get expensive at thousands of bookmarks.
 */
const saved = new SvelteSet<string>();

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

	list: db.all
};
