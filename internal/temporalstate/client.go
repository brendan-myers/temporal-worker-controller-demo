// Package temporalstate is the demo's read/write view of Temporal: what
// versions exist, which workflows are on them, and the two write operations the
// UI offers (starting orders, and moving individual workflows between versions).
//
// It deliberately does not call SetCurrentVersion or SetRampingVersion. The
// worker controller claims ManagerIdentity on the Worker Deployment and owns
// routing; the demo changes the WorkerDeployment resource instead and lets the
// controller react. Per-workflow versioning overrides are unaffected by that
// ownership, which is why the manual-upgrade button uses them.
package temporalstate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brendan-myers/temporal-worker-controller-demo/internal/orders"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
)

// Search attribute names used to place a running workflow in a version box.
// These are system search attributes, present on a dev server with no setup.
const (
	saDeployment = "TemporalWorkerDeployment"
	saVersion    = "TemporalWorkerDeploymentVersion"
	saBehavior   = "TemporalWorkflowVersioningBehavior"
	// saProblems is set after repeated workflow task failures and cleared once a
	// workflow task succeeds — exactly what a workflow wedged on a bad version
	// looks like. It arrives with the list call, so spotting failures costs no
	// extra RPC.
	saProblems = "TemporalReportedProblems"
)

// Versioning behaviors, normalized. The server's exact spelling for the
// behavior search attribute has varied across docs ("Pinned"/"Auto-Upgrade" vs
// "PINNED"/"AUTO_UPGRADE"), so we normalize rather than match a literal.
const (
	BehaviorPinned      = "Pinned"
	BehaviorAutoUpgrade = "AutoUpgrade"
)

// Client wraps a Temporal client with the demo's deployment identity.
type Client struct {
	c              client.Client
	deploymentName string
	taskQueue      string
}

// New returns a Client for the given Worker Deployment name, which the worker
// controller derives as "<k8s namespace>/<WorkerDeployment name>".
func New(c client.Client, deploymentName, taskQueue string) *Client {
	return &Client{c: c, deploymentName: deploymentName, taskQueue: taskQueue}
}

// Close releases the underlying Temporal connection.
func (c *Client) Close() { c.c.Close() }

// DeploymentName returns the Temporal-side Worker Deployment name.
func (c *Client) DeploymentName() string { return c.deploymentName }

// Workflow is one dot in the diagram.
type Workflow struct {
	ID        string    `json:"id"`
	RunID     string    `json:"runId"`
	Type      string    `json:"type"`
	BuildID   string    `json:"buildId"`
	Behavior  string    `json:"behavior"`
	StartTime time.Time `json:"startTime"`
	// Problems is non-empty while the workflow is failing to make progress, most
	// often a non-determinism error after being moved onto an incompatible
	// version. Entries look like "category=WorkflowTaskFailed cause=...".
	Problems []string `json:"problems,omitempty"`
}

// Version is one version box.
type Version struct {
	BuildID string `json:"buildId"`
	// Status is one of Current, Ramping, Draining, Drained or Inactive.
	Status         string     `json:"status"`
	RampPercentage float32    `json:"rampPercentage"`
	CreateTime     time.Time  `json:"createTime"`
	Workflows      []Workflow `json:"workflows"`
}

// Snapshot is everything the UI needs about Temporal in one poll.
type Snapshot struct {
	DeploymentName string    `json:"deploymentName"`
	CurrentBuildID string    `json:"currentBuildId"`
	RampingBuildID string    `json:"rampingBuildId"`
	RampPercentage float32   `json:"rampPercentage"`
	Versions       []Version `json:"versions"`
	// Unplaced holds running workflows that have no version search attribute
	// yet — typically just started, before their first workflow task completes.
	Unplaced []Workflow `json:"unplaced"`
}

