import { afterEach, describe, expect, it, vi } from 'vitest';
import {
	clampPosition,
	clampSize,
	clampSizeAt,
	clearGeometry,
	DEFAULT_SIZE,
	MARGIN,
	MIN_SIZE,
	placeAtPoint,
	placeAtRect,
	readGeometry,
	storedPosition,
	writeGeometry
} from './previewGeometry';

// Placement runs against whatever window the reader happens to have, including one
// smaller than the panel they last chose. So the cases that matter are the ones with no
// room: a panel that flips, a panel that is clamped, and a viewport too small to hold
// either. The rule throughout is that a panel overflowing a little is still readable
// where one collapsed to fit is not, which is why MIN_SIZE wins over the viewport.

const view = { width: 1200, height: 900 };

describe('clampSize', () => {
	it('leaves a panel that already fits alone', () => {
		expect(clampSize(DEFAULT_SIZE, view)).toEqual(DEFAULT_SIZE);
	});

	it('trims a panel wider than the window it is opening in', () => {
		expect(clampSize({ width: 5000, height: 5000 }, view)).toEqual({
			width: view.width - MARGIN * 2,
			height: view.height - MARGIN * 2
		});
	});

	it('keeps the minimum rather than collapsing into a window too small for it', () => {
		// 300 - 24 leaves 276, under the 280 minimum width. The panel overflows by four
		// pixels, which is the readable answer.
		expect(clampSize(DEFAULT_SIZE, { width: 300, height: 900 }).width).toBe(MIN_SIZE.width);
	});
});

describe('clampSizeAt', () => {
	it('measures from where the panel sits, so resizing never moves it', () => {
		const at = { left: 100, top: 100 };
		expect(clampSizeAt({ width: 5000, height: 5000 }, at, view)).toEqual({
			width: view.width - at.left - MARGIN,
			height: view.height - at.top - MARGIN
		});
	});

	it('lets a panel grow while there is room from this corner', () => {
		expect(clampSizeAt({ width: 500, height: 600 }, { left: 100, top: 100 }, view)).toEqual({
			width: 500,
			height: 600
		});
	});

	it('never shrinks below the minimum', () => {
		expect(clampSizeAt({ width: 10, height: 10 }, { left: 0, top: 0 }, view)).toEqual(MIN_SIZE);
	});
});

describe('clampPosition', () => {
	it('pulls a panel off the top-left edge', () => {
		expect(clampPosition({ left: -50, top: -50 }, DEFAULT_SIZE, view)).toEqual({
			left: MARGIN,
			top: MARGIN
		});
	});

	it('pulls a panel back from the bottom-right edge', () => {
		expect(clampPosition({ left: 5000, top: 5000 }, DEFAULT_SIZE, view)).toEqual({
			left: view.width - DEFAULT_SIZE.width - MARGIN,
			top: view.height - DEFAULT_SIZE.height - MARGIN
		});
	});
});

describe('placeAtPoint', () => {
	it('sits down and to the right of the pointer where there is room', () => {
		expect(placeAtPoint(100, 100, DEFAULT_SIZE, view)).toEqual({ left: 124, top: 124 });
	});

	it('flips to the left of the pointer rather than running off the right edge', () => {
		expect(placeAtPoint(1100, 100, DEFAULT_SIZE, view)).toEqual({ left: 656, top: 124 });
	});

	it('flips above the pointer rather than running off the bottom', () => {
		expect(placeAtPoint(100, 800, DEFAULT_SIZE, view)).toEqual({ left: 124, top: 276 });
	});
});

