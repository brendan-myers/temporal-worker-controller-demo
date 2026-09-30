package orders

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

// SleepOrFinish waits for d, returning true if a finish signal arrived first.
//
// Every workflow version calls this identically, so it does not affect replay
// compatibility between versions.
func SleepOrFinish(ctx workflow.Context, finish workflow.ReceiveChannel, d time.Duration) bool {
	timerCtx, cancel := workflow.WithCancel(ctx)
	defer cancel()

	stop := false
	sel := workflow.NewSelector(ctx)
	sel.AddFuture(workflow.NewTimer(timerCtx, d), func(workflow.Future) {})
	sel.AddReceive(finish, func(c workflow.ReceiveChannel, _ bool) {
		c.Receive(ctx, nil)
		stop = true
	})
	sel.Select(ctx)

	return stop
}