// Snapshot reads the deployment's routing config and version list, then buckets
// the deployment's running workflows into those versions. Two RPCs plus one
// list pagination, so it is cheap enough to poll once a second.
func (c *Client) Snapshot(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{
		DeploymentName: c.deploymentName,
		Versions:       []Version{},
		Unplaced:       []Workflow{},
	}

	desc, err := c.c.WorkerDeploymentClient().
		GetHandle(c.deploymentName).
		Describe(ctx, client.WorkerDeploymentDescribeOptions{})
	if err != nil {
		// Before the first worker ever polls, the deployment does not exist.
		// That is a normal starting state for the demo, not an error.
		if isNotFound(err) {
			return snap, nil
		}
		return nil, fmt.Errorf("describe worker deployment %q: %w", c.deploymentName, err)
	}

	routing := desc.Info.RoutingConfig
	if routing.CurrentVersion != nil {
		snap.CurrentBuildID = routing.CurrentVersion.BuildID
	}
	if routing.RampingVersion != nil {
		snap.RampingBuildID = routing.RampingVersion.BuildID
		snap.RampPercentage = routing.RampingVersionPercentage
	}

	// Index by build ID rather than holding pointers into snap.Versions: append
	// reallocates, which would leave earlier pointers addressing a dead array.
	indexOf := make(map[string]int, len(desc.Info.VersionSummaries))
	for _, vs := range desc.Info.VersionSummaries {
		v := Version{
			BuildID:    vs.Version.BuildID,
			Status:     versionStatus(vs.Version.BuildID, vs.DrainageStatus, snap),
			CreateTime: vs.CreateTime,
			Workflows:  []Workflow{},
		}
		if v.BuildID == snap.RampingBuildID {
			v.RampPercentage = snap.RampPercentage
		}
		indexOf[v.BuildID] = len(snap.Versions)
		snap.Versions = append(snap.Versions, v)
	}

	running, err := c.listRunning(ctx)
	if err != nil {
		return nil, err
	}
	for _, wf := range running {
		if i, ok := indexOf[wf.BuildID]; ok && wf.BuildID != "" {
			snap.Versions[i].Workflows = append(snap.Versions[i].Workflows, wf)
			continue
		}
		snap.Unplaced = append(snap.Unplaced, wf)
	}

	// Oldest version first, so the diagram reads left to right in rollout order.
	sortVersions(snap.Versions)
	return snap, nil
}

// versionStatus maps drainage plus routing onto the label shown on a box.
func versionStatus(buildID string, drainage client.WorkerDeploymentVersionDrainageStatus, snap *Snapshot) string {
	switch buildID {
	case snap.CurrentBuildID:
		return "Current"
	case snap.RampingBuildID:
		return "Ramping"
	}
	switch drainage {
	case client.WorkerDeploymentVersionDrainageStatusDraining:
		return "Draining"
	case client.WorkerDeploymentVersionDrainageStatusDrained:
		return "Drained"
	}
	return "Inactive"
}

// listRunning returns every running workflow that belongs to this deployment,
// with the build ID and behavior read off its search attributes.
func (c *Client) listRunning(ctx context.Context) ([]Workflow, error) {
	query := fmt.Sprintf(`%s = %q AND ExecutionStatus = "Running"`, saDeployment, c.deploymentName)

	var out []Workflow
	var token []byte
	for {
		resp, err := c.c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			Query:         query,
			PageSize:      500,
			NextPageToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list running workflows: %w", err)
		}
		for _, e := range resp.Executions {
			wf := Workflow{
				ID:       e.Execution.GetWorkflowId(),
				RunID:    e.Execution.GetRunId(),
				Type:     e.Type.GetName(),
				BuildID:  buildIDFromVersionSA(searchAttr(e.SearchAttributes, saVersion), c.deploymentName),
				Behavior: normalizeBehavior(searchAttr(e.SearchAttributes, saBehavior)),
				Problems: searchAttrList(e.SearchAttributes, saProblems),
			}
			if e.StartTime != nil {
				wf.StartTime = e.StartTime.AsTime()
			}
			out = append(out, wf)
		}
		token = resp.NextPageToken
		if len(token) == 0 {
			break
		}
	}
	return out, nil
}

// searchAttr decodes one keyword search attribute, returning "" if absent.
func searchAttr(sa *commonpb.SearchAttributes, name string) string {
	if sa == nil {
		return ""
	}
	p, ok := sa.GetIndexedFields()[name]
	if !ok {
		return ""
	}
	var s string
	if err := converter.GetDefaultDataConverter().FromPayload(p, &s); err != nil {
		return ""
	}
	return s
}

// searchAttrList decodes one KeywordList search attribute.
func searchAttrList(sa *commonpb.SearchAttributes, name string) []string {
	if sa == nil {
		return nil
	}
	p, ok := sa.GetIndexedFields()[name]
	if !ok {
		return nil
	}
	var v []string
	if err := converter.GetDefaultDataConverter().FromPayload(p, &v); err != nil {
		return nil
	}
	return v
}

