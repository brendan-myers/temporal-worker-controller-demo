#!/usr/bin/env bash
# Remove the demo. Pass --all to delete the minikube cluster outright, which is
# the fastest way back to a clean slate.
#
# Note this leaves cert-manager and the worker controller installed, since other
# things on the cluster may be using them.
set -euo pipefail

cd "$(dirname "$0")/.."
. scripts/common.sh

if [ "${1:-}" = "--all" ]; then
  echo "==> deleting the ${PROFILE} cluster"
  mk delete
  exit 0
fi

echo "==> removing demo resources"
kc delete -f deploy/ --ignore-not-found=true

echo "==> removing namespace ${NAMESPACE}"
kc delete namespace "${NAMESPACE}" --ignore-not-found=true

echo
echo "The worker controller and cert-manager were left installed."
echo "To remove the controller too:"
echo "  helm --kube-context ${PROFILE} uninstall temporal-worker-controller -n ${CONTROLLER_NS}"
