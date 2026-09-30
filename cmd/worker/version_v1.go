//go:build !demo_v2 && !demo_v3

package main

import (
	"github.com/brendan-myers/temporal-worker-controller-demo/internal/orders"
	v1 "github.com/brendan-myers/temporal-worker-controller-demo/internal/orders/v1"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// v1 is the default build, so a plain `go build ./cmd/worker` compiles and vets.
const demoVersion = "v1"

// registerWorkflows registers one implementation under two workflow type names.
// The only difference is the versioning behavior, which keeps "pinned or
// auto-upgrade" a property of the code — the UI just picks which type to start.
func registerWorkflows(w worker.Worker) {
	w.RegisterWorkflowWithOptions(v1.ProcessOrder, workflow.RegisterOptions{
		Name:               orders.WorkflowTypePinned,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	w.RegisterWorkflowWithOptions(v1.ProcessOrder, workflow.RegisterOptions{
		Name:               orders.WorkflowTypeAutoUpgrade,
		VersioningBehavior: workflow.VersioningBehaviorAutoUpgrade,
	})
}
