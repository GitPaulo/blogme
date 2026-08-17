/** How an article was discovered. Mirrors the origin constants in api/internal/article. */
export type Origin = 'feed' | 'sitemap';

/** Mirrors api/internal/article.Result. */
export type SearchResult = {
	url: string;
	title: string;
	author?: string;
	origin?: Origin;
	summary?: string;
	topics?: string[];
	publishedAt?: string;
	score: number;
};

export type SearchResponse = {
	query: string;
	/** Rows in this page; total counts every match in the corpus. */
	count: number;
	total: number;
	offset: number;
	results: SearchResult[];
};

/** Short queries match almost everything, so they are not worth a round trip. */
export const MIN_QUERY_LENGTH = 3;
/** Mirrors maxQueryLen in api/internal/httpapi, so the server never has to reject us. */
export const MAX_QUERY_LENGTH = 512;
/** Mirrors defaultLimit in api/internal/httpapi. */
export const PAGE_SIZE = 20;
/** How results are ranked. Mirrors the Rank constants in api/internal/index. */
export type Rank = 'semantic' | 'keyword';

/**
 * How deep each mode may page, mirroring maxOffsetFor in api/internal/httpapi.
 * Semantic reranking only reorders the top 50 matches, so its tail is not offered
 * rather than served with quietly worse ranking; keyword ranking scores the whole
 * result set and can go deeper.
 */
export const MAX_OFFSET_SEMANTIC = 30;
export const MAX_OFFSET_KEYWORD = 1000;

export const maxOffsetFor = (rank: Rank): number =>
	rank === 'keyword' ? MAX_OFFSET_KEYWORD : MAX_OFFSET_SEMANTIC;

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
		origin: raw.origin === 'sitemap' || raw.origin === 'feed' ? raw.origin : undefined,
		summary: text(raw.summary),
		topics: topics?.length ? topics : undefined,
		publishedAt: text(raw.publishedAt, 40),
		score: typeof raw.score === 'number' && Number.isFinite(raw.score) ? raw.score : 0
	};
}

/** The response is untrusted input: anything unexpected is dropped rather than rendered. */
function toResponse(body: unknown, query: string, offset: number): SearchResponse {
	const raw = (typeof body === 'object' && body !== null ? body : {}) as Record<string, unknown>;
	const results = (Array.isArray(raw.results) ? raw.results : [])
		.slice(0, MAX_RESULTS)
		.map(toResult)
		.filter((result): result is SearchResult => result !== undefined);

	const reported =
		typeof raw.total === 'number' && Number.isFinite(raw.total) ? Math.trunc(raw.total) : 0;

	return {
		query,
		count: results.length,
		// A total below what we already hold would make "load more" contradict itself.
		total: Math.max(reported, offset + results.length),
		offset,
		results
	};
}

export async function search(
	query: string,
	options: { offset?: number; origin?: Origin; rank?: Rank; signal?: AbortSignal } = {}
): Promise<SearchResponse> {
	const { signal, origin, rank = 'semantic' } = options;
	const term = query.trim().slice(0, MAX_QUERY_LENGTH);
	const offset = Math.min(Math.max(Math.trunc(options.offset ?? 0), 0), maxOffsetFor(rank));

	const params = new URLSearchParams({ q: term, limit: String(PAGE_SIZE) });
	if (offset > 0) params.set('offset', String(offset));
	if (origin) params.set('origin', origin);
	// Semantic is the server's default, so only the departure from it travels.
	if (rank === 'keyword') params.set('mode', rank);
	const url = `${API_BASE}/api/search?${params}`;

	// A hung request would otherwise leave the UI loading forever.
	const timeout = AbortSignal.timeout(REQUEST_TIMEOUT_MS);
	const combined = signal ? AbortSignal.any([signal, timeout]) : timeout;

	let response: Response;
	try {
		response = await fetch(url, { signal: combined, headers: { Accept: 'application/json' } });
	} catch (e) {
		if (signal?.aborted) throw e;
		if (timeout.aborted) throw new Error('The search timed out. Try again.');
		throw new Error('Could not reach the search service. Check your connection.');
	}

	const body = await response.json().catch(() => null);

	if (!response.ok) {
		// A 5xx body only restates the failure, so the reader gets a useful sentence instead.
		const detail =
			response.status < 500
				? text((body as Record<string, unknown> | null)?.error, MAX_ERROR_LENGTH)
				: undefined;
		throw new Error(
			detail
				? detail.charAt(0).toUpperCase() + detail.slice(1)
				: 'The search service is unavailable. Try again in a moment.'
		);
	}

	return toResponse(body, term, offset);
}
