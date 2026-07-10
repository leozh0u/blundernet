"""Evaluate the net on the fixed Lichess puzzle suite, bucketed by rating.

A Lichess puzzle stores the position (FEN) *before* a setup move. We apply
that setup move, then ask: does the net's top move match the puzzle's first
solution move? Accuracy is reported per rating bucket, so you can watch the
net solve progressively harder tactics as it learns.
"""
import csv
from pathlib import Path

import chess
import numpy as np
import torch

from .encode import encode_board, legal_move_mask, move_to_index

PUZZLES = Path("data/puzzles.csv")
BUCKETS = [(800, 1200), (1200, 1600), (1600, 2000), (2000, 2400), (2400, 3200)]


def _bucket_label(lo, hi):
    return f"{lo}-{hi if hi < 3200 else '+'}"


def load_puzzles():
    """Return list of (planes, target_index, legal_mask, rating)."""
    items = []
    with PUZZLES.open() as f:
        for row in csv.DictReader(f):
            board = chess.Board(row["FEN"])
            moves = row["Moves"].split()
            if len(moves) < 2:
                continue
            board.push(chess.Move.from_uci(moves[0]))       # setup move
            solution = chess.Move.from_uci(moves[1])         # what to find
            items.append((
                encode_board(board),
                move_to_index(solution),
                legal_move_mask(board),
                int(row["Rating"]),
            ))
    return items


@torch.no_grad()
def evaluate_puzzles(model, items=None, batch_size=256):
    model.eval()
    if items is None:
        if not PUZZLES.exists():
            return {}
        items = load_puzzles()
    if not items:
        return {}

    X = np.stack([it[0] for it in items])
    targets = np.array([it[1] for it in items], dtype=np.int64)
    masks = np.stack([it[2] for it in items])
    ratings = np.array([it[3] for it in items])

    hits = np.zeros(len(items), dtype=bool)
    for i in range(0, len(items), batch_size):
        logits, _ = model(torch.from_numpy(X[i:i + batch_size]))
        mb = torch.from_numpy(masks[i:i + batch_size])
        logits = logits.masked_fill(~mb, -1e9)
        pred = logits.argmax(dim=1).numpy()
        hits[i:i + batch_size] = pred == targets[i:i + batch_size]

    out = {"puzzle_overall": round(float(hits.mean()), 4), "puzzle_n": len(items)}
    for lo, hi in BUCKETS:
        sel = (ratings >= lo) & (ratings < hi)
        if sel.any():
            out[f"puzzle_{_bucket_label(lo, hi)}"] = round(float(hits[sel].mean()), 4)
    return out
