<script lang="ts">
	import { ArrowUpRightFromSquareOutline } from 'flowbite-svelte-icons';
	import { prefersReducedMotion } from 'svelte/motion';
	import { fade } from 'svelte/transition';
	import { safeHttpUrl } from '$lib/api';

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
	const GAP = 10;
	const MARGIN = 12;
	// No allow-top-navigation, allow-popups, allow-forms or allow-downloads: the framed
	// page renders, scrolls and follows its own links, and can do nothing else.
	const SANDBOX = 'allow-scripts allow-same-origin';

	type Target = { url: string; host: string; left: number; top: number };

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

	function place(rect: DOMRect) {
		const clamp = (value: number, limit: number) =>
			Math.max(MARGIN, Math.min(value, limit - MARGIN));

		// Beside the link where the viewport has room, so the card being previewed stays
		// readable; otherwise below it, or above when the bottom edge is the closer one.
		const beside =
			rect.right + GAP + WIDTH + MARGIN <= window.innerWidth
				? rect.right + GAP
				: rect.left - GAP - WIDTH;
		if (beside >= MARGIN) {
			return { left: beside, top: clamp(rect.top, window.innerHeight - HEIGHT) };
		}

		const below = rect.bottom + GAP;
		const top = below + HEIGHT + MARGIN <= window.innerHeight ? below : rect.top - GAP - HEIGHT;
		return {
			left: clamp(rect.left, window.innerWidth - WIDTH),
			top: clamp(top, window.innerHeight - HEIGHT)
		};
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

	function openLater(anchor: HTMLAnchorElement) {
		if (anchor.href === target?.url) return;
		warm(anchor.href);
		clearTimeout(openTimer);
		openTimer = setTimeout(() => {
			const url = safeHttpUrl(anchor.href);
			if (!url) return;
			const { left, top } = place(anchor.getBoundingClientRect());
			loading = true;
			target = { url, host: new URL(url).hostname.replace(/^www\./, ''), left, top };
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
			openLater(anchor);
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
				<a
					href={target.url}
					target="_blank"
					rel="noopener noreferrer"
					class="ms-auto flex shrink-0 items-center gap-1 rounded-sm font-medium text-primary-600 hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:text-primary-400"
				>
					Open
					<ArrowUpRightFromSquareOutline class="h-3 w-3" />
				</a>
			</div>

			<div class="relative bg-white" style:height="{HEIGHT}px">
				{#if loading}
					<div class="absolute inset-0 z-10 animate-pulse bg-gray-100 dark:bg-gray-700"></div>
				{/if}
				<!-- Sites that refuse framing leave this blank and there is no way to tell from
				here, so the header carries the host either way. -->
				<iframe
					title="Preview of {target.host}"
					src={target.url}
					sandbox={SANDBOX}
					referrerpolicy="no-referrer"
					class="h-full w-full border-0"
					onload={() => (loading = false)}
				></iframe>
			</div>
		{/key}
	</div>
{/if}
