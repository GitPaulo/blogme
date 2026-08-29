<script lang="ts">
	import { ClockOutline, FileLinesOutline, SearchOutline } from 'flowbite-svelte-icons';
	import { prefersReducedMotion } from 'svelte/motion';
	import { fly } from 'svelte/transition';
	import { highlight, type Suggestion } from '$lib/suggestions.svelte';

	type Props = {
		/** Ties the list to the search box through aria-controls and aria-activedescendant. */
		id: string;
		options: Suggestion[];
		/** What has been typed, so each row can show which of its words are the reader's. */
		query: string;
		/** Which row the keyboard is on, or -1 for none. */
		active: number;
		/**
		 * Bumped by the caller whenever the reader does something — a key, a row hovered.
		 * Every change restarts the countdown, which is the whole of this component's
		 * interest in what that something was.
		 */
		restart: number;
		onselect: (option: Suggestion) => void;
		onhover: (index: number) => void;
		/** Called when the countdown runs out and the list has outstayed its welcome. */
		onexpire: () => void;
	};

	let { id, options, query, active, restart, onselect, onhover, onexpire }: Props = $props();

	let list = $state<HTMLElement>();

	// Short enough to feel like the list was already there, long enough not to snap. The
	// slide is four pixels, which reads as the list settling out of the box above it
	// rather than as an animation anyone is meant to notice.
	const MOTION_MS = 120;
	const SLIDE_PX = 4;

	// Opening is animated and closing is not, which is the asymmetry a dropdown wants:
	// arriving gently reads as the list settling into place, while leaving gently reads
	// as a list that will not go away. It also keeps the markup honest. An outro holds
	// the element in the document until it finishes, so for those frames the box says
	// aria-expanded="false" above a listbox that is still there — and in a background
	// tab, where animation frames are paused, "those frames" can be the whole time the
	// reader is away.

	/**
	 * How long the list stays up after the reader stops touching it.
	 *
	 * A suggestion list is an interruption that has not been dismissed. Five seconds is
	 * long enough to read five rows and decide against them, and short enough that a list
	 * left behind by someone who has moved on to the results does not sit over them.
	 * Every keystroke and every row hovered starts it again, so it only ever expires on a
	 * reader who has stopped.
	 */
	const LINGER_MS = 5_000;

	// The countdown proper. Kept as a timer of its own rather than read off the end of the
	// bar's animation, because the bar does not animate for a reader who has asked for
	// less motion — and the list still has to close for them.
	$effect(() => {
		void restart;
		// Called through a wrapper rather than handed over directly, so that reading it
		// happens when the timer fires instead of while this is being set up. Passed
		// straight to setTimeout it would be read here, which makes it a dependency — and
		// the caller writes it as an inline arrow, so every re-render of the page would
		// hand over a new one and silently restart the countdown. The bar is keyed on
		// `restart` alone, so the two would drift: the bar would empty while the list sat
		// there waiting on a timer that had been quietly reset behind it.
		const timer = setTimeout(() => onexpire(), LINGER_MS);
		return () => clearTimeout(timer);
	});

	// Keeps the keyboard's row in view once the list is taller than the space it has —
	// on a short window, or a phone with half the screen given to a keyboard. "nearest"
	// so a row already on screen never scrolls, which would otherwise move the list
	// under the reader on every arrow press. Instant, not smooth: an animated scroll
	// cannot keep up with a held arrow key, and lands somewhere behind it.
	$effect(() => {
		list?.children[active]?.scrollIntoView({ block: 'nearest' });
	});
</script>

<!--
	Absolutely positioned, so opening it never moves the page underneath: the results, the
	filter bar and the reader's scroll position all stay exactly where they were. inset-x-0
	ties its width to the search box, which is what keeps it inside the viewport on a
	narrow screen, and the height is capped against the window rather than the row count so
	it cannot run off the bottom of a short one.

	z-30 sits above the results and below both the fixed toolbar and the floating shortcut,
	neither of which can be on screen at the same time as this: the shortcut appears only
	once the search box has scrolled away.

	The frame and the scrolling list are separate elements so the countdown along the
	frame's bottom edge stays where it is while the rows move under it.
-->
<div
	class="absolute inset-x-0 top-full z-30 mt-1 overflow-hidden rounded-lg border border-gray-200 bg-white shadow-lg dark:border-gray-600 dark:bg-gray-700"
	in:fly={{ y: -SLIDE_PX, duration: prefersReducedMotion.current ? 0 : MOTION_MS }}
