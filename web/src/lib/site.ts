/**
 * What a result card can say about the blog a post came from without the index carrying
 * a byte more than it already does.
 *
 * Everything here is derived from the article url. That is the whole point: the index is
 * capped at one partition and every field costs bytes on every document, so a card that
 * wants to show where a post came from either works this out in the browser or does not
 * happen. See the note on og:image at the bottom of this file for the one piece of
 * metadata that was priced and left out.
 */

/**
 * The blog a url belongs to, as a reader would name it: the hostname without the `www.`
 * that says nothing.
 *
 * Undefined rather than a best effort for anything that is not an http(s) page — a
 * `mailto:`, a `javascript:`, a string that is not a url at all. Callers show nothing in
 * that case, which is the honest answer: there is no site to name, and a card that
 * printed the raw string would be showing the reader a url it had just failed to parse.
 *
 * The hostname is left in whichever form `URL` hands back, which for an internationalised
 * domain is punycode — `xn--80ak6aa92e.com` rather than the Cyrillic that renders as
 * `apple.com`. Uglier, and deliberately so: a homograph is the one case where the prettier
 * string is the misleading one, and this label sits directly under a link.
 */
export function hostOf(url: string): string | undefined {
	let parsed: URL;
	try {
		parsed = new URL(url);
	} catch {
		return undefined;
	}
	if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') return undefined;
	// Already lowercased by the parser, which matters more than it looks: this string is
	// the key for the hue and for the missing-icon registry, so two spellings of one host
	// would be two sites wearing two colours.
	return parsed.hostname.replace(/^www\./, '');
}

/**
 * Where to ask a site for its icon.
 *
 * Its own origin, over https whatever the article was linked as. Not one of the icon
 * services that will fetch it for you: those would be told every blog in every set of
 * results this reader is looking at, and a search engine handing a reader's results to a
 * third party for a 16-pixel image is a bad trade at any price. The cost of doing it
 * ourselves is that a site with no `/favicon.ico` and only a `<link rel=icon>` in its
 * markup gets the fallback below — we cannot read its markup from here — and a blog still
 * on plain http gets it too. Both land somewhere reasonable rather than broken.
 */
export function faviconUrl(host: string): string {
	return `https://${host}/favicon.ico`;
}

/**
 * The letter to draw when there is no icon. The host's first letter or digit, which for
 * `blog.cloudflare.com` is `B` — the subdomain, not the brand, because picking the
 * "real" part of a hostname needs the public suffix list and this is a fallback.
 *
 * A host that is all punctuation or punycode markers has no letter worth showing, so it
 * gets a dot: still a tile of the right size and colour, and no reader will mistake it
 * for an initial.
 */
export function monogram(host: string): string {
	const letter = /\p{Letter}|\p{Number}/u.exec(host)?.[0];
	return letter ? letter.toUpperCase() : '·';
}

/**
 * A hue for a site's fallback tile, so two blogs in one list of results are told apart by
 * more than their initial — and so one blog wears the same colour every time it appears,
 * across searches and sessions, without anything being stored.
 *
 * Any stable spread of a string over the wheel would do. This is the mixing half of the
 * cyrb53 already used for [visit keys](visited/key.ts), which is cheap and, unlike a sum
 * of character codes, does not put `blog.rust-lang.org` and `blog.golang.org` on
 * neighbouring hues for having similar letters.
 */
export function hueFor(host: string): number {
	let h = 0xdeadbeef;
	for (let i = 0; i < host.length; i++) h = Math.imul(h ^ host.charCodeAt(i), 2654435761);
	h ^= h >>> 15;
	return (h >>> 0) % 360;
}

// Not here: og:image. A thumbnail per card is the obvious next thing to want and it is
// two separate costs, either of which alone rules it out at this size.
//
// The index would carry a url per document — call it 90 bytes of the 15 GiB one Basic
// partition holds — for a field nothing searches or filters on, spent on every document
// forever to decorate the twenty on screen. And the results page would then load an image
// from each of twenty third-party origins, which is twenty handshakes on a list that
// currently renders from one response, and hands each of those blogs the shape of what
// this reader is searching for.
//
// The favicon above has neither problem: it is derived, not stored, and it is one small
// cached request per *site* rather than per post.
