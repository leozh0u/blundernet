"""Fetch fresh blitz games of strong players from the Lichess API.

Keeps a cursor per player in data/state.json so every run trains on games
it has never seen before. Raw PGNs are transient — only summary stats are
committed to the repo.
"""
import io
import json
import time
from pathlib import Path

import chess.pgn
import numpy as np
import requests

from .encode import encode_board, move_to_index

API = "https://lichess.org/api/games/user/{user}"

# Fallback pool if the leaderboard fetch fails (strong, high-volume players)
FALLBACK_PLAYERS = [
    "penguingim1", "nihalsarin2004", "RebeccaHarris", "Zhigalko_Sergei",
    "Vladimirovich9000", "may6enexttime", "Night-King96", "Chesstoday",
    "IWANNABEADOORED", "Arka50", "muisback", "Msb2",
]


def get_players() -> list[str]:
    """Current top-50 blitz leaderboard — active players by construction."""
    try:
        r = requests.get("https://lichess.org/api/player/top/50/blitz",
                         headers={"Accept": "application/vnd.lichess.v3+json"},
                         timeout=30)
        r.raise_for_status()
        users = [u["username"] for u in r.json()["users"]]
        return users if len(users) >= 10 else FALLBACK_PLAYERS
    except (requests.RequestException, KeyError, ValueError):
        return FALLBACK_PLAYERS

STATE_PATH = Path("data/state.json")
RESULT_VALUE = {"1-0": 1.0, "0-1": -1.0, "1/2-1/2": 0.0}


def load_state() -> dict:
    if STATE_PATH.exists():
        return json.loads(STATE_PATH.read_text())
    return {"cursor": {}, "seen_ids": [], "rotation": 0,
            "total_games": 0, "total_positions": 0}


def save_state(state: dict) -> None:
    STATE_PATH.parent.mkdir(exist_ok=True)
    STATE_PATH.write_text(json.dumps(state, indent=2) + "\n")


def fetch_games(user: str, since_ms: int | None, max_games: int = 60) -> str:
    params = {
        "max": max_games,
        "perfType": "blitz,rapid",
        "rated": "true",
        "moves": "true",
        "tags": "true",
    }
    if since_ms is not None:
        params["since"] = since_ms
    r = requests.get(
        API.format(user=user),
        params=params,
        headers={"Accept": "application/x-chess-pgn"},
        timeout=120,
    )
    r.raise_for_status()
    return r.text


def pgn_to_samples(pgn_text: str, seen_ids: set | None = None,
                   sample_every: int = 1, min_ply: int = 8):
    """Yield (planes, policy_index, value) from mainline positions.

    Skips games whose GameId is in seen_ids and adds new ids to it.
    """
    stream = io.StringIO(pgn_text)
    n_games = 0
    while True:
        game = chess.pgn.read_game(stream)
        if game is None:
            break
        gid = game.headers.get("GameId", "")
        if seen_ids is not None:
            if gid in seen_ids:
                continue
            seen_ids.add(gid)
        result = RESULT_VALUE.get(game.headers.get("Result", "*"))
        if result is None:
            continue
        n_games += 1
        board = game.board()
        for ply, move in enumerate(game.mainline_moves()):
            if ply >= min_ply and ply % sample_every == 0:
                # value target is from the side-to-move's perspective
                v = result if board.turn == chess.WHITE else -result
                yield encode_board(board), move_to_index(move), v
            board.push(move)
    yield None, n_games, None  # sentinel carrying game count


def gather_batch(n_players: int = 3, max_games: int = 100,
                 min_positions: int = 2000, max_players: int = 8):
    """Fetch fresh games, walking the player rotation until the batch has at
    least min_positions (or max_players tried). Returns (X, policy, value, summary).
    """
    state = load_state()
    players = get_players()
    seen_ids = set(state.get("seen_ids", []))
    xs, ps, vs = [], [], []
    used, games_total = [], 0
    now_ms = int(time.time() * 1000)

    i = -1
    while (i := i + 1) < max_players:
        if i >= n_players and len(xs) >= min_positions:
            break
        user = players[(state["rotation"] + i) % len(players)]
        since = state["cursor"].get(user)
        try:
            pgn = fetch_games(user, since, max_games)
            if not pgn.strip() and since is None:
                pass  # player has no games at all
            elif not pgn.strip():
                # nothing new since cursor: grab their most recent games once
                pgn = fetch_games(user, None, max_games)
        except requests.RequestException as e:
            print(f"fetch failed for {user}: {e}")
            continue
        n_games = 0
        for x, p, v in pgn_to_samples(pgn, seen_ids):
            if x is None:
                n_games = p
                break
            xs.append(x); ps.append(p); vs.append(v)
        games_total += n_games
        state["cursor"][user] = now_ms
        used.append({"player": user, "games": n_games})
        time.sleep(1)  # be polite to the API

    state["rotation"] = (state["rotation"] + i) % len(players)
    state["seen_ids"] = sorted(seen_ids)[-20000:]  # cap state file size
    state["total_games"] += games_total
    state["total_positions"] += len(xs)
    save_state(state)

    summary = {"players": used, "games": games_total, "positions": len(xs)}
    if not xs:
        return None, None, None, summary
    return (
        np.stack(xs),
        np.array(ps, dtype=np.int64),
        np.array(vs, dtype=np.float32),
        summary,
    )
