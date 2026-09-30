// Package kube is the demo's view of the Kubernetes side: the WorkerDeployment
// resource the worker controller reconciles, and the per-version Deployments it
// creates.
//
// The WorkerDeployment is read and written through the dynamic client as an
// unstructured object, so the demo does not take a Go dependency on the
// controller's module and cannot drift out of sync with the version installed
// in the cluster.
package kube

import (
	"context"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Labels the worker controller puts on the Deployments it manages.
const (
	labelDeploymentName = "temporal.io/deployment-name"
	labelBuildID        = "temporal.io/build-id"
)

// workerDeploymentGVR is the current CRD. Note the kind was renamed from
// TemporalWorkerDeployment in controller v1.7.0; the old kind still exists but
// rejects creation.
var workerDeploymentGVR = schema.GroupVersionResource{
	Group:    "temporal.io",
	Version:  "v1alpha1",
	Resource: "workerdeployments",
}

// Client reads and patches one WorkerDeployment and its managed Deployments.
type Client struct {
	dyn       dynamic.Interface
	kube      kubernetes.Interface
	namespace string
	name      string
}

// New builds a Client from the ambient kubeconfig (KUBECONFIG, or
// ~/.kube/config).
//
// context selects a kubeconfig context; empty uses the current one. Pinning it
// matters here: Docker Desktop resets kubectl to its own context when it
// restarts, which would otherwise point the demo at the wrong cluster.
func New(namespace, name, context string) (*Client, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: context},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build dynamic client: %w", err)
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build kubernetes client: %w", err)
	}

	return &Client{dyn: dyn, kube: kube, namespace: namespace, name: name}, nil
}

// TemporalDeploymentName returns the Worker Deployment name as Temporal sees
// it. The controller derives this as "<k8s namespace>/<resource name>" and it
// cannot be configured.
func (c *Client) TemporalDeploymentName() string {
	return c.namespace + "/" + c.name
}

// deploymentNameLabel is the value the controller puts in the
// temporal.io/deployment-name label on the Deployments it manages.
//
// Note this is the bare resource name, not the namespace-qualified name Temporal
// knows the Worker Deployment by ("demo/order-service") — the Deployments are
// namespaced, so the namespace is already implied. Verified against controller
// v1.11.0.
func (c *Client) deploymentNameLabel() string {
	return c.name
}

// RolloutStep mirrors one entry of spec.rollout.steps.
//
// The CRD enforces rampPercentage in [1,99] and pauseDuration of at least 30s,
// so there is no such thing as a five-second demo ramp.
type RolloutStep struct {
	RampPercentage int    `json:"rampPercentage"`
	PauseDuration  string `json:"pauseDuration"`
}

// Spec is the part of the WorkerDeployment spec the demo shows and edits.
type Spec struct {
	Image    string        `json:"image"`
	Replicas int64         `json:"replicas"`
	Strategy string        `json:"strategy"`
	Steps    []RolloutStep `json:"steps"`
}

// Condition is a trimmed metav1.Condition for display.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// Status is the part of the WorkerDeployment status the demo shows. It is what
// the controller believes, as opposed to what Temporal reports — showing both
// makes the controller's role legible.
type Status struct {
	CurrentBuildID string      `json:"currentBuildId"`
	TargetBuildID  string      `json:"targetBuildId"`
	TargetStatus   string      `json:"targetStatus"`
	RampPercentage float64     `json:"rampPercentage"`
	VersionCount   int64       `json:"versionCount"`
	Conditions     []Condition `json:"conditions"`
}

// Resource is the demo's view of the WorkerDeployment.
type Resource struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Spec      Spec   `json:"spec"`
	Status    Status `json:"status"`
	// Found is false when the resource does not exist yet, which is a normal
	// pre-setup state rather than an error.
	Found bool `json:"found"`
}

