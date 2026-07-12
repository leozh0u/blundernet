"""Batched MCTS driver over the C++ tree core (blundercore).

The C++ side owns the tree and PUCT selection with virtual loss; this side
owns chess rules and the network. Each iteration asks the tree for a batch
of leaves, replays their move paths on board copies, and evaluates all of
them through the net in ONE forward pass — that batching, not the C++ per
se, is where most of the end-to-end speedup comes from.
"""
import chess
import numpy as np
import torch

from .encode import encode_board, move_to_index

try:
    import blundercore
    AVAILABLE = True
except ImportError:
    AVAILABLE = False


@torch.no_grad()
def search(board: chess.Board, model, simulations: int = 200,
           c_puct: float = 1.5, batch_size: int = 16,
           dirichlet_eps: float = 0.0) -> dict:
    """Run batched MCTS. Returns {move: visit_count} at the root."""
    model.eval()
    tree = blundercore.Tree(c_puct)
    node_moves = {}  # node_id -> ordered list of legal moves (child slots)

    def expand_leaves(leaf_ids, boards):
        """One batched NN call for every non-terminal leaf."""
        pending = []  # (leaf, board, moves)
        for leaf, b in zip(leaf_ids, boards):
            outcome = b.outcome()
            if outcome is not None:
                v = 0.0 if outcome.winner is None else -1.0
                tree.backprop(leaf, v)
            else:
                pending.append((leaf, b, list(b.legal_moves)))
        if not pending:
            return
        x = torch.from_numpy(np.stack([encode_board(b) for _, b, _ in pending]))
        logits, values = model(x)
        for row, (leaf, b, moves) in enumerate(pending):
            idxs = torch.tensor([move_to_index(m) for m in moves])
            priors = torch.softmax(logits[row][idxs], dim=0).tolist()
            node_moves[leaf] = moves
            tree.expand_backprop(leaf, priors, float(values[row]))

    # expand the root first so selection has children to walk
    expand_leaves([0], [board])
    if dirichlet_eps > 0:
        n = len(node_moves.get(0, []))
        if n:
            tree.add_root_noise(np.random.dirichlet([0.3] * n).tolist(),
                                dirichlet_eps)

    while tree.root_visits() < simulations:
        leaf_ids, paths = tree.select_batch(batch_size)
        if not leaf_ids:
            break  # every path converged on an in-flight leaf
        boards = []
        for path in paths:
            b = board.copy(stack=False)
            for parent_id, slot, _child_id in path:
                b.push(node_moves[parent_id][slot])
            boards.append(b)
        expand_leaves(leaf_ids, boards)

    visits = tree.root_child_visits()
    return {m: v for m, v in zip(node_moves[0], visits)}


def best_move(board: chess.Board, model, simulations: int = 200,
              temperature: float = 0.0, batch_size: int = 16) -> chess.Move:
    visits = search(board, model, simulations, batch_size=batch_size)
    moves, counts = zip(*visits.items())
    counts = np.array(counts, dtype=np.float64)
    if temperature <= 1e-6:
        return moves[int(counts.argmax())]
    probs = counts ** (1.0 / temperature)
    probs /= probs.sum()
    return moves[int(np.random.choice(len(moves), p=probs))]