// buildIDFromVersionSA strips the deployment prefix from the
// TemporalWorkerDeploymentVersion search attribute, whose documented format is
// "<deployment name>:<build id>".
//
// Note the delimiter is a colon here, even though the Go SDK's own internal
// canonical version string uses a dot — that dot form only feeds a deprecated
// proto field and must not be used for search attribute queries.
func buildIDFromVersionSA(v, deploymentName string) string {
	if v == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(v, deploymentName+":"); ok {
		return rest
	}
	// Tolerate a bare build ID, or the older dot delimiter, rather than dropping
	// the workflow out of the diagram entirely.
	if rest, ok := strings.CutPrefix(v, deploymentName+"."); ok {
		return rest
	}
	return v
}

// normalizeBehavior folds the behavior search attribute onto a stable spelling.
// Observed and documented variants include "Pinned", "PINNED", "AutoUpgrade",
// "Auto-Upgrade" and "AUTO_UPGRADE".
func normalizeBehavior(v string) string {
	switch strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(v)) {
	case "pinned":
		return BehaviorPinned
	case "autoupgrade":
		return BehaviorAutoUpgrade
	default:
		return ""
	}
}

func isNotFound(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// StartOrders starts n order workflows with the requested behavior, returning
// the IDs it created.
func (c *Client) StartOrders(ctx context.Context, n int, behavior string, steps int) ([]string, error) {
	workflowType := orders.WorkflowTypePinned
	if normalizeBehavior(behavior) == BehaviorAutoUpgrade {
		workflowType = orders.WorkflowTypeAutoUpgrade
	}

	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("order-%d-%d", time.Now().UnixMilli(), i)
		_, err := c.c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID:        id,
			TaskQueue: c.taskQueue,
		}, workflowType, orders.OrderInput{OrderID: id, Steps: steps})
		if err != nil {
			return ids, fmt.Errorf("start %s: %w", id, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Override modes accepted by SetOverride.
const (
	OverridePinned      = "pinned"
	OverrideAutoUpgrade = "autoUpgrade"
	OverrideClear       = "clear"
)

// SetOverride applies, changes or removes a workflow's versioning override.
//
// This is how a pinned workflow is manually moved onto a new version: pin it to
// the target build ID and the next workflow task runs there.
func (c *Client) SetOverride(ctx context.Context, workflowID, mode, buildID string) error {
	change := &client.VersioningOverrideChange{}

	switch mode {
	case OverridePinned:
		if buildID == "" {
			return fmt.Errorf("a build ID is required to pin %s", workflowID)
		}
		change.Value = &client.PinnedVersioningOverride{
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: c.deploymentName,
				BuildID:        buildID,
			},
		}
	case OverrideAutoUpgrade:
		change.Value = &client.AutoUpgradeVersioningOverride{}
	case OverrideClear:
		// A non-nil change carrying a nil Value removes the override. Leaving
		// the change itself nil would instead mean "no change" and be rejected.
		change.Value = nil
	default:
		return fmt.Errorf("unknown override mode %q", mode)
	}

	_, err := c.c.UpdateWorkflowExecutionOptions(ctx, client.UpdateWorkflowExecutionOptionsRequest{
		WorkflowId: workflowID,
		WorkflowExecutionOptionsChanges: client.WorkflowExecutionOptionsChanges{
			VersioningOverride: change,
		},
	})
	if err != nil {
		return fmt.Errorf("update options for %s: %w", workflowID, err)
	}

	// An override lands on the next workflow task. An order idling on its step
	// timer would not move for seconds; nudging it with a signal makes the
	// transition happen on cue, which matters when you are presenting.
	// A failed nudge costs us immediacy, not correctness, so it is not an error.
	_ = c.c.SignalWorkflow(ctx, workflowID, "", orders.SignalNudge, nil)
	return nil
}

// Detail is the expanded view of one workflow, shown when a dot is selected.
type Detail struct {
	Workflow
	// Override describes any versioning override in effect: "", "AutoUpgrade",
	// or "Pinned:<build id>".
	Override string `json:"override"`
	// Effective is the behavior that governs this execution from here on. Behavior
	// reports what the last completed workflow task used, which still reads as the
	// old value immediately after an override is applied.
	Effective string `json:"effectiveBehavior"`
	// TransitioningTo names the version this execution is being moved onto, when
	// a move is pending. A failing workflow is bucketed under the last version
	// that completed a task for it, so without this the dot appears to be broken
	// on the version it was happily running on a moment ago.
	TransitioningTo string             `json:"transitioningTo,omitempty"`
	State           *orders.OrderState `json:"state,omitempty"`
}

// Detail describes one workflow, including its versioning override, which is
// not available as a search attribute and so needs its own RPC. The UI fetches
// this only for the selected workflow.
func (c *Client) Detail(ctx context.Context, workflowID string) (*Detail, error) {
	resp, err := c.c.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		return nil, fmt.Errorf("describe %s: %w", workflowID, err)
	}

	info := resp.GetWorkflowExecutionInfo()
	d := &Detail{Workflow: Workflow{
		ID:    info.GetExecution().GetWorkflowId(),
		RunID: info.GetExecution().GetRunId(),
		Type:  info.GetType().GetName(),
	}}
	if info.StartTime != nil {
		d.StartTime = info.StartTime.AsTime()
	}
	d.Problems = searchAttrList(info.GetSearchAttributes(), saProblems)

	if vi := info.GetVersioningInfo(); vi != nil {
		switch vi.GetBehavior() {
		case enumspb.VERSIONING_BEHAVIOR_PINNED:
			d.Behavior = BehaviorPinned
		case enumspb.VERSIONING_BEHAVIOR_AUTO_UPGRADE:
			d.Behavior = BehaviorAutoUpgrade
		}
		if dv := vi.GetDeploymentVersion(); dv != nil {
			d.BuildID = dv.GetBuildId()
		}
		if t := vi.GetVersionTransition(); t != nil {
			d.TransitioningTo = t.GetDeploymentVersion().GetBuildId()
		}
		if o := vi.GetVersioningOverride(); o != nil {
			d.Override = describeOverride(o)
		}
	}

	d.Effective = d.Behavior
	switch {
	case d.Override == BehaviorAutoUpgrade:
		d.Effective = BehaviorAutoUpgrade
	case strings.HasPrefix(d.Override, "Pinned:"):
		d.Effective = BehaviorPinned
	}

	// Best effort: a workflow that has not yet completed a workflow task has no
	// query handler registered, and one wedged on a bad version cannot answer.
	// Neither should stop the detail panel from rendering.
	if v, err := c.c.QueryWorkflow(ctx, workflowID, "", orders.QueryGetState); err == nil {
		var st orders.OrderState
		if err := v.Get(&st); err == nil {
			d.State = &st
		}
	}

	return d, nil
}

