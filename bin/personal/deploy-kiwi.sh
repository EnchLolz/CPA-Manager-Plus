#!/usr/bin/env bash
# Deploy a personal image tag to kiwi, keeping the existing volume and env.
#   bin/personal/deploy-kiwi.sh ench-v1.14.2-1
#   bin/personal/deploy-kiwi.sh --local ench-v1.14.2-1   # build here, ship over ssh (no GHCR)
set -euo pipefail

host="${KIWI_HOST:-kiwi}"
image="ghcr.io/enchlolz/cpa-manager-plus"
container="cpa-manager-plus"
bind="${KIWI_BIND:-100.79.242.61}"
local_build=false
if [[ "${1:-}" == "--local" ]]; then local_build=true; shift; fi
tag="${1:?usage: deploy-kiwi.sh [--local] <tag>}"

if $local_build; then
  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
  echo ">> building ${image}:${tag} for linux/amd64"
  docker buildx build --platform linux/amd64 -f "$repo_root/Dockerfile.manager-server" \
    --build-arg "VERSION=${tag}" --build-arg "SOURCE_COMMIT=$(git -C "$repo_root" rev-parse HEAD)" \
    -t "${image}:${tag}" --load "$repo_root"
  echo ">> shipping image to ${host}"
  docker save "${image}:${tag}" | gzip | ssh "$host" 'gunzip | docker load'
else
  echo ">> pulling ${image}:${tag} on ${host}"
  ssh "$host" "docker pull ${image}:${tag}"
fi

echo ">> recreating ${container}"
ssh "$host" bash -s -- "$container" "$image" "$tag" "$bind" <<'REMOTE'
set -euo pipefail
container="$1"; image="$2"; tag="$3"; bind="$4"
previous="$(docker inspect --format '{{.Config.Image}}' "$container" 2>/dev/null || true)"
docker rm -f "$container" >/dev/null 2>&1 || true
docker run -d --name "$container" --restart unless-stopped \
  -p "${bind}:18317:18317" \
  -v cpa-manager-plus-data:/data \
  -e HTTP_ADDR=0.0.0.0:18317 \
  -e USAGE_DATA_DIR=/data \
  -e USAGE_DB_PATH=/data/usage.sqlite \
  -e CLAUDE_RESET_PRIORITY=true \
  "${image}:${tag}" >/dev/null
for _ in $(seq 1 30); do
  if curl -fsS "http://${bind}:18317/health" >/dev/null 2>&1; then
    echo "healthy: ${image}:${tag} (previous: ${previous:-none})"; exit 0
  fi
  sleep 1
done
echo "container did not become healthy; last logs:" >&2
docker logs --tail 40 "$container" >&2
exit 1
REMOTE
