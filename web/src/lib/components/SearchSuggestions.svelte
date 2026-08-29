<script lang="ts">
	import { ClockOutline, SearchOutline } from 'flowbite-svelte-icons';
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
		onselect: (option: Suggestion) => void;
		onhover: (index: number) => void;
	};

	let { id, options, query, active, onselect, onhover }: Props = $props();

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
-->
<ul
	{id}
	bind:this={list}
	role="listbox"
	aria-label="Search suggestions"
	class="absolute inset-x-0 top-full z-30 mt-1 max-h-[min(60vh,18rem)] overflow-y-auto overscroll-contain rounded-lg border border-gray-200 bg-white py-1 shadow-lg dark:border-gray-600 dark:bg-gray-700"
	in:fly={{ y: -SLIDE_PX, duration: prefersReducedMotion.current ? 0 : MOTION_MS }}
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
			{#if option.kind === 'recent'}
				<ClockOutline
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
			Only for remembered searches: a completion is what this list is by default. -->
			{#if option.kind === 'recent'}
				<span class="sr-only">recent search</span>
			{/if}
		</li>
	{/each}
</ul>
