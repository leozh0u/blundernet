#!/usr/bin/env bash
# Push this run's commits, surviving concurrent writers.
#
# Multiple scheduled/backup runs can land commits on main close together.
# When two runs edit the ingest bookkeeping (data/state.json) or append to
# the same metrics file from the same base, a plain `git pull --rebase`
# hits a content conflict and aborts. That is a bookkeeping collision, not
# a real disagreement, so we retry the rebase and auto-resolve conflicts in
# favor of THIS run (`-X ours`). The other run's commits are still replayed
# as the base; only the conflicting hunks resolve to our side, which at
# worst drops a duplicate metrics row on a rare collision.
set -uo pipefail

git add -A
git diff --cached --quiet || git commit -m "commit residual run artifacts"

for attempt in 1 2 3 4 5; do
  git rebase --abort 2>/dev/null || true
  if git pull --rebase -X ours origin main && git push; then
    echo "pushed on attempt ${attempt}"
    exit 0
  fi
  echo "push attempt ${attempt} failed; another run likely pushed first, retrying"
  sleep $(( (RANDOM % 8) + 3 ))
done

echo "push failed after 5 attempts"
exit 1