describe('placeAtRect', () => {
	// Keyboard focus has no pointer to anchor to, so the link's own box stands in.
	const rect = (left: number, top: number, right: number, bottom: number) => ({
		left,
		top,
		right,
		bottom
	});

	it('sits beside the link where the viewport has room', () => {
		expect(placeAtRect(rect(100, 200, 300, 220), DEFAULT_SIZE, view)).toEqual({
			left: 310,
			top: 200
		});
	});

	it('flips to the other side of the link rather than off the right edge', () => {
		expect(placeAtRect(rect(900, 200, 1100, 220), DEFAULT_SIZE, view)).toEqual({
			left: 470,
			top: 200
		});
	});

	it('drops below a link too wide to sit beside', () => {
		expect(placeAtRect(rect(20, 200, 900, 220), DEFAULT_SIZE, view)).toEqual({
			left: 20,
			top: 230
		});
	});

	it('goes above instead when there is no room below either', () => {
		expect(placeAtRect(rect(20, 680, 900, 700), DEFAULT_SIZE, view)).toEqual({
			left: 20,
			top: 170
		});
	});
});

describe('storedPosition', () => {
	it('has no place to report for a panel that was only ever resized', () => {
		expect(storedPosition({ width: 500, height: 600 })).toBeUndefined();
		expect(storedPosition(undefined)).toBeUndefined();
	});

	it('reports the place a drag chose', () => {
		expect(storedPosition({ width: 500, height: 600, left: 10, top: 20 })).toEqual({
			left: 10,
			top: 20
		});
	});
});

/** A stand-in for the browser's, with the failures that matter made reachable. */
function storage(seed?: string) {
	const map = new Map<string, string>();
	if (seed !== undefined) map.set('blogme:preview', seed);
	return {
		map,
		throwOnRead: false,
		throwOnWrite: false,
		getItem(k: string) {
			if (this.throwOnRead) throw new Error('blocked');
			return this.map.get(k) ?? null;
		},
		setItem(k: string, v: string) {
			if (this.throwOnWrite) throw new Error('quota');
			this.map.set(k, v);
		},
		removeItem(k: string) {
			this.map.delete(k);
		}
	};
}

afterEach(() => vi.unstubAllGlobals());

describe('readGeometry', () => {
	const read = (seed?: string) => {
		vi.stubGlobal('localStorage', storage(seed));
		return readGeometry();
	};

	it('reads back a size on its own', () => {
		expect(read('{"width":500,"height":600}')).toEqual({ width: 500, height: 600 });
	});

	it('reads back a size and the place a drag chose', () => {
		expect(read('{"width":500,"height":600,"left":10,"top":20}')).toEqual({
			width: 500,
			height: 600,
			left: 10,
			top: 20
		});
	});

	it('drops half a place rather than moving the panel along one axis', () => {
		expect(read('{"width":500,"height":600,"left":10}')).toEqual({ width: 500, height: 600 });
	});

	it('discards a record with no usable size', () => {
		for (const seed of [
			'not json',
			'null',
			'[]',
			'{"height":600}',
			'{"width":"500","height":600}',
			'{"width":null,"height":600}'
		]) {
			expect(read(seed), seed).toBeUndefined();
		}
	});

	it('reads as no preference when storage refuses to be read', () => {
		// A private window, or a browser set to block site data. The panel then places
		// itself against the link as it always did.
		const store = storage('{"width":500,"height":600}');
		store.throwOnRead = true;
		vi.stubGlobal('localStorage', store);
		expect(readGeometry()).toBeUndefined();
	});
});

describe('writeGeometry and clearGeometry', () => {
	it('writes a panel back and reads the same one', () => {
		const store = storage();
		vi.stubGlobal('localStorage', store);
		writeGeometry({ width: 500, height: 600, left: 10, top: 20 });
		expect(readGeometry()).toEqual({ width: 500, height: 600, left: 10, top: 20 });
	});

	it('forgets the remembered panel', () => {
		const store = storage('{"width":500,"height":600}');
		vi.stubGlobal('localStorage', store);
		clearGeometry();
		expect(readGeometry()).toBeUndefined();
	});

	it('keeps this session going when a write is refused', () => {
		const store = storage();
		store.throwOnWrite = true;
		vi.stubGlobal('localStorage', store);
		expect(() => writeGeometry({ width: 500, height: 600 })).not.toThrow();
	});
});
