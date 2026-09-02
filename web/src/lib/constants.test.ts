import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import {
	MAX_OFFSET_KEYWORD,
	MAX_QUERY_LENGTH,
	MAX_SUGGEST_LENGTH,
	MAX_SUGGESTIONS,
	MIN_SUGGEST_LENGTH,
	PAGE_SIZE,
	SEMANTIC_WINDOW
} from './api';

// A dozen numbers in this app are copies of numbers in the Go API, and until now a
// comment saying "Mirrors X" was the only thing holding them together. Drift is silent
// and lands in production: raising semanticWindow in Go without touching api.ts makes
// `hasMore` wrong, and lowering maxQueryLen turns every long search into a 400 nobody
// tested for.
//
// So the Go source is read and the numbers are compared. This is a text scrape rather
// than anything generated, which is the point — it costs one file and no build step, and
// it fails on the pull request that introduces the drift rather than in a search box.

const go = (path: string) =>
	readFileSync(fileURLToPath(new URL(`../../../api/${path}`, import.meta.url)), 'utf8');

/** The value of a Go `const name = 123`, however the block around it is spaced. */
function goConst(source: string, name: string): number {
	const match = new RegExp(`\\b${name}\\s*=\\s*(\\d+)\\b`).exec(source);
	// A rename on the Go side has to fail loudly here, or the comparison quietly stops
	// comparing anything and the guard becomes decoration.
	expect(match, `no Go constant named ${name}`).not.toBeNull();
	return Number(match![1]);
}

const httpapi = go('internal/httpapi/httpapi.go');
const suggestApi = go('internal/httpapi/suggest.go');
const suggestIndex = go('internal/index/suggest.go');
const extract = go('internal/discovery/extract.go');

describe('the query and paging caps mirror api/internal/httpapi', () => {
	it('agrees on how long a query may be', () => {
		expect(MAX_QUERY_LENGTH).toBe(goConst(httpapi, 'maxQueryLen'));
	});

	it('agrees on the page size the UI asks for', () => {
		expect(PAGE_SIZE).toBe(goConst(httpapi, 'defaultLimit'));
	});

	it('agrees on the reranked window semantic paging has to land inside', () => {
		expect(SEMANTIC_WINDOW).toBe(goConst(httpapi, 'semanticWindow'));
	});

	it('agrees on how deep keyword ranking may page', () => {
		expect(MAX_OFFSET_KEYWORD).toBe(goConst(httpapi, 'maxKeywordOffset'));
	});
});

describe('the suggestion caps mirror the API', () => {
	it('agrees on the shortest and longest query worth completing', () => {
		expect(MIN_SUGGEST_LENGTH).toBe(goConst(suggestApi, 'minSuggestLen'));
		expect(MAX_SUGGEST_LENGTH).toBe(goConst(suggestApi, 'maxSuggestLen'));
	});

	it('agrees on how many completions one request returns', () => {
		expect(MAX_SUGGESTIONS).toBe(goConst(suggestIndex, 'maxSuggestions'));
	});
});

describe('the snippet rule mirrors api/internal/discovery/extract.go', () => {
	it('agrees on how much of the budget a sentence cut has to keep', async () => {
		// Written as a fraction in Go so the comparison there is integer arithmetic, and
		// declared as one two-name const, which is why this is read as a pair.
		const pair = /sentenceFloorNum,\s*sentenceFloorDen\s*=\s*(\d+),\s*(\d+)/.exec(extract);
		expect(pair, 'no sentenceFloorNum/Den pair in extract.go').not.toBeNull();
		const [, num, den] = pair!.map(Number);
		// Not exported, because nothing but this needs it: read it out of the source the
		// same way, so the two are compared rather than one being trusted.
		const ts = readFileSync(fileURLToPath(new URL('./snippet.ts', import.meta.url)), 'utf8');
		const floor = Number(/SENTENCE_FLOOR\s*=\s*([\d.]+)/.exec(ts)?.[1]);
		expect(floor).toBeCloseTo(num / den, 10);
	});

	it('agrees on which words end in a full stop without ending a sentence', () => {
		const goWords = [...extract.matchAll(/"([a-z]+)":\s*true/g)].map((m) => m[1]).sort();
		const ts = readFileSync(fileURLToPath(new URL('./snippet.ts', import.meta.url)), 'utf8');
		const block = /const ABBREVIATIONS = new Set\(\[([^\]]*)\]\)/s.exec(ts)?.[1] ?? '';
		const tsWords = [...block.matchAll(/'([a-z]+)'/g)].map((m) => m[1]).sort();

		expect(tsWords.length).toBeGreaterThan(0);
		expect(tsWords).toEqual(goWords);
	});
});
