# Shared settings for the demo scripts. Source, do not execute.
#
# Every kubectl and helm call is pinned to the minikube context explicitly.
# Relying on the current context is not safe: Docker Desktop resets kubectl to
# its own context whenever it restarts, which will silently point the demo at
# the wrong cluster mid-setup.

PROFILE="${PROFILE:-minikube}"          # minikube profile, also the context name
NAMESPACE="${NAMESPACE:-demo}"          # namespace holding the demo resources
RESOURCE="${RESOURCE:-order-service}"   # name of the WorkerDeployment
CONTROLLER_NS="${CONTROLLER_NS:-temporal-system}"
IMAGE_REPO="${IMAGE_REPO:-order-worker}"

kc()   { kubectl --context "${PROFILE}" "$@"; }
hm()   { helm --kube-context "${PROFILE}" "$@"; }
mk()   { minikube -p "${PROFILE}" "$@"; }

require() {
  for cmd in "$@"; do
    command -v "${cmd}" >/dev/null 2>&1 || {
      echo "error: ${cmd} is required but not installed" >&2
      exit 1
    }
  done
}
