#!/bin/bash
# Change the demo box's instance type in place, keeping the root volume.
#
#   ./scripts/resize.sh t4g.small
#
# Not terraform apply. Plan wants to replace the instance (user_data changed,
# and the AL2023 AMI data source resolves to newest), and the database is on
# the root volume, so an apply destroys it. Stop, modify, start keeps the
# volume. The Elastic IP stays associated through a stop in a VPC, so the
# address does not move.
#
# Update var.instance_type in deploy/demo/main.tf afterwards, or the next plan
# shows drift. The Postgres settings in user_data.sh.tftpl are sized for the
# box too: shared_buffers wants about a quarter of RAM and
# effective_cache_size about two thirds, so they move in the same change.
#
# Expect two to four minutes of downtime.
set -euo pipefail

NEW_TYPE="${1:-}"
[ -n "$NEW_TYPE" ] || { echo "usage: $0 <instance-type>" >&2; exit 2; }

cd "$(dirname "$0")/.."
INSTANCE=$(terraform -chdir=deploy/demo output -raw instance_id)

ROOT_VOL=$(aws ec2 describe-instances --instance-ids "$INSTANCE" \
  --query 'Reservations[0].Instances[0].BlockDeviceMappings[0].Ebs.VolumeId' --output text)
CURRENT=$(aws ec2 describe-instances --instance-ids "$INSTANCE" \
  --query 'Reservations[0].Instances[0].InstanceType' --output text)
echo "$INSTANCE: $CURRENT -> $NEW_TYPE, root volume $ROOT_VOL"

echo "== stopping =="
aws ec2 stop-instances --instance-ids "$INSTANCE" --query 'StoppingInstances[0].CurrentState.Name' --output text
aws ec2 wait instance-stopped --instance-ids "$INSTANCE"

# On top of the nightly dump in S3, and taken while stopped so it is a clean
# image rather than a crash-consistent one. Async on purpose: the snapshot
# finishes in the background and nothing here needs to wait for it.
echo "== snapshot =="
aws ec2 create-snapshot --volume-id "$ROOT_VOL" \
  --description "before $NEW_TYPE resize" --query 'SnapshotId' --output text

echo "== resizing =="
aws ec2 modify-instance-attribute --instance-id "$INSTANCE" \
  --instance-type "{\"Value\": \"$NEW_TYPE\"}"

echo "== starting =="
aws ec2 start-instances --instance-ids "$INSTANCE" --query 'StartingInstances[0].CurrentState.Name' --output text
aws ec2 wait instance-running --instance-ids "$INSTANCE"

for _ in $(seq 1 60); do
  PING=$(aws ssm describe-instance-information --filters "Key=InstanceIds,Values=$INSTANCE" \
    --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null || echo None)
  [ "$PING" = "Online" ] && break
  sleep 5
done
echo "SSM: $PING"

# Wait for blundernet.service to finish its own "docker compose up -d" before
# running anything against compose. Racing it once left the worker in Created
# and the unit failed, which is invisible from the website: puzzles are api
# plus Postgres, so every page loaded while the engine and hints were dead.
echo "== waiting for the boot service =="
PARAMS=$(mktemp -t blundernet-resize)
trap 'rm -f "$PARAMS"' EXIT
cat >"$PARAMS" <<'JSON'
{
  "commands": [
    "for i in $(seq 1 60); do systemctl is-active --quiet blundernet.service && break; sleep 5; done",
    "systemctl is-active blundernet.service || systemctl restart blundernet.service",
    "cd /opt/blundernet && docker compose up -d",
    "sleep 15",
    "docker ps --format '{{.Names}} | {{.Status}}'",
    "free -m | head -2",
    "docker exec blundernet-postgres-1 psql -U blundernet -d blundernet -tAc 'show shared_buffers' -c 'select count(*) from puzzles'"
  ]
}
JSON

CMD=$(aws ssm send-command --instance-ids "$INSTANCE" --document-name AWS-RunShellScript \
  --parameters "file://$PARAMS" --query 'Command.CommandId' --output text)
STATUS=Pending
for _ in $(seq 1 120); do
  STATUS=$(aws ssm get-command-invocation --command-id "$CMD" --instance-id "$INSTANCE" \
    --query Status --output text 2>/dev/null || echo Pending)
  case "$STATUS" in Success|Failed|Cancelled|TimedOut) break ;; esac
  sleep 2
done
aws ssm get-command-invocation --command-id "$CMD" --instance-id "$INSTANCE" \
  --query 'StandardOutputContent' --output text
echo "--- $STATUS ---"

echo "== check =="
echo "five containers up, blundernet.service active, the puzzle count unchanged."
echo "Then re-measure: k6 run -e BASE=https://blundernet.com -e MIX=stress -e RATE=2 loadtest/puzzles.js"
echo "Run it twice. A rebooted box has an empty shared_buffers and the first pass reads cold."
