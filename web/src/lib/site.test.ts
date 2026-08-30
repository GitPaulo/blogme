import { describe, expect, it } from 'vitest';
import { faviconUrl, hostOf, hueFor, monogram } from './site';

// These run on every url the index hands back, and the index holds third-party urls that
// nobody here wrote. So the cases that matter are the malformed ones: a card is allowed to
// show less than it hoped for, and is never allowed to throw on the way to rendering a
// result the reader can already see the title of.

describe('hostOf', () => {
	it('names the blog behind an ordinary article url', () => {
		expect(hostOf('https://blog.cloudflare.com/post/one?utm=x#top')).toBe('blog.cloudflare.com');
		expect(hostOf('http://example.org')).toBe('example.org');
	});

	it('drops a leading www., which names nothing', () => {
		expect(hostOf('https://www.example.com/a')).toBe('example.com');
	});

	it('leaves a www elsewhere in the host alone', () => {
		// Only the leading label is noise. `www.example.com` under a subdomain is a real
		// host, and `wwwx.example.com` is a different site from `x.example.com`.
		expect(hostOf('https://blog.www.example.com/a')).toBe('blog.www.example.com');
		expect(hostOf('https://wwwx.example.com/a')).toBe('wwwx.example.com');
	});

	it('lowercases the host, so one blog is one blog', () => {
		// Hosts are case-insensitive, and the hue and the failure registry are both keyed
		// on this string: two spellings would draw two colours for the same site.
		expect(hostOf('https://Blog.EXAMPLE.com/a')).toBe('blog.example.com');
	});

	it('refuses anything that is not an http page', () => {
		for (const url of ['mailto:someone@example.com', 'javascript:alert(1)', 'data:text/html,x']) {
			expect(hostOf(url), url).toBeUndefined();
		}
	});

	it('refuses what it cannot parse, rather than guessing', () => {
		for (const url of ['', '   ', 'not a url', '//example.com/a', '/relative/path']) {
			expect(hostOf(url), url).toBeUndefined();
		}
	});

	it("takes the parser's reading of a url with a slash too many", () => {
		// `http:///a/b` is not a typo the parser rejects: it reads the first path segment as
		// the host. Pinned because it is surprising, and because the answer is harmless —
		// a card labelled `just` for a url no crawler would have produced.
		expect(hostOf('http:///just/a/path')).toBe('just');
	});

	it('keeps an internationalised host in punycode', () => {
		// The prettier rendering is the one that can be made to read as another site, and
		// this label sits directly under a link. See the note in site.ts.
		expect(hostOf('https://xn--80ak6aa92e.com/a')).toBe('xn--80ak6aa92e.com');
	});
});

describe('faviconUrl', () => {
	it('asks the site itself, over https, and nobody else', () => {
		expect(faviconUrl('example.com')).toBe('https://example.com/favicon.ico');
	});
});

describe('monogram', () => {
	it('takes the first letter or digit of the host', () => {
		expect(monogram('example.com')).toBe('E');
		expect(monogram('blog.cloudflare.com')).toBe('B');
		expect(monogram('37signals.com')).toBe('3');
	});

	it('skips leading punctuation to find one', () => {
		expect(monogram('-weird.example.com')).toBe('W');
	});

	it('falls back to a mark that reads as no initial at all', () => {
		expect(monogram('...')).toBe('·');
		expect(monogram('')).toBe('·');
	});
});

describe('hueFor', () => {
	it('always lands on the wheel', () => {
		for (const host of ['example.com', 'a', '', 'xn--80ak6aa92e.com', 'x'.repeat(300)]) {
			const hue = hueFor(host);
			expect(Number.isInteger(hue), host).toBe(true);
			expect(hue, host).toBeGreaterThanOrEqual(0);
			expect(hue, host).toBeLessThan(360);
		}
	});

	it('gives one site the same colour every time', () => {
		expect(hueFor('blog.example.com')).toBe(hueFor('blog.example.com'));
	});

	it('separates hosts that differ only slightly', () => {
		// The point of the mixing step: a sum of character codes would put these within a
		// degree or two of each other, which is the same colour to a reader scanning a list.
		const hues = ['blog.golang.org', 'blog.rust-lang.org', 'blog.example.com', 'blog.example.net'];
		expect(new Set(hues.map(hueFor)).size).toBe(hues.length);
	});
});
