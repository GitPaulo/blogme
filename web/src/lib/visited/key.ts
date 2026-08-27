/**
 * A visit is stored under a hash of the url rather than under the url itself.
 *
 * The hash is a fixed eight bytes where a url averages nearer eighty, which is what keeps
 * a history of hundreds of thousands of reads small enough to sit on a phone and leaves
 * the index comparing numbers rather than strings. It also keeps a reading history off
 * disk in plain text: obfuscation rather than encryption, since a guessed url can still
 * be tested, but the list cannot simply be read out of storage.
 *
 * The cost is collisions. cyrb53 spreads urls over 53 bits, so even a history filled to
 * the 250,000 record cap has roughly a one in 300,000 chance of holding a single
 * colliding pair, and the damage when it does is one link wearing a pill it did not earn.
 * That trade is only available because the mark is cosmetic; a bookmark, where a
 * collision would lose a saved post, is keyed by its url.
 */

/**
 * Parameters that describe where a click came from rather than which page it lands on.
 * Kept conservative, because a blog is free to route on anything not listed here.
 */
const TRACKING = /^(utm_[a-z]+|mc_[ce]id|fbclid|gclid|igshid|mkt_tok|ref|ref_src)$/i;

/** The url reduced to the article it points at: one page, one key, however it was linked. */
function canonical(url: string): string {
	let parsed: URL;
	try {
		parsed = new URL(url);
	} catch {
		return url.trim().toLowerCase(); // Not a url we can take apart, so hash it as it came.
	}

	// A fragment picks a place inside a page, not a different page.
	parsed.hash = '';
	for (const name of [...parsed.searchParams.keys()]) {
		if (TRACKING.test(name)) parsed.searchParams.delete(name);
	}
	// ?a=1&b=2 and ?b=2&a=1 are one page written two ways.
	parsed.searchParams.sort();

	// The scheme is dropped rather than normalised, because a blog served over both http
	// and https is one blog. A trailing slash and a leading www. go for the same reason.
	// The path keeps its case: hosts are case-insensitive and paths are not.
	const host = parsed.host.replace(/^www\./, '');
	return `${host}${parsed.pathname.replace(/\/+$/, '')}${parsed.search}`;
}

/**
 * cyrb53. Fifty-three bits is exactly what a JavaScript number holds without losing one,
 * so the key travels as an IndexedDB numeric key rather than boxed into a string.
 */
function hash(value: string): number {
	let h1 = 0xdeadbeef;
	let h2 = 0x41c6ce57;
	for (let i = 0; i < value.length; i++) {
		const ch = value.charCodeAt(i);
		h1 = Math.imul(h1 ^ ch, 2654435761);
		h2 = Math.imul(h2 ^ ch, 1597334677);
	}
	h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507);
	h1 ^= Math.imul(h2 ^ (h2 >>> 13), 3266489909);
	h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507);
	h2 ^= Math.imul(h1 ^ (h1 >>> 13), 3266489909);
	return 4294967296 * (2097151 & h2) + (h1 >>> 0);
}

/**
 * Keys already worked out. `visited.has` is called while rendering, once per row and
 * again for every filter pass, so the same handful of urls is converted over and over:
 * parsing a url, sorting its query and hashing it costs around 200 times what reading
 * the answer back does. Bounded because the urls are third-party and a long session of
 * paging would otherwise keep every one it ever saw.
 */
const keys = new Map<string, number>();
const MAX_KEYS = 5_000;

export function visitKey(url: string): number {
	const cached = keys.get(url);
	if (cached !== undefined) return cached;

	const key = hash(canonical(url));
	if (keys.size >= MAX_KEYS) {
		// Insertion order is oldest first, so the first entry is the one longest unused.
		const oldest = keys.keys().next();
		if (!oldest.done) keys.delete(oldest.value);
	}
	keys.set(url, key);
	return key;
}
