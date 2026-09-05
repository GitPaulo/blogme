import EmptyResults from './EmptyResults.svelte';
import FilterBar from './FilterBar.svelte';
import ResultCard from './ResultCard.svelte';

/**
 * The result view, in one module.
 *
 * These three are the only things the page draws once a search has come back, they can
 * only appear together, and between them they are most of what the site imports from
 * Flowbite that the landing page never touches — the tag select, the date dialog, the
 * card. So they are fetched together, as one request at one moment, rather than as three
 * imports racing each other the instant a query gets long enough.
 *
 * A default export rather than three named ones so that `lazy` has one shape to hand
 * back, whether what it fetched was a component or a handful of them.
 *
 * See routes/+page.svelte for what fetches this and when.
 */
export default { EmptyResults, FilterBar, ResultCard };
