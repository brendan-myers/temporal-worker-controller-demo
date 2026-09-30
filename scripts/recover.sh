#!/usr/bin/env bash
# Reset the demo to a clean slate when a rollout has wedged.
#
# The controller owns the Worker Deployment on the Temporal side (it claims
# ManagerIdentity), so the order here matters: the Kubernetes resource has to go
# first, then the claim is released, then the versions can be removed.
#
# Use this if the UI shows a controller error that does not clear, for example
# "missing active task queues from the current version" after rolling back and
# forth between two images.
set -euo pipefail

cd "$(dirname "$0")/.."
. scripts/common.sh

TEMPORAL_ADDRESS="${TEMPORAL_ADDRESS:-127.0.0.1:7233}"
DEPLOYMENT="${NAMESPACE}/${RESOURCE}"
export TEMPORAL_ADDRESS

require kubectl temporal

echo "==> deleting the WorkerDeployment"
kc delete workerdeployment "${RESOURCE}" -n "${NAMESPACE}" --ignore-not-found=true --timeout=60s || true

# The controller adds a temporal.io/delete-protection finalizer and will not
# remove it while it is failing to reconcile, which strands the resource in
# Terminating.
if kc get workerdeployment "${RESOURCE}" -n "${NAMESPACE}" >/dev/null 2>&1; then
  echo "    (stuck terminating; clearing the finalizer)"
  kc patch workerdeployment "${RESOURCE}" -n "${NAMESPACE}" \
    --type merge -p '{"metadata":{"finalizers":null}}' >/dev/null
fi

echo "==> releasing the controller's claim on ${DEPLOYMENT}"
temporal worker deployment manager-identity unset \
  --deployment-name "${DEPLOYMENT}" --yes 2>/dev/null || true

echo "==> deleting worker deployment versions"
# The current version cannot be deleted, and does not need to be: it re-registers
# from live pollers once the workers come back.
temporal worker deployment describe --name "${DEPLOYMENT}" -o json 2>/dev/null \
  | sed -n 's/.*"buildId":"\([^"]*\)".*/\1/p' | sort -u \
  | while read -r build; do
      [ -n "${build}" ] || continue
      echo -n "    ${build}: "
      temporal worker deployment delete-version \
        --deployment-name "${DEPLOYMENT}" --build-id "${build}" --skip-drainage 2>&1 \
        | tail -1 | cut -c1-90
    done

echo "==> re-applying the demo resources"
kc apply -f deploy/

echo
echo "Recovered. Watch it come back with:"
echo "  kubectl --context ${PROFILE} get workerdeployment -n ${NAMESPACE} -w"