// UpgradePinned pins every pinned workflow currently on fromBuildID onto
// toBuildID, and reports how many it moved.
func (c *Client) UpgradePinned(ctx context.Context, fromBuildID, toBuildID string) (int, error) {
	if toBuildID == "" {
		return 0, fmt.Errorf("no target version: the deployment has no current version yet")
	}
	if fromBuildID == toBuildID {
		return 0, nil
	}

	running, err := c.listRunning(ctx)
	if err != nil {
		return 0, err
	}

	moved := 0
	for _, wf := range running {
		if wf.BuildID != fromBuildID || wf.Behavior != BehaviorPinned {
			continue
		}
		if err := c.SetOverride(ctx, wf.ID, OverridePinned, toBuildID); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// TerminateAll ends every running workflow in the deployment, resetting the
// demo between runs.
func (c *Client) TerminateAll(ctx context.Context) (int, error) {
	running, err := c.listRunning(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, wf := range running {
		if err := c.c.TerminateWorkflow(ctx, wf.ID, wf.RunID, "demo reset"); err != nil {
			if isNotFound(err) {
				continue
			}
			return n, fmt.Errorf("terminate %s: %w", wf.ID, err)
		}
		n++
	}
	return n, nil
}

// describeOverride renders a versioning override for display.
func describeOverride(o *workflowpb.VersioningOverride) string {
	if ao := o.GetAutoUpgrade(); ao {
		return BehaviorAutoUpgrade
	}
	if p := o.GetPinned(); p != nil {
		return fmt.Sprintf("Pinned:%s", p.GetVersion().GetBuildId())
	}
	return ""
}

// sortVersions orders version boxes oldest first, so the diagram reads left to
// right in the order the rollout happened.
func sortVersions(vs []Version) {
	sort.SliceStable(vs, func(i, j int) bool {
		return vs[i].CreateTime.Before(vs[j].CreateTime)
	})
}
