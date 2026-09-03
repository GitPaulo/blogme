<script lang="ts">
	import { Badge } from 'flowbite-svelte';
	import { ArrowUpRightFromSquareOutline } from 'flowbite-svelte-icons';
	import { prefersReducedMotion } from 'svelte/motion';
	import { fade } from 'svelte/transition';
	import { safeHttpUrl } from '$lib/api';
	import { prefersReducedData } from '$lib/saveData';
	import { hostOf } from '$lib/site';
	import {
		clampPosition,
		clampSize,
		clampSizeAt,
		DEFAULT_SIZE,
		clearGeometry,
		type Geometry,
		placeAtPoint,
		placeAtRect,
		type Position,
		readGeometry,
		type Size,
		storedPosition,
		writeGeometry
	} from '$lib/previewGeometry';
	import SiteIcon from '$lib/components/SiteIcon.svelte';
	import { visited } from '$lib/visited/store.svelte';

	// One panel for the whole app: anchors opt in with a `data-preview` attribute and the
	// listeners are delegated, so a page of result cards costs nothing until a pointer
	// actually rests on one.

	// A preview is a whole third-party document load, so the dwell is long enough that
	// running the pointer down the list never starts one.
	const DWELL_MS = 350;
	// Long enough to cross the gap between the link and the panel.
	const CLOSE_MS = 200;
	// A panel opening where the reader dragged the last one can be right across the window
	// from the link that opened it, which is further than CLOSE_MS was measured for.
	const REACH_MS = 600;
	// The whole panel for a refusal, header included: the message is one line, so this is
	// sized to it rather than left as a tall empty box. Not resizable for the same reason.
	const DENIED_HEIGHT = 128;
	// The strip under a preview whose framing nobody has checked. The frame is the flexible
	// row, so this comes out of the panel's height rather than adding to it.
	const NOTE_HEIGHT = 28;
	// No allow-top-navigation, allow-popups, allow-forms or allow-downloads: the framed
	// page renders, scrolls and follows its own links, and can do nothing else.
	const SANDBOX = 'allow-scripts allow-same-origin';

	/**
	 * What the crawler found out about this page's framing headers, as the results list
	 * passed it on. `unknown` is not permission but a page nobody has checked, and it is
	 * tried, which is what every link did before any of this existed.
	 */
	type Framing = 'allowed' | 'denied' | 'unknown';

	type Target = Position &
		Size & {
			url: string;
			host: string;
			framing: Framing;
			/**
			 * Opened where the reader put the last panel rather than beside this link, which
			 * is a longer journey for the pointer. See scheduleClose.
			 */
			placed: boolean;
			/**
			 * The panel as it comes: default size, beside the link that opened it. Carried
			 * along so a double click can put it back without having to find the link again,
			 * by which time the pointer is on the header and the list may have moved.
			 */
			home: Position & Size;
		};

	/** A move or a resize in progress, and what the panel was when it started. */
	type Drag = {
		mode: 'move' | 'resize';
		/** So a second pointer landing mid-drag cannot drive the same panel. */
		pointer: number;
		x: number;
		y: number;
		// Every move is measured from where the drag began rather than from the move before
		// it, so a dropped frame cannot let the panel creep out from under the cursor.
		origin: Position & Size;
	};

	let target = $state.raw<Target | undefined>();
	let drag = $state.raw<Drag | undefined>();
	let loading = $state(false);

	let openTimer: ReturnType<typeof setTimeout> | undefined;
	let closeTimer: ReturnType<typeof setTimeout> | undefined;
	// Preconnecting twice to the same origin is wasted markup, not a wasted connection.
	const warmed = new Set<string>();
	// What the reader last left a panel at. Plain rather than state: it is read when a
	// panel opens and written when a drag ends, and never drives what is on screen. Filled
	// in from storage on mount, because this component is prerendered.
	let stored: Geometry | undefined;

	// The panel is `position: fixed`, so the box it is placed in is the layout viewport: the
	// window less whatever the document's scrollbar holds open. layout.css reserves that
	// gutter whether or not the page overflows, so `innerWidth` overstates the room by its
	// width for as long as the page is up — more than enough to swallow MARGIN whole and
	// leave the resize corner sitting under the scrollbar. `documentElement` measures the
	// box the panel is actually laid out in, which is the one every clamp here means.
	const viewport = () => ({
		width: document.documentElement.clientWidth,
		height: document.documentElement.clientHeight
	});

	function anchorFrom(node: EventTarget | null) {
		const el = node instanceof Element ? node : undefined;
		return el?.closest('a[data-preview]') as HTMLAnchorElement | null | undefined;
	}

	function inPanel(node: EventTarget | null) {
		return node instanceof Element && node.closest('[data-preview-panel]') !== null;
	}

	// The Open link and the Visited badge share the header row with the drag handle, and a
	// press on one of those is aimed at it rather than at the panel.
	function onControl(node: EventTarget | null) {
		return node instanceof Element && node.closest('a, button') !== null;
	}

	/**
	 * What is known about this link's framing. Read from the value of the attribute
	 * that opted it into previews, so a link carrying no answer reads as unknown, whether
	 * it was indexed before the crawler read headers or is not a search result at all.
	 */
	function framingOf(anchor: HTMLAnchorElement): Framing {
		const value = anchor.dataset.preview;
		return value === 'denied' || value === 'allowed' ? value : 'unknown';
	}

	// How long a preconnect tag stays in the head. The browser holds the socket in its
	// own pool once opened, so the tag has done its work long before this; it is only
	// generous because removing it early is the one way to waste the handshake.
	const WARM_TTL_MS = 10_000;

	// DNS and TLS on the way in, so the dwell timer is not also paying for the handshake.
	// see: https://developer.mozilla.org/en-US/docs/Web/HTML/Attributes/rel/preconnect
	function warm(href: string) {
		const url = safeHttpUrl(href);
		if (!url) return;
		const { origin } = new URL(url);
		if (warmed.has(origin)) return;
		warmed.add(origin);

		const link = document.createElement('link');
		link.rel = 'preconnect';
		link.href = origin;
		document.head.append(link);
		// Dropped once it has been acted on. A reader running down a long list of results
		// meets a new origin on most rows, and the tags would otherwise pile up in the
		// head for the life of the session. `warmed` still remembers the origin, so this
		// never re-adds one. Same shape as the revoke in bookmarks/export.ts.
		setTimeout(() => link.remove(), WARM_TTL_MS);
	}

	function openLater(anchor: HTMLAnchorElement, point?: { x: number; y: number }) {
		if (anchor.href === target?.url) return;
		warm(anchor.href);
		clearTimeout(openTimer);
		openTimer = setTimeout(() => {
			const url = safeHttpUrl(anchor.href);
			if (!url) return;
			const framing = framingOf(anchor);
			const view = viewport();

			// A refusal is one line whatever a framed panel has been resized to, so only the
			// width carries across: the message would otherwise sit in a column of nothing.
			const sizeOf = (size: Size): Size =>
				framing === 'denied' ? { width: size.width, height: DENIED_HEIGHT } : size;
			// Beside the link, which is where a panel goes when nothing is remembered.
			const beside = (size: Size) =>
				point
					? placeAtPoint(point.x, point.y, size, view)
					: placeAtRect(anchor.getBoundingClientRect(), size, view);

			const homeSize = sizeOf(DEFAULT_SIZE);
			const home = { ...homeSize, ...beside(homeSize) };

			const size = sizeOf(clampSize(stored ?? DEFAULT_SIZE, view));
			// A panel the reader dragged somewhere opens there again. One they have only
			// resized has no place of its own, so it goes beside the link as it always did.
			const chosen = storedPosition(stored);
			const position = chosen ? clampPosition(chosen, size, view) : beside(size);

			// Nothing is being waited for when there is no frame to load.
			loading = framing !== 'denied';
			target = {
				url,
				host: hostOf(url) ?? new URL(url).hostname,
				framing,
				placed: chosen !== undefined,
				home,
				...position,
				...size
			};
		}, DWELL_MS);
	}

	function scheduleClose() {
		// A drag holds the panel open wherever the pointer has got to, including off the
		// window entirely, which is one of the things that fires this.
		if (drag) return;
		clearTimeout(closeTimer);
		closeTimer = setTimeout(close, target?.placed ? REACH_MS : CLOSE_MS);
	}

	// A drag outranks a scroll. The panel is fixed to the window, so the page moving under
	// it changes nothing about where it sits, and a wheel nudged mid-gesture taking the
	// panel away would be the only surprise in it.
	function closeOnScroll() {
		if (drag) return;
		close();
	}

	function close() {
		clearTimeout(openTimer);
		clearTimeout(closeTimer);
		// Dropped along with the panel it was moving: a drag outliving its target would
		// write geometry for a preview that is no longer on screen.
		drag = undefined;
		target = undefined;
	}

	function startDrag(event: PointerEvent, mode: Drag['mode']) {
		// Primary button only, one drag at a time, and never from the link or the badge
		// sharing the header row. A second pointer arriving mid-drag would otherwise take
		// the panel over with an origin measured from the wrong place.
		if (!target || drag || event.button !== 0) return;
		if (mode === 'move' && onControl(event.target)) return;

		// Narrowed rather than asserted, as elsewhere: currentTarget is the handle this is
		// bound to, but the DOM types do not say so.
		const handle = event.currentTarget;
		if (!(handle instanceof HTMLElement)) return;
		// Without capture the pointer is lost the moment it crosses into the framed page,
		// which is cross-origin and hands back no events at all; the panel would then stay
		// stuck to the cursor. It also keeps every pointerover during the drag addressed to
		// the panel, which is what stops the hover machinery opening a different link.
		handle.setPointerCapture(event.pointerId);

		drag = {
			mode,
			pointer: event.pointerId,
			x: event.clientX,
			y: event.clientY,
			origin: { left: target.left, top: target.top, width: target.width, height: target.height }
		};
		// Or the drag picks up a text selection, or the favicon, on its way across.
		event.preventDefault();
	}

	function onDragMove(event: PointerEvent) {
		if (!drag || !target || event.pointerId !== drag.pointer) return;
		const dx = event.clientX - drag.x;
		const dy = event.clientY - drag.y;
		const view = viewport();

		if (drag.mode === 'move') {
			const moved = { left: drag.origin.left + dx, top: drag.origin.top + dy };
			target = { ...target, ...clampPosition(moved, target, view) };
			return;
		}

		// Anchored at the top-left, so only the far edges follow the pointer and the panel
		// never moves while it is being sized. Measured from where it sits rather than
		// against the viewport, so growing it stops at the edge of the window.
		const size = clampSizeAt(
			{ width: drag.origin.width + dx, height: drag.origin.height + dy },
			target,
			view
		);
		target = { ...target, ...size };
	}

	function endDrag(event: PointerEvent) {
		if (!drag || event.pointerId !== drag.pointer) return;
		const { origin } = drag;
		drag = undefined;
		if (!target) return;

		// A press that went nowhere is a click, not a drag, and settles nothing. Written
		// out anyway it would pin the panel to wherever it already sat, quietly turning a
		// reader who had only ever resized it into one with a remembered place — and every
		// click on the header, including the two that make up the double click below, into
		// a write.
		const moved = target.left !== origin.left || target.top !== origin.top;
		const resized = target.width !== origin.width || target.height !== origin.height;
		if (!moved && !resized) return;

		// A refusal is one line by design, so its height is never what the reader wants
		// kept — only the width it shares with a framed panel.
		const height =
			target.framing === 'denied' ? (stored?.height ?? DEFAULT_SIZE.height) : target.height;
		// Moving is the reader choosing a place; sizing says nothing about one, so it
		// leaves whatever place was already remembered alone.
		const position = moved ? { left: target.left, top: target.top } : storedPosition(stored);

		stored = { width: target.width, height, ...position };
		writeGeometry(stored);
	}

	/**
	 * Puts the panel back the way it comes: default size, beside the link that opened it,
	 * and nothing remembered for the next one either.
	 *
	 * On the header rather than a button of its own, because the header is already the
	 * handle and undoing a drag belongs on the thing that did it. Pointer-only, like the
	 * dragging it undoes; a reader who never moved a panel has nothing to put back.
	 */
	function reset(event: MouseEvent) {
		if (!target || onControl(event.target)) return;
		stored = undefined;
		clearGeometry();
		target = { ...target, placed: false, ...target.home };
	}

	// One handler, because "the pointer is now over something that is neither the open
	// link nor the panel" is exactly the condition for closing.
	function onPointerOver(event: PointerEvent) {
		if (drag) return;
		if (inPanel(event.target)) {
			clearTimeout(closeTimer);
			return;
		}
		const anchor = anchorFrom(event.target);
		if (anchor) {
			clearTimeout(closeTimer);
			openLater(anchor, { x: event.clientX, y: event.clientY });
			return;
		}
		clearTimeout(openTimer);
		if (target) scheduleClose();
	}

	// Tabbing through the list would otherwise load a document per keystroke, so keyboard
	// focus waits out the same dwell a pointer does.
	function onFocusIn(event: FocusEvent) {
		if (drag) return;
		if (inPanel(event.target)) {
			clearTimeout(closeTimer);
			return;
		}
		const anchor = anchorFrom(event.target);
		if (anchor) {
			clearTimeout(closeTimer);
			openLater(anchor);
		} else close();
	}

	function onKeydown(event: KeyboardEvent) {
		if (event.key === 'Escape') close();
	}

	$effect(() => {
		// A preview costs a document load, so devices that cannot hover, and readers who
		// asked for less data, never install any of this.
		const hoverable = window.matchMedia('(hover: hover) and (pointer: fine)').matches;
		if (!hoverable || prefersReducedData()) return;

		stored = readGeometry();

		document.addEventListener('pointerover', onPointerOver);
		document.addEventListener('pointerleave', scheduleClose);
		document.addEventListener('focusin', onFocusIn);
		document.addEventListener('keydown', onKeydown);
		// Scrolling inside the frame never reaches us, so this only fires once the reader
		// has moved the pointer off the panel.
		window.addEventListener('scroll', closeOnScroll, { passive: true });
		// A panel is placed against a viewport that has now changed, and a remembered one
		// can be wider than what is left. Both are settled by opening the next one, which
		// clamps to the window it finds.
		window.addEventListener('resize', close);

		return () => {
			document.removeEventListener('pointerover', onPointerOver);
			document.removeEventListener('pointerleave', scheduleClose);
			document.removeEventListener('focusin', onFocusIn);
			document.removeEventListener('keydown', onKeydown);
			window.removeEventListener('scroll', closeOnScroll);
			window.removeEventListener('resize', close);
			close();
		};
	});
