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
