<script lang="ts">
	import SiteIcon from '$lib/components/SiteIcon.svelte';
	import trending from '$lib/data/trending.json';
	import { visited } from '$lib/visited/store.svelte';

	/**
	 * What the corpus is being read for this week, above the twelve it recommends always.
	 *
	 * The list below this one ranks lifetime Hacker News points, which have barely moved in
	 * a decade. That is its virtue — it names writers worth knowing — and its whole problem:
	 * a reader who comes back sees the same page forever, and nothing on it says the index
	 * knows about anything published since.
	 *
	 * These four are posts rather than blogs, because a blog only trends for one post and
	 * naming the blog throws away the reason it is here. Every one is in the index, checked
	 * at build time, so the section is evidence the search is current rather than a slow
	 * mirror of a site the reader has already read this morning.
	 *
	 * Generated into Git by `make trending` and daily by refresh-trending.yml, so it is
	 * inlined into the prerendered page exactly as the twelve are: no request, no spinner.
	 * See docs/plans/popular-blogs-landing-plan.md.
	 */

	/**
	 * The shape `make trending` writes. Declared rather than inferred from whatever is
	 * checked in, so a change to the generator fails here instead of rendering blanks.
	 */
	type Trending = {
		/** Days of Hacker News the four are drawn from. */
		windowDays: number;
		posts: { title: string; url: string; blog: string; host: string }[];
	};

	const { posts }: Trending = trending;
</script>

<!--
	Guarded, though the generator refuses to write a short list rather than shipping one:
	a heading with nothing under it is the one state this section must never reach, and
	the check costs a line. No transition, for the reason the list below gives at length —
	it is in the prerendered HTML, so a fade would animate something that never arrived.
-->
{#if posts.length}
	<section aria-labelledby="trending-heading" class="mt-10">
		<h2 id="trending-heading" class="text-sm font-medium text-gray-700 dark:text-gray-200">
			Popular this week
		</h2>
		<!-- "This week" and not "right now": the window is seven days, because a
		twenty-four hour one yields three eligible blogs across the whole corpus and cannot
		fill four rows. The heading says what the ranking knows, which is the same rule the
		"Widely shared" heading below follows. -->
		<p class="mt-0.5 text-sm text-gray-500 dark:text-gray-400">
			Posts doing the rounds on Hacker News.
		</p>

		<!-- The same grid, gaps and negative inset as the twelve below, so the two sections
		read as one system rather than two designs that met on a page. -->
		<ul class="-mx-2 mt-3 grid gap-x-4 gap-y-1 sm:grid-cols-2">
			{#each posts as post (post.url)}
				<li>
					<!--
						A link, where the row below is a button, and the difference is the point.
						Browsing a blog keeps a reader in the corpus with twenty of its posts in
						front of them; this row already names the one post they came for, and
						sending them anywhere else would be answering a question they did not ask.

						data-visit opts it into the shared tracker, so an opened post greys the
						same way a result does. No data-preview: the hover panel belongs to a page
						of results a reader is triaging, not to four rows above a search box.
					-->
					<a
						href={post.url}
						target="_blank"
						rel="noopener noreferrer"
						data-visit
						class="flex w-full items-start gap-2.5 rounded-lg px-2 py-2 transition-colors hover:bg-gray-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:hover:bg-gray-800"
					>
						<!-- The same icon at the same size the result card and the twelve wear
						beside a host. mt-1 centres a 16px icon on a 24px first line. -->
						<SiteIcon host={post.host} class="mt-1 h-4 w-4" />
						<span class="min-w-0 flex-1">
							<!-- line-clamp-2 rather than truncate: a post title is a sentence and
							deserves the second line a blog name does not. An opened post takes
							the theme's blue, as it does on a result card. -->
							<span
								class="line-clamp-2 block text-base font-medium {visited.has(post.url)
									? 'text-primary-700 dark:text-primary-400'
									: 'text-gray-900 dark:text-white'}"
							>
								{post.title}
							</span>
							<!-- Who wrote it, in the shade and size every byline on this site uses.
							The blog's name rather than its host: the host is on the icon's job, and
							a name is what a reader recognises. -->
							<span class="block truncate text-sm text-gray-500 dark:text-gray-400">
								{post.blog}
							</span>
						</span>
					</a>
				</li>
			{/each}
		</ul>
	</section>
{/if}
