import { SvelteMap } from 'svelte/reactivity';
import * as db from './db';
import { visitKey } from './key';

/**
 * What is known about the links on screen, never about the whole history. A history of
 * hundreds of thousands of urls is too large to hold in memory and too slow to read at
 * startup, and nothing on screen ever needs more than the handful of links in front of
 * the reader — so a lookup costs what is rendered, not what is stored.
 *
 * A SvelteMap so a caller re-renders when the answer it asked for lands, and only then.
 */
const answers = new SvelteMap<number, boolean>();

/** Enough for a long session of paging. Anything evicted is simply asked for again. */
const MAX_ANSWERS = 4_000;
/** Keeps a runaway script or an accidental loop from filling the user's disk. */
const MAX_VISITS = 250_000;
/** Trimmed back well past the cap, so eviction runs once in a long while, not per open. */
const TRIM_TO = 200_000;
const CHANNEL = 'blogme/visited';

/** Asked about but not yet looked up, collected so one render costs one transaction. */
const asked = new Set<number>();
let scheduled = false;

/** Records on disk: counted once a session and tracked from there, -1 until asked. */
let stored = -1;

let channel: BroadcastChannel | undefined;

function lookup() {
	if (scheduled) return;
	scheduled = true;
	// A microtask, so every link a single render asks about is served by one transaction.
	queueMicrotask(async () => {
		scheduled = false;
		const keys = [...asked];
		asked.clear();

		let hits = new Set<number>();
		try {
			hits = new Set(await db.known(keys));
		} catch {
			// Storage is unavailable, so nothing is on record. The misses are still cached
			// below, which is what keeps this from being asked again on the next render.
		}
		// Only what is still unknown: an open recorded while this lookup was in flight is
		// the newer answer, and the store it was written to may not have committed yet.
		for (const key of keys) if (!answers.has(key)) answers.set(key, hits.has(key));

		// Insertion order is ask order, and what is on screen was asked about last, so
		// what goes here is what scrolled away.
		const over = answers.size - MAX_ANSWERS;
		if (over > 0) for (const key of [...answers.keys()].slice(0, over)) answers.delete(key);
	});
}

async function record(key: number) {
	try {
		await db.mark(key);
	} catch {
		return; // Cosmetic, and this session keeps its own answer either way.
	}

	// count() walks the store on some engines, so it is asked once a session and tracked
	// from there rather than run after every open.
	if (stored < 0) stored = await db.count().catch(() => 0);
	else stored += 1;
	if (stored <= MAX_VISITS) return;

	// The tracked figure counts re-opens that only overwrote a record, so the store gets
	// the last word before anything is deleted.
	stored = await db.count().catch(() => 0);
	if (stored <= MAX_VISITS) return;
	await db.trim(stored - TRIM_TO).catch(() => {});
	stored = TRIM_TO;
}

function mark(url: string) {
	const key = visitKey(url);
	// Optimistic and never rolled back: the reader opened the article, so the mark is
	// true whether or not the write lands. Unlike a bookmark, nothing is lost by being
	// right for one session only, which is why a failure is not worth reporting.
	answers.set(key, true);
	channel?.postMessage(key);
	void record(key);
}

export const visited = {
	/**
	 * Whether this article has been opened from here before. Synchronous, because it is
	 * read while rendering: an answer that is not cached yet reads as unvisited and asks
	 * for itself, and the caller re-renders a frame or two later with the real one.
	 */
	has(url: string): boolean {
		const key = visitKey(url);
		const answer = answers.get(key);
		if (answer !== undefined) return answer;
		asked.add(key);
		lookup();
		return false;
	},

	/**
	 * Records an open for every `data-visit` anchor on the page, and keeps other tabs of
	 * the app in step. Delegated to the document, so a page of results costs one listener
	 * rather than one per row. Mounted once, from the layout.
	 */
	track() {
		const onOpen = (event: MouseEvent) => {
			// Something up the tree cancelled the navigation, so nothing was opened.
			if (event.defaultPrevented) return;
			// Left and middle click both open an article; a right click only offers to.
			if (event.button !== 0 && event.button !== 1) return;
			const node = event.target instanceof Element ? event.target : undefined;
			const anchor = node?.closest('a[data-visit]') as HTMLAnchorElement | null | undefined;
			if (anchor) mark(anchor.href);
		};

		// click covers a left click, a modified click and Enter on a focused link; middle
		// click opens a tab without ever firing one and arrives as auxclick instead.
		document.addEventListener('click', onOpen);
		document.addEventListener('auxclick', onOpen);

		// Two tabs of the app share one history, so an open in either shows in both.
		if (typeof BroadcastChannel !== 'undefined') {
			channel = new BroadcastChannel(CHANNEL);
			channel.onmessage = (event) => {
				if (typeof event.data === 'number') answers.set(event.data, true);
			};
		}

		return () => {
			document.removeEventListener('click', onOpen);
			document.removeEventListener('auxclick', onOpen);
			channel?.close();
			channel = undefined;
		};
	}
};
