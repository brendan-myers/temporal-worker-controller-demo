#!/usr/bin/env bash
# One-time setup: a minikube cluster with the Temporal worker controller
# installed, the demo namespace created, worker images built, and the demo
# resources applied.
#
# Assumes `temporal server start-dev` is already running on the host with
# --ip 0.0.0.0 (see `make temporal`), because the worker pods dial back out to
# host.minikube.internal:7233.
#
# Safe to re-run: every step is an upgrade or an apply.
set -euo pipefail

cd "$(dirname "$0")/.."
. scripts/common.sh

CONTROLLER_CHART_VERSION="${CONTROLLER_CHART_VERSION:-0.30.0}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.20.0}"

require minikube kubectl helm

echo "==> starting minikube (profile ${PROFILE})"
mk status >/dev/null 2>&1 || mk start

echo "==> installing cert-manager ${CERT_MANAGER_VERSION}"
# The controller chart always registers a validating webhook for
# WorkerResourceTemplate, which needs cert-manager to issue its serving cert.
hm upgrade --install cert-manager cert-manager \
  --repo https://charts.jetstack.io \
  --version "${CERT_MANAGER_VERSION}" \
  --namespace cert-manager --create-namespace \
  --set crds.enabled=true \
  --wait --timeout 10m

echo "==> installing the Temporal worker controller (chart ${CONTROLLER_CHART_VERSION})"
# CRDs ship as their own chart and must go in first.
#
# The CRDs are cluster-scoped but the Helm release that owns them is not, and an
# older install may have put that release in a different namespace. Helm refuses
# to adopt CRDs owned by a release elsewhere, so upgrade the existing release
# where it actually lives rather than trying to create a second one.
crds_ns=$(hm list -A -f '^temporal-worker-controller-crds$' -o json 2>/dev/null \
  | sed -n 's/.*"namespace":"\([^"]*\)".*/\1/p' | head -1)
crds_ns="${crds_ns:-${CONTROLLER_NS}}"
if [ "${crds_ns}" != "${CONTROLLER_NS}" ]; then
  echo "    (existing CRD release found in namespace ${crds_ns}; upgrading it there)"
fi

hm upgrade --install temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version "${CONTROLLER_CHART_VERSION}" \
  --namespace "${crds_ns}" --create-namespace \
  --wait --timeout 10m

# A generous timeout: upgrading from an older controller has to roll two
# replicas, and a --wait that expires leaves the release marked failed even
# though the rollout eventually succeeds.
hm upgrade --install temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version "${CONTROLLER_CHART_VERSION}" \
  --namespace "${CONTROLLER_NS}" \
  --wait --timeout 10m

kc wait --for=condition=available \
  deployment/temporal-worker-controller-manager \
  -n "${CONTROLLER_NS}" --timeout=120s

echo "==> creating namespace ${NAMESPACE}"
kc create namespace "${NAMESPACE}" --dry-run=client -o yaml | kc apply -f -

echo "==> building worker images"
scripts/build-images.sh

echo "==> applying demo resources"
kc apply -f deploy/

cat <<EOF

Setup complete.

  watch the rollout:   kubectl --context ${PROFILE} get workerdeployment -n ${NAMESPACE} -w
  controller logs:     kubectl --context ${PROFILE} logs -n ${CONTROLLER_NS} deployment/temporal-worker-controller-manager -f
  start the demo UI:   make ui

EOF
