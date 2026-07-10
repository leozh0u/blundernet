#!/usr/bin/env python3
"""One-time: build a fixed, reproducible puzzle eval set from the Lichess DB.

Streams the public puzzle database, stratified-samples N puzzles per rating
bucket, and writes data/puzzles.csv (committed so every run evaluates on the
exact same held-out set). Re-run only if you want to refresh the suite.

    python scripts/build_puzzles.py
"""
import csv
import io
import subprocess
import sys
from pathlib import Path

URL = "https://database.lichess.org/lichess_db_puzzle.csv.zst"
BUCKETS = [(800, 1200), (1200, 1600), (1600, 2000), (2000, 2400), (2400, 3200)]
PER_BUCKET = 240
OUT = Path("data/puzzles.csv")
COLS = ["PuzzleId", "FEN", "Moves", "Rating", "Themes"]


def bucket_of(rating: int):
    for lo, hi in BUCKETS:
        if lo <= rating < hi:
            return (lo, hi)
    return None


def main() -> None:
    counts = {b: 0 for b in BUCKETS}
    rows = []
    # stream: curl | zstd -d  -> parse CSV line by line, stop when buckets full
    proc = subprocess.Popen(
        f"curl -sL {URL} | zstd -dc",
        shell=True, stdout=subprocess.PIPE, bufsize=1 << 20,
    )
    text = io.TextIOWrapper(proc.stdout, encoding="utf-8")
    reader = csv.DictReader(text)
    seen = 0
    for r in reader:
        seen += 1
        try:
            rating = int(r["Rating"])
        except (ValueError, KeyError):
            continue
        b = bucket_of(rating)
        if b is None or counts[b] >= PER_BUCKET:
            continue
        # deterministic thinning so we don't just take the first N ids
        if seen % 7 != 0:
            continue
        counts[b] += 1
        rows.append({k: r[k] for k in COLS})
        if all(c >= PER_BUCKET for c in counts.values()):
            break
        if seen % 50000 == 0:
            print(f"scanned {seen:,}  filled {dict(counts)}", file=sys.stderr)
    proc.terminate()

    rows.sort(key=lambda x: int(x["Rating"]))
    OUT.parent.mkdir(exist_ok=True)
    with OUT.open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=COLS)
        w.writeheader()
        w.writerows(rows)
    print(f"wrote {len(rows)} puzzles -> {OUT}  ({dict(counts)})")


if __name__ == "__main__":
    main()
