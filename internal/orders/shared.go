// Package orders holds the constants and data types shared by every version of
// the demo workflow, plus the activities those workflows call.
//
// The workflow itself is versioned: internal/orders/v1, v2 and v3 each contain a
// ProcessOrder implementation. Exactly one of them is compiled into a given
// worker image, which is what makes a rollout observable.
package orders

// TaskQueue is the task queue every version of the worker polls. It is set on
// the pod template as TEMPORAL_TASK_QUEUE; the worker controller does not inject
// it for us.
const TaskQueue = "order-service"

// Workflow type names. The same Go function is registered under both names, with
// a different VersioningBehavior, so the UI can offer "Pinned" or "Auto-Upgrade"
// as a choice at start time while keeping the behavior a property of the code.
const (
	WorkflowTypePinned      = "ProcessOrder"
	WorkflowTypeAutoUpgrade = "ProcessOrderAutoUpgrade"
)

// QueryGetState is the query handler name the UI calls to show a single order's
// progress, including which worker version ran each step.
const QueryGetState = "getState"

// SignalFinish asks a running order to stop at its next step. Used by the demo's
// reset button, and to force a workflow task so a pending version transition
// happens on cue rather than at the next timer.
const SignalFinish = "finish"

// SignalNudge is an intentionally unhandled signal. Sending it forces a workflow
// task, which is how the demo makes a pending versioning override take effect
// immediately instead of at the order's next step timer.
const SignalNudge = "nudge"

// DefaultSteps gives an order a lifetime of roughly 25 minutes at the default
// step delay. A Progressive rollout takes a couple of minutes on its own — the
// CRD enforces a 30s minimum pause per step — and orders need to still be alive
// well afterwards, through however long it takes to talk through what happened
// and then manually upgrade the pinned ones.
const DefaultSteps = 300

// OrderInput is the workflow argument.
type OrderInput struct {
	OrderID string `json:"orderId"`
	// Steps is the number of processing steps to run. Zero means DefaultSteps.
	Steps int `json:"steps"`
}

// OrderState is the result of the getState query.
type OrderState struct {
	OrderID    string `json:"orderId"`
	Step       int    `json:"step"`
	TotalSteps int    `json:"totalSteps"`
	// History records one entry per completed step, each naming the worker
	// version that ran it. An auto-upgraded order shows the handoff directly,
	// e.g. "v1 ... v1, v2, v2".
	History []string `json:"history"`
	Done    bool     `json:"done"`
}
