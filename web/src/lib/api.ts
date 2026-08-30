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
	/**
	 * Whether the page's own headers refuse to let it be framed, as the crawler saw
	 * them. Undefined where it never looked, which is not the same as permission:
	 * see LinkPreview for what each answer does.
	 */
	framingDenied?: boolean;
};

export type SearchResponse = {
	query: string;
	/** Rows in this page; total counts every match in the corpus. */
	count: number;
	total: number;
	offset: number;
	/**
	 * Where the next page starts. Not `offset + count`: the API drops rows that put one
	 * blog over its share of a page, after the index has ranked them, so a page is wider
	 * than the rows it hands back. Advancing by our own page size would step over
	 * whatever it dropped. Mirrors index.Page.NextOffset.
	 */
	nextOffset: number;
	/**
	 * Whether this page reached the end of the index. When it has, the rows gathered so
	 * far are every row there is, and there are fewer of them than `total`, which counts
	 * documents including the ones the API's per-source cap dropped after ranking. Left to
	 * infer this from `nextOffset` against `total`, the UI showed "26 of 27" beside a dead
	 * button. Mirrors index.Page.Exhausted.
	 */
	exhausted: boolean;
	/**
	 * Whether nothing matched every word, so these rows match any of them. The reader can
	 * see the words they typed, so the page says so rather than quietly answering a looser
	 * question. Mirrors index.Page.Broadened.
	 */
	broadened: boolean;
	results: SearchResult[];
};

/** Short queries match almost everything, so they are not worth a round trip. */
export const MIN_QUERY_LENGTH = 3;
/** Mirrors maxQueryLen in api/internal/httpapi. Both ends count characters, not bytes. */
export const MAX_QUERY_LENGTH = 512;
/** Mirrors defaultLimit in api/internal/httpapi. */
export const PAGE_SIZE = 20;
/** How results are ranked. Mirrors the Rank constants in api/internal/index. */
export type Rank = 'semantic' | 'keyword';

/** Mirrors semanticWindow in api/internal/httpapi. */
export const SEMANTIC_WINDOW = 50;
export const MAX_OFFSET_KEYWORD = 1000;

/**
 * How deep each mode may page, mirroring maxOffsetFor in api/internal/httpapi. The last
 * semantic page has to land inside the reranked window, so the limit moves with the page
 * size; keyword ranking scores the whole result set and can go deeper.
 */
export const maxOffsetFor = (rank: Rank, limit: number = PAGE_SIZE): number =>
	rank === 'keyword' ? MAX_OFFSET_KEYWORD : Math.max(SEMANTIC_WINDOW - limit, 0);

/**
 * Trims a query and holds it to MAX_QUERY_LENGTH characters. Cut by code point, because
 * `slice` counts UTF-16 units and would halve an astral character on the boundary.
 */
export function clampQuery(value: string): string {
	const trimmed = value.trim();
	// Fewer UTF-16 units than the cap means fewer code points too, so skip the split.
	return trimmed.length <= MAX_QUERY_LENGTH
		? trimmed
		: [...trimmed].slice(0, MAX_QUERY_LENGTH).join('');
}

/**
 * The shortest and longest query worth completing, mirroring minSuggestLen and
 * maxSuggestLen in api/internal/httpapi. Both ends count characters, not bytes. Asking
 * outside them is a 400, so the caller checks rather than spending the round trip.
 */
export const MIN_SUGGEST_LENGTH = 3;
export const MAX_SUGGEST_LENGTH = 64;
/** Mirrors maxSuggestions in api/internal/index. */
export const MAX_SUGGESTIONS = 8;

/**
 * One suggestion as the API sends it. Mirrors the Kind constants in
 * api/internal/index: a `title` is a phrase somebody wrote and the service ranked, a
 * `query` one the suggester assembled from a pair of terms and did not rank.
 */
export type ApiSuggestion = { text: string; kind: 'title' | 'query' };

/** Mirrors maxLimit in api/internal/httpapi. */
const MAX_RESULTS = 50;
/** A completion is a query, so it cannot be longer than one. */
const MAX_SUGGESTION_LENGTH = MAX_QUERY_LENGTH;
const MAX_TEXT_LENGTH = 2_000;
const MAX_TOPICS = 12;
const MAX_ERROR_LENGTH = 200;
const REQUEST_TIMEOUT_MS = 15_000;
/**
 * Shorter than a search's, because the two are waited on differently. A search is what
 * the reader asked for and is worth waiting out; a completion is for a word they have
 * since finished typing, so past a few seconds there is nothing left to complete. The
 * API gives up on its own side at 1.5s, and this only has to cover the trip there and
 * an instance starting cold.
 */
