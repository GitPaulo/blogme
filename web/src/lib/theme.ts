const STORAGE_KEY = 'blogme:theme';
const DARK_QUERY = '(prefers-color-scheme: dark)';

export type Theme = 'light' | 'dark';

/** Applies the class the `dark` custom variant in layout.css keys off. */
function apply(theme: Theme) {
	document.documentElement.classList.toggle('dark', theme === 'dark');
}

/** The theme the reader picked, or undefined while they are still following the system. */
function chosen(): Theme | undefined {
	// Storage access throws when cookies are blocked, which must not break the toggle.
	try {
		const stored = localStorage.getItem(STORAGE_KEY);
		if (stored === 'light' || stored === 'dark') return stored;
	} catch {
		// Read as no choice, so the system preference decides.
	}
	return undefined;
}

const systemTheme = (): Theme => (window.matchMedia(DARK_QUERY).matches ? 'dark' : 'light');

export const readTheme = (): Theme => chosen() ?? systemTheme();

export function setTheme(theme: Theme) {
	try {
		localStorage.setItem(STORAGE_KEY, theme);
	} catch {
		// The choice still applies for this session.
	}
	apply(theme);
}

/**
 * Follows the system for as long as the reader has made no choice of their own, reporting
 * each change. The inline script in app.html reads the preference once at load, so without
 * this a laptop switching to dark at sunset leaves the page in the theme it started in.
 */
export function watchSystemTheme(onChange: (theme: Theme) => void): () => void {
	const query = window.matchMedia(DARK_QUERY);
	const update = () => {
		if (chosen()) return; // An explicit choice outranks the system.
		const theme = systemTheme();
		apply(theme);
		onChange(theme);
	};

	query.addEventListener('change', update);
	return () => query.removeEventListener('change', update);
}
