"""Incremental training: one run = one pass over a freshly fetched batch."""
import json
from pathlib import Path

import numpy as np
import torch
import torch.nn.functional as F

from .model import BlunderNet

CKPT = Path("checkpoint/model.pt")


def load_model() -> tuple[BlunderNet, torch.optim.Optimizer, dict]:
    model = BlunderNet()
    opt = torch.optim.AdamW(model.parameters(), lr=3e-4, weight_decay=1e-4)
    meta = {"steps": 0, "samples_seen": 0}
    if CKPT.exists():
        ckpt = torch.load(CKPT, map_location="cpu", weights_only=True)
        model.load_state_dict(ckpt["model"])
        opt.load_state_dict(ckpt["opt"])
        meta = ckpt["meta"]
    return model, opt, meta


def save_model(model, opt, meta) -> None:
    CKPT.parent.mkdir(exist_ok=True)
    torch.save({"model": model.state_dict(), "opt": opt.state_dict(), "meta": meta}, CKPT)


def train_on_batch(model, opt, meta, X, policy, value, batch_size=256, epochs=1):
    model.train()
    n = len(X)
    losses, p_losses, v_losses = [], [], []
    for _ in range(epochs):
        perm = np.random.permutation(n)
        for i in range(0, n, batch_size):
            idx = perm[i:i + batch_size]
            xb = torch.from_numpy(X[idx])
            pb = torch.from_numpy(policy[idx])
            vb = torch.from_numpy(value[idx])
            logits, v = model(xb)
            p_loss = F.cross_entropy(logits, pb)
            v_loss = F.mse_loss(v, vb)
            loss = p_loss + 0.5 * v_loss
            opt.zero_grad()
            loss.backward()
            opt.step()
            losses.append(loss.item())
            p_losses.append(p_loss.item())
            v_losses.append(v_loss.item())
            meta["steps"] += 1
    meta["samples_seen"] += n * epochs
    return {
        "loss": float(np.mean(losses)),
        "policy_loss": float(np.mean(p_losses)),
        "value_loss": float(np.mean(v_losses)),
        "steps": meta["steps"],
        "samples_seen": meta["samples_seen"],
    }
