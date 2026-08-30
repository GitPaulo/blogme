import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vitest/config';

// Its own config rather than test options bolted onto vite.config.ts.
//
// That file runs the SvelteKit plugin, which wants a route manifest, a generated
// $lib alias and an adapter — none of which a unit test of a pure function needs, and
// all of which have to be in place before it will load. What these tests do need is
// the Svelte compiler, because the modules under test are `.svelte.ts` rune modules:
// `highlight` and `merge` use no runes themselves, but they sit in a file that does,
// and TypeScript alone cannot read it.
export default defineConfig({
	// configFile: false because there is no svelte.config.js to find — this project
	// configures SvelteKit inline in vite.config.ts — and without it the plugin says so
	// on every run.
	plugins: [svelte({ configFile: false, compilerOptions: { runes: true } })],
	resolve: {
		// $lib by hand, since svelte-kit is not here to provide it.
		alias: { $lib: new URL('./src/lib', import.meta.url).pathname }
	},
	test: {
		// Node, not a browser: everything covered here is a pure function or a wrapper
		// over localStorage, and the storage tests bring their own stub. A DOM would be
		// a slower way to run the same assertions.
		environment: 'node',
		include: ['src/**/*.test.ts']
	}
});