</script>

{#if target}
	<div
		data-preview-panel
		role="group"
		aria-label="Preview of {target.host}"
		class="fixed z-60 flex flex-col overflow-hidden rounded-lg border border-gray-200 bg-white shadow-2xl dark:border-gray-700 dark:bg-gray-800"
		style:left="{target.left}px"
		style:top="{target.top}px"
		style:width="{target.width}px"
		style:height="{target.height}px"
		transition:fade={{ duration: prefersReducedMotion.current ? 0 : 120 }}
	>
		<!-- Keyed so every target gets a fresh frame: assigning src to a live iframe pushes
		a session history entry, which would turn the back button into frame navigation. -->
		{#key target.url}
			<!-- The header doubles as the handle, because it is the one strip of the panel
			that is neither the framed page nor a control, and a window dragged by its title
			bar is what a reader already expects. touch-none keeps a pen from scrolling the
			page out from under a drag on the hybrid devices that pass the hover gate. -->
			<div
				role="presentation"
				class="flex shrink-0 touch-none items-center gap-2 border-b border-gray-200 px-3 py-2 text-xs select-none dark:border-gray-700 {drag?.mode ===
				'move'
					? 'cursor-grabbing'
					: 'cursor-grab'}"
				onpointerdown={(event) => startDrag(event, 'move')}
				ondblclick={reset}
				onpointermove={onDragMove}
				onpointerup={endDrag}
				onpointercancel={endDrag}
				onlostpointercapture={endDrag}
			>
				<!-- The same icon the result cards use, which shares their record of which hosts
				have no favicon: hovering a card whose icon already fell back does not send the
				panel off to ask for the same missing file again. A host with no icon now keeps
				its place in the row as a lettered tile rather than leaving a gap, so the header
				no longer reflows the moment a request fails. -->
				<SiteIcon host={target.host} class="h-4 w-4" />
				<span class="truncate text-gray-500 dark:text-gray-400">{target.host}</span>
				<!-- Beside the host rather than on the result card: this row is the one line
				that describes the destination, and having been here before is a fact about the
				destination. Grey, because it reports what the reader already did rather than
				telling them something about the post. -->
				{#if visited.has(target.url)}
					<!-- Trimmed to the height of the line it sits on, so a header that learns it
					has been here before does not grow by four pixels mid-fade. -->
					<Badge color="gray" class="shrink-0 !py-0">Visited</Badge>
				{/if}
				<a
					href={target.url}
					target="_blank"
					rel="noopener noreferrer"
					data-visit
					class="ms-auto flex shrink-0 cursor-pointer items-center gap-1 rounded-sm font-medium text-primary-600 hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:text-primary-400"
				>
					Open
					<ArrowUpRightFromSquareOutline class="h-3 w-3" />
				</a>
			</div>

			{#if target.framing === 'denied'}
				<!-- The site's own headers refuse to be framed anywhere, so this says so rather
				than opening a box the browser would leave blank and complain about. The way in
				is the link above, which is where this was always going to send them. -->
				<p
					class="flex flex-1 items-center justify-center px-6 text-center text-sm text-gray-500 dark:text-gray-400"
				>
					{target.host} does not allow previews. Open it to read the post.
				</p>
			{:else}
				<div class="relative min-h-0 flex-1 bg-white">
					{#if loading}
						<div class="absolute inset-0 z-10 animate-pulse bg-gray-100 dark:bg-gray-700"></div>
					{/if}
					<!-- A site that refuses framing leaves this blank, and from here there is no
					way to tell that apart from a page that rendered nothing: the frame loads
					either way and its document is cross-origin either way. Which is why the
					answer comes from the crawler rather than from anything measured here.

					Deaf to the pointer while a drag is running, because a cross-origin frame
					swallows the events that would otherwise finish it. -->
					<iframe
						title="Preview of {target.host}"
						src={target.url}
						sandbox={SANDBOX}
						referrerpolicy="no-referrer"
						class="h-full w-full border-0 {drag ? 'pointer-events-none' : ''}"
						onload={() => (loading = false)}
					></iframe>
				</div>
				{#if target.framing === 'unknown'}
					<!-- Nobody has read this one's headers yet, so a blank frame has two possible
					meanings and the reader gets told which they might be looking at. Goes away on
					its own as the crawler comes back round to the post. -->
					<p
						class="flex shrink-0 items-center justify-center border-t border-gray-200 px-3 text-center text-xs text-gray-400 dark:border-gray-700 dark:text-gray-500"
						style:height="{NOTE_HEIGHT}px"
					>
						If nothing appears, this site does not allow previews.
					</p>
				{/if}
			{/if}
		{/key}

		{#if target.framing !== 'denied'}
			<!-- Outside the keyed block: the corner belongs to the panel rather than to what
			is framed in it, and remounting it would drop a capture mid-drag. Physical
			bottom-right rather than the logical end, so it agrees with both the resize cursor
			and the sign of the delta the drag applies. -->
			<div
				role="presentation"
				class="absolute right-0 bottom-0 z-20 h-4 w-4 cursor-nwse-resize touch-none"
				onpointerdown={(event) => startDrag(event, 'resize')}
				onpointermove={onDragMove}
				onpointerup={endDrag}
				onpointercancel={endDrag}
				onlostpointercapture={endDrag}
			>
				<!-- Two rules rather than a filled corner: legible over a framed page of any
				colour, and small enough not to cover any of it. -->
				<span
					class="pointer-events-none absolute right-0.5 bottom-0.5 h-2 w-px bg-gray-400 dark:bg-gray-500"
				></span>
				<span
					class="pointer-events-none absolute right-0.5 bottom-0.5 h-px w-2 bg-gray-400 dark:bg-gray-500"
				></span>
			</div>
		{/if}
	</div>
{/if}
