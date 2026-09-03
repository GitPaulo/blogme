import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// The bookmarks drawer is a `dialog:modal`, and a modal dialog is laid out inside the
// layout viewport: the window less whatever the document's scrollbar is holding open.
// So is its backdrop. On a platform with classic scrollbars that leaves a strip of
// undimmed page down the inline end of a drawer that is supposed to touch the glass,
// and `scrollbar-gutter: stable` makes the strip permanent by holding the space open
// even on a page with nothing to scroll.
//
// Nothing written on the drawer can cover it. Margins, insets and widths are all
// measured in that same shrunken box, so every fix aimed at the panel has landed,
// looked right in review, and changed nothing — which is why this gap has been found
// and "fixed" several times over. The only thing that closes it is the page giving the
// space back for as long as a dialog is up.
//
// Neither half of that is visible from the other's file, and the whole thing is
// invisible on a machine with overlay scrollbars, which is most of them. So it is
// asserted here: a text scrape, no build step, failing on the pull request that drops
// the rule rather than on the one desktop that would have shown it.

const css = readFileSync(fileURLToPath(new URL('./layout.css', import.meta.url)), 'utf8');

/** The declarations inside the first rule whose selector matches, whitespace flattened. */
function block(selector: string): string {
	const at = css.indexOf(selector);
	expect(at, `no rule for ${selector} in layout.css`).toBeGreaterThan(-1);
	const open = css.indexOf('{', at);
	const close = css.indexOf('}', open);
	return css.slice(open + 1, close).replace(/\s+/g, ' ');
}

describe('the page hands its scrollbar space back to modal dialogs', () => {
	it('releases the reserved gutter while a dialog is up', () => {
		expect(block('html:has(dialog:modal)')).toMatch(/scrollbar-gutter:\s*auto/);
	});

	it('takes the scrollbar itself away too, since a real one holds the same space open', () => {
		// Releasing the reservation alone only helps a page short enough not to scroll.
		// Scrolling is already suppressed under a modal, so nothing is lost by it.
		expect(block('html:has(dialog:modal)')).toMatch(/overflow:\s*hidden/);
	});

	it('still reserves the gutter the rest of the time', () => {
		// The reservation is what keeps a result set arriving or clearing from shifting
		// every centred thing on the page sideways. If it goes, this file's release rule
		// is still required: a real scrollbar shrinks the dialog's box just the same.
		expect(block('html {')).toMatch(/scrollbar-gutter:\s*stable/);
	});
});
