/**
 * Publication dates are rendered in UTC. The value comes from the blog's own feed
 * and some entries carry a bare calendar date, so shifting into the reader's zone
 * would silently misdate those posts by a day.
 */
const formatter = new Intl.DateTimeFormat(undefined, {
	year: 'numeric',
	month: 'short',
	day: 'numeric',
	timeZone: 'UTC'
});

/** Undefined for missing or unparseable input, so callers can skip the line entirely. */
export function formatDate(value?: string): string | undefined {
	if (!value) return undefined;
	const time = Date.parse(value);
	return Number.isNaN(time) ? undefined : formatter.format(time);
}
