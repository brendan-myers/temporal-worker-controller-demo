//go:build demo_v3

package main

import (
	"github.com/brendan-myers/temporal-worker-controller-demo/internal/orders"
	v3 "github.com/brendan-myers/temporal-worker-controller-demo/internal/orders/v3"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const demoVersion = "v3"

// registerWorkflows registers one implementation under two workflow type names.
// The only difference is the versioning behavior, which keeps "pinned or
// auto-upgrade" a property of the code — the UI just picks which type to start.
func registerWorkflows(w worker.Worker) {
	w.RegisterWorkflowWithOptions(v3.ProcessOrder, workflow.RegisterOptions{
		Name:               orders.WorkflowTypePinned,
		VersioningBehavior: workflow.VersioningBehaviorPinned,
	})
	w.RegisterWorkflowWithOptions(v3.ProcessOrder, workflow.RegisterOptions{
		Name:               orders.WorkflowTypeAutoUpgrade,
		VersioningBehavior: workflow.VersioningBehaviorAutoUpgrade,
	})
}
