package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/brendan-myers/temporal-worker-controller-demo/internal/kube"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError reports a failure to the UI and, because demo failures are usually
// the interesting part, also records it in the event log.
func (s *Server) writeError(w http.ResponseWriter, status int, err error) {
	s.addEvent("error", "%s", err.Error())
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshot())
}

// handleStream pushes state to the browser with server-sent events. The current
// state goes out immediately so a reloading page never renders empty.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	if b, err := json.Marshal(s.snapshot()); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	// Comment frames keep proxies and idle connections from timing out.
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

type startRequest struct {
	Count    int    `json:"count"`
	Behavior string `json:"behavior"`
	Steps    int    `json:"steps"`
}

func (s *Server) handleStartWorkflows(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Steps <= 0 {
		req.Steps = s.cfg.OrderSteps
	}
	if req.Count > 100 {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("refusing to start %d workflows at once", req.Count))
		return
	}

	ids, err := s.tc.StartOrders(r.Context(), req.Count, req.Behavior, req.Steps)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.addEvent("workflow", "started %d %s order(s)", len(ids), req.Behavior)
	writeJSON(w, http.StatusOK, map[string]any{"started": ids})
}

type versioningRequest struct {
	WorkflowID string `json:"workflowId"`
	Mode       string `json:"mode"`
	BuildID    string `json:"buildId"`
}

// handleSetVersioning moves one workflow between versions, or changes its
// behavior. This is a per-execution versioning override, which the worker
// controller's ownership of the deployment's routing config does not affect.
func (s *Server) handleSetVersioning(w http.ResponseWriter, r *http.Request) {
	var req versioningRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return
	}
	if req.WorkflowID == "" {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("workflowId is required"))
		return
	}

	if err := s.tc.SetOverride(r.Context(), req.WorkflowID, req.Mode, req.BuildID); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}

	switch req.Mode {
	case "pinned":
		s.addEvent("workflow", "pinned %s to %s", req.WorkflowID, req.BuildID)
	case "autoUpgrade":
		s.addEvent("workflow", "set %s to auto-upgrade", req.WorkflowID)
	default:
		s.addEvent("workflow", "cleared versioning override on %s", req.WorkflowID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type upgradePinnedRequest struct {
	FromBuildID string `json:"fromBuildId"`
	ToBuildID   string `json:"toBuildId"`
}

// handleUpgradePinned moves every pinned workflow off one version in a batch —
// the "drain this old version so it can be retired" move.
func (s *Server) handleUpgradePinned(w http.ResponseWriter, r *http.Request) {
	var req upgradePinnedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return
	}

	to := req.ToBuildID
	if to == "" {
		to = s.snapshot().CurrentBuildID
	}

	moved, err := s.tc.UpgradePinned(r.Context(), req.FromBuildID, to)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.addEvent("workflow", "moved %d pinned order(s) from %s to %s", moved, req.FromBuildID, to)
	writeJSON(w, http.StatusOK, map[string]any{"moved": moved, "toBuildId": to})
}

// handleRollout patches the WorkerDeployment. The demo never sets the current
// or ramping version itself: it changes the desired state and lets the worker
// controller do the rollout, which is the whole thing being demonstrated.
func (s *Server) handleRollout(w http.ResponseWriter, r *http.Request) {
	var spec kube.Spec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return
	}
	if spec.Image == "" {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("image is required"))
		return
	}
	if spec.Strategy == "" {
		spec.Strategy = "AllAtOnce"
	}

	if err := s.kc.Apply(r.Context(), spec); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.addEvent("rollout", "applied %s with %s strategy", spec.Image, spec.Strategy)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleWorkflowDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := s.tc.Detail(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	n, err := s.tc.TerminateAll(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.addEvent("workflow", "terminated %d running order(s)", n)
	writeJSON(w, http.StatusOK, map[string]any{"terminated": n})
}
