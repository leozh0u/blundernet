#!/usr/bin/env python3
"""Strength gauntlet: engine (policy-only and MCTS) vs the baseline ladder.

    python scripts/gauntlet.py [--games 20] [--sims 100] [--commit]

Writes metrics/strength.json and appends metrics/strength_history.csv.
"""
import argparse
import csv
import datetime as dt
import json
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))  # for blundercore.so

import torch

METRICS = Path("metrics")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--games", type=int, default=20)
    ap.add_argument("--sims", type=int, default=100)
    ap.add_argument("--commit", action="store_true")
    args = ap.parse_args()

    from blundernet.arena import match, material_greedy, random_mover
    from blundernet import mcts_cpp
    if mcts_cpp.AVAILABLE:
        from blundernet.mcts_cpp import best_move
        print("using C++ batched MCTS core")
    else:
        from blundernet.mcts import best_move
    from blundernet.train import load_model
    from blundernet.encode import encode_board, move_to_index, legal_move_mask
    import chess
    import numpy as np

    model, _, meta = load_model()
    model.eval()

    @torch.no_grad()
    def policy_only(board: chess.Board) -> chess.Move:
        x = torch.from_numpy(encode_board(board)).unsqueeze(0)
        logits, _ = model(x)
        logits = logits[0].numpy()
        logits[~legal_move_mask(board)] = -1e9
        idx = int(logits.argmax())
        for m in board.legal_moves:
            if move_to_index(m) == idx:
                return m
        return next(iter(board.legal_moves))

    def mcts_engine(board: chess.Board) -> chess.Move:
        return best_move(board, model, simulations=args.sims)

    results = {"timestamp": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds"),
               "steps": meta["steps"], "sims": args.sims, "games_per_match": args.games}
    for engine_name, engine in [("policy", policy_only), ("mcts", mcts_engine)]:
        for opp_name, opp in [("random", random_mover), ("material", material_greedy)]:
            print(f"{engine_name} vs {opp_name}...", flush=True)
            m = match(engine, opp, games=args.games)
            results[f"{engine_name}_vs_{opp_name}"] = m
            print(f"  -> {m}")

    METRICS.mkdir(exist_ok=True)
    (METRICS / "strength.json").write_text(json.dumps(results, indent=2) + "\n")

    hist = METRICS / "strength_history.csv"
    fields = ["timestamp", "steps", "sims",
              "policy_vs_random", "policy_vs_material",
              "mcts_vs_random", "mcts_vs_material"]
    exists = hist.exists()
    with hist.open("a", newline="") as f:
        w = csv.DictWriter(f, fieldnames=fields)
        if not exists:
            w.writeheader()
        w.writerow({
            "timestamp": results["timestamp"], "steps": results["steps"], "sims": args.sims,
            **{k: results[k]["score"] for k in fields[3:]},
        })

    if args.commit:
        subprocess.run(["git", "add", "-A"], check=True)
        pm, mm = results["mcts_vs_random"], results["mcts_vs_material"]
        subprocess.run(["git", "commit", "-m",
                        f"strength: mcts scores {pm['score']:.0%} vs random, "
                        f"{mm['score']:.0%} vs material @ step {meta['steps']}"],
                       capture_output=True)


if __name__ == "__main__":
    main()
