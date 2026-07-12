"""Head-to-head match runner + baseline opponents.

Baselines exist so every strength claim is anchored: if the engine can't
crush random-mover, nothing else matters. Material-greedy is the classic
"knows piece values, zero positional sense" straw man.
"""
import random

import chess
import numpy as np

PIECE_VALUES = {
    chess.PAWN: 1, chess.KNIGHT: 3, chess.BISHOP: 3,
    chess.ROOK: 5, chess.QUEEN: 9, chess.KING: 0,
}


def random_mover(board: chess.Board) -> chess.Move:
    return random.choice(list(board.legal_moves))


def material_greedy(board: chess.Board) -> chess.Move:
    """1-ply material search: take the best capture, else a random safe-ish move."""
    def material(b: chess.Board, color: bool) -> int:
        return sum(PIECE_VALUES[p.piece_type] * (1 if p.color == color else -1)
                   for p in b.piece_map().values())

    best, best_gain = [], -1e9
    for move in board.legal_moves:
        board.push(move)
        if board.is_checkmate():
            board.pop()
            return move
        gain = material(board, not board.turn)  # from mover's perspective
        board.pop()
        if gain > best_gain:
            best, best_gain = [move], gain
        elif gain == best_gain:
            best.append(move)
    return random.choice(best)


def play_game(white_fn, black_fn, max_plies: int = 300) -> str:
    """Play one game between two move functions. Returns '1-0', '0-1', '1/2-1/2'."""
    board = chess.Board()
    while not board.is_game_over(claim_draw=True) and board.ply() < max_plies:
        fn = white_fn if board.turn == chess.WHITE else black_fn
        board.push(fn(board))
    outcome = board.outcome(claim_draw=True)
    if outcome is None or outcome.winner is None:
        return "1/2-1/2"
    return "1-0" if outcome.winner == chess.WHITE else "0-1"


def match(engine_fn, opponent_fn, games: int = 20, max_plies: int = 300) -> dict:
    """Play a color-alternating match. Returns W/D/L from the engine's view."""
    w = d = l = 0
    for g in range(games):
        if g % 2 == 0:
            result = play_game(engine_fn, opponent_fn, max_plies)
            w += result == "1-0"; l += result == "0-1"
        else:
            result = play_game(opponent_fn, engine_fn, max_plies)
            w += result == "0-1"; l += result == "1-0"
        d += result == "1/2-1/2"
    score = (w + 0.5 * d) / games
    return {"wins": w, "draws": d, "losses": l, "games": games,
            "score": round(score, 3), "elo_diff": elo_diff(score)}


def elo_diff(score: float) -> float | None:
    """Elo difference implied by a match score (logistic model)."""
    s = min(max(score, 1e-3), 1 - 1e-3)  # clamp: a 100% score is not +inf Elo
    return round(float(-400 * np.log10(1 / s - 1)), 1)
