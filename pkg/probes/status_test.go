package probes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

var statusPod = types.NamespacedName{Namespace: "ns", Name: "pod"}

func TestContainerReadiness_NoProbe(t *testing.T) {
	cr := &ContainerReadiness{Results: NewResultStore()}
	spec := corev1.Container{Name: "c1"}

	assert.True(t, cr.IsReady(statusPod, spec, true))
	assert.False(t, cr.IsReady(statusPod, spec, false))
	assert.True(t, cr.IsStarted(statusPod, spec))
}

func TestContainerReadiness_ReadinessPassing(t *testing.T) {
	store := NewResultStore()
	store.Record(statusPod, "c1", Readiness, Success)
	cr := &ContainerReadiness{Results: store}
	spec := corev1.Container{
		Name:           "c1",
		ReadinessProbe: &corev1.Probe{},
	}
	assert.True(t, cr.IsReady(statusPod, spec, false))
}

func TestContainerReadiness_ReadinessFailing(t *testing.T) {
	store := NewResultStore()
	store.Record(statusPod, "c1", Readiness, Failure)
	cr := &ContainerReadiness{Results: store}
	spec := corev1.Container{
		Name:           "c1",
		ReadinessProbe: &corev1.Probe{},
	}
	assert.False(t, cr.IsReady(statusPod, spec, true))
}

func TestContainerReadiness_StartupPassing(t *testing.T) {
	store := NewResultStore()
	store.Record(statusPod, "c1", Startup, Success)
	cr := &ContainerReadiness{Results: store}
	spec := corev1.Container{
		Name:         "c1",
		StartupProbe: &corev1.Probe{},
	}
	assert.True(t, cr.IsStarted(statusPod, spec))
}

func TestContainerReadiness_StartupNotYetPassing(t *testing.T) {
	cr := &ContainerReadiness{Results: NewResultStore()}
	spec := corev1.Container{
		Name:         "c1",
		StartupProbe: &corev1.Probe{},
	}
	assert.False(t, cr.IsStarted(statusPod, spec))
}