>
	<ul
		{id}
		bind:this={list}
		role="listbox"
		aria-label="Search suggestions"
		class="max-h-[min(60vh,18rem)] overflow-y-auto overscroll-contain py-1"
	>
		{#each options as option, i (option.kind + option.text)}
			<!--
				mousedown rather than click, with the default prevented: the press would
				otherwise take focus off the search box, and the blur that follows closes this
				list before the click lands on it. Preventing it keeps the caret where it was
				and makes the press the whole interaction.

				mousemove rather than mouseenter, because the list scrolls under a still pointer
				during keyboard navigation, and mouseenter fires on that — the row that happened
				to arrive under the cursor would steal the selection from the arrow keys.
			-->
			<li
				id="{id}-{i}"
				role="option"
				aria-selected={i === active}
				class="flex cursor-pointer items-center gap-2.5 px-3 py-2 text-sm transition-colors duration-75 {i ===
				active
					? 'bg-gray-100 dark:bg-gray-600'
					: ''}"
				onmousedown={(event) => {
					event.preventDefault();
					onselect(option);
				}}
				onmousemove={() => onhover(i)}
			>
				<!-- What the row is: a search this browser has run before, an article somebody
			wrote, or a phrase the suggester assembled from a pair of terms. -->
				{#if option.kind === 'recent'}
					<ClockOutline
						class="h-4 w-4 shrink-0 text-gray-400 dark:text-gray-400"
						aria-hidden="true"
					/>
				{:else if option.kind === 'title'}
					<FileLinesOutline
						class="h-4 w-4 shrink-0 text-gray-400 dark:text-gray-400"
						aria-hidden="true"
					/>
				{:else}
					<SearchOutline
						class="h-4 w-4 shrink-0 text-gray-400 dark:text-gray-400"
						aria-hidden="true"
					/>
				{/if}
				<!-- Truncated rather than wrapped: a row is one line, so a long completion
				cannot make the list taller or push it wider than the box above it.

				The matched run is the one in bold, which is the reader's own words inside the
				suggestion. Segments rather than interpolated markup, so a title carrying
				anything that looks like a tag is escaped like any other text. -->
				<span class="truncate text-gray-900 dark:text-gray-100">
					{#each highlight(option.text, query) as segment, s (s)}{#if segment.match}<span
								class="font-semibold">{segment.text}</span
							>{:else}{segment.text}{/if}{/each}
				</span>
				<!-- Said in words as well as in an icon, for a reader who cannot see the icon.
				Not for completions: a phrase the suggester assembled is what this list is by
				default, and naming it on every row would be noise on most of them. -->
				{#if option.kind === 'recent'}
					<span class="sr-only">recent search</span>
				{:else if option.kind === 'title'}
					<span class="sr-only">article title</span>
				{/if}
			</li>
		{/each}
	</ul>

	<!--
		The countdown, drawn along the bottom edge of the box.

		The bottom border is what the reader is already looking past on their way down the
		list, so the time left reads at a glance without a control appearing to hold it. It
		is scaled rather than resized — a transform is composited, so the bar cannot cost a
		layout on any of the frames it draws — and it retreats leftwards from the full width
		of the box, which needs no measuring: `inset-x-0` is however wide the box turned out
		to be.

		Keyed on `restart`, because restarting a CSS animation means starting a new element:
		the countdown resets by being replaced. Decorative and behind aria-hidden — the
		listbox above it is what a screen reader is told about, and the list closing is
		announced by the box's own aria-expanded.
	-->
	{#key restart}
		<div
			class="countdown pointer-events-none absolute inset-x-0 bottom-0 h-0.5 origin-left bg-primary-600 dark:bg-primary-400"
			style="--linger: {LINGER_MS}ms"
			aria-hidden="true"
		></div>
	{/key}
</div>

<style>
	.countdown {
		animation: drain var(--linger) linear forwards;
	}

	@keyframes drain {
		from {
			transform: scaleX(1);
		}
		to {
			transform: scaleX(0);
		}
	}

	/* The countdown still runs — the list still closes — but it stops being drawn, because
	   a bar sliding along the edge of the screen is exactly what was asked not to happen.
	   The border underneath is unaffected, so the box looks like any other dropdown. */
	@media (prefers-reduced-motion: reduce) {
		.countdown {
			display: none;
		}
	}
</style>
