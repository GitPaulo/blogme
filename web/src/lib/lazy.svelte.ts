/**
 * A component fetched the first time something asks for it, rather than at page load.
 *
 * Everything on this site that is not the landing page — the result view, the bookmarks
 * drawer, the hover preview — was in the first chunk the browser had to parse, evaluate
 * and hydrate, and none of it draws a pixel until the reader does something. This is the
 * one way that work is put off, so there is a single pattern to read rather than an
 * `await import` spelled slightly differently at each site.
 *
 * `current` is undefined until the module lands, which is what the call sites guard on.
 * That is also what makes this safe to prerender: nothing calls `load` without an event,
 * so the server and the first client frame both render nothing and hydration matches.
 *
 * `load` returns its promise so a click can wait for a module that is not there yet —
 * see BookmarksPanel, where the drawer has to exist before it can be opened. Callers that
 * only want to warm the cache can ignore it. Calling it twice costs one request.
 *
 * Unlike the rest of `lib/*.svelte.ts` this holds no effect, so it does not have to be
 * called while a component is initialising — but it is, everywhere, because the thing it
 * hands back belongs to the component that renders it.
 */
export function lazy<T>(load: () => Promise<{ default: T }>) {
	// Raw, because a component is an opaque value to be handed straight to the renderer.
	// A deep proxy over it would be work spent wrapping something nothing ever reads into.
	let component = $state.raw<T | undefined>();
	let started: Promise<void> | undefined;

	return {
		get current() {
			return component;
		},
		load() {
			started ??= load().then(
				(module) => {
					component = module.default;
				},
				() => {
					// A chunk can fail to arrive — a connection that dropped, or a deploy that
					// replaced the file the cached page is still asking for. Forgetting the
					// attempt is what makes the next one a retry rather than a repeat of the
					// same answer: without this the first failure is the last word, and the
					// panel that could not be fetched can never be fetched again this visit.
					//
					// Swallowed rather than rethrown because most callers only warm the cache
					// and would have nowhere to put it. What the reader sees is the state they
					// were already in, and the way out is the thing they just did, again.
					started = undefined;
				}
			);
			return started;
		}
	};
}
