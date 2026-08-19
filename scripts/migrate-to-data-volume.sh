#!/usr/bin/env bash
# Move Postgres off the root volume and onto the EBS volume that outlives the
# instance. Run in three steps, with a terraform apply in the middle.
#
#   ./scripts/migrate-to-data-volume.sh backup    # fresh dump to S3
#   terraform -chdir=deploy/demo apply            # replaces the box, ~5 min down
#   ./scripts/migrate-to-data-volume.sh restore   # load the dump into the new box
#   ./scripts/migrate-to-data-volume.sh verify
#
# Why this is worth a planned outage. `user_data_replace_on_change = true`
# means any edit to the boot script replaces the instance, and the boot script
# is where the Postgres tuning, the Caddyfile, the systemd units and the backup
# timer all live. So the box gets replaced on purpose, more than once. Today
# the database is on the root volume, which dies with the instance, so every
# one of those edits is a restore-from-backup. After this, it is a reboot.
#
# The boot script already has the whole mechanism: it waits for the attachment,
# formats the device only if `blkid` says it has never been used, mounts it at
# /var/lib/blundernet and writes fstab. That `blkid` check is what makes the
# *second* replacement keep its data. It has simply never run with a volume
# attached, so it has been taking the "no data volume found" fallback and
# writing to the root disk every boot.
#
# The one-time cost is that this first replacement starts with an empty volume,
# so the data has to come back from a dump. That path is already tested.
set -euo pipefail

cd "$(dirname "$0")/.."
STEP="${1:-}"
INSTANCE=$(terraform -chdir=deploy/demo output -raw instance_id)
BUCKET="blundernet-backups-$(aws sts get-caller-identity --query Account --output text)"

# ssm runs one shell command on the box and returns its output, failing loudly.
ssm() {
  local params cmd status
  params=$(mktemp -t blundernet-mig)
  python3 -c 'import json,sys; print(json.dumps({"commands":[sys.argv[1]]}))' "$1" >"$params"
  cmd=$(aws ssm send-command --instance-ids "$INSTANCE" --document-name AWS-RunShellScript \
    --parameters "file://$params" --query 'Command.CommandId' --output text)
  rm -f "$params"
  status=Pending
  for _ in $(seq 1 180); do
    status=$(aws ssm get-command-invocation --command-id "$cmd" --instance-id "$INSTANCE" \
      --query Status --output text 2>/dev/null || echo Pending)
    case "$status" in Success|Failed|Cancelled|TimedOut) break ;; esac
    sleep 2
  done
  aws ssm get-command-invocation --command-id "$cmd" --instance-id "$INSTANCE" \
    --query 'StandardOutputContent' --output text
  if [ "$status" != "Success" ]; then
    aws ssm get-command-invocation --command-id "$cmd" --instance-id "$INSTANCE" \
      --query 'StandardErrorContent' --output text >&2
    echo "ssm step failed: $status" >&2
    return 1
  fi
}

case "$STEP" in
backup)
  # The nightly dump is up to 24 hours old. Take a fresh one, because the
  # window between it and the apply is data nobody gets back.
  echo "== counts before =="
  ssm "docker exec blundernet-postgres-1 psql -U blundernet -d blundernet -tAc \
    \"select 'users=' || (select count(*) from users) || ' games=' || (select count(*) from games) || ' puzzles=' || (select count(*) from puzzles)\""
  echo "== fresh dump =="
  ssm "/opt/blundernet/backup.sh"
  aws s3 ls "s3://$BUCKET/pg/" | tail -3
  echo
  echo "Write those counts down. Then: terraform -chdir=deploy/demo apply"
  ;;

restore)
  # The instance is new, so it has a new id. Re-read it rather than trusting
  # anything cached.
  echo "== new instance: $INSTANCE =="
  echo "== confirming Postgres is on the data volume, not the root disk =="
  ssm "findmnt -no SOURCE,TARGET /var/lib/blundernet || { echo 'NOT MOUNTED: the boot script took the fallback, stop here' >&2; exit 1; }"

  LATEST=$(aws s3 ls "s3://$BUCKET/pg/" | sort | tail -1 | awk '{print $4}')
  echo "== restoring $LATEST =="
  # --clean --if-exists is already baked into the dump, so this drops what it
  # replaces rather than erroring on a fresh database.
  ssm "set -e
    aws s3 cp s3://$BUCKET/pg/$LATEST /tmp/restore.sql.gz --only-show-errors
    gunzip -c /tmp/restore.sql.gz | docker exec -i blundernet-postgres-1 psql -U blundernet -d blundernet -q
    rm -f /tmp/restore.sql.gz
    echo restored"
  ;;

verify)
  echo "== counts after =="
  ssm "docker exec blundernet-postgres-1 psql -U blundernet -d blundernet -tAc \
    \"select 'users=' || (select count(*) from users) || ' games=' || (select count(*) from games) || ' puzzles=' || (select count(*) from puzzles) || ' cells=' || (select count(*) from puzzle_cells)\""
  echo "== where the data now lives =="
  ssm "findmnt -no SOURCE,TARGET,SIZE,USED /var/lib/blundernet; lsblk"
  echo "== stack =="
  ssm "docker ps --format '{{.Names}} | {{.Status}}'; systemctl is-active blundernet.service"
  echo
  echo "Counts must match the backup step. Then run the e2e:"
  echo "  BASE=https://blundernet.com ./scripts/e2e.sh"
  ;;

*)
  echo "usage: $0 {backup|restore|verify}" >&2
  exit 2
  ;;
esac
