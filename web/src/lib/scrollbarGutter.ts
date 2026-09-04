/**
 * The width of this platform's scrollbar, published to CSS as `--scrollbar-gutter`.
 *
 * layout.css holds a gutter open for the document's scrollbar so the page does not slide
 * sideways as results arrive and clear, and hands it back for as long as a modal dialog
 * is up — a dialog is laid out inside the layout viewport, so while the gutter is held
 * the bookmarks drawer stops a scrollbar's width short of the glass.
 *
 * Handing it back makes that viewport wider, though, and half of every pixel gained lands
 * on either side of anything centred in it: the page jumped sideways as a dialog opened
 * and back as it closed. So the page takes the same width straight back as padding, and
 * this is the number it does that with.
 *
 * Measured from a probe rather than from `innerWidth - documentElement.clientWidth`. That
 * difference is not the reserved gutter but the scrollbar currently being painted, and
 * the two part company exactly when it matters: filter a page down to nothing and it
 * reads zero while the gutter is still held open. The probe puts the question to a box
 * that always has a scrollbar, so the answer does not depend on how much the page happens
 * to be showing, or on whether a dialog is already open when it is asked.
 *
 * Zero wherever scrollbars overlay the content, which is most platforms, and there every
 * part of this arrangement is a no-op.
 */

const PROPERTY = '--scrollbar-gutter';

function scrollbarWidth(): number {
	const probe = document.createElement('div');
	// `overflow: scroll` rather than `auto`, so there is a bar to measure whether or not
	// the box has anything to scroll. Out of flow and off the top-left, where overflow is
	// never scrollable, so it can neither paint nor lengthen the page while it is here.
	probe.style.cssText =
		'position:absolute;top:-9999px;left:-9999px;width:100px;height:100px;overflow:scroll;visibility:hidden';
	document.body.append(probe);
	const width = probe.offsetWidth - probe.clientWidth;
	probe.remove();
	return width;
}

/**
 * Publishes the width and keeps it current, returning the teardown an `$effect` wants.
 * Re-measured on resize, because a scrollbar's width in CSS pixels moves with page zoom.
 */
export function trackScrollbarGutter(): () => void {
	const publish = () =>
		document.documentElement.style.setProperty(PROPERTY, `${scrollbarWidth()}px`);

	publish();
	window.addEventListener('resize', publish);
	return () => window.removeEventListener('resize', publish);
}
