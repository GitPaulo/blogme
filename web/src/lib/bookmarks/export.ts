import type { Bookmark } from './db';

/**
 * Self-describing envelope so an exported file stays readable on its own and
 * can be identified on a future import.
 */
export const FORMAT = 'blogme/bookmarks';
export const VERSION = 1;

type ExportedBookmark = Omit<Bookmark, 'savedAt'> & { savedAt: string };

export type BookmarksExport = {
	format: typeof FORMAT;
	version: number;
	exportedAt: string;
	count: number;
	bookmarks: ExportedBookmark[];
};

export function toExport(items: Bookmark[], now = new Date()): BookmarksExport {
	return {
		format: FORMAT,
		version: VERSION,
		exportedAt: now.toISOString(),
		count: items.length,
		bookmarks: items.map(({ savedAt, ...rest }) => ({
			...rest,
			savedAt: new Date(savedAt).toISOString()
		}))
	};
}

export const filename = (now = new Date()) =>
	`blogme-bookmarks-${now.toISOString().slice(0, 10)}.json`;

export function download(items: Bookmark[]) {
	const now = new Date();
	// undefined fields are dropped by stringify, which keeps the file tidy.
	const blob = new Blob([JSON.stringify(toExport(items, now), null, 2)], {
		type: 'application/json'
	});
	const url = URL.createObjectURL(blob);
	const anchor = document.createElement('a');
	anchor.href = url;
	anchor.download = filename(now);
	// Firefox only follows the click when the anchor is in the document.
	anchor.hidden = true;
	document.body.append(anchor);
	anchor.click();
	anchor.remove();
	// Revoking too early cancels an in-flight download, so leave the blob a moment.
	setTimeout(() => URL.revokeObjectURL(url), 10_000);
}
