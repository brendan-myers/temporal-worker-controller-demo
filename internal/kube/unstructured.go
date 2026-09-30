package kube

import (
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Helpers for reading the unstructured WorkerDeployment. They all tolerate
// missing or unexpected shapes and return zero values, because the demo polls
// this resource continuously and must not fall over mid-reconcile.

func isNotFound(err error) bool {
	return apierrors.IsNotFound(err)
}

func firstContainerImage(spec map[string]any) string {
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return ""
	}
	podSpec, ok := template["spec"].(map[string]any)
	if !ok {
		return ""
	}
	containers, ok := podSpec["containers"].([]any)
	if !ok || len(containers) == 0 {
		return ""
	}
	c, ok := containers[0].(map[string]any)
	if !ok {
		return ""
	}
	image, _ := c["image"].(string)
	return image
}

func intField(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func floatField(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func parseSteps(raw any) []RolloutStep {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]RolloutStep, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		pause, _ := m["pauseDuration"].(string)
		out = append(out, RolloutStep{
			RampPercentage: int(intField(m, "rampPercentage")),
			PauseDuration:  pause,
		})
	}
	return out
}

func parseConditions(raw any) []Condition {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]Condition, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		s, _ := m["status"].(string)
		r, _ := m["reason"].(string)
		msg, _ := m["message"].(string)
		out = append(out, Condition{Type: t, Status: s, Reason: r, Message: msg})
	}
	return out
}
