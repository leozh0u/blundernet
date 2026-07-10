"""Board -> tensor encoding and move -> policy-index mapping."""
import chess
import numpy as np

# 18 planes: 12 piece planes, side to move, 4 castling rights, en passant file
PLANES = 18


def encode_board(board: chess.Board) -> np.ndarray:
    x = np.zeros((PLANES, 8, 8), dtype=np.float32)
    for sq, piece in board.piece_map().items():
        plane = (piece.piece_type - 1) + (0 if piece.color == chess.WHITE else 6)
        x[plane, sq // 8, sq % 8] = 1.0
    if board.turn == chess.WHITE:
        x[12].fill(1.0)
    for i, right in enumerate([
        board.has_kingside_castling_rights(chess.WHITE),
        board.has_queenside_castling_rights(chess.WHITE),
        board.has_kingside_castling_rights(chess.BLACK),
        board.has_queenside_castling_rights(chess.BLACK),
    ]):
        if right:
            x[13 + i].fill(1.0)
    if board.ep_square is not None:
        x[17, :, board.ep_square % 8] = 1.0
    return x


# Policy head: 64*64 = 4096 (from-square, to-square). Underpromotions are
# folded into the queen-promotion move (same from/to squares) — a standard
# simplification that costs <0.5% of moves in practice.
POLICY_SIZE = 4096


def move_to_index(move: chess.Move) -> int:
    return move.from_square * 64 + move.to_square


def index_to_move(idx: int, board: chess.Board) -> chess.Move:
    move = chess.Move(idx // 64, idx % 64)
    # restore promotion if this from/to pair is a pawn reaching last rank
    piece = board.piece_at(move.from_square)
    if piece and piece.piece_type == chess.PAWN and chess.square_rank(move.to_square) in (0, 7):
        move.promotion = chess.QUEEN
    return move


def legal_move_mask(board: chess.Board) -> np.ndarray:
    mask = np.zeros(POLICY_SIZE, dtype=bool)
    for m in board.legal_moves:
        mask[move_to_index(m)] = True
    return mask
