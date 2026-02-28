package probes

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agoda-com/macOS-vz-kubelet/internal/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func alwaysSucceed(_ context.Context, _, _, _ string, _ []string, _ *node.ExecIO) error {
	return nil
}

func noopResolver(_ context.Context, _, _, _ string) (string, error) {
	return "127.0.0.1", nil
}

func testPodWithProbe(probe *corev1.Probe) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:           "c1",
				ReadinessProbe: probe,
			}},
		},
	}
}

func TestRunner_StartAndStop(t *testing.T) {
	r := NewRunner(alwaysSucceed, noopResolver)
	pod := testPodWithProbe(&corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{Command: []string{"true"}},
		},
		PeriodSeconds: 1,
	})

	r.StartForPod(pod)
	nn := types.NamespacedName{Namespace: "ns", Name: "pod"}

	// Wait a bit for probe to run
	time.Sleep(200 * time.Millisecond)
	assert.True(t, r.Results.Passing(nn, "c1", Readiness, 1))

	r.StopForPod(nn)
	// Results should be cleared
	assert.False(t, r.Results.Passing(nn, "c1", Readiness, 1))
}

func TestRunner_NoProbes(t *testing.T) {
	r := NewRunner(alwaysSucceed, noopResolver)
	pod := testPodWithProbe(nil) // no probes configured

	r.StartForPod(pod)
	nn := types.NamespacedName{Namespace: "ns", Name: "pod"}

	// Should have no goroutines and no results
	assert.False(t, r.Results.Passing(nn, "c1", Readiness, 1))
	r.StopForPod(nn)
}

func alwaysFail(_ context.Context, _, _, _ string, _ []string, _ *node.ExecIO) error {
	return fmt.Errorf("probe failed")
}

// TestRunner_StartupGate verifies that readiness probes are blocked
// until the startup probe succeeds.
func TestRunner_StartupGate(t *testing.T) {
	var startupOK atomic.Bool
	gatedExec := func(_ context.Context, _, _, _ string, cmd []string, _ *node.ExecIO) error {
		if cmd[0] == "startup" && !startupOK.Load() {
			return fmt.Errorf("not started")
		}
		return nil
	}

	r := NewRunner(gatedExec, noopResolver)
	nn := types.NamespacedName{Namespace: "ns", Name: "pod"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: nn.Namespace, Name: nn.Name},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "c1",
			StartupProbe: &corev1.Probe{
				ProbeHandler:  corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"startup"}}},
				PeriodSeconds: 1,
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler:  corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"ready"}}},
				PeriodSeconds: 1,
			},
		}}},
	}

	r.StartForPod(pod)
	defer r.StopForPod(nn)

	// Readiness should not pass while startup gate is closed
	time.Sleep(1500 * time.Millisecond)
	assert.False(t, r.Results.Passing(nn, "c1", Readiness, 1),
		"readiness must not pass while startup gate is closed")

	// Open the startup gate
	startupOK.Store(true)

	// Readiness should eventually pass
	require.Eventually(t, func() bool {
		return r.Results.Passing(nn, "c1", Readiness, 1)
	}, 5*time.Second, 200*time.Millisecond,
		"readiness should pass after startup gate opens")
}

// TestRunner_InitialDelay verifies that the probe respects InitialDelaySeconds.
func TestRunner_InitialDelay(t *testing.T) {
	var firstExec atomic.Int64
	timedExec := func(_ context.Context, _, _, _ string, _ []string, _ *node.ExecIO) error {
		firstExec.CompareAndSwap(0, time.Now().UnixMilli())
		return nil
	}

	r := NewRunner(timedExec, noopResolver)
	nn := types.NamespacedName{Namespace: "ns", Name: "delay-pod"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: nn.Namespace, Name: nn.Name},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "c1",
			ReadinessProbe: &corev1.Probe{
				ProbeHandler:       corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"check"}}},
				InitialDelaySeconds: 2,
				PeriodSeconds:       1,
			},
		}}},
	}

	start := time.Now()
	r.StartForPod(pod)
	defer r.StopForPod(nn)

	require.Eventually(t, func() bool {
		return firstExec.Load() > 0
	}, 5*time.Second, 200*time.Millisecond, "probe should eventually run")

	elapsed := time.UnixMilli(firstExec.Load()).Sub(start)
	assert.GreaterOrEqual(t, elapsed, 1900*time.Millisecond,
		"first probe execution should be delayed by InitialDelaySeconds")
}

// TestRunner_SuccessThreshold verifies that SuccessThreshold > 1 requires
// multiple consecutive successes before Passing returns true.
func TestRunner_SuccessThreshold(t *testing.T) {
	r := NewRunner(alwaysSucceed, noopResolver)
	nn := types.NamespacedName{Namespace: "ns", Name: "thresh-pod"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: nn.Namespace, Name: nn.Name},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "c1",
			ReadinessProbe: &corev1.Probe{
				ProbeHandler:     corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"ok"}}},
				PeriodSeconds:    1,
				SuccessThreshold: 3,
			},
		}}},
	}

	r.StartForPod(pod)
	defer r.StopForPod(nn)

	// After first probe (~200ms) — threshold 3 not yet met
	time.Sleep(200 * time.Millisecond)
	assert.False(t, r.Results.Passing(nn, "c1", Readiness, 3),
		"should not pass after only 1 success")

	// After enough time for 3+ successes
	require.Eventually(t, func() bool {
		return r.Results.Passing(nn, "c1", Readiness, 3)
	}, 5*time.Second, 200*time.Millisecond,
		"should pass after 3 consecutive successes")
}

// TestRunner_FailureThreshold verifies that FailureThreshold consecutive
// failures are required before the probe is considered failing.
func TestRunner_FailureThreshold(t *testing.T) {
	r := NewRunner(alwaysFail, noopResolver)
	nn := types.NamespacedName{Namespace: "ns", Name: "fail-pod"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: nn.Namespace, Name: nn.Name},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "c1",
			LivenessProbe: &corev1.Probe{
				ProbeHandler:     corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"fail"}}},
				PeriodSeconds:    1,
				FailureThreshold: 3,
			},
		}}},
	}

	r.StartForPod(pod)
	defer r.StopForPod(nn)

	// After first probe (~200ms) — threshold 3 not yet met
	time.Sleep(200 * time.Millisecond)
	assert.False(t, r.Results.Failing(nn, "c1", Liveness, 3),
		"should not be failing after only 1 failure")

	// After enough failures
	require.Eventually(t, func() bool {
		return r.Results.Failing(nn, "c1", Liveness, 3)
	}, 5*time.Second, 200*time.Millisecond,
		"should be failing after 3 consecutive failures")
}
