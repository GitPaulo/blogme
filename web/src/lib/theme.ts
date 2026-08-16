const STORAGE_KEY = 'blogme:theme';

export type Theme = 'light' | 'dark';

/** Applies the class the `dark` custom variant in layout.css keys off. */
function apply(theme: Theme) {
	document.documentElement.classList.toggle('dark', theme === 'dark');
}

function preferred(): Theme {
	return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function readTheme(): Theme {
	const stored = localStorage.getItem(STORAGE_KEY);
	return stored === 'light' || stored === 'dark' ? stored : preferred();
}

export function setTheme(theme: Theme) {
	localStorage.setItem(STORAGE_KEY, theme);
	apply(theme);
}
