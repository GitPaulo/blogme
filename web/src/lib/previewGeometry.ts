/**
 * Where the link preview panel sits, how big it is, and what of that survives the reader
 * closing it.
 *
 * Split out from the panel because it is the half worth reasoning about on its own:
 * placement flips the panel to whichever side has room, a reader can drag and resize it,
 * and whatever they leave it at has to come back inside a window that may since have
 * become smaller than the one they chose it on.
 */

const STORAGE_KEY = 'blogme:preview';

export type Size = { width: number; height: number };
export type Position = { left: number; top: number };
/**
 * A remembered panel. The position is optional because only a drag chooses one: a reader
 * who has merely resized the panel has said nothing about where it belongs, and pinning
 * every later preview to wherever that one happened to sit would put it a long way from
 * the link that opens it.
 */
export type Geometry = Size & Partial<Position>;
/** Just the part of `window` this needs, so every function here stays testable. */
export type Viewport = { width: number; height: number };

/**
 * The panel before anyone has resized one. The height is the whole panel rather than the
 * framed page alone, so the reader dragging a corner to 500 gets a panel 500 tall and
 * placement reserves the room it actually occupies.
 */
export const DEFAULT_SIZE: Size = { width: 420, height: 500 };

/**
 * The smallest panel worth having: wide enough that the host and the Open link still
 * share their row, tall enough to show that something loaded.
 */
export const MIN_SIZE: Size = { width: 280, height: 180 };

/** Kept clear of the viewport edge, so a panel never looks pinned to the glass. */
export const MARGIN = 12;
/** Between the panel and the link it was opened from. */
const GAP = 10;
// Wider than GAP because a cursor is not a point: the arrow glyph is roughly 20px tall,
// and a panel edge tucked under it swallows the hover that opened the preview and lands
// the iframe's scrollbar beneath the pointer.
const CURSOR_GAP = 24;

// Ordered so the minimum wins a viewport too small to hold it: a panel overflowing a
// little is still readable, where one collapsed to fit is not.
const clamp = (value: number, min: number, max: number) => Math.max(min, Math.min(value, max));

/** Fits a size inside the viewport, never below MIN_SIZE. */
export function clampSize(size: Size, viewport: Viewport): Size {
	return {
		width: clamp(size.width, MIN_SIZE.width, viewport.width - MARGIN * 2),
		height: clamp(size.height, MIN_SIZE.height, viewport.height - MARGIN * 2)
	};
}

/**
 * The largest panel that still fits from this corner. Used while resizing, where the
 * top-left is pinned: capped against the viewport alone, a panel grown past the right
 * edge would be shoved leftwards to make room for itself, so pushing the corner right
 * would slide the whole panel left.
 */
export function clampSizeAt(size: Size, position: Position, viewport: Viewport): Size {
	return {
		width: clamp(size.width, MIN_SIZE.width, viewport.width - position.left - MARGIN),
		height: clamp(size.height, MIN_SIZE.height, viewport.height - position.top - MARGIN)
	};
}

/** Keeps a panel of this size inside the viewport. */
export function clampPosition(position: Position, size: Size, viewport: Viewport): Position {
	return {
		left: clamp(position.left, MARGIN, viewport.width - size.width - MARGIN),
		top: clamp(position.top, MARGIN, viewport.height - size.height - MARGIN)
	};
}

/**
 * Anchored to the pointer, offset clear of the cursor and flipped to whichever side has
 * room, the way native tooltips and floating-ui popovers avoid clipping.
 */
export function placeAtPoint(x: number, y: number, size: Size, viewport: Viewport): Position {
	const left =
		x + CURSOR_GAP + size.width + MARGIN <= viewport.width
			? x + CURSOR_GAP
			: x - CURSOR_GAP - size.width;
	const top =
		y + CURSOR_GAP + size.height + MARGIN <= viewport.height
			? y + CURSOR_GAP
			: y - CURSOR_GAP - size.height;
	return clampPosition({ left, top }, size, viewport);
}

/**
 * Keyboard focus has no pointer position to anchor to, so it falls back to the link's own
 * rect, beside it where the viewport has room and below or above otherwise.
 */
export function placeAtRect(rect: DOMRect, size: Size, viewport: Viewport): Position {
	const beside =
		rect.right + GAP + size.width + MARGIN <= viewport.width
			? rect.right + GAP
			: rect.left - GAP - size.width;
	if (beside >= MARGIN) {
		return clampPosition({ left: beside, top: rect.top }, size, viewport);
	}

	const below = rect.bottom + GAP;
	const top =
		below + size.height + MARGIN <= viewport.height ? below : rect.top - GAP - size.height;
	return clampPosition({ left: rect.left, top }, size, viewport);
}

const isNumber = (value: unknown): value is number =>
	typeof value === 'number' && Number.isFinite(value);

/**
 * Stored geometry is as untrusted as anything else read back from a browser: it may have
 * been hand-edited, or written by a version of this that stored a different shape. Any
 * field missing or not a real number and the whole record is discarded, because a partly
 * applied one puts the panel somewhere no reader chose.
 */
function toGeometry(value: unknown): Geometry | undefined {
	if (typeof value !== 'object' || value === null) return undefined;
	const { left, top, width, height } = value as Record<string, unknown>;
	if (!isNumber(width) || !isNumber(height)) return undefined;
	// Both halves of a place or neither, so a half-written record cannot move the panel
	// along one axis and leave the other wherever the last preview put it.
	return isNumber(left) && isNumber(top) ? { width, height, left, top } : { width, height };
}

/** The place in a stored record, when the reader picked one by dragging. */
export function storedPosition(geometry: Geometry | undefined): Position | undefined {
	return geometry?.left !== undefined && geometry.top !== undefined
		? { left: geometry.left, top: geometry.top }
		: undefined;
}

/** What the reader last left a panel at, or undefined while they have not moved one. */
export function readGeometry(): Geometry | undefined {
	// Storage access throws where cookies are blocked, which must not stop previews opening.
	try {
		const stored = localStorage.getItem(STORAGE_KEY);
		return stored ? toGeometry(JSON.parse(stored)) : undefined;
	} catch {
		// Read as no preference, so the panel places itself against the link as it always did.
		return undefined;
	}
}

/** Forgets the remembered panel, so the next one comes back beside its link at DEFAULT_SIZE. */
export function clearGeometry() {
	try {
		localStorage.removeItem(STORAGE_KEY);
	} catch {
		// Nothing was readable to begin with, so there is nothing to forget.
	}
}

export function writeGeometry(geometry: Geometry) {
	try {
		localStorage.setItem(STORAGE_KEY, JSON.stringify(geometry));
	} catch {
		// The panel keeps the size and place it was given for the rest of this session.
	}
}
