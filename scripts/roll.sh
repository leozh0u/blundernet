#!/bin/bash
# Ship already-pushed images to the running demo box, without terraform.
#
#   ./scripts/roll.sh            # roll api and worker
#   ./scripts/roll.sh api        # roll one service
#
# terraform apply is the wrong tool for a code change here: it replaces the
# instance on any user_data edit, and the database is on the root volume. This
# pulls the new image over SSM instead, so nothing about the box changes.
#
# Push the images first, with the platform the box actually runs:
#   docker build --platform linux/arm64 --provenance=false --target api -t "$(terraform -chdir=deploy/demo output -raw ecr_api):latest" .
#   docker push "$(terraform -chdir=deploy/demo output -raw ecr_api):latest"
#
# Note the braces. zsh reads ":l" as a lowercase modifier, so "$API:latest"
# silently becomes "...-apiatest".
set -euo pipefail

cd "$(dirname "$0")/.."
SERVICES="${*:-api worker}"

INSTANCE=$(terraform -chdir=deploy/demo output -raw instance_id)
REGISTRY=$(terraform -chdir=deploy/demo output -raw ecr_api | cut -d/ -f1)

PARAMS=$(mktemp -t blundernet-roll)
trap 'rm -f "$PARAMS"' EXIT

# The box's ECR token expires after twelve hours, so a roll that has not run
# for a day fails on "authorization token has expired" before it pulls
# anything. Re-authenticating first is cheaper than diagnosing that again. The
# instance role mints the token itself; nothing secret passes through here.
python3 - "$REGISTRY" "$SERVICES" >"$PARAMS" <<'PY'
import json, sys
registry, services = sys.argv[1], sys.argv[2]
print(json.dumps({"commands": [
    "set -e",
    f"aws ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin {registry}",
    f"cd /opt/blundernet && docker compose pull {services} && docker compose up -d {services}",
    "sleep 10",
    "docker ps --format '{{.Names}} | {{.Status}}'",
    "systemctl is-active blundernet.service",
]}))
PY

echo "rolling [$SERVICES] on $INSTANCE"
CMD=$(aws ssm send-command --instance-ids "$INSTANCE" \
  --document-name AWS-RunShellScript --parameters "file://$PARAMS" \
  --query 'Command.CommandId' --output text)

STATUS=Pending
for _ in $(seq 1 90); do
  STATUS=$(aws ssm get-command-invocation --command-id "$CMD" --instance-id "$INSTANCE" \
    --query Status --output text 2>/dev/null || echo Pending)
  case "$STATUS" in Success|Failed|Cancelled|TimedOut) break ;; esac
  sleep 2
done

aws ssm get-command-invocation --command-id "$CMD" --instance-id "$INSTANCE" \
  --query 'StandardOutputContent' --output text
aws ssm get-command-invocation --command-id "$CMD" --instance-id "$INSTANCE" \
  --query 'StandardErrorContent' --output text >&2

echo "--- $STATUS ---"
[ "$STATUS" = "Success" ] || exit 1

# All five containers, not just the site. Puzzles are api plus Postgres, so a
# dead worker leaves every page loading while the engine and hints are down.
echo "expect five containers up and blundernet.service active"
