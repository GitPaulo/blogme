<script lang="ts">
	import Button from 'flowbite-svelte/Button.svelte';
	import Card from 'flowbite-svelte/Card.svelte';
	import Heading from 'flowbite-svelte/Heading.svelte';
	import Input from 'flowbite-svelte/Input.svelte';
	import P from 'flowbite-svelte/P.svelte';
	import Spinner from 'flowbite-svelte/Spinner.svelte';
	import Tooltip from 'flowbite-svelte/Tooltip.svelte';
	import ChevronDoubleUpOutline from 'flowbite-svelte-icons/ChevronDoubleUpOutline.svelte';
	import ExclamationCircleOutline from 'flowbite-svelte-icons/ExclamationCircleOutline.svelte';
	import SearchOutline from 'flowbite-svelte-icons/SearchOutline.svelte';
	import WandMagicSparklesOutline from 'flowbite-svelte-icons/WandMagicSparklesOutline.svelte';
	import { tick } from 'svelte';
	import { prefersReducedMotion } from 'svelte/motion';
	import { fade } from 'svelte/transition';
	import { base } from '$app/paths';
	import PopularBlogs from '$lib/components/PopularBlogs.svelte';
	import SearchSuggestions from '$lib/components/SearchSuggestions.svelte';
	import TrendingPosts from '$lib/components/TrendingPosts.svelte';
	import { MAX_QUERY_LENGTH, MAX_SUGGESTIONS, MIN_QUERY_LENGTH } from '$lib/api';
	import { onArticleOpen } from '$lib/articleOpen';
	import { bookmarks } from '$lib/bookmarks/store.svelte';
	import { elementWidth } from '$lib/elementWidth.svelte';
	import { lazy } from '$lib/lazy.svelte';
	import { onScreen } from '$lib/onScreen.svelte';
	import { looksLikeAQuestion } from '$lib/query';
	import { recent } from '$lib/recent.svelte';
	import { createSearch } from '$lib/search.svelte';
	import { snippetBudget } from '$lib/snippet';
	import { merge as mergeSuggestions, suggestions, type Suggestion } from '$lib/suggestions.svelte';

	// Gap left above the row "load more" scrolls to, clearing the fixed toolbar and leaving
	// a sliver of the previous row in view so the batch reads as a continuation.
	const SCROLL_TOP_GAP = 64;
	// What counts as the reader taking the page over mid-load. Read from input rather than
	// from scrollY, because the page also moves under our own smooth scroll and a position
	// check cannot tell the two apart: it cancelled the scroll on every click that landed
	// while the previous one was still animating.
	const SCROLL_INPUT = ['wheel', 'touchmove', 'keydown'] as const;
	const SCROLL_KEYS = new Set([' ', 'ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End']);
	// What a card's padding and border take off the row width before the description
	// gets any: p-4 either side, plus the Card's own hairline.
	const CARD_TEXT_INSET = 34;
	// How many remembered searches may take the top of the dropdown. Two, because they
	// are the reader's own and belong first, and because more than two would start
	// crowding out what the index has to offer on a list this short.
	const MAX_RECENT = 2;
	// The dropdown's own id, which is what ties it to the search box for a screen reader.
	const LISTBOX_ID = 'search-suggestions';
	// Hoisted: building a formatter is the expensive half of rendering the total.
	const decimal = new Intl.NumberFormat();

	// Everything about the search itself — what was asked, what came back, what is still
	// in flight — lives in one place. See lib/search.svelte.ts.
	const search = createSearch();

	// The rows, the filter bar and the empty state, fetched the first time this page has
	// any reason to draw them. Nothing below the search box exists until a search has
	// happened, and all of it was in the chunk a reader downloaded to look at six blogs.
	// See lib/components/resultsUi.ts.
	const resultsUi = lazy(() => import('$lib/components/resultsUi'));

	// The backstop, and the only trigger that is not optional: a link shared with a query
	// in it arrives searchable with nobody having typed anything. The first keystroke asks
	// for the same module earlier — see the field below — which is what puts the fetch
	// alongside the debounce and the request rather than after them.
	$effect(() => {
		if (search.searchable) resultsUi.load();
	});

	// What is left here is the page's own: what has focus, which suggestion is
	// highlighted, and where the reader has scrolled to.

	// Whether the document is taller than the window, which is the only thing that makes
	// a shortcut back to the top worth offering.
	let scrollable = $state(false);
	// The last suggestion the reader picked, so it is not suggested back at them. See
	// `suggested` below.
	let accepted = $state('');
	// Whether the search box has the caret, and whether the reader has dismissed the
	// dropdown for the query now in it. Both are needed: a list that reopened on the next
	// suggestion to arrive would undo the Escape that closed it.
	let searchFocused = $state(false);
	let dismissed = $state(false);
	// The highlighted row, held as the suggestion itself rather than as its position.
	//
	// Suggestions land while the reader is still typing, so the list is reordered under
	// whatever they had arrowed onto. A remembered position would then point at a
	// different row than the one they chose, and Enter would search for it. Holding the
	// text means the highlight follows its row when the list changes around it and lets
	// go when that row is gone, which is both the smoother behaviour and the safe one.
	// Empty for no selection, in which case Enter searches for what was typed.
	let selected = $state('');
	// A reader who has waved the offer of semantic ranking away is not asked again this
	// visit. Keyed by nothing: one dismissal covers the session, because an offer that
	// comes back is the thing that made it annoying.
	let questionHintDismissed = $state(false);
	// Bumped on every sign the reader is still with the list, which restarts the countdown
	// drawn around it. A counter rather than a timestamp: the list only needs to know that
	// something happened, not when.
	let interaction = $state(0);

	let searchInput = $state<HTMLInputElement>();
	// One element per visible result, so children[n] is result n.
	let resultList = $state<HTMLElement>();
	// The row holding "load more" and the shortcut back to the top, watched so the shortcut
	// can return as a floating one once the row itself has scrolled out of reach.
	let controlsRow = $state<HTMLElement>();
	let searchForm = $state<HTMLElement>();

	// The counts and the formatted total are their own deriveds, so the summary string is
	// rebuilt only when a number it actually shows changes. Re-running the filter pass, as
	// a bookmark toggle or a half-typed date bound does, usually leaves both counts where
	// they were, and the total moves only on a new page.
	const totalLabel = $derived(decimal.format(search.total));
	// "about", until the index says there is nothing left. The figure it reports counts
	// documents, and rows are dropped from them after ranking, so it is an upper bound on
	// what paging can reach rather than a number the reader will ever see reached. Written
	// as a flat "of 27" it reads as a promise the last page always breaks.
	const summary = $derived(
		search.partial
			? `Showing ${search.shown} of ${search.loaded} loaded ${search.loaded === 1 ? 'result' : 'results'}`
			: search.exhausted
				? `Showing all ${totalLabel} ${search.total === 1 ? 'result' : 'results'}`
				: `Showing ${search.loaded} of about ${totalLabel} ${search.total === 1 ? 'result' : 'results'}`
	);
	// These filters narrow the rows already fetched rather than the query behind them, so
	// the figure they are counted against climbs every time another page arrives. Said out
	// loud because a total that grows while you page through it otherwise reads as a bug.
	const partialNote =
		'Filters apply to the results loaded so far, not the whole index, so both numbers grow each time you load more.';
	// Said in full for the reader who hovers or tabs to it: nothing matched every word
	// of the query, so these rows match any of them instead.
	const broadenedNote =
		'Nothing matched every word of the search, so these results match any of the words instead.';
	// The button's own label, which a screen reader reads and a tooltip cannot replace:
	// it has to say what pressing it does, in one string, with no markup.
	const rankLabel = $derived(
		search.semanticRanking
			? 'Semantic search reads the query by meaning not keywords. Try using keyword search.'
			: 'Keyword search needs every word to appear. Try using semantic search.'
	);
	// Whether to offer semantic ranking for what has been typed. Only in keyword mode,
	// only once there is a search to talk about, and only until it is waved away — see
	// looksLikeAQuestion for how hard it is made to trigger.
	const offerSemantic = $derived(
		!search.semanticRanking &&
			!questionHintDismissed &&
			looksLikeAQuestion(search.term) &&
			// While a search is in flight, and afterwards only if it found something. A
			// failed search has nothing to do with ranking, and an empty one is already
			// answered by emptyMessage, which makes the same offer where the reader is
			// looking. Two nudges towards the same button is one too many.
			(search.status === 'loading' || search.loaded > 0)
	);
	// Announced rather than shown, so it carries in a sentence what the empty state below
	// lays out in three parts. It names the same destination as that button, because a
	// reader who hears one thing and tabs to another has been told about two controls.
	const emptyMessage = $derived(
		`No results found. Try different words, or switch to ${search.semanticRanking ? 'keyword' : 'semantic'} search.`
	);
	// Filters narrow the rows already fetched rather than the query behind them, so an
	// empty list with pages still to come is a prompt rather than a dead end.
	const noMatchMessage = $derived(
		search.hasMore
			? 'Filters only narrow the results already loaded. Load more to widen what they can reach.'
			: 'Clear or widen a filter to see the rest of what came back.'
	);

	// The bookmarked filter needs the saved keys, which the drawer would otherwise only
	// load on its own schedule.
	$effect(() => {
		bookmarks.load();
	});

	// The page is a search box with a page around it, so the caret starts in it — unless
	// the link already carries a search.
	//
	// Someone opening a shared or bookmarked result set came to read it, and focusing the
	// box for them opens the suggestion list on top of the results the moment they arrive:
	// suggestions for a query they did not type, over the answer they followed the link
	// for. Leaving the caret alone means the list appears when they go to the box, which
	// is when they have asked for it.
	$effect(() => {
		if (search.openedWithSearch) return;
		searchInput?.focus();
	});

	// Watching the document rather than recomputing per render: every filter, page and
	// window resize changes the answer, and the observer already fires on all of them.
	$effect(() => {
		const measure = () => {
			const doc = document.documentElement;
			scrollable = doc.scrollHeight > doc.clientHeight;
		};
		const observer = new ResizeObserver(measure);
		observer.observe(document.documentElement);
		window.addEventListener('resize', measure);
		return () => {
			observer.disconnect();
			window.removeEventListener('resize', measure);
		};
	});

	// Completions for what is in the box. Against `term` rather than the raw query so
	// that the cache behind it is keyed by what the API would be asked anyway: a
	// trailing space is not a different question.
	//
	// A query the reader just picked is asked about as an empty one, which is to say not
	// at all. Left to itself the box would complete what it had only now completed: the
	// list reopens under the cursor showing the one line already in the box, and a
	// request is spent to fetch it. Editing the query again makes it a different one,
	// and suggestions resume on their own.
	const suggested = suggestions(() => (search.term === accepted ? '' : search.term));

	// Remembered searches are matched against whatever is in the box, including the one
	// and two characters the index is never asked about: they are held locally and cost
	// nothing to look through, so there is no reason to make the reader type a third
	// letter before showing them their own history.
	const options = $derived(
		mergeSuggestions(
			recent.matching(search.term, MAX_RECENT),
			suggested.current,
			search.term,
			MAX_SUGGESTIONS
		)
	);
	const suggestionsOpen = $derived(searchFocused && !dismissed && options.length > 0);
	// Where the highlighted suggestion currently sits, or -1 once it is no longer offered.
	// Derived rather than stored, so nothing has to be kept in step with the list.
	const active = $derived(selected ? options.findIndex((o) => o.text === selected) : -1);

	const controlsOnScreen = onScreen(() => controlsRow);
	// Every card is the same width, so one row measurement sizes all of their descriptions.
	const listWidth = elementWidth(() => resultList);
	const summaryChars = $derived(snippetBudget(listWidth.current - CARD_TEXT_INSET));
	// The shortcut exists to get back to the search box, so it has nothing left to offer
	// once the box is in view.
	const searchOnScreen = onScreen(() => searchForm);
	const floatingTop = $derived(
		search.loaded > 0 && !controlsOnScreen.current && !searchOnScreen.current
	);

	// The chase itself lives in the search module; what is left here is the part about
	// where the reader is looking. New rows land below the fold, so without the reveal a
	// click leaves them on the same screen with the button they pressed now buried under
	// everything that arrived — unless they took the page over themselves mid-load, in
	// which case moving it under them would be the rudest thing this page does.
	async function loadMore() {
		const before = search.shown;
		const readerTookOver = watchReaderScroll();

		const landed = await search.loadMore();

		// Detached before anything is decided, never as half of a condition: reading it is
		// also what removes the listeners, so short-circuiting past it leaks a set of them
		// on every click that lands no rows — a filter hiding everything that arrives, the
		// chase running out its budget, a page refused for going too fast.
		const tookOver = readerTookOver();

		// After the chase settles rather than per page, so several pages move the reader once.
		if (landed && !tookOver) await revealNewResults(before);
	}

	/** Reports, once detached, whether the reader scrolled the page while it was called. */
	function watchReaderScroll(): () => boolean {
		let taken = false;
		const mark = (event: Event) => {
			// Every wheel and touch counts; only the keys that actually scroll do.
			if (!(event instanceof KeyboardEvent) || SCROLL_KEYS.has(event.key)) taken = true;
		};
		const options = { passive: true, capture: true } as const;
		for (const type of SCROLL_INPUT) window.addEventListener(type, mark, options);
		return () => {
			for (const type of SCROLL_INPUT) window.removeEventListener(type, mark, options);
			return taken;
		};
	}

	// New rows land below the fold, so without this a click leaves the reader on the same
	// screen with the button they pressed now buried under everything that arrived.
	async function revealNewResults(firstNew: number) {
		await tick(); // The rows are in state but not yet on the page.

		const target = resultList?.children[firstNew];
		if (!target) return;

		// Clamped to what the document can actually offer, so a final short page scrolls to
		// the bottom rather than asking for a position past it. Computed rather than left to
		// scrollIntoView so the destination is a number this code chose and can check.
		const doc = document.documentElement;
		const furthest = doc.scrollHeight - doc.clientHeight;
		const top = window.scrollY + target.getBoundingClientRect().top - SCROLL_TOP_GAP;

		window.scrollTo({
			top: Math.max(0, Math.min(top, furthest)),
			behavior: prefersReducedMotion.current ? 'auto' : 'smooth'
		});
	}

	function toTop() {
		window.scrollTo({ top: 0, behavior: prefersReducedMotion.current ? 'auto' : 'smooth' });
	}

	// Taking a suggestion is choosing a whole query, not completing the word being typed.
	//
	// The list holds whole queries because it has to read as the thing that will be
	// searched for: a dropdown of "query", "queue", "quantum" under a box saying
	// "postgres qu" does not tell a reader what they are about to get. So this replaces
	// what is in the box rather than appending to it. The search needs no prompting —
	// the effect below already watches the query.
	// Picking a blog off the landing page is choosing a query, so it goes through the same
	// motions as accepting a suggestion: the box gets the text, the search that follows is
	// remembered like any other, the dropdown stays shut, and the caret ends up back in the
	// box. The search itself needs no prompting — the module is already watching the query.
	function searchForBlog(name: string, ids: string[]) {
		search.browseBlog(name, ids);
		dismissed = true;
		selected = '';
		recent.record(name);
		searchInput?.focus();
	}

	function acceptSuggestion(option: Suggestion) {
		accepted = option.text;
		search.query = option.text;
		// A suggestion is a query, so taking one leaves whatever blog was being browsed.
		search.leaveBlog();
		dismissed = true;
		selected = '';
		// Taking a suggestion is committing to it, whichever list it came from. A
		// remembered search moves back to the front for having been used again.
		recent.record(option.text);
		// The press was prevented from moving focus, but a click on a row still leaves
		// the caret where the reader last put it. Ending on the box means they can keep
		// typing.
		searchInput?.focus();
	}

	// The combobox keyboard contract. Only the keys that mean something to an open list
	// are taken; everything else, Home and End included, belongs to the text field.
	function onSearchKeydown(event: KeyboardEvent) {
		// Every key, before any of them is read: a reader still typing is a reader still
		// using the list, whether or not the key meant anything to it.
		interaction++;

		if (event.key === 'Escape') {
			// With the list closed, Escape belongs to the browser, which clears a search
			// field with it.
			if (!suggestionsOpen) return;
			event.preventDefault();
			dismissed = true;
			selected = '';
			return;
		}

		if (!suggestionsOpen) return;

		switch (event.key) {
			case 'ArrowDown':
				// Wrapping, and starting from the top on the first press.
				event.preventDefault();
				selected = options[(active + 1) % options.length].text;
				break;
			case 'ArrowUp':
				// Up from nothing is the last row, which is what makes the two symmetrical.
				event.preventDefault();
				selected = options[active <= 0 ? options.length - 1 : active - 1].text;
				break;
			case 'Enter':
				// Only when a row is chosen. Otherwise this is an ordinary submit of
				// whatever was typed, which is what the form below does with it.
				if (active >= 0) {
					event.preventDefault();
					acceptSuggestion(options[active]);
				}
				break;
			case 'Tab':
				// Focus is leaving, so the list has nothing left to be open for.
				dismissed = true;
				break;
		}
	}

	// Taking the offer switches the mode, which the module re-runs the search for.
	// Dismissing it is the same decision made the other way, so both close the row.
	function useSemanticRanking() {
		search.useSemanticRanking();
		questionHintDismissed = true;
		searchInput?.focus();
	}

	// The wordmark is the way back to an empty page. Here that means clearing the search
	// rather than loading anything: this is one route, and the module already puts the
	// results, the filters, the error and the address bar back the moment the box is empty.
	//
	// It is a real link rather than a button so that the browser's own ways of opening one
	// — middle click, ctrl or cmd, "open in new tab" — keep working and still land on a
	// bare page. Only a plain left click is handled here; everything else is left alone,
	// including a click something else has already answered.
	function goHome(event: MouseEvent) {
		if (event.defaultPrevented || event.button !== 0) return;
		if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
		event.preventDefault();
		search.clear();
		// Focused but not opened: `dismissed` is still set from the search being cleared,
		// so the caret lands in the box without a list of suggestions arriving over a page
		// the reader has just asked to be empty.
		searchInput?.focus();
	}

	function onsubmit(event: SubmitEvent) {
		event.preventDefault();
		if (!search.searchable) return;
		dismissed = true;
		// Pressing Enter is the plainest way a reader says this query was the one they
		// meant, which is what makes it worth remembering.
		recent.record(search.term);
		search.submit();
	}

	// Opening a result is the other way of saying a query was the one they meant, and the
	// more common one: most searches here are typed, read and clicked without Enter ever
	// being pressed, so a history that only recorded submissions would stay empty for most
	// readers.
	//
	// Scoped to the list rather than the document, which is the only difference from the
	// visited tracker reading the same anchors: this is about the search behind the click,
	// so a bookmark opened from the drawer is not one of this page's searches. `term` is
	// read when the click lands rather than when this is set up, so it is the query that
	// found the row.
	$effect(() => {
		const list = resultList;
		if (!list) return;
		return onArticleOpen(list, () => recent.record(search.term));
	});
