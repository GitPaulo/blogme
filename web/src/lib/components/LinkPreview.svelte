<script lang="ts">
	import { Badge } from 'flowbite-svelte';
	import { ArrowUpRightFromSquareOutline } from 'flowbite-svelte-icons';
	import { prefersReducedMotion } from 'svelte/motion';
	import { fade } from 'svelte/transition';
	import { safeHttpUrl } from '$lib/api';
	import { visited } from '$lib/visited/store.svelte';

	// One panel for the whole app: anchors opt in with a `data-preview` attribute and the
	// listeners are delegated, so a page of result cards costs nothing until a pointer
	// actually rests on one.

	// A preview is a whole third-party document load, so the dwell is long enough that
	// running the pointer down the list never starts one.
	const DWELL_MS = 350;
	// Long enough to cross the gap between the link and the panel.
	const CLOSE_MS = 200;
	const WIDTH = 420;
	const HEIGHT = 460;
	// A refusal is one line, so its panel is sized to one rather than left as a tall
	// empty box. Both of these are also what the placement below reserves room for.
	const DENIED_HEIGHT = 96;
	// The strip under a preview whose framing nobody has checked. Taken out of HEIGHT
	// rather than added to it, so a panel is the same size either way.
	const NOTE_HEIGHT = 28;
	const GAP = 10;
	// Wider than GAP because a cursor is not a point: the arrow glyph is roughly 20px
	// tall, and a panel edge tucked under it swallows the hover that opened the preview
	// and lands the iframe's scrollbar beneath the pointer.
	const CURSOR_GAP = 24;
	const MARGIN = 12;
	// No allow-top-navigation, allow-popups, allow-forms or allow-downloads: the framed
	// page renders, scrolls and follows its own links, and can do nothing else.
	const SANDBOX = 'allow-scripts allow-same-origin';

	/**
	 * What the crawler found out about this page's framing headers, as the results list
	 * passed it on. `unknown` is not permission — it is a page nobody has checked —
	 * and it is tried, which is what every link did before any of this existed.
	 */
	type Framing = 'allowed' | 'denied' | 'unknown';

	type Target = { url: string; host: string; left: number; top: number; framing: Framing };

	let target = $state.raw<Target | undefined>();
	let loading = $state(false);

	let openTimer: ReturnType<typeof setTimeout> | undefined;
	let closeTimer: ReturnType<typeof setTimeout> | undefined;
	// Preconnecting twice to the same origin is wasted markup, not a wasted connection.
	const warmed = new Set<string>();

	function anchorFrom(node: EventTarget | null) {
		const el = node instanceof Element ? node : undefined;
		return el?.closest('a[data-preview]') as HTMLAnchorElement | null | undefined;
	}

	function inPanel(node: EventTarget | null) {
		return node instanceof Element && node.closest('[data-preview-panel]') !== null;
	}

	const clamp = (value: number, limit: number) => Math.max(MARGIN, Math.min(value, limit - MARGIN));

	// Anchored to the pointer, offset clear of the cursor and flipped to whichever side
	// has room, the way native tooltips and floating-ui popovers avoid clipping.
	function placeAtPoint(x: number, y: number, height: number) {
		const left =
			x + CURSOR_GAP + WIDTH + MARGIN <= window.innerWidth
				? x + CURSOR_GAP
				: x - CURSOR_GAP - WIDTH;
		const top =
			y + CURSOR_GAP + height + MARGIN <= window.innerHeight
				? y + CURSOR_GAP
				: y - CURSOR_GAP - height;
		return {
			left: clamp(left, window.innerWidth - WIDTH),
			top: clamp(top, window.innerHeight - height)
		};
	}

	// Keyboard focus has no pointer position to anchor to, so it falls back to the link's
	// own rect, beside it where the viewport has room and below or above otherwise.
	function placeAtRect(rect: DOMRect, height: number) {
		const beside =
			rect.right + GAP + WIDTH + MARGIN <= window.innerWidth
				? rect.right + GAP
				: rect.left - GAP - WIDTH;
		if (beside >= MARGIN) {
			return { left: beside, top: clamp(rect.top, window.innerHeight - height) };
		}

		const below = rect.bottom + GAP;
		const top = below + height + MARGIN <= window.innerHeight ? below : rect.top - GAP - height;
		return {
			left: clamp(rect.left, window.innerWidth - WIDTH),
			top: clamp(top, window.innerHeight - height)
		};
	}

	/**
	 * What is known about this link's framing. Read from the value of the attribute
	 * that opted it into previews, so a link carrying no answer — indexed before the
	 * crawler read headers, or not a search result at all — reads as unknown.
	 */
	function framingOf(anchor: HTMLAnchorElement): Framing {
		const value = anchor.dataset.preview;
		return value === 'denied' || value === 'allowed' ? value : 'unknown';
	}

	// DNS and TLS on the way in, so the dwell timer is not also paying for the handshake.
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
	}

	function openLater(anchor: HTMLAnchorElement, point?: { x: number; y: number }) {
		if (anchor.href === target?.url) return;
		warm(anchor.href);
		clearTimeout(openTimer);
		openTimer = setTimeout(() => {
			const url = safeHttpUrl(anchor.href);
			if (!url) return;
			const framing = framingOf(anchor);
			const height = framing === 'denied' ? DENIED_HEIGHT : HEIGHT;
			const { left, top } = point
				? placeAtPoint(point.x, point.y, height)
				: placeAtRect(anchor.getBoundingClientRect(), height);
			// Nothing is being waited for when there is no frame to load.
			loading = framing !== 'denied';
			target = { url, host: new URL(url).hostname.replace(/^www\./, ''), left, top, framing };
		}, DWELL_MS);
	}

	function scheduleClose() {
		clearTimeout(closeTimer);
		closeTimer = setTimeout(close, CLOSE_MS);
	}

	function close() {
		clearTimeout(openTimer);
		clearTimeout(closeTimer);
		target = undefined;
	}

	// One handler, because "the pointer is now over something that is neither the open
	// link nor the panel" is exactly the condition for closing.
	function onPointerOver(event: PointerEvent) {
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
		// A preview costs a document load, so devices that cannot hover — and readers who
		// asked for less data — never install any of this.
		const hoverable = window.matchMedia('(hover: hover) and (pointer: fine)').matches;
		const { connection } = navigator as Navigator & { connection?: { saveData?: boolean } };
		if (!hoverable || connection?.saveData) return;

		document.addEventListener('pointerover', onPointerOver);
		document.addEventListener('pointerleave', scheduleClose);
		document.addEventListener('focusin', onFocusIn);
		document.addEventListener('keydown', onKeydown);
		// Scrolling inside the frame never reaches us, so this only fires once the reader
		// has moved the pointer off the panel.
		window.addEventListener('scroll', close, { passive: true });

		return () => {
			document.removeEventListener('pointerover', onPointerOver);
			document.removeEventListener('pointerleave', scheduleClose);
			document.removeEventListener('focusin', onFocusIn);
			document.removeEventListener('keydown', onKeydown);
			window.removeEventListener('scroll', close);
			close();
		};
	});
