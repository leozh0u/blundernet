#!/bin/bash
# Put a change on blundernet.com, and prove it landed.
#
#   ./scripts/ship.sh              # ship the api
#   ./scripts/ship.sh api worker   # ship both
#
# This exists because the alternative is a chain of eight commands typed out by
# hand every time, which is how the traps below get forgotten. Every one of
# them was learned by getting it wrong at least once.
set -euo pipefail

cd "$(dirname "$0")/.."

SERVICES="${*:-api}"
TF_DIR=deploy/demo
PLATFORM=linux/arm64        # the box is Graviton, so images have to be arm64
AWS_REGION=us-east-1

# docker login writes credsStore into the real config and then hangs on this
# laptop. Pointing DOCKER_CONFIG at a scratch directory sidesteps it.
#
# The plugins have to be linked across, because DOCKER_CONFIG is also where the
# CLI looks for them. Without this docker quietly falls back to the legacy
# builder, which does not understand --provenance, and the build fails on a
# flag that works fine by hand.
export DOCKER_CONFIG=/tmp/blundernet-docker
mkdir -p "$DOCKER_CONFIG"
if [ -d "$HOME/.docker/cli-plugins" ] && [ ! -e "$DOCKER_CONFIG/cli-plugins" ]; then
  ln -s "$HOME/.docker/cli-plugins" "$DOCKER_CONFIG/cli-plugins"
fi

REGISTRY=$(terraform -chdir="$TF_DIR" output -raw ecr_api | cut -d/ -f1)

echo "==> logging in to $REGISTRY"
aws ecr get-login-password --region "$AWS_REGION" \
  | docker login --username AWS --password-stdin "$REGISTRY" >/dev/null

for svc in $SERVICES; do
  # Note the braces on "${image}:latest". zsh reads ":l" as a lowercase
  # modifier, so "$image:latest" silently becomes "...-apiatest": a correctly
  # built image under a name nothing will ever pull.
  image=$(terraform -chdir="$TF_DIR" output -raw "ecr_$svc")

  echo "==> building $svc"
  docker build --platform "$PLATFORM" --provenance=false \
    --target "$svc" -t "${image}:latest" . >/dev/null

  echo "==> pushing $svc"
  docker push "${image}:latest" >/dev/null

  # What we believe we just shipped, to check against the box afterwards.
  eval "want_$svc=\$(docker inspect --format='{{index .RepoDigests 0}}' '${image}:latest' | cut -d@ -f2)"
done

./scripts/roll.sh $SERVICES

# The check that matters. An expired ECR token makes the push fail while the
# roll still reports success, because pulling an unchanged tag is not an error.
# "Success" is therefore not evidence, and twice now it has quietly redeployed
# the previous image. Ask the box what it is actually running.
INSTANCE=$(terraform -chdir="$TF_DIR" output -raw instance_id)
PARAMS=$(mktemp -t blundernet-ship)
trap 'rm -f "$PARAMS"' EXIT

for svc in $SERVICES; do
  eval "want=\$want_$svc"

  # printf rather than an escaped JSON string inside the aws call: the $( ) has
  # to reach the box intact, and quoting that through two layers by hand is how
  # the first version of this sent a broken command and then reported the empty
  # answer as a mismatch.
  # Two hops on purpose: a container has no RepoDigests, that is an image
  # field, so the container's image id is resolved first and the digest read
  # off that.
  printf '{"commands":["cd /opt/blundernet","docker inspect --format=%s{{index .RepoDigests 0}}%s $(docker inspect --format=%s{{.Image}}%s $(docker compose ps -q %s))"]}' \
    "'" "'" "'" "'" "$svc" >"$PARAMS"

  cmd=$(aws ssm send-command --instance-ids "$INSTANCE" \
    --document-name AWS-RunShellScript --parameters "file://$PARAMS" \
    --query 'Command.CommandId' --output text)

  for _ in $(seq 1 30); do
    state=$(aws ssm get-command-invocation --command-id "$cmd" --instance-id "$INSTANCE" \
      --query Status --output text 2>/dev/null || echo Pending)
    case "$state" in Success|Failed|Cancelled|TimedOut) break ;; esac
    sleep 2
  done

  got=$(aws ssm get-command-invocation --command-id "$cmd" --instance-id "$INSTANCE" \
    --query 'StandardOutputContent' --output text | tr -d '\r\n' | cut -d@ -f2)

  if [ -n "$got" ] && [ "$got" = "$want" ]; then
    echo "==> $svc is running what was just pushed (${want:0:19}...)"
  else
    echo "==> $svc MISMATCH" >&2
    echo "    pushed:  $want" >&2
    echo "    running: ${got:-<no answer from the box>}" >&2
    exit 1
  fi
done
