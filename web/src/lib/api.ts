/** Mirrors api/internal/article.Result. */
export type SearchResult = {
	url: string;
	title: string;
	author?: string;
	summary?: string;
	topics?: string[];
	publishedAt?: string;
	score: number;
};

export type SearchResponse = {
	query: string;
	count: number;
	results: SearchResult[];
};

// Empty in development, where Vite proxies /api to the Functions host.
const API_BASE = import.meta.env.VITE_API_BASE_URL ?? '';

export async function search(query: string, signal?: AbortSignal): Promise<SearchResponse> {
	const url = `${API_BASE}/api/search?q=${encodeURIComponent(query)}`;
	const response = await fetch(url, { signal });

	if (!response.ok) {
		const body = await response.json().catch(() => null);
		throw new Error(body?.error ?? `The server returned an error (${response.status}).`);
	}

	return response.json();
}
