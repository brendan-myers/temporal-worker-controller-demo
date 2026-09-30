// Package server is the demo's control plane: it polls Temporal and Kubernetes,
// pushes a merged view to the browser over SSE, and exposes the handful of
// write operations the UI offers.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brendan-myers/temporal-worker-controller-demo/internal/kube"
	"github.com/brendan-myers/temporal-worker-controller-demo/internal/temporalstate"
	"github.com/brendan-myers/temporal-worker-controller-demo/web"
)

// rollbackWindow mirrors the controller's own RollbackMaxVersionAge. Rolling out
// to a version that was current more recently than this is treated as a
// rollback: 100% of traffic moves at once and the rollout strategy is ignored.
// The controller hard-codes it; there is no field on the CRD to change it.
const rollbackWindow = time.Hour

// Config is everything the server needs to know about the demo environment.
type Config struct {
	K8sNamespace string
	ResourceName string
	TaskQueue    string
	TemporalNS   string
	TemporalUI   string
	ImageRepo    string
	ImageTags    []string
	PollInterval time.Duration
	MaxEvents    int
	// OrderSteps is how many steps a started order runs for. Zero uses the
	// workflow's own default.
	OrderSteps int
}

// Server owns the poll loop, the latest state and the SSE subscribers.
type Server struct {
	cfg Config
	tc  *temporalstate.Client
	kc  *kube.Client
	hub *hub

	mu     sync.RWMutex
	state  State
	events []Event
	// lastCurrent records when each version was last observed as the Current
	// version. The controller treats a rollout to a version that was current
	// within the last hour as a rollback and ignores the configured strategy, so
	// knowing this is the difference between a ramp that runs and one that is
	// silently skipped. Observed rather than queried: Temporal does not report a
	// "last current" time, so this only covers versions seen since startup.
	lastCurrent map[string]time.Time
}

// New wires a Server. It does not start polling; call Run for that.
func New(cfg Config, tc *temporalstate.Client, kc *kube.Client) *Server {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.MaxEvents <= 0 {
		cfg.MaxEvents = 200
	}
	return &Server{cfg: cfg, tc: tc, kc: kc, hub: newHub(), lastCurrent: map[string]time.Time{}}
}

// Run polls until the context is cancelled, broadcasting each new state.
func (s *Server) Run(ctx context.Context) {
	s.addEvent("info", "demo started; watching %s", s.tc.DeploymentName())

	tick := time.NewTicker(s.cfg.PollInterval)
	defer tick.Stop()

	for {
		s.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// poll builds one merged snapshot and publishes it.
//
// Failures on either side are recorded on the state rather than aborting the
// loop: during a rollout the WorkerDeployment is briefly mid-reconcile, and the
// demo should degrade visibly rather than stop updating.
func (s *Server) poll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	next := State{
		DeploymentName: s.tc.DeploymentName(),
		TaskQueue:      s.cfg.TaskQueue,
		TemporalNS:     s.cfg.TemporalNS,
		K8sNamespace:   s.cfg.K8sNamespace,
		ResourceName:   s.cfg.ResourceName,
		TemporalUI:     s.cfg.TemporalUI,
		ImageRepo:      s.cfg.ImageRepo,
		ImageTags:      s.cfg.ImageTags,
		UpdatedAt:      time.Now(),
		Versions:       []Version{},
		Unplaced:       []temporalstate.Workflow{},
	}

	tsnap, err := s.tc.Snapshot(ctx)
	if err != nil {
		next.Errors = append(next.Errors, "temporal: "+err.Error())
		tsnap = nil
	}

	cr, err := s.kc.Get(ctx)
	if err != nil {
		next.Errors = append(next.Errors, "kubernetes: "+err.Error())
	}
	next.CR = cr
	if msg := controllerFailure(cr); msg != "" {
		next.Errors = append(next.Errors, "worker controller: "+msg)
	}

	pods, err := s.kc.Pods(ctx)
	if err != nil {
		next.Errors = append(next.Errors, "kubernetes: "+err.Error())
	}

	if tsnap != nil {
		next.CurrentBuildID = tsnap.CurrentBuildID
		next.RampingBuildID = tsnap.RampingBuildID
		next.RampPercentage = tsnap.RampPercentage
		next.Unplaced = tsnap.Unplaced

		for _, v := range tsnap.Versions {
			view := Version{
				BuildID:        v.BuildID,
				Status:         v.Status,
				RampPercentage: v.RampPercentage,
				Workflows:      v.Workflows,
			}
			if p, ok := pods[v.BuildID]; ok {
				view.Ready, view.Desired = p.Ready, p.Desired
			}
			for _, wf := range v.Workflows {
				switch wf.Behavior {
				case temporalstate.BehaviorPinned:
					view.Pinned++
				case temporalstate.BehaviorAutoUpgrade:
					view.AutoUpgrade++
				}
				if len(wf.Problems) > 0 {
					view.Failing++
				}
			}
			if cr != nil && cr.Status.TargetBuildID == v.BuildID {
				view.IsTarget = true
			}
			next.Versions = append(next.Versions, view)
		}
	}

	// A version's pods can exist before Temporal has heard from them. Showing
	// the box early is the honest picture: the controller has created workers
	// and is waiting for pollers.
	next.Versions = appendPodOnlyVersions(next.Versions, pods, cr)

	s.publish(next)
}

// appendPodOnlyVersions adds boxes for versions that have Kubernetes pods but
// no Temporal presence yet.
func appendPodOnlyVersions(versions []Version, pods map[string]kube.VersionPods, cr *kube.Resource) []Version {
	seen := make(map[string]bool, len(versions))
	for _, v := range versions {
		seen[v.BuildID] = true
	}
	extra := make([]Version, 0)
	for buildID, p := range pods {
		if seen[buildID] {
			continue
		}
		v := Version{BuildID: buildID, Status: "Starting", Ready: p.Ready, Desired: p.Desired}
		if cr != nil && cr.Status.TargetBuildID == buildID {
			v.IsTarget = true
		}
		extra = append(extra, v)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].BuildID < extra[j].BuildID })
	return append(versions, extra...)
}

