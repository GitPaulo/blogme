/**
 * How wide an element currently is, in CSS pixels.
 *
 * Observed rather than derived from the viewport, because the two are not the same
 * question: the caller wants the width its own content has to run in, and that is the
 * viewport minus every container and gutter above it. Watching the element keeps the
 * answer right when any of those change.
 *
 * Takes a getter rather than the element itself, so it can be wired to a `bind:this`
 * that is still undefined when this is called. Like any rune, call it while the
 * component is initialising. An absent element measures 0, which callers read as "not
 * measured yet" rather than as a real width.
 */
export function elementWidth(target: () => HTMLElement | undefined) {
	let width = $state(0);

	$effect(() => {
		const element = target();
		if (!element) {
			width = 0;
			return;
		}

		// Read once here rather than waiting for the observer's first callback, which is
		// delivered on the next frame and never at all while the tab is hidden. A caller
		// sizing content to this would otherwise render its fallback and stay there for
		// as long as nobody looked at the page.
		// Padding comes off because clientWidth includes it and contentRect below does
		// not, and a width that changed the moment the observer caught up would be worse
		// than no width at all.
		const padding = getComputedStyle(element);
		width =
			element.clientWidth - parseFloat(padding.paddingLeft) - parseFloat(padding.paddingRight);

		// contentRect is the box the content itself runs in, padding and border excluded.
		const observer = new ResizeObserver(([entry]) => (width = entry.contentRect.width));
		observer.observe(element);
		return () => observer.disconnect();
	});

	return {
		get current() {
			return width;
		}
	};
}
