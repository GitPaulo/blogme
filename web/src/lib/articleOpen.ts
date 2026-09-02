/**
 * Noticing that a reader opened an article.
 *
 * Two listeners want to know. The visited store marks the link as read; the page records
 * the search that led to it, because most searches here are typed, read and clicked
 * without Enter ever being pressed, and a history that only counted submissions would
 * stay empty for most readers.
 *
 * Both did the same three checks and registered the same pair of events, in two files,
 * with a comment in each pointing at the other. This is that, once.
 */

/**
 * The opt-in an article link carries. A link without it is not an article — the wordmark,
 * a button, a blog's own name on the landing page — so opening one counts as nothing.
 */
const ARTICLE = 'a[data-visit]';

/** The anchor this event opened, or undefined where it opened nothing. */
export function openedArticle(event: MouseEvent): HTMLAnchorElement | undefined {
	// Something up the tree cancelled the navigation, so nothing was opened.
	if (event.defaultPrevented) return undefined;
	// Left and middle both open the article; a right click only offers to.
	if (event.button !== 0 && event.button !== 1) return undefined;
	if (!(event.target instanceof Element)) return undefined;

	// Narrowed rather than asserted, as elsewhere: `closest` is only typed by its selector
	// for a bare tag name, and this one carries an attribute.
	const anchor = event.target.closest(ARTICLE);
	return anchor instanceof HTMLAnchorElement ? anchor : undefined;
}

/**
 * Calls `onopen` whenever an article inside `root` is opened, and returns the detach.
 *
 * Delegated rather than bound per row, so a page of twenty results costs one listener
 * rather than twenty. `click` covers a left click, a modified click and Enter on a
 * focused link; a middle click opens a tab without ever firing one and arrives as
 * `auxclick` instead, which is the half that is easy to miss.
 */
export function onArticleOpen(
	root: Document | HTMLElement,
	onopen: (anchor: HTMLAnchorElement) => void
): () => void {
	const listener = (event: Event) => {
		if (!(event instanceof MouseEvent)) return;
		const anchor = openedArticle(event);
		if (anchor) onopen(anchor);
	};

	root.addEventListener('click', listener);
	root.addEventListener('auxclick', listener);
	return () => {
		root.removeEventListener('click', listener);
		root.removeEventListener('auxclick', listener);
	};
}
