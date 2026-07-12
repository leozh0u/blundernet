"""PUCT Monte-Carlo Tree Search over the policy/value network (AlphaZero-style).

The net's policy head gives priors over moves; the value head evaluates leaf
positions. Search repeatedly walks the tree picking the move that maximizes
    Q(s,a) + c_puct * P(s,a) * sqrt(N(s)) / (1 + N(s,a))
— exploitation (Q) plus a prior-weighted exploration bonus that decays as a
move gets visited. Visit counts at the root become the improved policy.
"""
import math

import chess
import numpy as np
import torch

from .encode import encode_board, move_to_index


class Node:
    __slots__ = ("prior", "visits", "value_sum", "children")

    def __init__(self, prior: float):
        self.prior = prior
        self.visits = 0
        self.value_sum = 0.0
        self.children = {}  # move -> Node

    @property
    def q(self) -> float:
        return self.value_sum / self.visits if self.visits else 0.0


@torch.no_grad()
def _expand(node: Node, board: chess.Board, model) -> float:
    """Expand a leaf: set children priors from the policy head, return value."""
    x = torch.from_numpy(encode_board(board)).unsqueeze(0)
    logits, value = model(x)
    logits = logits[0]
    moves = list(board.legal_moves)
    idxs = torch.tensor([move_to_index(m) for m in moves])
    priors = torch.softmax(logits[idxs], dim=0).numpy()
    for move, p in zip(moves, priors):
        node.children[move] = Node(float(p))
    return float(value[0])


def _terminal_value(board: chess.Board) -> float | None:
    """Value from the perspective of the side to move, if game over."""
    outcome = board.outcome()
    if outcome is None:
        return None
    if outcome.winner is None:
        return 0.0
    return -1.0  # side to move has no moves and lost (or was mated)


def search(board: chess.Board, model, simulations: int = 200,
           c_puct: float = 1.5, dirichlet_eps: float = 0.0) -> dict:
    """Run MCTS from `board`. Returns {move: visit_count} at the root."""
    model.eval()
    root = Node(0.0)
    _expand(root, board, model)

    # optional root exploration noise (used during self-play training)
    if dirichlet_eps > 0 and root.children:
        noise = np.random.dirichlet([0.3] * len(root.children))
        for n, child in zip(noise, root.children.values()):
            child.prior = (1 - dirichlet_eps) * child.prior + dirichlet_eps * n

    for _ in range(simulations):
        node, path = root, []
        b = board.copy(stack=False)

        # 1. SELECT: walk down via PUCT until we hit a leaf
        while node.children:
            sqrt_n = math.sqrt(node.visits + 1)
            best, best_score = None, -1e9
            for move, child in node.children.items():
                u = c_puct * child.prior * sqrt_n / (1 + child.visits)
                score = -child.q + u  # child.q is from the child mover's view
                if score > best_score:
                    best, best_score = move, score
            path.append(node.children[best])
            b.push(best)
            node = node.children[best]

        # 2. EXPAND + EVALUATE
        term = _terminal_value(b)
        value = term if term is not None else _expand(node, b, model)

        # 3. BACKPROPAGATE: flip sign each ply (alternating perspectives)
        root.visits += 1
        for n in reversed(path):
            n.visits += 1
            n.value_sum += value
            value = -value

    return {move: child.visits for move, child in root.children.items()}


def best_move(board: chess.Board, model, simulations: int = 200,
              temperature: float = 0.0) -> chess.Move:
    """Pick a move: argmax visits (T=0) or sample proportional to visits^(1/T)."""
    visits = search(board, model, simulations)
    moves, counts = zip(*visits.items())
    counts = np.array(counts, dtype=np.float64)
    if temperature <= 1e-6:
        return moves[int(counts.argmax())]
    probs = counts ** (1.0 / temperature)
    probs /= probs.sum()
    return moves[int(np.random.choice(len(moves), p=probs))]
