"""Small AlphaZero-style residual policy+value network (~450k params, CPU-friendly)."""
import torch
import torch.nn as nn
import torch.nn.functional as F

from .encode import PLANES, POLICY_SIZE

CHANNELS = 64
BLOCKS = 6


class ResBlock(nn.Module):
    def __init__(self, c: int):
        super().__init__()
        self.c1 = nn.Conv2d(c, c, 3, padding=1, bias=False)
        self.b1 = nn.BatchNorm2d(c)
        self.c2 = nn.Conv2d(c, c, 3, padding=1, bias=False)
        self.b2 = nn.BatchNorm2d(c)

    def forward(self, x):
        y = F.relu(self.b1(self.c1(x)))
        y = self.b2(self.c2(y))
        return F.relu(x + y)


class BlunderNet(nn.Module):
    def __init__(self):
        super().__init__()
        self.stem = nn.Sequential(
            nn.Conv2d(PLANES, CHANNELS, 3, padding=1, bias=False),
            nn.BatchNorm2d(CHANNELS),
            nn.ReLU(),
        )
        self.blocks = nn.Sequential(*[ResBlock(CHANNELS) for _ in range(BLOCKS)])
        self.policy_head = nn.Sequential(
            nn.Conv2d(CHANNELS, 8, 1),
            nn.Flatten(),
            nn.ReLU(),
            nn.Linear(8 * 64, POLICY_SIZE),
        )
        self.value_head = nn.Sequential(
            nn.Conv2d(CHANNELS, 4, 1),
            nn.Flatten(),
            nn.ReLU(),
            nn.Linear(4 * 64, 64),
            nn.ReLU(),
            nn.Linear(64, 1),
            nn.Tanh(),
        )

    def forward(self, x):
        h = self.blocks(self.stem(x))
        return self.policy_head(h), self.value_head(h).squeeze(-1)
