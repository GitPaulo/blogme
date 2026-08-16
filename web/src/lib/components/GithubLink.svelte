<script lang="ts">
	import { Button, Tooltip } from 'flowbite-svelte';
	import { GithubSolid } from 'flowbite-svelte-icons';

	const REPO_URL = 'https://github.com/GitPaulo/blogme';
</script>

<Button
	color="alternative"
	class="!p-2.5"
	pill
	href={REPO_URL}
	target="_blank"
	rel="noopener noreferrer"
	aria-label="View this project on GitHub"
>
	<GithubSolid class="h-4 w-4" />
</Button>
<Tooltip>View on GitHub</Tooltip>