const SUGGEST_TIMEOUT_MS = 5_000;

// Empty in development, where Vite proxies /api to the Functions host.
const API_BASE = import.meta.env.VITE_API_BASE_URL ?? '';

/**
 * The origin the API answers from, for the page to open a connection to before anyone
 * searches. Empty where there is nothing to open early: in development the API shares
 * this page's origin, so the connection already exists by the time the app has loaded.
 *
 * It is worth saying out loud because the two are not the same host in production — the
 * page is served from GitHub Pages and the API from Azure — so the first search of a
 * visit pays for a DNS lookup, a TCP handshake and a TLS handshake before it can even
 * be sent. Measured against the live API that is 15 ms of TCP and 40 ms of TLS, plus a
 * DNS lookup on a cold resolver, and all of it lands after the reader has pressed
 * enter. Preconnecting spends it during page load instead, where there is nobody
 * waiting on it.
 *
 * Read at build time, and the site is prerendered, so the resulting link tag is in the
 * HTML the browser parses first rather than in the JavaScript it has yet to run.
 */
export const API_ORIGIN = (() => {
	if (!API_BASE) return '';
	try {
		return new URL(API_BASE).origin;
	} catch {
		// A relative base is same-origin, and anything unparseable is a misconfiguration
		// that should not also cost a bogus link tag in every page.
		return '';
	}
})();

/**
 * A refusal from the API, carrying the status so a caller can tell one apart from
 * another. Being throttled is the case that matters: it says try later, where every
 * other failure says something is wrong.
 */
export class SearchError extends Error {
	constructor(
		message: string,
		readonly status: number
	) {
		super(message);
		this.name = 'SearchError';
	}
}

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

	// Deduped because topics are keyed in the markup, and the index has no constraint
	// that stops a document repeating one.
	const topics = Array.isArray(raw.topics)
		? [
				...new Set(
					raw.topics
						.map((topic) => text(topic, 64))
						.filter((topic): topic is string => topic !== undefined)
				)
			].slice(0, MAX_TOPICS)
		: undefined;

	return {
		url,
		title: text(raw.title, 300) ?? url,
		author: text(raw.author, 120),
		origin: raw.origin === 'sitemap' || raw.origin === 'feed' ? raw.origin : undefined,
		summary: text(raw.summary),
		topics: topics?.length ? topics : undefined,
		publishedAt: text(raw.publishedAt, 40),
		score: typeof raw.score === 'number' && Number.isFinite(raw.score) ? raw.score : 0,
		// Only an actual boolean is an answer. Null, missing, or anything else is the
		// API saying it does not know.
		framingDenied: typeof raw.framingDenied === 'boolean' ? raw.framingDenied : undefined
	};
}

/** The response is untrusted input: anything unexpected is dropped rather than rendered. */
function toResponse(body: unknown, query: string, offset: number): SearchResponse {
	const raw = (typeof body === 'object' && body !== null ? body : {}) as Record<string, unknown>;
	const seen = new Set<string>();
	const results = (Array.isArray(raw.results) ? raw.results : [])
		.slice(0, MAX_RESULTS)
		.map(toResult)
		.filter((result): result is SearchResult => result !== undefined)
		// Rows are keyed by url in the markup, and the same article can be indexed twice
		// when a blog serves it under more than one path.
		.filter((result) => {
			if (seen.has(result.url)) return false;
			seen.add(result.url);
			return true;
		});

	const reported =
		typeof raw.total === 'number' && Number.isFinite(raw.total) ? Math.trunc(raw.total) : 0;

	// It has to move forward, or "load more" asks for this page again for as long as the
	// reader keeps clicking. An older API that does not send it still pages, one page at
	// a time, which is exactly what it used to do.
	const advanced =
		typeof raw.nextOffset === 'number' && Number.isFinite(raw.nextOffset)
			? Math.trunc(raw.nextOffset)
			: 0;

	return {
		query,
		count: results.length,
		// A total below what we already hold would make "load more" contradict itself.
		total: Math.max(reported, offset + results.length),
		offset,
		nextOffset: Math.max(advanced, offset + PAGE_SIZE),
		// Only an actual boolean is an answer, as with framingDenied above. An older API
		// that does not send it says nothing, and nothing has to read as "there may be
		// more": claiming the end early would strand every result past this page.
		exhausted: raw.exhausted === true,
		// Same rule, and the safe default is the quiet one: an API that does not send it
		// has not broadened anything, so nothing is announced.
		broadened: raw.broadened === true,
		results
	};
}

