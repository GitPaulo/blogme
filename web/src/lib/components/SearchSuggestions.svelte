<script lang="ts">
	import ClockOutline from 'flowbite-svelte-icons/ClockOutline.svelte';
	import FileLinesOutline from 'flowbite-svelte-icons/FileLinesOutline.svelte';
	import SearchOutline from 'flowbite-svelte-icons/SearchOutline.svelte';
	import { prefersReducedMotion } from 'svelte/motion';
	import { highlight, type Suggestion } from '$lib/suggestions.svelte';

	type Props = {
		/** Ties the list to the search box through aria-controls and aria-activedescendant. */
		id: string;
		/**
		 * Whether the list is showing.
		 *
		 * A prop rather than an `{#if}` around this component, because closing is animated:
		 * the element has to outlive the decision to hide it, and the rows have to be there
		 * to be measured before it is made.
		 */
		open: boolean;
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

	let { id, open, options, query, active, restart, onselect, onhover, onexpire }: Props = $props();

	let list = $state<HTMLElement>();
	// What the rows currently come to. Measured rather than counted, because rows are not
	// all one height once the list is capped and scrolling, and because this is the number
	// the animation below interpolates towards.
	let content = $state(0);

	// Long enough to read as the page making room rather than jumping, short enough that a
	// reader typing the next character is not waiting on it.
	const MOTION_MS = 160;
	const motion = $derived(prefersReducedMotion.current ? 0 : MOTION_MS);

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
		// A closed list has nothing to outstay. Without this the timer would keep firing
		// against a list that is not there, and each expiry marks the query dismissed —
		// which is the state that stops it opening again.
		if (!open) return;

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
		if (!open) return;
		list?.children[active]?.scrollIntoView({ block: 'nearest' });
	});
</script>

<!--
	Part of the layout rather than laid over it: opening the list makes room for itself and
	moves the filter bar and the results down, instead of covering rows the reader cannot
	then read past.

	One animated height covers all three things that change it — opening, closing, and the
	row count moving as suggestions arrive under a reader still typing. The last is why this
	is a transition on a measured height rather than a Svelte in/out transition: those only
	run at the ends of the element's life, so a list that went from five rows to three would
	snap, and take every result below it along.

	Held at the measured height rather than `auto` so there is something to interpolate
	between, and interrupted cleanly: a transition restarted mid-flight carries on from where
	it is, so a reader typing faster than the list settles sees one movement rather than a
	stack of them fighting.
-->
<div
	class="reveal"
	style:height="{open ? content : 0}px"
	style:--motion="{motion}ms"
	inert={!open}
	aria-hidden={!open}
>
	<!-- The measured element, which is everything the open list occupies: the gap under the
	search box, the frame, and the room the frame's shadow needs to fall into without being
	clipped by the overflow that hides the rows on the way down. -->
	<div bind:clientHeight={content} class="pt-1 pb-2">
		<!--
			The frame and the scrolling list are separate elements so the countdown along the
			frame's bottom edge stays where it is while the rows move under it, and `relative` is
			what that countdown is positioned against.

			The height is capped against the window as well as the row count, so a full list on a
			short screen pushes the results out of the way rather than off it.
		-->
		<div
			class="relative overflow-hidden rounded-lg border border-gray-200 bg-white shadow-md dark:border-gray-600 dark:bg-gray-700"
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
		the countdown resets by being replaced. Only drawn while the list is open, so that
		opening it again starts the bar full rather than wherever the last one had drained
		to. Decorative and behind aria-hidden — the listbox above it is what a screen reader
		is told about, and the list closing is announced by the box's own aria-expanded.
	-->
			{#if open}
				{#key restart}
					<div
						class="countdown pointer-events-none absolute inset-x-0 bottom-0 h-0.5 origin-left bg-primary-600 dark:bg-primary-400"
						style="--linger: {LINGER_MS}ms"
						aria-hidden="true"
					></div>
				{/key}
			{/if}
		</div>
	</div>
</div>

<style>
	.reveal {
		/* Clips the rows on their way down, and is what makes the animated height read as
		   the list being uncovered rather than squashed. */
		overflow: hidden;
		transition: height var(--motion) cubic-bezier(0.22, 0.61, 0.36, 1);
	}

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
