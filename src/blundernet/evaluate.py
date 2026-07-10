"""Evaluation: held-out move-prediction accuracy on fresh games.

Top-1 accuracy = how often the net's highest-scored legal move is exactly
the move the strong player chose. Top-3 counts a hit if the played move is
among the net's three best.
"""
import numpy as np
import torch

from .encode import POLICY_SIZE


@torch.no_grad()
def move_accuracy(model, X, policy, masks=None, batch_size=512):
    model.eval()
    n = len(X)
    top1 = top3 = 0
    for i in range(0, n, batch_size):
        xb = torch.from_numpy(X[i:i + batch_size])
        logits, _ = model(xb)
        if masks is not None:
            mb = torch.from_numpy(masks[i:i + batch_size])
            logits = logits.masked_fill(~mb, -1e9)
        target = torch.from_numpy(policy[i:i + batch_size])
        top = logits.topk(3, dim=1).indices
        top1 += (top[:, 0] == target).sum().item()
        top3 += (top == target.unsqueeze(1)).any(dim=1).sum().item()
    return {"top1": top1 / n, "top3": top3 / n, "eval_positions": n}
