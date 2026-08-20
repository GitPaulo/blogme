"""Progress reporting. Everything goes to stderr so stdout stays clean."""

from __future__ import annotations

import sys
import time


def log(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)


class Progress:
    """Counts finished checks and prints a rate and an estimate now and then."""

    def __init__(self, total: int, every: int = 500, label: str = "validated") -> None:
        self.total = total
        self.every = every
        self.label = label
        self.done = 0
        self.started = time.time()

    def advance(self, n: int = 1) -> None:
        """Count n finished checks, reporting when a multiple of `every` is crossed.

        Takes a count rather than one at a time because results come back a chunk at a
        time from the workers; the cadence of the reports is the same either way.
        """
        before = self.done
        self.done = min(self.done + n, self.total)
        if self.done == self.total or self.done // self.every > before // self.every:
            rate = self.done / max(time.time() - self.started, 0.001)
            remaining = (self.total - self.done) / rate if rate else 0
            log(f"{self.label} {self.done}/{self.total} "
                f"({rate:.1f}/s, ~{remaining / 60:.0f} min left)")
