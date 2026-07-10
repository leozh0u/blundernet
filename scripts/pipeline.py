#!/usr/bin/env python3
"""One scheduled training run: ingest -> train -> eval -> log, committing per stage.

Usage: python scripts/pipeline.py [--no-commit] [--chart]
"""
import argparse
import csv
import datetime as dt
import json
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))

import numpy as np

METRICS_DIR = Path("metrics")
HISTORY = METRICS_DIR / "history.csv"
LATEST = METRICS_DIR / "latest.json"
FIELDS = [
    "timestamp", "steps", "samples_seen", "games", "positions",
    "loss", "policy_loss", "value_loss", "top1", "top3",
    "puzzle_overall", "puzzle_800-1200", "puzzle_1200-1600",
    "puzzle_1600-2000", "puzzle_2000-2400", "puzzle_2400-+",
]


def git_commit(message: str, no_commit: bool) -> None:
    if no_commit:
        print(f"[skip commit] {message}")
        return
    subprocess.run(["git", "add", "-A"], check=True)
    r = subprocess.run(["git", "commit", "-m", message], capture_output=True, text=True)
    print(r.stdout or r.stderr)


def append_history(row: dict) -> None:
    METRICS_DIR.mkdir(exist_ok=True)
    exists = HISTORY.exists()
    with HISTORY.open("a", newline="") as f:
        w = csv.DictWriter(f, fieldnames=FIELDS)
        if not exists:
            w.writeheader()
        w.writerow({k: row.get(k, "") for k in FIELDS})


def make_chart() -> None:
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt

    rows = list(csv.DictReader(HISTORY.open()))
    if len(rows) < 2:
        return
    steps = [int(r["steps"]) for r in rows]

    def col(name):
        return [float(r[name]) if r.get(name) else float("nan") for r in rows]

    fig, axes = plt.subplots(1, 3, figsize=(16, 4))
    axes[0].plot(steps, col("loss"), label="total")
    axes[0].plot(steps, col("policy_loss"), label="policy")
    axes[0].set_title("training loss"); axes[0].set_xlabel("optimizer steps"); axes[0].legend()

    axes[1].plot(steps, [100 * v for v in col("top1")], label="top-1")
    axes[1].plot(steps, [100 * v for v in col("top3")], label="top-3")
    axes[1].set_title("held-out move prediction (%)"); axes[1].set_xlabel("optimizer steps"); axes[1].legend()

    for b in ("puzzle_800-1200", "puzzle_1200-1600", "puzzle_1600-2000",
              "puzzle_2000-2400", "puzzle_2400-+"):
        axes[2].plot(steps, [100 * v for v in col(b)], label=b.replace("puzzle_", ""))
    axes[2].plot(steps, [100 * v for v in col("puzzle_overall")], "k--", lw=2, label="overall")
    axes[2].set_title("tactics puzzle accuracy by rating (%)")
    axes[2].set_xlabel("optimizer steps"); axes[2].legend(fontsize=8)

    fig.tight_layout()
    fig.savefig(METRICS_DIR / "curve.png", dpi=110)
    print("chart updated")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--no-commit", action="store_true")
    ap.add_argument("--chart", action="store_true")
    args = ap.parse_args()

    from blundernet.data import gather_batch
    from blundernet.evaluate import move_accuracy
    from blundernet.puzzles import evaluate_puzzles
    from blundernet.train import load_model, save_model, train_on_batch

    model, opt, meta = load_model()
    now = dt.datetime.now(dt.timezone.utc)
    # 1-3 ingest/train sub-batches, varying by day+hour so runs differ in size
    rng = np.random.default_rng(now.year * 10_000 + now.month * 100 + now.day + now.hour)
    n_batches = int(rng.integers(1, 4))
    print(f"run at {now.isoformat()} -> {n_batches} batch(es)")

    last_train, last_summary, holdout = None, None, None
    for b in range(n_batches):
        X, policy, value, summary = gather_batch(n_players=2)
        if X is None:
            print(f"batch {b}: no new games ({summary})")
            continue
        # hold out 10% for evaluation (never trained on)
        n_hold = max(1, len(X) // 10)
        holdout = (X[:n_hold], policy[:n_hold])
        stats = train_on_batch(model, opt, meta, X[n_hold:], policy[n_hold:], value[n_hold:])
        save_model(model, opt, meta)
        last_train, last_summary = stats, summary
        git_commit(
            f"train: {summary['games']} games / {summary['positions']} positions, "
            f"loss {stats['loss']:.3f} @ step {stats['steps']}",
            args.no_commit,
        )

    if last_train is None:
        print("no data this run; exiting")
        return

    acc = move_accuracy(model, *holdout)
    row = {
        "timestamp": now.isoformat(timespec="seconds"),
        **{k: last_train[k] for k in ("steps", "samples_seen", "loss", "policy_loss", "value_loss")},
        "games": last_summary["games"],
        "positions": last_summary["positions"],
        **{k: round(acc[k], 4) for k in ("top1", "top3")},
    }
    METRICS_DIR.mkdir(exist_ok=True)
    LATEST.write_text(json.dumps({**row, **acc}, indent=2) + "\n")
    git_commit(
        f"eval: top-1 {acc['top1']:.1%} / top-3 {acc['top3']:.1%} "
        f"on {acc['eval_positions']} held-out positions",
        args.no_commit,
    )

    # tactics puzzle suite (fixed, bucketed by difficulty)
    puz = evaluate_puzzles(model)
    row.update(puz)
    append_history(row)
    LATEST.write_text(json.dumps({**row, **acc}, indent=2) + "\n")
    if puz:
        by_bucket = " ".join(
            f"{k.replace('puzzle_', '')}:{v:.0%}"
            for k, v in puz.items()
            if k.startswith("puzzle_") and k not in ("puzzle_overall", "puzzle_n")
        )
        git_commit(
            f"puzzles: {puz['puzzle_overall']:.1%} overall on {puz['puzzle_n']} "
            f"tactics  [{by_bucket}]",
            args.no_commit,
        )

    if args.chart:
        make_chart()
        git_commit("chart: refresh training curves", args.no_commit)


if __name__ == "__main__":
    main()