</script>

{#if target}
	<div
		data-preview-panel
		role="group"
		aria-label="Preview of {target.host}"
		class="fixed z-60 overflow-hidden rounded-lg border border-gray-200 bg-white shadow-2xl dark:border-gray-700 dark:bg-gray-800"
		style:left="{target.left}px"
		style:top="{target.top}px"
		style:width="{WIDTH}px"
		transition:fade={{ duration: prefersReducedMotion.current ? 0 : 120 }}
	>
		<!-- Keyed so every target gets a fresh frame: assigning src to a live iframe pushes
		a session history entry, which would turn the back button into frame navigation. -->
		{#key target.url}
			<div
				class="flex items-center gap-2 border-b border-gray-200 px-3 py-2 text-xs dark:border-gray-700"
			>
				<img
					src="https://{target.host}/favicon.ico"
					alt=""
					class="h-4 w-4 shrink-0 rounded-xs"
					onerror={(event) => event.currentTarget.remove()}
				/>
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
					class="ms-auto flex shrink-0 items-center gap-1 rounded-sm font-medium text-primary-600 hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:text-primary-400"
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
					class="flex items-center justify-center px-6 text-center text-sm text-gray-500 dark:text-gray-400"
					style:height="{DENIED_HEIGHT}px"
				>
					{target.host} does not allow previews. Open it to read the post.
				</p>
			{:else}
				{@const frameHeight = target.framing === 'unknown' ? HEIGHT - NOTE_HEIGHT : HEIGHT}
				<div class="relative bg-white" style:height="{frameHeight}px">
					{#if loading}
						<div class="absolute inset-0 z-10 animate-pulse bg-gray-100 dark:bg-gray-700"></div>
					{/if}
					<!-- A site that refuses framing leaves this blank, and from here there is no
					way to tell that apart from a page that rendered nothing: the frame loads
					either way and its document is cross-origin either way. Which is why the
					answer comes from the crawler rather than from anything measured here. -->
					<iframe
						title="Preview of {target.host}"
						src={target.url}
						sandbox={SANDBOX}
						referrerpolicy="no-referrer"
						class="h-full w-full border-0"
						onload={() => (loading = false)}
					></iframe>
				</div>
				{#if target.framing === 'unknown'}
					<!-- Nobody has read this one's headers yet, so a blank frame has two possible
					meanings and the reader gets told which they might be looking at. Goes away on
					its own as the crawler comes back round to the post. -->
					<p
						class="flex items-center justify-center border-t border-gray-200 px-3 text-center text-xs text-gray-400 dark:border-gray-700 dark:text-gray-500"
						style:height="{NOTE_HEIGHT}px"
					>
						If nothing appears, this site does not allow previews.
					</p>
				{/if}
			{/if}
		{/key}
	</div>
{/if}
