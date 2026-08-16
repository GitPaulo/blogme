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

/** Short queries match almost everything, so they are not worth a round trip. */
export const MIN_QUERY_LENGTH = 3;
/** Mirrors maxQueryLen in api/internal/httpapi, so the server never has to reject us. */
export const MAX_QUERY_LENGTH = 512;

/** Mirrors maxLimit in api/internal/httpapi. */
const MAX_RESULTS = 50;
const MAX_TEXT_LENGTH = 2_000;
const MAX_TOPICS = 12;
const MAX_ERROR_LENGTH = 200;
const REQUEST_TIMEOUT_MS = 15_000;

// Empty in development, where Vite proxies /api to the Functions host.
const API_BASE = import.meta.env.VITE_API_BASE_URL ?? '';

/** Indexed content is third-party data, so only plain web links are ever rendered. */
export function safeHttpUrl(value: unknown): string | undefined {
	if (typeof value !== 'string') return undefined;
	try {
		const url = new URL(value);
		return url.protocol === 'http:' || url.protocol === 'https:' ? url.href : undefined;
	} catch {
		return undefined;
	}
}

function text(value: unknown, max = MAX_TEXT_LENGTH): string | undefined {
	if (typeof value !== 'string') return undefined;
	const trimmed = value.trim().slice(0, max);
	return trimmed || undefined;
}

function toResult(value: unknown): SearchResult | undefined {
	if (typeof value !== 'object' || value === null) return undefined;
	const raw = value as Record<string, unknown>;

	const url = safeHttpUrl(raw.url);
	if (!url) return undefined;

	const topics = Array.isArray(raw.topics)
		? raw.topics
				.map((topic) => text(topic, 64))
				.filter((topic): topic is string => topic !== undefined)
				.slice(0, MAX_TOPICS)
		: undefined;

	return {
		url,
		title: text(raw.title, 300) ?? url,
		author: text(raw.author, 120),
		summary: text(raw.summary),
		topics: topics?.length ? topics : undefined,
		publishedAt: text(raw.publishedAt, 40),
		score: typeof raw.score === 'number' && Number.isFinite(raw.score) ? raw.score : 0
	};
}

/** The response is untrusted input: anything unexpected is dropped rather than rendered. */
function toResponse(body: unknown, query: string): SearchResponse {
	const raw = (typeof body === 'object' && body !== null ? body : {}) as Record<string, unknown>;
	const results = (Array.isArray(raw.results) ? raw.results : [])
		.slice(0, MAX_RESULTS)
		.map(toResult)
		.filter((result): result is SearchResult => result !== undefined);

	return { query, count: results.length, results };
}

export async function search(query: string, signal?: AbortSignal): Promise<SearchResponse> {
	const term = query.trim().slice(0, MAX_QUERY_LENGTH);
	const url = `${API_BASE}/api/search?q=${encodeURIComponent(term)}`;

	// A hung request would otherwise leave the UI loading forever.
	const timeout = AbortSignal.timeout(REQUEST_TIMEOUT_MS);
	const combined = signal ? AbortSignal.any([signal, timeout]) : timeout;

	let response: Response;
	try {
		response = await fetch(url, { signal: combined, headers: { Accept: 'application/json' } });
	} catch (e) {
		if (timeout.aborted && !signal?.aborted) throw new Error('The search timed out. Try again.');
		throw e;
	}

	const body = await response.json().catch(() => null);

	if (!response.ok) {
		const message = text((body as Record<string, unknown> | null)?.error, MAX_ERROR_LENGTH);
		throw new Error(message ?? `The server returned an error (${response.status}).`);
	}

	return toResponse(body, term);
}
