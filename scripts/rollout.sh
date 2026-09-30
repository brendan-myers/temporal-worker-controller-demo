#!/usr/bin/env bash
# Roll the WorkerDeployment onto a different worker image from the terminal.
#
# This is deliberately the same operation the demo UI performs, so you can
# switch between driving the demo from the browser and from a shell and see
# both reflected in the UI.
#
# Usage: scripts/rollout.sh <version> [AllAtOnce|Progressive|Manual]
set -euo pipefail

cd "$(dirname "$0")/.."
. scripts/common.sh

VERSION="${1:-}"
STRATEGY="${2:-AllAtOnce}"

if [ -z "${VERSION}" ]; then
  echo "usage: $0 <version> [AllAtOnce|Progressive|Manual]" >&2
  exit 1
fi

# A Progressive rollout needs steps and the CRD enforces a pause of at least
# 30s; any other strategy must not carry steps at all.
if [ "${STRATEGY}" = "Progressive" ]; then
  rollout="{\"strategy\":\"${STRATEGY}\",\"steps\":[{\"rampPercentage\":25,\"pauseDuration\":\"30s\"},{\"rampPercentage\":50,\"pauseDuration\":\"30s\"}]}"
else
  rollout="{\"strategy\":\"${STRATEGY}\"}"
fi

# A JSON Patch, not a merge patch. Merging into spec.template.spec.containers
# would replace the whole array and drop imagePullPolicy, env and resources —
# and because the build ID hashes the entire pod template, that would also mint
# a new version for an unchanged image.
patch="[
  {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/image\",\"value\":\"${IMAGE_REPO}:${VERSION}\"},
  {\"op\":\"add\",\"path\":\"/spec/rollout\",\"value\":${rollout}}
]"

echo "==> patching ${RESOURCE} to ${IMAGE_REPO}:${VERSION} (${STRATEGY})"
kc patch workerdeployment "${RESOURCE}" -n "${NAMESPACE}" \
  --type json --patch "${patch}"

kc get workerdeployment "${RESOURCE}" -n "${NAMESPACE}"
