"""Running the check pass across several processes.

A check pass looks like it should be bound by the network — tens of thousands of
requests, almost all of it waiting — and it is not. Parsing the answers costs more
than waiting for them: profiling one pass put the interpreter at 95% of a core with
feedparser alone accounting for about half of that, while the network sat idle.

That single saturated core is why every knob that added concurrency made things worse.
Requests did not get slower because sites were slow; they timed out because the event
loop could not get round to the socket in time, and a feed lookup that times out is
recorded as a blog having no feed. Measured over the same 800 candidates, six
processes finished in 27.5s against 74.0s for one, and found 353 feeds against 303 —
the only change tried that made the run both faster and more complete.

Chunks are handed out smaller than one per process so a chunk full of dead hosts
cannot leave a worker idle while another grinds, and so the parent can report progress
as they land.

check_all is synchronous and must be called with no event loop running: starting a pool
forks on Linux, and forking a process in the middle of its own loop is a good way to
inherit half of it. The harvest finishes and closes its loop before the checks begin.
"""

from __future__ import annotations

import asyncio
import concurrent.futures
import os
from dataclasses import dataclass

import httpx

from .checks import RETRY_SITE_TIMEOUT, SITE_TIMEOUT, check_candidate
from .models import Candidate
from .progress import Progress, log

USER_AGENT = "blog-source-extractor"

# Chunks per worker. Enough that a slow chunk is absorbed by the others and progress
# is reported often; few enough that handing work over stays a rounding error.
CHUNKS_PER_WORKER = 8

# Candidates a worker needs before it is worth starting. A process costs a second or
# two to spawn and has to import everything again, so a small pass — the tail of a
# retry, or a trial run under --limit-candidates — is quicker in one process than
# spread over six.
MIN_PER_WORKER = 250


def default_processes() -> int:
    """Workers to use when none is asked for.

    Capped below the core count because the machine running this usually has
    something else to do, and because the gain flattens once the network rather than
    the interpreter is the constraint again.
    """
    return max(1, min(6, (os.cpu_count() or 2) - 1))


@dataclass
class CheckJob:
    """One chunk of candidates and everything a worker needs to check them."""

    candidates: list[Candidate]
    require_feed: bool
    concurrency: int
    # The retry pass: longer budgets, and fewer at once so a slow host is not outrun
    # a second time.
    slow: bool = False


# The feeds the last build recorded, held per worker rather than packed into every job.
# There are twenty-odd thousand of them and they are the same for every chunk: sending
# them with each one would ship 58 MB of pickled dictionary over a full run instead of 7.
_known_feeds: dict[str, str] = {}


def _adopt_known_feeds(known_feeds: dict[str, str]) -> None:
    """Give this process the feeds it should recognise. Runs once per worker."""
    global _known_feeds
    _known_feeds = known_feeds


async def _check_chunk(job: CheckJob) -> list[bool]:
    """Check one chunk in this process, reporting which candidates were kept."""
    # Host lookups run in the loop's default executor; the stock 32 threads throttle
    # everything behind them.
    asyncio.get_running_loop().set_default_executor(
        concurrent.futures.ThreadPoolExecutor(max_workers=max(64, job.concurrency)))

    options = {
        "headers": {"User-Agent": USER_AGENT},
        "timeout": httpx.Timeout(30.0, connect=10.0),
        "limits": httpx.Limits(max_connections=job.concurrency * 8,
                               max_keepalive_connections=256),
    }
    timeout = RETRY_SITE_TIMEOUT if job.slow else SITE_TIMEOUT

    async with httpx.AsyncClient(**options) as client:
        limit = asyncio.Semaphore(job.concurrency)

        async def one(candidate: Candidate) -> bool:
            async with limit:
                try:
                    checked = await check_candidate(
                        client, candidate, job.require_feed, timeout, _known_feeds)
                except Exception as exc:
                    # One unhappy site must not take a run that has been going for an
                    # hour with it. Recorded on the candidate like any other failure,
                    # so it shows up in the audit rather than only in a traceback.
                    candidate.error = f"{type(exc).__name__}: {exc}"[:200]
                    return False
                return checked is not None

        return list(await asyncio.gather(*(one(c) for c in job.candidates)))


def run_job(job: CheckJob) -> tuple[list[Candidate], list[bool]]:
    """Worker entry point: check a chunk and hand the filled-in candidates back.

    The candidates are returned rather than only the survivors, because the caller
    decides what to retry from the status and error a failed check leaves behind.
    """
    kept = asyncio.run(_check_chunk(job))
    return job.candidates, kept


def _chunks(candidates: list[Candidate], count: int) -> list[list[Candidate]]:
    size = max(1, (len(candidates) + count - 1) // count)
    return [candidates[i:i + size] for i in range(0, len(candidates), size)]


def check_all(
    candidates: list[Candidate],
    *,
    require_feed: bool,
    concurrency: int,
    processes: int,
    known_feeds: dict[str, str],
    slow: bool = False,
    label: str = "validated",
) -> list[tuple[Candidate, bool]]:
    """Check every candidate, returning each one filled in with whether it was kept.

    The candidates are returned rather than filled in place, because a worker fills in
    its own copy: the returned candidate is the one carrying the status, error and feed,
    and the one the audit has to report on. In a single process it happens to be the
    same object that went in, which is not something to rely on.

    `concurrency` is the number of candidates in flight across the whole run, not per
    worker: the point of the extra processes is more cores, not more sockets, and
    letting each worker take the full budget would change how hard the run leans on
    the sites it is visiting.
    """
    if not candidates:
        return []

    processes = max(1, min(processes, len(candidates) // MIN_PER_WORKER))
    per_worker = max(1, concurrency // processes)

    if processes == 1:
        _adopt_known_feeds(known_feeds)
        log(f"{label}: {len(candidates)} candidates in one process")
        checked, kept = run_job(CheckJob(candidates, require_feed, concurrency, slow))
        return list(zip(checked, kept))

    chunks = _chunks(candidates, processes * CHUNKS_PER_WORKER)
    jobs = [CheckJob(chunk, require_feed, per_worker, slow) for chunk in chunks]

    log(f"{label}: {len(candidates)} candidates across {processes} processes "
        f"({per_worker} at once each, {len(chunks)} chunks)")

    # Chunks finish in whatever order they finish, so results are kept by position and
    # reassembled in the order the chunks were cut.
    done: dict[int, tuple[list[Candidate], list[bool]]] = {}
    progress = Progress(len(candidates), label=label)
    with concurrent.futures.ProcessPoolExecutor(
        max_workers=processes,
        initializer=_adopt_known_feeds,
        initargs=(known_feeds,),
    ) as pool:
        futures = {pool.submit(run_job, job): i for i, job in enumerate(jobs)}
        for future in concurrent.futures.as_completed(futures):
            index = futures[future]
            done[index] = future.result()
            progress.advance(len(jobs[index].candidates))

    out: list[tuple[Candidate, bool]] = []
    for i in range(len(jobs)):
        checked, flags = done[i]
        out.extend(zip(checked, flags))

    return out
