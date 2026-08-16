const STORAGE_KEY = 'blogme:theme';

export type Theme = 'light' | 'dark';

/** Applies the class the `dark` custom variant in layout.css keys off. */
function apply(theme: Theme) {
	document.documentElement.classList.toggle('dark', theme === 'dark');
}

function preferred(): Theme {
	return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

// Storage access throws when cookies are blocked, which must not break the toggle.
export function readTheme(): Theme {
	try {
		const stored = localStorage.getItem(STORAGE_KEY);
		if (stored === 'light' || stored === 'dark') return stored;
	} catch {
		// Fall through to the system preference.
	}
	return preferred();
}

export function setTheme(theme: Theme) {
	try {
		localStorage.setItem(STORAGE_KEY, theme);
	} catch {
		// The choice still applies for this session.
	}
	apply(theme);
}
