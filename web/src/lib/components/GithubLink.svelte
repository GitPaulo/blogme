<script lang="ts">
	import { Tooltip } from 'flowbite-svelte';
	import { GithubSolid } from 'flowbite-svelte-icons';

	const REPO_URL = 'https://github.com/GitPaulo/blogme';
</script>

<!-- A mark at the foot of the page rather than a control at the top of it: no fill, no
	border, no pill. Where it sits, nothing is being offered — the page is over — so it is
	drawn at the weight of the background and lifts to meet a pointer, which is the whole
	of its styling.

	The padding is not chrome and not spacing. It is what makes a twenty-pixel glyph a
	target worth aiming at, and it carries no background, so the link stays the size of a
	control while looking like part of the page. -->
<a
	href={REPO_URL}
	target="_blank"
	rel="noopener noreferrer"
	aria-label="View this project on GitHub"
	class="rounded-full p-2 text-gray-400 transition-colors hover:text-gray-600 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-600 dark:text-gray-600 dark:hover:text-gray-400"
>
	<GithubSolid class="h-5 w-5" />
</a>
<Tooltip>View on GitHub</Tooltip>
