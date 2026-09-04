/**
 * Whether an element laid over the page has anything painted underneath it.
 *
 * For marks that float above the document rather than sit in it: they have no row of
 * their own to keep clear, so the only thing stopping them landing on a line of text is
 * knowing when they have. This answers that, and the caller fades out on it.
 *
 * Asked of the browser by hit-testing the element's own box rather than derived from a
 * scroll offset, because the question is what is under this mark now — which moves with
 * the page growing, the window resizing and the layout reflowing, and a scroll position
 * tracks none of those on its own.
 *
 * Takes a getter rather than the element itself, so it can be wired to a `bind:this`
 * that is still undefined when this is called. Like any rune, call it while the
 * component is initialising. An absent element covers nothing.
 */

// The page itself rather than anything on it. A point that reaches one of these has
// found only the canvas, whose background is the one every page has everywhere.
const CANVAS = new Set(['HTML', 'BODY']);

/** Replaced content, which paints whatever its box says regardless of its style. */
const REPLACED = new Set(['IMG', 'SVG', 'VIDEO', 'CANVAS', 'IFRAME']);

function opaque(colour: string): boolean {
	// Computed colours come back as `rgb(r g b)` or `rgb(r g b / a)`; only the second can
	// be transparent, and `transparent` itself computes to an alpha of 0.
	const alpha = colour.match(/\/\s*([\d.]+)\s*\)$/) ?? colour.match(/,\s*([\d.]+)\s*\)$/);
	return alpha ? parseFloat(alpha[1]) > 0 : true;
}

function intersects(a: DOMRect, b: DOMRect): boolean {
	return a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top;
}

/**
 * Where this element's own text actually is, line by line.
 *
 * A paragraph's box is the width of the column whatever its last line does with it, so a
 * sentence ending a third of the way across still has a box under a mark centred on the
 * page. The mark faded for text it was nowhere near. A range over the text measures the
 * lines rather than the block, and answers what the eye would.
 */
function textRects(element: Element): DOMRect[] {
	const range = document.createRange();
	const rects: DOMRect[] = [];
	for (const node of element.childNodes) {
		if (node.nodeType !== Node.TEXT_NODE || !node.textContent?.trim()) continue;
		range.selectNodeContents(node);
		rects.push(...range.getClientRects());
	}
	return rects;
}

/** Whether this element itself draws anything within `over`, as opposed to holding what does. */
function paints(element: Element, over: DOMRect): boolean {
	if (CANVAS.has(element.tagName)) return false;
	// A box that is its own paint: reaching it at all is the answer.
	if (REPLACED.has(element.tagName)) return true;

	const style = getComputedStyle(element);
	if (style.backgroundImage !== 'none') return true;
	if (opaque(style.backgroundColor)) return true;
	if (parseFloat(style.borderTopWidth) > 0 && opaque(style.borderTopColor)) return true;
	if (parseFloat(style.borderBottomWidth) > 0 && opaque(style.borderBottomColor)) return true;

	// Text this element holds directly. A wrapper whose text lives in a child is not
	// itself painting anything — that child is, and the stack below will reach it.
	return textRects(element).some((rect) => intersects(rect, over));
}

/**
 * Corners and centre rather than the whole box, which is all a 36px mark needs: content
 * small enough to fit between these points is not big enough to collide with.
 */
function samples({ left, top, right, bottom }: DOMRect): [number, number][] {
	const inset = 1;
	const [x0, y0, x1, y1] = [left + inset, top + inset, right - inset, bottom - inset];
	return [
		[x0, y0],
		[x1, y0],
		[x0, y1],
		[x1, y1],
		[(x0 + x1) / 2, (y0 + y1) / 2]
	];
}

export function overlapsContent(target: () => HTMLElement | undefined) {
	let covered = $state(false);

	$effect(() => {
		const element = target();
		if (!element) {
			covered = false;
			return;
		}

		const measure = () => {
			const rect = element.getBoundingClientRect();
			if (!rect.width || !rect.height) {
				covered = false;
				return;
			}

			// Every element under every sample point, gathered before any is asked what it
			// paints: the stacks overlap almost entirely across five points a few pixels
			// apart, and the set spares us pricing the same element five times.
			//
			// Points are the coarse pass and never the verdict. An element's box is only
			// ever larger than what it paints inside it, so hit-testing over-collects and
			// nothing that overlaps the mark is missed; `paints` then throws back what only
			// reserved the room.
			const beneath = new Set<Element>();
			for (const [x, y] of samples(rect)) {
				for (const hit of document.elementsFromPoint(x, y)) {
					// The mark is on top of its own hit test, and its tooltip travels with it.
					if (!element.contains(hit)) beneath.add(hit);
				}
			}

			covered = [...beneath].some((hit) => paints(hit, rect));
		};

		// Coalesced to a frame: scrolling fires far faster than anything can be painted in
		// response, and this reads layout, which is the one thing not worth doing twice
		// between frames.
		let frame = 0;
		const schedule = () => {
			if (frame) return;
			frame = requestAnimationFrame(() => {
				frame = 0;
				measure();
			});
		};

		measure();
		window.addEventListener('scroll', schedule, { passive: true });
		window.addEventListener('resize', schedule);
		// The page growing or shrinking under a mark that has not moved changes the answer
		// as surely as scrolling does — results arriving is exactly that.
		const observer = new ResizeObserver(schedule);
		observer.observe(document.documentElement);

		return () => {
			cancelAnimationFrame(frame);
			window.removeEventListener('scroll', schedule);
			window.removeEventListener('resize', schedule);
			observer.disconnect();
		};
	});

	return {
		get current() {
			return covered;
		}
	};
}
