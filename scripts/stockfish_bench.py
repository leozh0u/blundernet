#!/usr/bin/env python3
"""Anchor the engine's strength against Stockfish at fixed skill levels.

    python scripts/stockfish_bench.py [--games 10] [--sims 128] [--levels 0 1 3] [--commit]

Stockfish's "Skill Level" (0-20) throttles it to roughly 1300 Elo at level 0
up to full strength at 20 (the mapping is approximate and time-control
dependent — treat the resulting Elo as an anchor, not a certificate).
Each level plays a color-alternating match; the implied Elo difference per
level is combined into a single estimate. Results: metrics/stockfish.json
+ a row in metrics/stockfish_history.csv.
"""
import argparse
import csv
import datetime as dt
import json
import shutil
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import chess
import chess.engine

# rough published anchors for Stockfish skill levels at fast time controls
LEVEL_ELO = {0: 1320, 1: 1470, 2: 1600, 3: 1740, 4: 1920, 5: 2200}

METRICS = Path("metrics")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--games", type=int, default=10)
    ap.add_argument("--sims", type=int, default=128)
    ap.add_argument("--levels", type=int, nargs="+", default=[0, 1, 3])
    ap.add_argument("--movetime", type=float, default=0.05)
    ap.add_argument("--commit", action="store_true")
    args = ap.parse_args()

    sf_path = shutil.which("stockfish")
    if not sf_path:
        sys.exit("stockfish not found on PATH")

    from blundernet.arena import match, elo_diff
    from blundernet import mcts_cpp, mcts
    from blundernet.train import load_model

    engine_mod = mcts_cpp if mcts_cpp.AVAILABLE else mcts
    model, _, meta = load_model()
    model.eval()

    def our_engine(board: chess.Board) -> chess.Move:
        return engine_mod.best_move(board, model, simulations=args.sims)

    sf = chess.engine.SimpleEngine.popen_uci(sf_path)
    results = {"timestamp": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds"),
               "steps": meta["steps"], "sims": args.sims,
               "games_per_level": args.games, "levels": {}}
    estimates = []
    try:
        for level in args.levels:
            sf.configure({"Skill Level": level})

            def sf_move(board: chess.Board) -> chess.Move:
                return sf.play(board, chess.engine.Limit(time=args.movetime)).move

            print(f"vs Stockfish skill {level} (~{LEVEL_ELO.get(level, '?')} Elo)...",
                  flush=True)
            m = match(our_engine, sf_move, games=args.games)
            m["sf_level"] = level
            m["sf_elo_anchor"] = LEVEL_ELO.get(level)
            if m["sf_elo_anchor"] is not None and m["elo_diff"] is not None:
                est = m["sf_elo_anchor"] + m["elo_diff"]
                m["implied_engine_elo"] = round(est)
                # only matches with real signal (not 0% or 100%) inform the estimate
                if 0.05 <= m["score"] <= 0.95:
                    estimates.append(est)
            results["levels"][str(level)] = m
            print(f"  -> {m}")
    finally:
        sf.quit()

    results["elo_estimate"] = round(sum(estimates) / len(estimates)) if estimates else None
    print(f"combined Elo estimate: {results['elo_estimate']}")

    METRICS.mkdir(exist_ok=True)
    (METRICS / "stockfish.json").write_text(json.dumps(results, indent=2) + "\n")
    hist = METRICS / "stockfish_history.csv"
    exists = hist.exists()
    with hist.open("a", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["timestamp", "steps", "sims", "elo_estimate",
                                          "scores_by_level"])
        if not exists:
            w.writeheader()
        w.writerow({"timestamp": results["timestamp"], "steps": meta["steps"],
                    "sims": args.sims, "elo_estimate": results["elo_estimate"],
                    "scores_by_level": " ".join(
                        f"L{k}:{v['score']}" for k, v in results["levels"].items())})

    if args.commit:
        subprocess.run(["git", "add", "-A"], check=True)
        subprocess.run(["git", "commit", "-m",
                        f"strength: Elo estimate {results['elo_estimate']} vs Stockfish "
                        f"anchors @ step {meta['steps']}"], capture_output=True)


if __name__ == "__main__":
    main()
