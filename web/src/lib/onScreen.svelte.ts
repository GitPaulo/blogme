/**
 * Whether an element is currently on screen.
 *
 * Asked of the browser rather than derived from a scroll position, because the answer
 * then survives the page growing underneath the element, which is exactly when the
 * elements this watches tend to move. An absent element reads as off screen.
 *
 * Takes a getter rather than the element itself, so it can be wired to a `bind:this`
 * that is still undefined when this is called. Like any rune, call it while the
 * component is initialising.
 */
export function onScreen(target: () => HTMLElement | undefined) {
	let visible = $state(false);

	$effect(() => {
		const element = target();
		if (!element) {
			visible = false;
			return;
		}

		const observer = new IntersectionObserver(([entry]) => (visible = entry.isIntersecting));
		observer.observe(element);
		return () => observer.disconnect();
	});

	return {
		get current() {
			return visible;
		}
	};
}
