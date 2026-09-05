/**
 * When the site icons are allowed to start fetching.
 *
 * The landing page names nine blogs before anyone has typed anything, and every one of
 * them wears its own favicon from its own origin. That is nine cross-origin connections
 * opened while the page is still painting, for decoration that is aria-hidden — they were
 * a fifth of the load and none of them tells the reader anything the host beside them
 * does not already say.
 *
 * So the icons wait. Nothing is lost by it: SiteIcon always draws the lettered tile
 * underneath and layers the icon on top, so the row a reader sees at first paint is the
 * same row a blog with no favicon keeps forever, and swapping one for the other changes
 * no box on the page. See lib/components/SiteIcon.svelte for why that layering exists.
 *
 * A shared flag rather than a per-component timer, because the answer is about the page
 * and not about any one icon: nine of them counting down separately would be nine ways to
 * get the same moment slightly wrong.
 *
 * Not gated behind `prefersReducedData` — that is SiteIcon's own decision, and it is the
 * stronger one: this delays an icon, that never asks for it at all.
 */

/**
 * Long enough that a busy page still gets its icons, short enough that nobody waits on
 * one. Only reached when the main thread never goes idle on its own.
 */
const IDLE_TIMEOUT = 2000;

let ready = $state(false);
/**
 * Plain, where `ready` is reactive, and that is the whole point of it: the guard in
 * `release` runs inside the caller's effect, and reading `ready` there would make the
 * effect depend on it — so releasing the icons would invalidate the effect that released
 * them and run the whole thing again for nothing.
 */
let released = false;

export const siteIcons = {
	get ready() {
		return ready;
	},

	/**
	 * Called once for the whole app, from the layout, beside the other things mounted for
	 * every page. Returns its own teardown, so it can be handed straight to an `$effect`.
	 *
	 * After `load` rather than after hydration: hydration finishing says the page is
	 * interactive, not that it has stopped fetching, and the connections these open are
	 * exactly what would compete with whatever is still in flight.
	 */
	release() {
		if (released) return;
		released = true;

		let idle = 0;
		let timer: ReturnType<typeof setTimeout> | undefined;

		const soon = () => {
			// An idle callback where there is one — Safari only shipped it in 17 — and a
			// bare timeout everywhere else. The point is to be after the load burst rather
			// than at any particular moment, so the fallback is as good as the real thing.
			if (typeof requestIdleCallback === 'function') {
				idle = requestIdleCallback(() => (ready = true), { timeout: IDLE_TIMEOUT });
			} else {
				timer = setTimeout(() => (ready = true), 0);
			}
		};

		// A page restored from the back/forward cache, or one this ran late on, has already
		// loaded and will never fire the event again.
		if (document.readyState === 'complete') soon();
		else window.addEventListener('load', soon, { once: true });

		return () => {
			// Put back, so this is a guard against a second caller rather than a one-shot the
			// app can never re-arm. Running again on a page that has already loaded settles on
			// the same answer immediately, which is what it should do.
			released = false;
			window.removeEventListener('load', soon);
			if (idle) cancelIdleCallback(idle);
			clearTimeout(timer);
		};
	}
};
