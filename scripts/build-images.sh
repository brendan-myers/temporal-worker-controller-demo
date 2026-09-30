#!/usr/bin/env bash
# Build the worker images directly into minikube's image store.
#
# Nothing is pushed to a registry: the WorkerDeployment sets
# imagePullPolicy: Never, so the images only have to exist inside the cluster.
#
# Usage: scripts/build-images.sh [version ...]   (default: v1 v2 v3)
set -euo pipefail

cd "$(dirname "$0")/.."
. scripts/common.sh

require minikube

versions=("$@")
if [ ${#versions[@]} -eq 0 ]; then
  versions=(v1 v2 v3)
fi

for v in "${versions[@]}"; do
  echo "==> building ${IMAGE_REPO}:${v}"
  mk image build \
    -t "${IMAGE_REPO}:${v}" \
    --build-opt="build-arg=WORKER_VERSION=${v}" \
    .
done

echo "==> images in ${PROFILE}"
mk image ls | grep -F "${IMAGE_REPO}" || true
