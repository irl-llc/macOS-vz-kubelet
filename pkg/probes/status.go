package probes

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ContainerReadiness evaluates readiness/started for a container based on probe config and results.
type ContainerReadiness struct {
	Results *ResultStore
}

// IsReady reports whether a container passes its readiness probe.
// If no readiness probe is configured, returns the provided default.
func (cr *ContainerReadiness) IsReady(pod types.NamespacedName, spec corev1.Container, defaultReady bool) bool {
	if spec.ReadinessProbe == nil {
		return defaultReady
	}
	threshold := successThreshold(spec.ReadinessProbe)
	return cr.Results.Passing(pod, spec.Name, Readiness, threshold)
}

// IsStarted reports whether a container passes its startup probe.
// If no startup probe is configured, returns true.
func (cr *ContainerReadiness) IsStarted(pod types.NamespacedName, spec corev1.Container) bool {
	if spec.StartupProbe == nil {
		return true
	}
	threshold := successThreshold(spec.StartupProbe)
	return cr.Results.Passing(pod, spec.Name, Startup, threshold)
}

// IsLive reports whether a container passes its liveness probe.
// If no liveness probe is configured, returns true (default alive).
func (cr *ContainerReadiness) IsLive(pod types.NamespacedName, spec corev1.Container) bool {
	if spec.LivenessProbe == nil {
		return true
	}
	threshold := failureThreshold(spec.LivenessProbe)
	return !cr.Results.Failing(pod, spec.Name, Liveness, threshold)
}

func successThreshold(p *corev1.Probe) int {
	if p.SuccessThreshold > 0 {
		return int(p.SuccessThreshold)
	}
	return 1
}

func failureThreshold(p *corev1.Probe) int {
	if p.FailureThreshold > 0 {
		return int(p.FailureThreshold)
	}
	return 3
}
