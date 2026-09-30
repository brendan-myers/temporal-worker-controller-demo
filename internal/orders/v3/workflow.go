// Package v3 is a deliberately NOT replay-safe change to v1/v2.
//
// It adds an unguarded FraudCheck activity call inside the loop. That is an
// extra workflow command with no workflow.GetVersion patch around it, so any
// history started on v1 or v2 fails to replay against this code.
//
// That is the point. Roll v3 out and Auto-Upgrade executions that migrate onto
// it wedge on a workflow task failure, while Pinned executions carry on
// untouched on their old version. It is the strongest argument for pinning, and
// it is why the UI labels this image "v3 (breaking)" and never selects it by
// default.
package v3

import (
	"time"

	"github.com/brendan-myers/temporal-worker-controller-demo/internal/orders"
	"go.temporal.io/sdk/workflow"
)

const stepDelay = 4 * time.Second

// ProcessOrder walks an order through a fixed number of processing steps,
// recording which worker version ran each one, and fraud-checks every step.
func ProcessOrder(ctx workflow.Context, in orders.OrderInput) (orders.OrderState, error) {
	steps := in.Steps
	if steps <= 0 {
		steps = orders.DefaultSteps
	}

	state := orders.OrderState{OrderID: in.OrderID, TotalSteps: steps}

	if err := workflow.SetQueryHandler(ctx, orders.QueryGetState, func() (orders.OrderState, error) {
		return state, nil
	}); err != nil {
		return state, err
	}

	finish := workflow.GetSignalChannel(ctx, orders.SignalFinish)

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	})

	var a *orders.Activities

	for state.Step < steps {
		in := orders.StepInput{OrderID: in.OrderID, Step: state.Step + 1}

		// The breaking change: an extra command, unguarded by workflow.GetVersion.
		var fraud string
		if err := workflow.ExecuteActivity(ctx, a.FraudCheck, in).Get(ctx, &fraud); err != nil {
			return state, err
		}

		var label string
		if err := workflow.ExecuteActivity(ctx, a.ProcessStep, in).Get(ctx, &label); err != nil {
			return state, err
		}

		state.Step++
		state.History = append(state.History, label)

		if orders.SleepOrFinish(ctx, finish, stepDelay) {
			break
		}
	}

	state.Done = true
	return state, nil
}