</script>

<svelte:head>
	<title>blogme</title>
	<meta name="description" content="Search across thousands of independent tech blogs." />
</svelte:head>

<!-- The footer no longer sits under this, it floats over it, so the bottom sixteen is back
	to being this element's own: it is the room the repository mark occupies, and what keeps
	the last line of a long page from ending up behind it. -->
<main class="mx-auto max-w-3xl px-6 py-16">
	<Heading tag="h1" class="mb-2">
		<!-- The visible word is the whole accessible name, so no aria-label: a link labelled
		differently from what it reads as is the one thing WCAG's "label in name" asks not to
		do, and "blogme" linking to a bare page is already the pattern every reader knows. -->
		<a
			href={base || '/'}
			onclick={goHome}
			class="wordmark rounded-sm focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-primary-600"
		>
			<!-- The word twice: once solid, once as an outline of itself lying exactly on top.
			Hovering wipes the first away from the left while the second is wiped in, so the
			letters are redrawn as a stencil in the order they would be written.

			A real element rather than a `::before`, because the second copy has to be hidden
			from screen readers and `aria-hidden` is only honoured on real ones — generated
			content is announced by several, which would have made this link read "blogme
			blogme". -->
			<span class="wordmark-fill">blogme</span>
			<span class="wordmark-stencil" aria-hidden="true">blogme</span>
		</a>
	</Heading>
	<P class="mb-8 text-gray-500 dark:text-gray-400">A search engine for tech blogs.</P>

	<!-- The suggestion list sits below the box as part of this form, so opening it makes
	room for itself and moves what follows down rather than covering it. -->
	<form {onsubmit} role="search" bind:this={searchForm} class="relative">
		<Input
			type="search"
			bind:value={search.query}
			bind:elementRef={searchInput}
			size="md"
			placeholder="something you want to read about..."
			class="ps-10 placeholder-gray-400"
			maxlength={MAX_QUERY_LENGTH}
			aria-label="Search query"
			aria-busy={search.status === 'loading'}
			role="combobox"
			aria-expanded={suggestionsOpen}
			aria-controls={suggestionsOpen ? LISTBOX_ID : undefined}
			aria-activedescendant={active >= 0 ? `${LISTBOX_ID}-${active}` : undefined}
			aria-autocomplete="list"
			autocomplete="off"
			onkeydown={onSearchKeydown}
			onfocus={() => (searchFocused = true)}
			onblur={() => (searchFocused = false)}
			oninput={() => {
				dismissed = false;
				// Typing is how a reader leaves a blog and goes back to searching, and it has
				// to be the input event: browsing puts the blog's name in the box itself, so a
				// watcher on the query could not tell the two apart and would close the view
				// the instant it opened.
				search.leaveBlog();
				// The first character is the earliest honest sign that a search is coming, and
				// it is three characters and a debounce ahead of one being sent. Not the focus
				// event, which is not a signal at all: the effect above puts the caret in this
				// box on arrival, so every visit would ask for the result view whether or not
				// anyone went on to search.
				resultsUi.load();
			}}
		>
			{#snippet left()}
				<!--
					pointer-events-auto is load-bearing: Flowbite's left slot is
					pointer-events-none so the icon never eats a click meant for the field.
					type="button" keeps it from submitting the form it sits inside.
				-->
				<button
					type="button"
					onclick={search.toggleRanking}
					aria-pressed={search.semanticRanking}
					aria-label={rankLabel}
					class="pointer-events-auto -m-1 rounded-sm p-1 transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 {search.semanticRanking
						? 'text-primary-600 dark:text-primary-400'
						: 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'}"
				>
					{#if search.status === 'loading'}
						<Spinner size="4" aria-hidden="true" />
					{:else if search.semanticRanking}
						<WandMagicSparklesOutline class="h-4 w-4" aria-hidden="true" />
					{:else}
						<SearchOutline class="h-4 w-4" aria-hidden="true" />
					{/if}
				</button>
				<!--
					Two modes is one more than most search boxes have, so the tooltip has to
					teach rather than label: what this one matches on, what it is good for,
					and two queries worth trying. The button's aria-label says the same thing
					in a sentence, because a tooltip is not reachable by every reader.
				-->
				<Tooltip class="max-w-72 text-left">
					{#if search.semanticRanking}
						<span class="mb-1 flex items-center gap-1.5 font-semibold">
							<WandMagicSparklesOutline class="h-3.5 w-3.5" aria-hidden="true" />
							Semantic: finds the idea
						</span>
						<span class="block text-gray-300 dark:text-gray-400">
							Reads the query as a sentence, so posts that never use your exact words can still come
							back. Best for questions and descriptions.
						</span>
						<span class="mt-1.5 block font-mono text-xs text-gray-200 dark:text-gray-300">
							why is my postgres slow<br />essays about leaving big tech
						</span>
					{:else}
						<span class="mb-1 flex items-center gap-1.5 font-semibold">
							<SearchOutline class="h-3.5 w-3.5" aria-hidden="true" />
							Keyword: matches your words
						</span>
						<span class="block text-gray-300 dark:text-gray-400">
							Every word has to appear. Best for names, libraries and exact phrases.
						</span>
						<span class="mt-1.5 block font-mono text-xs text-gray-200 dark:text-gray-300">
							sean goedecke<br />rust ownership
						</span>
					{/if}
					<span class="mt-1.5 block text-gray-400 dark:text-gray-500">
						Click to switch to {search.semanticRanking ? 'keyword' : 'semantic'} search.
					</span>
				</Tooltip>
			{/snippet}
		</Input>

		<SearchSuggestions
			id={LISTBOX_ID}
			open={suggestionsOpen}
			{options}
			{active}
			query={search.term}
			restart={interaction}
			onselect={acceptSuggestion}
			onhover={(index) => {
				// A pointer on the list is the other way of still being with it, and the
				// one that matters most: without this the list can close under a hand on
				// its way to the row it was reaching for.
				interaction++;
				selected = options[index].text;
			}}
			onexpire={() => {
				dismissed = true;
				selected = '';
			}}
		/>
	</form>

	{#if search.tooShort}
		<P size="sm" class="mt-2 text-gray-500 dark:text-gray-400">
			Type at least {MIN_QUERY_LENGTH} characters to search.
		</P>
	{/if}

	<!--
		Offered rather than applied. Switching somebody's search mode out from under them
		because their words looked a certain way is the kind of help that is hard to
		undo; a row they can take or wave away is not. It only appears for a query that
		reads as a question, which looksLikeAQuestion is deliberately strict about.

		Arriving is animated and leaving is not, for the reason the suggestion list gives
		at length: an outro holds the element in the document until it finishes, and in a
		background tab, where animation frames are paused, that is until the reader comes
		back. A row offering to re-rank a query they have since replaced is worse than no
		animation at all.
	-->
	{#if offerSemantic}
		<div
			class="mt-2 flex items-center gap-2 text-sm text-gray-600 dark:text-gray-400"
			in:fade={{ duration: prefersReducedMotion.current ? 0 : 120 }}
		>
			<WandMagicSparklesOutline
				class="h-4 w-4 shrink-0 text-primary-600 dark:text-primary-400"
				aria-hidden="true"
			/>
			<span class="min-w-0">Question detected. Semantic ranking finds the idea.</span>
			<button
				type="button"
				class="shrink-0 rounded-sm font-medium text-primary-600 underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:text-primary-400"
				onclick={useSemanticRanking}
			>
				Try it
			</button>
			<button
				type="button"
				class="shrink-0 rounded-sm px-1 text-gray-400 hover:text-gray-600 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:hover:text-gray-200"
				onclick={() => (questionHintDismissed = true)}
				aria-label="Dismiss the suggestion to use semantic ranking"
			>
				&times;
			</button>
		</div>
	{/if}

	<p class="sr-only" role="status">
		{#if search.status === 'loading'}
			Searching
		{:else if search.loadingMore}
			Loading more results
		{:else if search.status === 'done'}
			{search.loaded === 0 ? emptyMessage : summary}
		{/if}
	</p>

	<!-- Same card as the empty-state below it, so an error reads as another answer
	the page has for you rather than a system message interrupting it. Only the text
	carries red, not the box: red-500 (Flowbite's default) on this card's gray-50 is
	3.99:1 and its dark variant on gray-900 is 3.79:1, both short of the 4.50:1 AA
	wants, so it's darkened to red-800, which clears both (8.85:1 and 5.98:1). -->
	{#if search.error}
		<Card
			class="mt-6 max-w-none flex-row items-start gap-3 bg-gray-50 p-4 shadow-none dark:bg-gray-900"
		>
			<ExclamationCircleOutline
				class="mt-0.5 h-5 w-5 shrink-0 text-red-800 dark:text-red-800"
				aria-hidden="true"
			/>
			<div>
				<p class="font-medium text-red-800 dark:text-red-800">Search failed</p>
				<p class="mt-1 text-sm text-red-800 dark:text-red-800">{search.error}</p>
			</div>
		</Card>
	{/if}

	{#if search.searchable}
		<!-- The result view arrives a moment after the query does, because it is fetched
		rather than shipped. Every use of it below is guarded on this one value: results the
		page is still loading the components for read as results that have not arrived yet,
		which is the state the reader is already in. -->
		{@const ui = resultsUi.current}
		<div class="mt-8">
			<!-- One guard for the whole result view: the summary, the filters, the rows and
			the controls each describe a set of loaded results, and none of them mean anything
			without one. Gated on what was loaded rather than on what survives the filters, so
			a filter narrow enough to match nothing still leaves the bar that undoes it. -->
			{#if search.loaded > 0}
				<!-- The sentence is aria-hidden because the live region above already reads it.

				The notes beside it are not, and they are not buttons either. They were, so that
				a Flowbite tooltip had something to hang off — but a button that does nothing
				when pressed is a control by every measure a reader has, and the aria-label
				carrying the whole explanation replaced the visible name, which is the one thing
				"label in name" asks not to do. That is the rule the wordmark below is written
				to honour, so honouring it here too.

				Now the note is an ordinary span: the visible word is the word, the explanation
				is read inline by a screen reader, and the tooltip is what it always was for a
				pointer. What this costs is the sighted keyboard-only reader, who could focus
				the old button to see the tooltip and now cannot. That is the smaller loss —
				the explanation is a footnote, not a control, and nothing on this page depends
				on having read it.

				Ordinary inline text rather than a flex row, because that is what it is: a
				sentence with two notes hung off the end of it. As flex items the notes could
				not wrap, so on a narrow screen the sentence broke over two lines and left
				"broadened" stranded out to the right of the first one, reading as though it
				annotated nothing. The non-breaking space is what keeps a note attached to the
				word before it when the line does break.

				The sentence carries the `aria-hidden`, not the row: the live region above
				reads the sentence and nothing else, so the notes have to stay audible. Each
				tooltip follows its own note, which is how Flowbite finds what it belongs to. -->
				<div class="text-sm text-gray-500 tabular-nums dark:text-gray-400">
					<span aria-hidden="true">{summary}</span>
					{#if search.broadened}
						<span class="cursor-help"
							>&nbsp;· broadened<span class="sr-only">. {broadenedNote}</span></span
						>
						<Tooltip class="max-w-64 text-center">{broadenedNote}</Tooltip>
					{/if}
					{#if search.partial}
						<!-- The space before this note is load-bearing where the one above has none:
						that note's explanation opens with the full stop that ends the word in front
						of it, and this one opens with a word, which would otherwise be read fused to
						the asterisk. -->
						<span class="cursor-help"
							>&nbsp;*
							<span class="sr-only">{partialNote}</span></span
						>
						<Tooltip class="max-w-64 text-center">{partialNote}</Tooltip>
					{/if}
				</div>

				{#if ui}
					<ui.FilterBar results={search.results} bind:filters={search.filters} />
				{/if}

				{#if search.shown === 0}
					<!-- The same card the results are in, because it stands where a result would.
					A failed search gets its own card with red text below; this one is empty, not
					broken, so it stays neutral.

					The way out is already on screen: Load more sits directly below, and the
					filters that emptied the list are directly above. A third copy of either here
					would be a second place to look for the same control. -->
					<!-- Grey rather than the white a result card is, and flat rather than lifted:
					the card outline says a row belongs here, and the recessed surface inside it says
					none arrived. In dark mode that means dropping to the page's own gray-900, which
					leaves an outlined well where the lifted gray-800 of a result would be. -->
					<Card
						class="mt-4 max-w-none flex-col items-start gap-1 bg-gray-50 p-4 shadow-none dark:bg-gray-900"
						role="status"
					>
						<p class="font-medium text-gray-900 dark:text-white">
							No loaded results match these filters
						</p>
						<p class="text-sm text-gray-600 dark:text-gray-400">{noMatchMessage}</p>
					</Card>
				{/if}

				<!-- A replaced set of results is faded through rather than cut to: the rows on
				screen are the previous search's until the new ones land, and dimming them says so
				while the page is still answering. One property on one element, so the cost does not
				grow with the number of rows underneath it. -->
				<div
					class="mt-3 space-y-4 motion-safe:transition-opacity motion-safe:duration-200 {search.status ===
					'loading'
						? 'opacity-40'
						: ''}"
					bind:this={resultList}
				>
					{#if ui}
						{#each search.filtered as result (result.url)}
							<ui.ResultCard {result} {summaryChars} />
						{/each}
					{/if}
				</div>

				<div class="mt-6 flex items-center justify-center gap-2" bind:this={controlsRow}>
					<!-- Present for as long as there are results, so the end of the list is a
					disabled button rather than a control that vanishes from under the pointer. -->
					<Button
						color="alternative"
						loading={search.loadingMore}
						disabled={!search.hasMore}
						onclick={loadMore}
					>
						Load more
					</Button>
					<!-- Stood down while the floating copy below has it, so the shortcut is never
					two tab stops, one of them off screen and scrolling the page when focused. -->
					{#if scrollable && !floatingTop}
						<!-- Same button, squared off around the icon: a shortcut back is a peer of
						the way forward, not a different kind of control. The icon is sized to the
						text line box beside it so both buttons come out the same height. -->
						<Button
							color="alternative"
							class="shrink-0 !px-3"
							onclick={toTop}
							aria-label="Back to top"
						>
							<ChevronDoubleUpOutline class="h-5 w-5" />
						</Button>
						<Tooltip>Back to top</Tooltip>
					{/if}
				</div>

				<!-- The same shortcut, floated once its row has scrolled away and the search box
				with it. Bottom end rather than bottom centre: beside the results, within reach of
				a thumb, clear of the toolbar at the top. Sized to a 44px touch target, which the
				inline one does not need because a pointer is already on the row it sits in. -->
				{#if floatingTop}
					<div
						class="fixed end-4 bottom-4 z-40"
						transition:fade={{ duration: prefersReducedMotion.current ? 0 : 150 }}
					>
						<Button
							color="alternative"
							class="size-11 shrink-0 !p-0 shadow-lg"
							onclick={toTop}
							aria-label="Back to top"
						>
							<ChevronDoubleUpOutline class="h-5 w-5" />
						</Button>
						<Tooltip>Back to top</Tooltip>
					</div>
				{/if}
			{:else if search.status === 'done' && ui}
				<ui.EmptyResults semanticRanking={search.semanticRanking} ontoggle={search.toggleRanking} />
			{/if}
		</div>
	{:else}
		<!-- The other half of the same gate. The result view and these are the two things
		that can occupy the page below the search box, they are mutually exclusive, and
		saying so here is what keeps them from ever being on screen together.

		This week's reading above what the corpus recommends always: four posts that answer
		"is anything happening here?", then six blogs that answer "what is in here?".
		Both are generated into Git and inlined into this page, so neither costs a request. -->
		<TrendingPosts />
		<PopularBlogs onpick={searchForBlog} />
	{/if}
</main>

<style>
	/* The wordmark is the only piece of brand on the page, so it gets the only flourish:
	   hovering redraws it as a stencil of itself, left to right, as though the outline were
	   being written. The two copies are stacked exactly, and one mask wipes the solid away
	   while its inverse wipes the outline in — so at every moment of the sweep the word is
	   whole, outlined on the left and solid on the right, never half missing.

	   `currentColor` throughout, so the stencil is white on the dark theme and near-black on
	   the light one without either being named here.

	   Everything is decoration and the link is not, which is what the guards are for. Under
	   `prefers-reduced-motion` none of it applies and the word is simply solid. And the whole
	   effect asks for masks and a text stroke together, because the fill is hidden by a mask
	   and the outline is drawn by a stroke: a browser with one and not the other would wipe
	   the word away and put nothing in its place. */
	.wordmark {
		position: relative;
		display: inline-block;
	}

	/* Hidden until the support test below says the effect can be drawn at all, so a browser
	   that cannot mask never stacks two solid copies of the word on top of each other. */
	.wordmark-stencil {
		display: none;
	}

	@media (prefers-reduced-motion: no-preference) {
		/* The sweep is a hover flourish and a touch screen has no hover: what it has is a
		   `:hover` that latches on tap and stays latched until something else is tapped, so
		   on a phone the word simply sat there hollow — an outline with no sweep to explain
		   it. Gated rather than rewritten as a tap animation, because the wordmark's tap job
		   is to go home and clear the search; playing 550ms of decoration over a navigation
		   is not worth the delay it implies. `pointer: fine` as well as `hover`, so a device
		   that reports a hover it can only fake through touch is excluded too. Keyboard
		   focus still gets the effect wherever the query passes; on a phone there is no
		   focus ring to pair it with anyway. */
		@media (hover: hover) and (pointer: fine) {
			@supports (
				(
						(mask-image: linear-gradient(#000, #000)) or
							(-webkit-mask-image: linear-gradient(#000, #000))
					)
					and (-webkit-text-stroke: 1px red)
			) {
				.wordmark-fill,
				.wordmark-stencil {
					/* The gradient is a hard edge, twice the width of the word, so sliding it is a
					   wipe rather than a fade. Repeated down the axis it does not vary in, so a box
					   shorter than the ink inside it still gets a mask everywhere. */
					-webkit-mask-repeat: repeat-y;
					mask-repeat: repeat-y;
					-webkit-mask-size: 200% 100%;
					mask-size: 200% 100%;
					-webkit-mask-position: 100% 0;
					mask-position: 100% 0;
					transition:
						-webkit-mask-position 550ms ease-out,
						mask-position 550ms ease-out;
				}

				.wordmark-fill {
					-webkit-mask-image: linear-gradient(90deg, transparent 50%, #000 50%);
					mask-image: linear-gradient(90deg, transparent 50%, #000 50%);
				}

				.wordmark-stencil {
					display: block;
					position: absolute;
					top: -0.2em;
					left: 0;
					/* A mask paints nothing outside the box it is clipped to, however far it
					   repeats, and both the ascenders of "b"/"l" and the tail of the "g" reach
					   past a block box one line high — so the box is grown to hold them.
					   Padding rather than height, so it follows the type size, and on an
					   out-of-flow element it moves nothing by itself; the negative `top` above
					   pairs with `padding-top` below so the box grows upward without dragging the
					   text down with it. The solid copy needs none of this: an inline box is
					   already as deep as the font's ascent and descent. Absent it, the ascenders
					   and the descender are cut clean off for the length of the sweep. */
					padding-top: 0.2em;
					padding-bottom: 0.25em;
					-webkit-text-fill-color: transparent;
					-webkit-text-stroke: 1.5px currentColor;
					-webkit-mask-image: linear-gradient(90deg, #000 50%, transparent 50%);
					mask-image: linear-gradient(90deg, #000 50%, transparent 50%);
				}

				/* Both halves move together, so the seam between solid and outline is a single
				   edge travelling across the word rather than two that can drift apart. */
				.wordmark:hover .wordmark-fill,
				.wordmark:focus-visible .wordmark-fill,
				.wordmark:hover .wordmark-stencil,
				.wordmark:focus-visible .wordmark-stencil {
					-webkit-mask-position: 0 0;
					mask-position: 0 0;
				}
			}
		}

		.wordmark {
			transition: transform 150ms ease-out;
		}

		.wordmark:active {
			transform: scale(0.97);
		}
	}
</style>
