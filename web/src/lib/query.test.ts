import { describe, expect, it } from 'vitest';
import { looksLikeAQuestion } from './query';

// This decides whether to offer a reader semantic ranking, and the cost of the two
// mistakes is not symmetric. A question it misses costs a hint nobody saw. A false
// positive puts an offer over an ordinary search, which is how a reader learns to
// ignore the offer — so the negatives below matter more than the positives, and most
// of them are drawn from real titles in the corpus that read like questions and are not.
describe('looksLikeAQuestion', () => {
	it('accepts anything ending in a question mark', () => {
		// Nobody types one by accident, so it needs no vocabulary at all.
		for (const q of ['monads?', 'is this thing on?', 'why?']) {
			expect(looksLikeAQuestion(q), q).toBe(true);
		}
	});

	it('accepts an interrogative followed by an auxiliary', () => {
		for (const q of [
			'why is my postgres query slow',
			'how do i debug a segfault',
			'what does the garbage collector do',
			'when should i use a mutex',
			'which is faster grpc or rest'
		]) {
			expect(looksLikeAQuestion(q), q).toBe(true);
		}
	});

	it('accepts an auxiliary followed by a subject', () => {
		for (const q of ['can i run postgres locally', 'should we use kubernetes']) {
			expect(looksLikeAQuestion(q), q).toBe(true);
		}
	});

	it('rejects titles that merely start with a question word', () => {
		// Every one of these is a real article title, and a keyword search finds each
		// of them exactly. "how" alone is not a question; "how do" is.
		for (const q of [
			'how i built my blog',
			'what the hell happened to css',
			'why rust is fast',
			'how to write a compiler',
			'when computers were human'
		]) {
			expect(looksLikeAQuestion(q), q).toBe(false);
		}
	});

	it('rejects ordinary searches full of function words', () => {
		for (const q of [
			'the rust book',
			'a tour of go',
			'the art of computer programming',
			'end of life for python 2',
			'history of the internet'
		]) {
			expect(looksLikeAQuestion(q), q).toBe(false);
		}
	});

	it('rejects a pair of question words with nothing to ask about', () => {
		// "how do" on its own is half a thought, and re-ranking two words helps nobody.
		expect(looksLikeAQuestion('how do')).toBe(false);
		expect(looksLikeAQuestion('why is')).toBe(false);
	});

	it('rejects punctuation that has no question in it', () => {
		// "???" ends in a question mark and asks nothing. Offering to re-rank it would
		// put the hint exactly where it is plainly no help.
		for (const q of ['???', '!!!', '---', '   ', '']) {
			expect(looksLikeAQuestion(q), JSON.stringify(q)).toBe(false);
		}
	});

	it('ignores case and surrounding space', () => {
		expect(looksLikeAQuestion('  WHY IS My Postgres Slow  ')).toBe(true);
	});
});

// "how to" is a tutorial, not a question, and it is one of the most common prefixes
// typed into this box. It read as a question until a test said otherwise.
describe('looksLikeAQuestion, on how-to phrasing', () => {
	it('rejects tutorial titles that open with an interrogative and "to"', () => {
		for (const q of [
			'how to write a compiler',
			'how to debug a segfault',
			'what to read in 2026',
			'when to use a mutex',
			'where to start with kubernetes'
		]) {
			expect(looksLikeAQuestion(q), q).toBe(false);
		}
	});

	it('still accepts the same openers with a real auxiliary', () => {
		for (const q of ['how do i write a compiler', 'when should i use a mutex']) {
			expect(looksLikeAQuestion(q), q).toBe(true);
		}
	});
});
