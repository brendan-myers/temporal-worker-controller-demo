# Temporal Worker Controller demo.
#
# Typical first run:
#   make temporal      # in one terminal, and leave it running
#   make setup         # in another
#   make ui            # then open http://localhost:8080

# PROFILE is both the minikube profile and the kubeconfig context. Every kubectl
# call pins it: Docker Desktop resets the current context to its own whenever it
# restarts, which would otherwise aim the demo at the wrong cluster.
PROFILE       ?= minikube
NAMESPACE     ?= demo
RESOURCE      ?= order-service
CONTROLLER_NS ?= temporal-system
IMAGE_REPO    ?= order-worker
VERSION       ?= v2
STRATEGY      ?= AllAtOnce

KUBECTL := kubectl --context $(PROFILE)

export PROFILE NAMESPACE RESOURCE IMAGE_REPO CONTROLLER_NS

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: temporal
temporal: ## Run a Temporal dev server the cluster can reach (leave running)
	@# --ip 0.0.0.0 is required or pods cannot dial host.minikube.internal.
	@# The two dynamic config values switch on the Worker Deployment APIs, which
	@# the dev server leaves off by default.
	temporal server start-dev \
		--ip 0.0.0.0 \
		--dynamic-config-value frontend.workerVersioningWorkflowAPIs=true \
		--dynamic-config-value system.enableDeploymentVersions=true

.PHONY: setup
setup: ## Create the cluster, install the controller, build images, apply resources
	scripts/setup.sh

.PHONY: images
images: ## Rebuild the worker images into minikube
	scripts/build-images.sh

.PHONY: deploy
deploy: ## Apply the Connection and WorkerDeployment resources
	$(KUBECTL) apply -f deploy/

.PHONY: ui
ui: ## Run the demo UI on http://localhost:8080
	go run ./cmd/demoui -kube-context $(PROFILE) -k8s-namespace $(NAMESPACE) -resource $(RESOURCE) -image-repo $(IMAGE_REPO)

.PHONY: rollout
rollout: ## Roll out a version from the terminal: make rollout VERSION=v2 STRATEGY=Progressive
	scripts/rollout.sh $(VERSION) $(STRATEGY)

.PHONY: status
status: ## Show what the controller and Temporal each think is going on
	@echo "--- WorkerDeployment ---"
	@$(KUBECTL) get workerdeployment -n $(NAMESPACE) || true
	@echo
	@echo "--- worker pods ---"
	@$(KUBECTL) get pods -n $(NAMESPACE) -L temporal.io/build-id || true
	@echo
	@echo "--- Temporal ---"
	@temporal worker deployment describe --name $(NAMESPACE)/$(RESOURCE) || true

.PHONY: logs
logs: ## Follow the worker controller's logs
	$(KUBECTL) logs -n $(CONTROLLER_NS) deployment/temporal-worker-controller-manager -f

.PHONY: reset
reset: ## Terminate every running order workflow
	temporal workflow terminate \
		--query 'TemporalWorkerDeployment="$(NAMESPACE)/$(RESOURCE)" AND ExecutionStatus="Running"' \
		--reason "demo reset" --yes

.PHONY: recover
recover: ## Reset the demo to a clean slate after a wedged rollout
	scripts/recover.sh

.PHONY: teardown
teardown: ## Remove the demo resources and the controller
	scripts/teardown.sh

.PHONY: teardown-all
teardown-all: ## Delete the whole minikube cluster
	scripts/teardown.sh --all

.PHONY: check
check: ## Build every worker variant, vet and test
	go build ./...
	go test ./...
	go build -tags demo_v2 -o /dev/null ./cmd/worker
	go build -tags demo_v3 -o /dev/null ./cmd/worker
	go vet ./...