// publish diffs against the previous state to grow the event log, then stores
// and broadcasts.
func (s *Server) publish(next State) {
	s.mu.Lock()
	if next.CurrentBuildID != "" {
		s.lastCurrent[next.CurrentBuildID] = next.UpdatedAt
	}
	next.RollbackWindow = s.rollbackWindowLocked(next)
	prev := s.state
	s.diff(prev, next)
	next.Events = append([]Event(nil), s.events...)
	s.state = next
	s.mu.Unlock()

	if b, err := json.Marshal(next); err == nil {
		s.hub.broadcast(b)
	}
}

// diff appends log lines for the transitions worth narrating out loud.
// It must be called with s.mu held.
func (s *Server) diff(prev, next State) {
	if prev.UpdatedAt.IsZero() {
		return
	}

	if prev.CurrentBuildID != next.CurrentBuildID && next.CurrentBuildID != "" {
		s.addEventLocked("rollout", "current version is now %s", next.CurrentBuildID)
	}
	if prev.RampingBuildID != next.RampingBuildID {
		switch {
		case next.RampingBuildID == "":
			s.addEventLocked("rollout", "ramp finished")
		default:
			s.addEventLocked("rollout", "ramping to %s", next.RampingBuildID)
		}
	} else if next.RampingBuildID != "" && prev.RampPercentage != next.RampPercentage {
		s.addEventLocked("rollout", "ramp to %s now at %.0f%%", next.RampingBuildID, next.RampPercentage)
	}

	prevVersions := indexVersions(prev.Versions)
	for _, v := range next.Versions {
		p, existed := prevVersions[v.BuildID]
		if !existed {
			s.addEventLocked("rollout", "version %s appeared", v.BuildID)
			continue
		}
		if p.Status != v.Status {
			s.addEventLocked("rollout", "version %s is now %s", v.BuildID, strings.ToLower(v.Status))
		}
		if p.Failing != v.Failing && v.Failing > 0 {
			s.addEventLocked("error", "%d order(s) on %s are failing to make progress", v.Failing, v.BuildID)
		}
		if p.Ready != v.Ready || p.Desired != v.Desired {
			s.addEventLocked("info", "version %s pods %d/%d ready", v.BuildID, v.Ready, v.Desired)
		}
	}
	nextVersions := indexVersions(next.Versions)
	for _, v := range prev.Versions {
		if _, still := nextVersions[v.BuildID]; !still {
			s.addEventLocked("rollout", "version %s was removed", v.BuildID)
		}
	}

	if prev.CR != nil && next.CR != nil && prev.CR.Found && next.CR.Found {
		if prev.CR.Spec.Image != next.CR.Spec.Image {
			s.addEventLocked("rollout", "WorkerDeployment image set to %s", next.CR.Spec.Image)
		}
		if prev.CR.Spec.Strategy != next.CR.Spec.Strategy {
			s.addEventLocked("rollout", "rollout strategy set to %s", next.CR.Spec.Strategy)
		}
		if prev.CR.Spec.Replicas != next.CR.Spec.Replicas {
			s.addEventLocked("info", "replicas set to %d", next.CR.Spec.Replicas)
		}
	}
}

