// Command worker is the versioned Temporal worker that runs inside the cluster.
//
// Exactly one workflow version is compiled in, selected by build tag
// (demo_v1 / demo_v2 / demo_v3 — see version_*.go). The Dockerfile passes the
// tag through its WORKER_VERSION build arg, so each image genuinely contains
// only its own workflow code rather than switching at runtime.
//
// Everything about the worker's Temporal identity comes from the environment
// the worker controller injects. We fail fast if it is missing, because a
// silently unversioned worker is far more confusing to debug than a crash loop.
package main

import (
	"log"
	"os"

	"github.com/brendan-myers/temporal-worker-controller-demo/internal/orders"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
	"go.temporal.io/sdk/worker"
)

func main() {
	deploymentName := mustGetEnv("TEMPORAL_DEPLOYMENT_NAME")
	buildID := mustGetEnv("TEMPORAL_WORKER_BUILD_ID")
	taskQueue := os.Getenv("TEMPORAL_TASK_QUEUE")
	if taskQueue == "" {
		taskQueue = orders.TaskQueue
	}

	// The controller injects TEMPORAL_ADDRESS, TEMPORAL_NAMESPACE and any TLS or
	// API-key settings using the SDK's own envconfig names, so this picks them up
	// with no per-environment code.
	opts, err := envconfig.LoadDefaultClientOptions()
	if err != nil {
		log.Fatalf("unable to load Temporal client options from environment: %v", err)
	}

	c, err := client.Dial(opts)
	if err != nil {
		log.Fatalf("unable to connect to Temporal at %q: %v", opts.HostPort, err)
	}
	defer c.Close()

	log.Printf("starting worker: version=%s deployment=%s buildID=%s taskQueue=%s address=%s namespace=%s",
		demoVersion, deploymentName, buildID, taskQueue, opts.HostPort, opts.Namespace)

	w := worker.New(c, taskQueue, worker.Options{
		DeploymentOptions: worker.DeploymentOptions{
			UseVersioning: true,
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: deploymentName,
				BuildID:        buildID,
			},
		},
		// Both workflow types are registered under explicit names, so aliasing
		// the Go function name to a workflow type would only hide mistakes.
		DisableRegistrationAliasing: true,
	})

	registerWorkflows(w)
	w.RegisterActivity(&orders.Activities{})

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalf("worker stopped: %v", err)
	}
}

// mustGetEnv reads a variable the worker controller is responsible for setting.
// Its absence means this pod is not controller-managed, which versioning cannot
// work without.
func mustGetEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s is not set: this worker must be run by the Temporal worker controller", name)
	}
	return v
}
