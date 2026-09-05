import { prefersReducedData } from '$lib/saveData';

/**
 * Whether the hover preview is worth having on this device at all.
 *
 * A preview costs a whole third-party document load and is reached by resting a pointer
 * on a link, so a device with no pointer to rest can never ask for one, and a reader who
 * asked for less data has already said not to spend it on this.
 *
 * Read in two places and they have to agree: the layout asks before it fetches the panel,
 * and the panel asks again before it installs its listeners. Either could be dropped and
 * the other would still be correct — the pair is here so that the fetch and the install
 * can never disagree about whether this reader gets previews.
 */
export function previewable(): boolean {
	if (typeof window === 'undefined') return false;
	return window.matchMedia('(hover: hover) and (pointer: fine)').matches && !prefersReducedData();
}
