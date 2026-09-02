/**
 * Reading a bookmarks export back in.
 *
 * A file the reader picked is third-party data even when this app wrote it: it may have
 * been hand-edited, truncated, or produced by another version. Every field is therefore
 * rebuilt rather than trusted, the same way a search response is in $lib/api. A record
 * that cannot be rebuilt is dropped instead of taking the file down with it.
 */
import { safeHttpUrl } from '$lib/api';
import { CAPS, text, topics } from '$lib/sanitise';
import type { Bookmark } from './db';
import { FORMAT, VERSION } from './export';

/** A 5,000 bookmark export runs to about 3MB, so this leaves room and still bounds the read. */
const MAX_FILE_BYTES = 8_000_000;
/** The furthest a Date can travel; past it toISOString throws and breaks the next export. */
const MAX_TIME = 8.64e15;

/** The export writes an ISO string. Anything unreadable is treated as saved just now. */
function time(value: unknown): number {
	const parsed =
		typeof value === 'string' ? Date.parse(value) : typeof value === 'number' ? value : NaN;
	return Number.isFinite(parsed) && Math.abs(parsed) <= MAX_TIME ? parsed : Date.now();
}

function toBookmark(value: unknown): Bookmark | undefined {
	if (typeof value !== 'object' || value === null) return undefined;
	const raw = value as Record<string, unknown>;

	// Only plain web links, because these end up as hrefs in the drawer.
	const url = safeHttpUrl(raw.url);
	if (!url) return undefined;

	return {
		url,
		title: text(raw.title, CAPS.title) ?? url,
		author: text(raw.author, CAPS.author),
		summary: text(raw.summary, CAPS.summary),
		topics: topics(raw.topics),
		publishedAt: text(raw.publishedAt, CAPS.date),
		savedAt: time(raw.savedAt)
	};
}

/**
 * The readable bookmarks in an export, deduplicated by url. Throws a sentence worth
 * showing the reader when the file is not one of ours or holds nothing we can use.
 */
function parseExport(source: string): Bookmark[] {
	let body: unknown;
	try {
		body = JSON.parse(source);
	} catch {
		throw new Error('That file is not JSON.');
	}

	const raw = (typeof body === 'object' && body !== null ? body : {}) as Record<string, unknown>;
	if (raw.format !== FORMAT) throw new Error('That file is not a blogme bookmarks export.');
	// Unknown fields are ignored rather than fatal, so a file from a later version still
	// imports what this one understands, but it is worth saying where it came from.
	if (typeof raw.version === 'number' && raw.version > VERSION) {
		throw new Error('That file was written by a newer version of blogme.');
	}

	// Keyed by url, because the store is: the same post twice in a file is one bookmark,
	// and the later save is the one worth keeping.
	const byUrl = new Map<string, Bookmark>();
	for (const entry of Array.isArray(raw.bookmarks) ? raw.bookmarks : []) {
		const bookmark = toBookmark(entry);
		if (!bookmark) continue;
		const existing = byUrl.get(bookmark.url);
		if (!existing || existing.savedAt < bookmark.savedAt) byUrl.set(bookmark.url, bookmark);
	}

	if (byUrl.size === 0) throw new Error('That file holds no bookmarks we can read.');
	return [...byUrl.values()];
}

/** Reads a picked file, refusing one too large to be an export before it is loaded. */
export async function readFile(file: File): Promise<Bookmark[]> {
	if (file.size > MAX_FILE_BYTES) throw new Error('That file is too large to be an export.');
	return parseExport(await file.text());
}
