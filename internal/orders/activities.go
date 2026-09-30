package orders

import (
	"context"
	"fmt"
	"os"
	"time"
)

// WorkerVersion is the demo version label baked into the worker image at build
// time (see Dockerfile's WORKER_VERSION build arg). It is only used to make
// activity results self-identifying in the UI; the real version identity comes
// from TEMPORAL_WORKER_BUILD_ID.
func WorkerVersion() string {
	if v := os.Getenv("DEMO_WORKER_VERSION"); v != "" {
		return v
	}
	return "dev"
}

// StepInput is the argument to ProcessStep.
type StepInput struct {
	OrderID string `json:"orderId"`
	Step    int    `json:"step"`
}

// Activities carries the activity implementations. It has no state today, but
// registering methods rather than bare functions keeps room for injecting a
// version label or client later without changing registration sites.
type Activities struct{}

// ProcessStep does one unit of order processing. It returns a label naming the
// worker version that ran it, which the workflow accumulates so the UI can show
// exactly where an auto-upgraded order changed hands.
func (a *Activities) ProcessStep(ctx context.Context, in StepInput) (string, error) {
	// A short sleep keeps a handful of activities visibly in flight without
	// making the order take meaningfully longer.
	time.Sleep(100 * time.Millisecond)
	return fmt.Sprintf("%s: step %d", WorkerVersion(), in.Step), nil
}

// FraudCheck exists only in v3, where it is called from the workflow without a
// patch. That extra workflow-level command is a genuine non-determinism against
// histories started on v1 or v2, which is the point: it shows why you pin.
func (a *Activities) FraudCheck(ctx context.Context, in StepInput) (string, error) {
	return fmt.Sprintf("%s: fraud check on %s", WorkerVersion(), in.OrderID), nil
}
