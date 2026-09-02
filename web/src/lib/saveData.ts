/**
 * Whether this browser has been told to spend as little data as possible.
 *
 * Two things read it, and both answer by not spending: a site icon is decoration, so
 * every blog wears its lettered tile instead; a link preview is a whole third-party
 * document load, so the panel never installs itself at all.
 *
 * Named to sit beside `prefersReducedMotion` from `svelte/motion`, which the same
 * components read for the same kind of decision.
 *
 * see: https://developer.mozilla.org/en-US/docs/Web/API/NetworkInformation/saveData
 */

/** Not in the DOM types, and not in every browser. */
type WithConnection = Navigator & { connection?: { saveData?: boolean } };

/**
 * False where the question cannot be asked: no `navigator` during prerendering, and no
 * `connection` on browsers that do not implement it. Only an explicit `true` is a yes,
 * so an absent answer never quietly turns features off.
 */
export function prefersReducedData(): boolean {
	if (typeof navigator === 'undefined') return false;
	return (navigator as WithConnection).connection?.saveData === true;
}
