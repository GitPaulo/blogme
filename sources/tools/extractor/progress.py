"""Progress reporting. Everything goes to stderr so stdout stays clean."""

from __future__ import annotations

import sys
import time


def log(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)


class Progress:
    """Counts finished checks and prints a rate and an estimate now and then."""

    def __init__(self, total: int, every: int = 500) -> None:
        self.total = total
        self.every = every
        self.done = 0
        self.started = time.time()

    def tick(self) -> None:
        self.done += 1
        if self.done % self.every == 0 or self.done == self.total:
            rate = self.done / max(time.time() - self.started, 0.001)
            remaining = (self.total - self.done) / rate if rate else 0
            log(f"validated {self.done}/{self.total} ({rate:.1f}/s, ~{remaining / 60:.0f} min left)")