// Get reads the current WorkerDeployment.
func (c *Client) Get(ctx context.Context) (*Resource, error) {
	u, err := c.dyn.Resource(workerDeploymentGVR).Namespace(c.namespace).
		Get(ctx, c.name, metav1.GetOptions{})
	if err != nil {
		if isNotFound(err) {
			return &Resource{Namespace: c.namespace, Name: c.name}, nil
		}
		return nil, fmt.Errorf("get workerdeployment %s/%s: %w", c.namespace, c.name, err)
	}

	res := &Resource{Namespace: c.namespace, Name: c.name, Found: true}

	obj := u.Object
	spec, _ := obj["spec"].(map[string]any)
	res.Spec.Image = firstContainerImage(spec)
	res.Spec.Replicas = intField(spec, "replicas")
	if rollout, ok := spec["rollout"].(map[string]any); ok {
		res.Spec.Strategy, _ = rollout["strategy"].(string)
		res.Spec.Steps = parseSteps(rollout["steps"])
	}

	status, _ := obj["status"].(map[string]any)
	res.Status.VersionCount = intField(status, "versionCount")
	if cur, ok := status["currentVersion"].(map[string]any); ok {
		res.Status.CurrentBuildID, _ = cur["buildID"].(string)
	}
	if tgt, ok := status["targetVersion"].(map[string]any); ok {
		res.Status.TargetBuildID, _ = tgt["buildID"].(string)
		res.Status.TargetStatus, _ = tgt["status"].(string)
		res.Status.RampPercentage = floatField(tgt, "rampPercentage")
	}
	res.Status.Conditions = parseConditions(status["conditions"])

	return res, nil
}

// Apply patches the parts of the spec the demo controls: the worker image, the
// replica count and the rollout strategy.
//
// This is a JSON Patch rather than a merge patch. A merge patch on
// spec.template.spec.containers would replace the whole array, silently
// dropping imagePullPolicy, env and resources from the container — and since
// the build ID is a hash of the entire pod template, that also mints a
// different version for what is supposed to be the same image, which breaks
// rolling back to a version that was current before.
func (c *Client) Apply(ctx context.Context, s Spec) error {
	rollout := map[string]any{"strategy": s.Strategy}
	if s.Strategy == "Progressive" {
		if len(s.Steps) == 0 {
			return fmt.Errorf("a Progressive rollout needs at least one step")
		}
		steps := make([]any, 0, len(s.Steps))
		for _, st := range s.Steps {
			if st.RampPercentage < 1 || st.RampPercentage > 99 {
				return fmt.Errorf("rampPercentage must be between 1 and 99, got %d", st.RampPercentage)
			}
			steps = append(steps, map[string]any{
				"rampPercentage": st.RampPercentage,
				"pauseDuration":  st.PauseDuration,
			})
		}
		rollout["steps"] = steps
	}
	// Replacing spec.rollout wholesale is deliberate: the CRD rejects steps on a
	// non-Progressive strategy, so they have to go away rather than linger.

	// "add" on an existing object member replaces its value and creates it when
	// absent, which "replace" would not.
	ops := []map[string]any{
		{"op": "add", "path": "/spec/template/spec/containers/0/image", "value": s.Image},
		{"op": "add", "path": "/spec/rollout", "value": rollout},
	}

	// Only touch the replica count when a caller actually asked to. Replicas are
	// not part of the pod template, so they never affect the build ID; leaving
	// the field alone keeps the manifest the single source of truth and avoids
	// clobbering a `kubectl scale` from a terminal.
	if s.Replicas > 0 {
		ops = append(ops, map[string]any{"op": "add", "path": "/spec/replicas", "value": s.Replicas})
	}

	data, err := json.Marshal(ops)
	if err != nil {
		return fmt.Errorf("marshal patch: %w", err)
	}

	_, err = c.dyn.Resource(workerDeploymentGVR).Namespace(c.namespace).
		Patch(ctx, c.name, types.JSONPatchType, data, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch workerdeployment %s/%s: %w", c.namespace, c.name, err)
	}
	return nil
}

// VersionPods reports pod readiness for one worker version.
type VersionPods struct {
	BuildID string `json:"buildId"`
	Ready   int32  `json:"ready"`
	Desired int32  `json:"desired"`
}

// Pods returns pod counts keyed by build ID, for the Deployments the controller
// manages on behalf of this WorkerDeployment.
func (c *Client) Pods(ctx context.Context) (map[string]VersionPods, error) {
	list, err := c.kube.AppsV1().Deployments(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelDeploymentName + "=" + c.deploymentNameLabel(),
	})
	if err != nil {
		return nil, fmt.Errorf("list worker deployments in %s: %w", c.namespace, err)
	}

	out := make(map[string]VersionPods, len(list.Items))
	for i := range list.Items {
		d := &list.Items[i]
		buildID := d.Labels[labelBuildID]
		if buildID == "" {
			continue
		}
		out[buildID] = VersionPods{
			BuildID: buildID,
			Ready:   d.Status.ReadyReplicas,
			Desired: desiredReplicas(d),
		}
	}
	return out, nil
}

func desiredReplicas(d *appsv1.Deployment) int32 {
	if d.Spec.Replicas == nil {
		return d.Status.Replicas
	}
	return *d.Spec.Replicas
}
