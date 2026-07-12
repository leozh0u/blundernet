#!/usr/bin/env python3
"""Self-play reinforcement: the net plays itself with MCTS, then trains on
what the search discovered.

    python scripts/selfplay.py [--games 2] [--sims 96] [--commit]

The RL loop in miniature (AlphaZero's policy improvement):
- MCTS + the current net play both sides. Early moves are sampled with
  temperature 1 and root Dirichlet noise, so games explore.
- Every position stores the root VISIT DISTRIBUTION — the search's improved
  opinion — not the move actually played.
- The policy head trains toward that distribution (soft cross-entropy) and
  the value head toward the eventual game result. Search output becomes the
  training target: the net learns to predict what a deeper search would say.

At this compute scale (a few CPU games/day) the effect on strength is
modest next to supervised training — the point is that the loop is real,
measured, and runs continuously alongside imitation learning.
"""
import argparse
import datetime as dt
import json
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import chess
import numpy as np
import torch
import torch.nn.functional as F

METRICS = Path("metrics")
TEMP_PLIES = 20  # sample with T=1 for the first N plies, then play greedily


def play_selfplay_game(model, search_fn, sims: int, max_plies: int = 220):
    """One self-play game. Returns (samples, result_str) where each sample is
    (planes, sparse_visit_dist, value_from_mover_perspective placeholder)."""
    from blundernet.encode import encode_board, move_to_index

    board = chess.Board()
    history = []  # (planes, move_indices, visit_probs, mover_is_white)
    while not board.is_game_over(claim_draw=True) and board.ply() < max_plies:
        visits = search_fn(board, model, simulations=sims, dirichlet_eps=0.25)
        moves, counts = zip(*visits.items())
        counts = np.array(counts, dtype=np.float64)
        probs = counts / counts.sum()
        history.append((
            encode_board(board),
            np.array([move_to_index(m) for m in moves], dtype=np.int64),
            probs.astype(np.float32),
            board.turn == chess.WHITE,
        ))
        if board.ply() < TEMP_PLIES:
            move = moves[int(np.random.choice(len(moves), p=probs))]
        else:
            move = moves[int(counts.argmax())]
        board.push(move)

    outcome = board.outcome(claim_draw=True)
    if outcome is None or outcome.winner is None:
        result, z_white = "1/2-1/2", 0.0
    elif outcome.winner == chess.WHITE:
        result, z_white = "1-0", 1.0
    else:
        result, z_white = "0-1", -1.0

    samples = [(x, idxs, probs, z_white if is_white else -z_white)
               for x, idxs, probs, is_white in history]
    return samples, result


def train_on_selfplay(model, opt, meta, samples, batch_size=128):
    """Soft-target policy loss (KL to the visit distribution) + value MSE."""
    from blundernet.encode import POLICY_SIZE

    model.train()
    np.random.shuffle(samples)
    losses = []
    for i in range(0, len(samples), batch_size):
        chunk = samples[i:i + batch_size]
        x = torch.from_numpy(np.stack([s[0] for s in chunk]))
        target = torch.zeros(len(chunk), POLICY_SIZE)
        for r, (_, idxs, probs, _) in enumerate(chunk):
            target[r, idxs] = torch.from_numpy(probs)
        z = torch.tensor([s[3] for s in chunk])
        logits, v = model(x)
        p_loss = -(target * F.log_softmax(logits, dim=1)).sum(dim=1).mean()
        v_loss = F.mse_loss(v, z)
        loss = p_loss + 0.5 * v_loss
        opt.zero_grad(); loss.backward(); opt.step()
        losses.append(loss.item())
        meta["steps"] += 1
    meta["samples_seen"] += len(samples)
    return float(np.mean(losses))


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--games", type=int, default=2)
    ap.add_argument("--sims", type=int, default=96)
    ap.add_argument("--commit", action="store_true")
    args = ap.parse_args()

    from blundernet import mcts, mcts_cpp
    from blundernet.train import load_model, save_model

    search_fn = (mcts_cpp if mcts_cpp.AVAILABLE else mcts).search
    model, opt, meta = load_model()

    all_samples, results = [], []
    for g in range(args.games):
        samples, result = play_selfplay_game(model, search_fn, args.sims)
        all_samples.extend(samples)
        results.append(result)
        print(f"game {g + 1}: {result} ({len(samples)} positions)")

    loss = train_on_selfplay(model, opt, meta, all_samples)
    save_model(model, opt, meta)

    log = {"timestamp": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds"),
           "games": args.games, "sims": args.sims, "results": results,
           "positions": len(all_samples), "loss": round(loss, 4),
           "steps": meta["steps"]}
    METRICS.mkdir(exist_ok=True)
    hist = METRICS / "selfplay_log.jsonl"
    with hist.open("a") as f:
        f.write(json.dumps(log) + "\n")
    print(log)

    if args.commit:
        subprocess.run(["git", "add", "-A"], check=True)
        subprocess.run(["git", "commit", "-m",
                        f"selfplay: {args.games} games ({' '.join(results)}), "
                        f"{len(all_samples)} positions, loss {loss:.3f} "
                        f"@ step {meta['steps']}"], capture_output=True)


if __name__ == "__main__":
    main()
