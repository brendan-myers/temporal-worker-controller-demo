// Package v1 is the baseline version of the demo order workflow.
//
// v2 is a replay-safe change to this: it issues the same sequence of workflow
// commands, so Auto-Upgrade executions can migrate onto it mid-flight. v3 is
// deliberately not replay-safe. See each package for details.
package v1

import (
	"time"

	"github.com/brendan-myers/temporal-worker-controller-demo/internal/orders"
	"go.temporal.io/sdk/workflow"
)

// stepDelay paces the order so it stays alive long enough to survive a rollout.
const stepDelay = 5 * time.Second

// ProcessOrder walks an order through a fixed number of processing steps,
// recording which worker version ran each one.
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
		var label string
		err := workflow.ExecuteActivity(ctx, a.ProcessStep, orders.StepInput{
			OrderID: in.OrderID,
			Step:    state.Step + 1,
		}).Get(ctx, &label)
		if err != nil {
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
