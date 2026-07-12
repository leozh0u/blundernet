#!/usr/bin/env python3
"""Benchmark: pure-Python MCTS vs C++ core with batched evaluation.

    python scripts/bench_mcts.py [--sims 400] [--positions 6]

Checks move agreement between the two implementations, then times both.
Writes metrics/mcts_bench.json.
"""
import argparse
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))  # for blundercore.so

import chess

# a few middlegame-ish test positions (start + common structures)
FENS = [
    chess.STARTING_FEN,
    "r1bqkbnr/pppp1ppp/2n5/4p3/4P3/5N2/PPPP1PPP/RNBQKB1R w KQkq - 2 3",
    "rnbqkb1r/ppp1pppp/5n2/3p4/3P1B2/8/PPP1PPPP/RN1QKBNR w KQkq - 2 3",
    "r2q1rk1/ppp2ppp/2npbn2/2b1p3/2B1P3/2NP1N2/PPP2PPP/R1BQ1RK1 w - - 4 8",
    "8/5pk1/6p1/8/2B5/6P1/5PK1/3r4 w - - 0 40",
    "r3k2r/p1ppqpb1/bn2pnp1/3PN3/1p2P3/2N2Q1p/PPPBBPPP/R3K2R w KQkq - 0 1",
]


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sims", type=int, default=400)
    ap.add_argument("--positions", type=int, default=len(FENS))
    ap.add_argument("--batch", type=int, default=16)
    args = ap.parse_args()

    from blundernet import mcts, mcts_cpp
    from blundernet.train import load_model

    if not mcts_cpp.AVAILABLE:
        sys.exit("blundercore not built: python cpp/setup.py build_ext --inplace")

    model, _, meta = load_model()
    model.eval()
    fens = FENS[: args.positions]

    results = {"sims": args.sims, "batch": args.batch, "steps": meta["steps"],
               "positions": len(fens)}
    agree = 0
    for impl, mod, kwargs in [("python", mcts, {}),
                              ("cpp_batched", mcts_cpp, {"batch_size": args.batch})]:
        t0 = time.perf_counter()
        moves = []
        for fen in fens:
            board = chess.Board(fen)
            visits = mod.search(board, model, simulations=args.sims, **kwargs)
            moves.append(max(visits, key=visits.get))
        dt = time.perf_counter() - t0
        sims_per_s = args.sims * len(fens) / dt
        results[impl] = {"seconds": round(dt, 2),
                         "sims_per_sec": round(sims_per_s, 1)}
        results[f"{impl}_moves"] = [m.uci() for m in moves]
        print(f"{impl:12s}: {dt:6.2f}s  ({sims_per_s:7.1f} sims/s)")

    a, b = results["python_moves"], results["cpp_batched_moves"]
    agree = sum(x == y for x, y in zip(a, b))
    results["move_agreement"] = f"{agree}/{len(a)}"
    results["speedup"] = round(results["cpp_batched"]["sims_per_sec"]
                               / results["python"]["sims_per_sec"], 2)
    print(f"move agreement: {agree}/{len(a)}   speedup: {results['speedup']}x")

    Path("metrics").mkdir(exist_ok=True)
    Path("metrics/mcts_bench.json").write_text(json.dumps(results, indent=2) + "\n")


if __name__ == "__main__":
    main()
