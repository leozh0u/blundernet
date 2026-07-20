#!/usr/bin/env bash
# Publish the checkpoint to the rolling release, tolerating transient API
# errors. The commits are already pushed by the time this runs, so a failed
# upload only means the next run resumes from a slightly older checkpoint.
# That is worth a retry and a warning, never a failed run: GitHub's API
# returns the occasional 503, and a red X over bookkeeping is pure noise.
set -uo pipefail

if [ ! -f checkpoint/model.pt ]; then
  echo "no checkpoint to publish"
  exit 0
fi

gh release create model-latest --title "rolling checkpoint" \
  --notes "auto-updated by the training pipeline" 2>/dev/null || true

for attempt in 1 2 3 4; do
  if gh release upload model-latest checkpoint/model.pt --clobber; then
    echo "checkpoint published on attempt ${attempt}"
    exit 0
  fi
  echo "upload attempt ${attempt} failed (transient API error?); retrying"
  sleep $(( attempt * 10 ))
done

echo "::warning::checkpoint upload failed after 4 attempts; next run resumes from the previous checkpoint"
exit 0
