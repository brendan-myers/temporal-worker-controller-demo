// Command demoui runs the Temporal Worker Controller demo's control plane and
// web UI.
//
// It runs on the presenter's machine, not in the cluster: it reads Temporal
// over localhost and Kubernetes through the ambient kubeconfig, which keeps the
// demo free of in-cluster RBAC and ingress setup.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/brendan-myers/temporal-worker-controller-demo/internal/kube"
	"github.com/brendan-myers/temporal-worker-controller-demo/internal/orders"
	"github.com/brendan-myers/temporal-worker-controller-demo/internal/server"
	"github.com/brendan-myers/temporal-worker-controller-demo/internal/temporalstate"
	"go.temporal.io/sdk/client"
)

func main() {
	var (
		addr         = flag.String("addr", ":8080", "address to serve the demo UI on")
		temporalAddr = flag.String("temporal-address", envOr("TEMPORAL_ADDRESS", "127.0.0.1:7233"), "Temporal frontend address")
		temporalNS   = flag.String("temporal-namespace", envOr("TEMPORAL_NAMESPACE", "default"), "Temporal namespace")
		temporalUI   = flag.String("temporal-ui", envOr("TEMPORAL_UI", "http://localhost:8233"), "base URL of the Temporal Web UI, for deep links")
		k8sNS        = flag.String("k8s-namespace", "demo", "Kubernetes namespace holding the WorkerDeployment")
		kubeContext  = flag.String("kube-context", "minikube", "kubeconfig context to use (empty uses the current context)")
		resource     = flag.String("resource", "order-service", "name of the WorkerDeployment resource")
		taskQueue    = flag.String("task-queue", orders.TaskQueue, "task queue the workers poll")
		imageRepo    = flag.String("image-repo", "order-worker", "worker image repository")
		imageTags    = flag.String("image-tags", "v1,v2,v3", "comma-separated worker image tags the rollout form offers")
		pollEvery    = flag.Duration("poll-interval", time.Second, "how often to poll Temporal and Kubernetes")
		orderSteps   = flag.Int("order-steps", 0, "steps each order runs for, controlling how long it stays alive (0 uses the workflow default)")
	)
	flag.Parse()

	kc, err := kube.New(*k8sNS, *resource, *kubeContext)
	if err != nil {
		log.Fatalf("kubernetes: %v", err)
	}

	c, err := client.Dial(client.Options{
		HostPort:  *temporalAddr,
		Namespace: *temporalNS,
	})
	if err != nil {
		log.Fatalf("connect to Temporal at %s: %v", *temporalAddr, err)
	}

	// The Worker Deployment name is derived by the controller as
	// "<k8s namespace>/<resource name>" and cannot be configured, so we derive
	// it the same way rather than making it a flag people can get wrong.
	tc := temporalstate.New(c, kc.TemporalDeploymentName(), *taskQueue)
	defer tc.Close()

	srv := server.New(server.Config{
		K8sNamespace: *k8sNS,
		ResourceName: *resource,
		TaskQueue:    *taskQueue,
		TemporalNS:   *temporalNS,
		TemporalUI:   strings.TrimRight(*temporalUI, "/"),
		ImageRepo:    *imageRepo,
		ImageTags:    splitTags(*imageTags),
		PollInterval: *pollEvery,
		OrderSteps:   *orderSteps,
	}, tc, kc)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go srv.Run(ctx)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	log.Printf("watching worker deployment %q on %s", kc.TemporalDeploymentName(), *temporalAddr)
	log.Printf("demo UI: http://localhost%s", *addr)

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
