package server

import (
	"time"

	"github.com/brendan-myers/temporal-worker-controller-demo/internal/kube"
	"github.com/brendan-myers/temporal-worker-controller-demo/internal/temporalstate"
)

// Version is one box in the diagram: a worker deployment version, the pods
// serving it, and the workflow executions currently running on it.
type Version struct {
	BuildID string `json:"buildId"`
	// Status is Current, Ramping, Draining, Drained or Inactive, as Temporal
	// reports it.
	Status         string                   `json:"status"`
	RampPercentage float32                  `json:"rampPercentage"`
	Ready          int32                    `json:"ready"`
	Desired        int32                    `json:"desired"`
	Workflows      []temporalstate.Workflow `json:"workflows"`
	Pinned         int                      `json:"pinned"`
	AutoUpgrade    int                      `json:"autoUpgrade"`
	// Failing counts workflows on this version that are not making progress,
	// which is what a breaking change looks like from the outside.
	Failing int `json:"failing"`
	// IsTarget marks the version the controller is currently rolling out to,
	// which can differ from Current mid-rollout.
	IsTarget bool `json:"isTarget"`
}

// State is the whole UI payload. It merges what Temporal reports about routing
// and workflows with what Kubernetes reports about the WorkerDeployment and its
// pods, because the point of the demo is how those two move together.
type State struct {
	DeploymentName string `json:"deploymentName"`
	TaskQueue      string `json:"taskQueue"`
	TemporalNS     string `json:"temporalNamespace"`
	K8sNamespace   string `json:"k8sNamespace"`
	ResourceName   string `json:"resourceName"`
	TemporalUI     string `json:"temporalUi"`

	CurrentBuildID string  `json:"currentBuildId"`
	RampingBuildID string  `json:"rampingBuildId"`
	RampPercentage float32 `json:"rampPercentage"`

	Versions []Version                `json:"versions"`
	Unplaced []temporalstate.Workflow `json:"unplaced"`

	CR *kube.Resource `json:"cr"`
	// ImageTags are the worker images the rollout form offers.
	ImageTags []string `json:"imageTags"`
	ImageRepo string   `json:"imageRepo"`

	// RollbackWindow names the versions a rollout would be treated as a rollback
	// to, so the UI can warn that the ramp steps will be skipped.
	RollbackWindow []RecentVersion `json:"rollbackWindow"`

	Events []Event `json:"events"`
	// Errors carries poll failures so the UI can show a degraded banner rather
	// than silently freezing on stale data.
	Errors    []string  `json:"errors"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RecentVersion is a version that was Current recently enough for a rollout to
// it to be treated as a rollback rather than a fresh rollout.
type RecentVersion struct {
	BuildID    string `json:"buildId"`
	Tag        string `json:"tag"`
	SecondsAgo int    `json:"secondsAgo"`
}

// Event is one line of the activity log.
type Event struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
	// Kind tints the line: info, rollout, workflow or error.
	Kind string `json:"kind"`
}