export async function search(
	query: string,
	options: { offset?: number; rank?: Rank; signal?: AbortSignal } = {}
): Promise<SearchResponse> {
	const { signal, rank = 'keyword' } = options;
	const term = clampQuery(query);
	const offset = Math.min(
		Math.max(Math.trunc(options.offset ?? 0), 0),
		maxOffsetFor(rank, PAGE_SIZE)
	);

	const params = new URLSearchParams({ q: term, limit: String(PAGE_SIZE) });
	if (offset > 0) params.set('offset', String(offset));
	// Keyword is the server's default, so only the departure from it travels.
	if (rank === 'semantic') params.set('mode', rank);
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

	let body: unknown = null;
	try {
		body = await response.json();
	} catch (e) {
		// The body arrives after the headers, so an abort can land between the two. Only
		// a genuinely malformed payload falls through to be handled below: reading an
		// abort as an empty body would report a cancelled search as one that found
		// nothing.
		if (signal?.aborted) throw e;
		if (timeout.aborted) throw new Error('The search timed out. Try again.');
	}

	if (!response.ok) {
		// A 5xx body only restates the failure, so the reader gets a useful sentence instead.
		const detail =
			response.status < 500
				? text((body as Record<string, unknown> | null)?.error, MAX_ERROR_LENGTH)
				: undefined;
		throw new SearchError(
			detail
				? detail.charAt(0).toUpperCase() + detail.slice(1)
				: 'The search service is unavailable. Try again in a moment.',
			response.status
		);
	}

	return toResponse(body, term, offset);
}

/**
 * Completions for the query being typed, most likely first.
 *
 * Each one is a whole query rather than the word that finishes it, so a caller can put
 * it straight in the search box. Throws like `search` does; a caller that treats
 * suggestions as a convenience should catch, because a failure here is not something a
 * reader mid-word can act on.
 */
export async function suggest(
	query: string,
	options: { signal?: AbortSignal } = {}
): Promise<ApiSuggestion[]> {
	const term = clampQuery(query);
	// Checked rather than sent: both ends are a 400 at the API, and a request known to
	// be refused is a round trip and an execution spent to learn nothing.
	if (term.length < MIN_SUGGEST_LENGTH || term.length > MAX_SUGGEST_LENGTH) return [];

	const url = `${API_BASE}/api/suggest?${new URLSearchParams({ q: term })}`;

	const timeout = AbortSignal.timeout(SUGGEST_TIMEOUT_MS);
	const combined = options.signal ? AbortSignal.any([options.signal, timeout]) : timeout;

	const response = await fetch(url, {
		signal: combined,
		headers: { Accept: 'application/json' }
	});
	if (!response.ok) {
		throw new SearchError('Suggestions unavailable.', response.status);
	}

	return toSuggestions(await response.json());
}

/** The response is untrusted input, so anything unexpected is dropped rather than shown. */
function toSuggestions(body: unknown): ApiSuggestion[] {
	const raw = (typeof body === 'object' && body !== null ? body : {}) as Record<string, unknown>;
	if (!Array.isArray(raw.suggestions)) return [];

	const seen = new Set<string>();
	const rows: ApiSuggestion[] = [];
	for (const entry of raw.suggestions) {
		if (rows.length === MAX_SUGGESTIONS) break;
		if (typeof entry !== 'object' || entry === null) continue;

		const row = entry as Record<string, unknown>;
		const suggestion = text(row.text, MAX_SUGGESTION_LENGTH);
		if (suggestion === undefined) continue;
		// Deduped because the list is keyed by the suggestion itself in the markup, and
		// two rows sharing a key is an error rather than a repeat on screen.
		if (seen.has(suggestion)) continue;
		seen.add(suggestion);

		// Only the two kinds the API sends are an answer. Anything else — a kind added
		// later, or none at all — is a suggestion whose provenance we do not know, and
		// the safe reading of that is the ordinary one.
		rows.push({ text: suggestion, kind: row.kind === 'title' ? 'title' : 'query' });
	}
	return rows;
}
