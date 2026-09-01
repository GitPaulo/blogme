<script lang="ts">
	import SiteIcon from '$lib/components/SiteIcon.svelte';
	import popular from '$lib/data/popular.json';

	/**
	 * What the landing page offers before anyone has searched.
	 *
	 * The empty state was a heading, a subtitle and a search box on white, which tells a
	 * reader with no query in mind nothing about what is in here and gives them no way in
	 * that does not start with typing. Twelve blogs they might recognise does both at once,
	 * and every row is a search they did not have to think of.
	 *
	 * The list is generated into Git by `make popular` and imported here, so it is inlined
	 * into the prerendered page: no request, no spinner, and no empty state before the empty
	 * state. See docs/popular-blogs-landing-plan.md for why it is not an API route.
	 */
	let { onpick }: { onpick: (name: string, ids: string[]) => void } = $props();

	// Everything the twelve stand for. Generated alongside them rather than written here,
	// so the sentence cannot drift from the corpus the way a pasted number would.
	const rest = new Intl.NumberFormat().format(popular.corpus - popular.blogs.length);
</script>

<!--
	No transition, in either direction.

	It is in the prerendered HTML, so it is on screen at first paint and a fade would be a
	flicker animating something that never arrived. And it leaves the moment a query is
	typed, which is exactly where an outro is wrong for the reason the suggestion list gives
	at length: an outro holds the element in the document until it finishes, and in a
	background tab, where frames are paused, that is until the reader comes back. A list of
	blog recommendations sitting under a page of results is that bug.
-->
<section aria-labelledby="popular-heading" class="mt-10">
	<h2 id="popular-heading" class="text-sm font-medium text-gray-700 dark:text-gray-200">
		Widely shared
	</h2>
	<!-- Says what the list is, not how it was built. Which signal ranks it, what that
	signal is biased towards and what this site does not measure are all real, and all
	belong in docs/popular-blogs-landing-plan.md rather than above the fold. The heading
	avoids "most popular" for the same reason it is short: it should not claim more than
	the ranking knows. -->
	<p class="mt-0.5 text-sm text-gray-500 dark:text-gray-400">Blogs readers pass around the most.</p>

	<!-- Pulled back by the padding the rows carry, so the icons line up with the heading
	above them rather than sitting eight pixels inside it. The rows keep their padding so
	the hover background has somewhere to be. -->
	<ul class="-mx-2 mt-3 grid gap-x-4 gap-y-1 sm:grid-cols-2">
		{#each popular.blogs as blog (blog.host)}
			<li>
				<!--
					A button, not a link to the blog. Sending a reader straight out to the blog
					from a search engine's front page gives up on what the page is for; opening
					it here keeps them with twenty of its posts in front of them.

					It hands over the source ids rather than the name. Searching for the name
					does not reach the blog: measured over these twelve, only four came back in
					their own top three and two returned none of their own posts at all. See
					docs/popular-blogs-landing-plan.md.

					No hover underline: that is this page's link idiom, and this is not a link.
				-->
				<button
					type="button"
					onclick={() => onpick(blog.name, blog.ids)}
					class="flex w-full items-start gap-2.5 rounded-lg px-2 py-2 text-left transition-colors hover:bg-gray-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:hover:bg-gray-800"
				>
					<!-- The same icon at the same size the result card wears beside a host, so the
					landing page is teaching the vocabulary the results pages use rather than
					inventing a second one. mt-1 centres a 16px icon on a 24px first line. -->
					<SiteIcon host={blog.host} class="mt-1 h-4 w-4" />
					<span class="min-w-0 flex-1">
						<span class="block truncate text-base font-medium text-gray-900 dark:text-white">
							{blog.name}
						</span>
						<!-- gray-500 rather than the 400 that would sit quieter: 400 on white is 3:1,
						under AA for text this size. The same call the result card's byline makes. -->
						<span class="block truncate text-sm text-gray-500 dark:text-gray-400">
							{blog.host}
						</span>
					</span>
				</button>
			</li>
		{/each}
	</ul>

	<!-- Twelve names cannot carry the size of the thing they are drawn from, and the size is
	half of why a reader should search it. -->
	<p class="mt-3 text-sm text-gray-500 dark:text-gray-400">Plus {rest} more in the index.</p>
</section>
