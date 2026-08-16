<script lang="ts">
	import { Button, Tooltip } from 'flowbite-svelte';
	import { MoonSolid, SunSolid } from 'flowbite-svelte-icons';
	import { readTheme, setTheme, type Theme } from '$lib/theme';

	let theme = $state<Theme>('light');

	$effect(() => {
		theme = readTheme();
	});

	function toggle() {
		theme = theme === 'dark' ? 'light' : 'dark';
		setTheme(theme);
	}
</script>

<Button
	color="alternative"
	class="fixed end-4 top-4 z-50 !p-2.5"
	pill
	onclick={toggle}
	aria-label="Toggle dark mode"
	aria-pressed={theme === 'dark'}
>
	{#if theme === 'dark'}
		<SunSolid class="h-4 w-4" />
	{:else}
		<MoonSolid class="h-4 w-4" />
	{/if}
</Button>
<Tooltip>{theme === 'dark' ? 'Light mode' : 'Dark mode'}</Tooltip>
