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

	const label = $derived(theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode');
</script>

<Button color="alternative" class="!p-2.5" pill onclick={toggle} aria-label={label}>
	{#if theme === 'dark'}
		<SunSolid class="h-4 w-4" />
	{:else}
		<MoonSolid class="h-4 w-4" />
	{/if}
</Button>
<Tooltip>{label}</Tooltip>
