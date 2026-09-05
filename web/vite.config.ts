import tailwindcss from '@tailwindcss/vite';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Set BASE_PATH=blogme when deploying to a GitHub Pages project site.
const basePath = process.env.BASE_PATH?.replace(/^\/+/, '') ?? '';
const base: '' | `/${string}` = basePath === '' ? '' : `/${basePath}`;

export default defineConfig({
	plugins: [
		tailwindcss(),
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			adapter: adapter({ fallback: '404.html' }),
			paths: { base }
		})
	],
	build: {
		rollupOptions: {
			treeshake: {
				/**
				 * Flowbite's components are pure: a component definition and a table of class
				 * names, and nothing that happens on import. Its package.json does not say so,
				 * and without that a bundler has to assume the worst — so every component the
				 * library exports was kept alive by `dist/index.js`, which every component
				 * reaches (Button imports Spinner from it), which the landing page reaches.
				 *
				 * The result was one 309 KB chunk holding the drawer, the modals, the tag
				 * select and the virtual list on a page that draws a search box and six links.
				 * Splitting them out of the page is what the lazy imports are for; this is what
				 * lets the split actually happen.
				 *
				 * A rule rather than a blanket `false`, so this is a claim about one dependency
				 * and not about every dependency. Everything unmatched keeps the default.
				 */
				moduleSideEffects: [{ test: /flowbite-svelte(-icons)?[\\/]dist[\\/]/, sideEffects: false }]
			}
		}
	},
	server: {
		// Lets the browser see a single origin in development, so CORS stays out of the inner loop.
		proxy: {
			'/api': {
				target: 'http://localhost:7071',
				changeOrigin: true
			}
		}
	}
});
