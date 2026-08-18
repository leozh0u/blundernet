#!/usr/bin/env bash
# Nightly database backup: pg_dump out of the running container, gzip, and put
# it in S3 under a dated key.
#
# A dump rather than an EBS snapshot on purpose. A snapshot copies the whole
# volume, is tied to this account and region, and restoring one means building
# an instance around it. A 40MB gzip of SQL restores into any Postgres,
# including a laptop, which is what makes it testable. Testing the restore is
# the only thing that separates a backup from a hope.
set -euo pipefail

BUCKET="${BACKUP_BUCKET:-blundernet-backups-222210925967}"
STAMP="$(date -u +%Y-%m-%dT%H-%M-%SZ)"
KEY="pg/blundernet-${STAMP}.sql.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --clean --if-exists so a restore drops what it is replacing rather than
# erroring on objects that already exist.
docker exec blundernet-postgres-1 pg_dump -U blundernet --clean --if-exists blundernet \
  | gzip -9 > "$TMP/dump.sql.gz"

SIZE=$(stat -c %s "$TMP/dump.sql.gz")
# A dump that comes back tiny means pg_dump failed halfway and the pipe still
# succeeded. Refuse to upload it over a good one.
if [ "$SIZE" -lt 100000 ]; then
  echo "dump is only ${SIZE} bytes, refusing to upload" >&2
  exit 1
fi

aws s3 cp "$TMP/dump.sql.gz" "s3://${BUCKET}/${KEY}" --only-show-errors
echo "backed up ${SIZE} bytes to s3://${BUCKET}/${KEY}"
