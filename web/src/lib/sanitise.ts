/**
 * Rebuilding a third-party record field by field.
 *
 * Two things arrive from outside this app and neither was written here: a search response,
 * which carries text the crawler took from blogs, and a bookmarks file the reader picked,
 * which this app may have written but which can still come back truncated, hand-edited or
 * from another version. Both are rebuilt rather than trusted.
 *
 * The caps live here rather than beside either reader, so an imported record can hold
 * nothing a searched one could not — the two used to keep their own copies of the same
 * numbers, with a comment in one asking the other to be kept in step.
 */

/** How long each field of a record may be, in characters. */
export const CAPS = {
	title: 300,
	author: 120,
	summary: 2_000,
	/** An ISO timestamp with room to spare. Anything longer is not one. */
	date: 40,
	topic: 64,
	/** How many topics one record may carry. */
	topics: 12
} as const;

/**
 * A trimmed string of at most `max` characters, or undefined for anything that is not
 * one. Undefined rather than an empty string, so a caller can leave the field off
 * entirely instead of rendering a blank.
 */
export function text(value: unknown, max: number): string | undefined {
	if (typeof value !== 'string') return undefined;
	const trimmed = value.trim().slice(0, max);
	return trimmed || undefined;
}

/**
 * The readable topics on a record, in the order they arrived.
 *
 * Deduplicated because topics are keyed in the markup and nothing in the index stops a
 * document repeating one — two rows sharing a key is an error rather than a repeat on
 * screen. Undefined for a record with none, so the tag row can be left off.
 */
export function topics(value: unknown): string[] | undefined {
	if (!Array.isArray(value)) return undefined;

	const found = new Set<string>();
	for (const entry of value) {
		const topic = text(entry, CAPS.topic);
		if (topic !== undefined) found.add(topic);
		if (found.size === CAPS.topics) break;
	}
	return found.size > 0 ? [...found] : undefined;
}