// controllerFailure returns the message of a Ready condition that indicates the
// controller is stuck, or "" when it is simply mid-rollout.
//
// The reason alone is not enough to act on: "TemporalStateFetchFailed" says
// nothing, while its message names the actual problem. Reasons like Ramping or
// WaitingForPollers are normal progress and must not be reported as errors.
func controllerFailure(cr *kube.Resource) string {
	if cr == nil || !cr.Found {
		return ""
	}
	for _, c := range cr.Status.Conditions {
		if c.Type != "Ready" || c.Status == "True" {
			continue
		}
		if !isFailureReason(c.Reason) {
			return ""
		}
		if c.Message != "" {
			return c.Message
		}
		return c.Reason
	}
	return ""
}

func isFailureReason(reason string) bool {
	for _, suffix := range []string{"Failed", "Invalid", "NotFound", "Unsupported"} {
		if strings.HasSuffix(reason, suffix) {
			return true
		}
	}
	return false
}

// rollbackWindowLocked lists versions, other than the current one, that were
// current recently enough that rolling out to them counts as a rollback.
// Must be called with s.mu held.
func (s *Server) rollbackWindowLocked(st State) []RecentVersion {
	out := []RecentVersion{}
	for buildID, at := range s.lastCurrent {
		if buildID == st.CurrentBuildID {
			continue
		}
		age := st.UpdatedAt.Sub(at)
		if age >= rollbackWindow {
			continue
		}
		out = append(out, RecentVersion{
			BuildID:    buildID,
			Tag:        imageTagFromBuildID(buildID),
			SecondsAgo: int(age.Seconds()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SecondsAgo < out[j].SecondsAgo })
	return out
}

// imageTagFromBuildID recovers the image tag a build ID was derived from. The
// controller builds them as "<image tag>-<pod template hash>".
func imageTagFromBuildID(buildID string) string {
	if i := strings.LastIndex(buildID, "-"); i > 0 {
		return buildID[:i]
	}
	return buildID
}

func indexVersions(vs []Version) map[string]Version {
	m := make(map[string]Version, len(vs))
	for _, v := range vs {
		m[v.BuildID] = v
	}
	return m
}

func (s *Server) addEvent(kind, format string, args ...any) {
	s.mu.Lock()
	s.addEventLocked(kind, format, args...)
	s.mu.Unlock()
}

// addEventLocked must be called with s.mu held.
func (s *Server) addEventLocked(kind, format string, args ...any) {
	s.events = append(s.events, Event{At: time.Now(), Kind: kind, Text: fmt.Sprintf(format, args...)})
	if len(s.events) > s.cfg.MaxEvents {
		s.events = s.events[len(s.events)-s.cfg.MaxEvents:]
	}
}

// snapshot returns a copy of the latest state.
func (s *Server) snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Handler builds the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /", http.FileServer(http.FS(web.Files)))
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("GET /api/workflows/{id}", s.handleWorkflowDetail)
	mux.HandleFunc("POST /api/workflows", s.handleStartWorkflows)
	mux.HandleFunc("POST /api/workflows/versioning", s.handleSetVersioning)
	mux.HandleFunc("POST /api/workflows/upgrade-pinned", s.handleUpgradePinned)
	mux.HandleFunc("POST /api/rollout", s.handleRollout)
	mux.HandleFunc("POST /api/reset", s.handleReset)

	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}
