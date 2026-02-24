//go:build integration

package resourcemanager_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"

	"github.com/agoda-com/macOS-vz-kubelet/internal/volumes"
	eventmocks "github.com/agoda-com/macOS-vz-kubelet/pkg/event/mocks"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
	rm "github.com/agoda-com/macOS-vz-kubelet/pkg/resourcemanager"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const integrationClientImage = "alpine:latest"

func findContainerBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("container")
	require.NoError(t, err, "container CLI not found in PATH; is Apple Container installed?")
	return path
}

func skipUnlessServicesRunning(t *testing.T) {
	t.Helper()
	out, err := exec.Command("container", "system", "status").CombinedOutput()
	if err != nil {
		t.Skipf("container services not running (run `container system start`): %s", strings.TrimSpace(string(out)))
	}
}

func newIntegrationClient(t *testing.T, ctx context.Context) *rm.AppleContainerClient {
	t.Helper()
	cli, cliErr := rm.NewAppleContainerCLI(findContainerBinary(t))
	require.NoError(t, cliErr)
	rec := eventmocks.NewMockEventRecorder(t)
	stubAllEvents(rec)
	client, err := rm.NewAppleContainerClient(ctx, cli, rec)
	require.NoError(t, err)
	return client
}

func awaitRunning(
	t *testing.T, ctx context.Context,
	client *rm.AppleContainerClient,
	ns, pod, ctr string, timeout time.Duration,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		containers, err := client.GetContainers(ctx, ns, pod)
		if err != nil || len(containers) == 0 {
			return false
		}
		return containers[0].State.Status == resource.ContainerStatusRunning
	}, timeout, 500*time.Millisecond, "container never reached running state")
}

// stubAllEvents accepts any call to any EventRecorder method.
func stubAllEvents(rec *eventmocks.MockEventRecorder) {
	methods := []string{
		"PullingImage", "PulledImage", "FailedToValidateOCI",
		"FailedToPullImage", "BackOffPullImage",
		"CreatedContainer", "StartedContainer",
		"FailedToCreateContainer", "FailedToStartContainer",
		"FailedPostStartHook", "FailedPreStopHook",
		"FailedToResolveImagePullSecrets",
	}
	wildcard := []interface{}{
		mock.Anything, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything,
	}
	for _, m := range methods {
		rec.On(m, wildcard...).Return().Maybe()
	}
}

// TestClientIntegration_FullLifecycle exercises the AppleContainerClient
// with a real CLI backend: create, inspect, list, stats, remove.
func TestClientIntegration_FullLifecycle(t *testing.T) {
	skipUnlessServicesRunning(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newIntegrationClient(t, ctx)

	const (
		ns  = "integration"
		pod = "lifecycle-pod"
		ctr = "worker"
	)
	t.Cleanup(func() { _ = client.RemoveContainers(context.Background(), ns, pod, 0) })

	err := client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    ns,
		PodName:         pod,
		Name:            ctr,
		Image:           integrationClientImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sh", "-c", "echo alive && sleep 300"},
	})
	require.NoError(t, err)
	awaitRunning(t, ctx, client, ns, pod, ctr, 30*time.Second)

	t.Run("GetContainers", func(t *testing.T) {
		containers, err := client.GetContainers(ctx, ns, pod)
		require.NoError(t, err)
		require.Len(t, containers, 1)
		assert.Equal(t, ctr, containers[0].Name)
		assert.Equal(t, resource.ContainerStatusRunning, containers[0].State.Status)
	})

	t.Run("IsContainerPresent", func(t *testing.T) {
		assert.True(t, client.IsContainerPresent(ctx, ns, pod, ctr))
		assert.False(t, client.IsContainerPresent(ctx, ns, pod, "nonexistent"))
	})

	t.Run("GetContainersListResult", func(t *testing.T) {
		result, err := client.GetContainersListResult(ctx)
		require.NoError(t, err)
		key := types.NamespacedName{Namespace: ns, Name: pod}
		containers, ok := result[key]
		require.True(t, ok, "pod %s/%s not found in list result", ns, pod)
		require.Len(t, containers, 1)
		assert.Equal(t, ctr, containers[0].Name)
	})

	t.Run("GetContainerStats", func(t *testing.T) {
		require.Eventually(t, func() bool {
			s, err := client.GetContainerStats(ctx, ns, pod, ctr)
			return err == nil && s.CPU != nil
		}, 15*time.Second, 1*time.Second, "stats never became available")

		s, err := client.GetContainerStats(ctx, ns, pod, ctr)
		require.NoError(t, err)
		assert.Equal(t, ctr, s.Name)
		assert.NotNil(t, s.CPU)
		assert.NotNil(t, s.Memory)
	})

	t.Run("RemoveContainers", func(t *testing.T) {
		err := client.RemoveContainers(ctx, ns, pod, 0)
		require.NoError(t, err)
		assert.False(t, client.IsContainerPresent(ctx, ns, pod, ctr))
	})
}

// TestClientIntegration_DuplicateCreate verifies creating the same container
// twice returns an error.
func TestClientIntegration_DuplicateCreate(t *testing.T) {
	skipUnlessServicesRunning(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := newIntegrationClient(t, ctx)

	const (
		ns  = "integration"
		pod = "dup-pod"
		ctr = "dup"
	)
	t.Cleanup(func() { _ = client.RemoveContainers(context.Background(), ns, pod, 0) })

	params := rm.ContainerParams{
		PodNamespace:    ns,
		PodName:         pod,
		Name:            ctr,
		Image:           integrationClientImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sleep", "300"},
	}

	require.NoError(t, client.CreateContainer(ctx, params))

	err := client.CreateContainer(ctx, params)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// TestClientIntegration_VolumeMounts verifies files are accessible inside
// the container via volume mounts in ContainerParams.
func TestClientIntegration_VolumeMounts(t *testing.T) {
	skipUnlessServicesRunning(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newIntegrationClient(t, ctx)

	const (
		ns  = "integration"
		pod = "volmount-pod"
		ctr = "app"
	)
	t.Cleanup(func() { _ = client.RemoveContainers(context.Background(), ns, pod, 0) })

	hostDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "config.txt"), []byte("mount-value"), 0644))

	err := client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    ns,
		PodName:         pod,
		Name:            ctr,
		Image:           integrationClientImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Mounts: []volumes.Mount{{
			Name:          "config",
			HostPath:      hostDir,
			ContainerPath: "/mnt/config",
			ReadOnly:      true,
		}},
		Command: []string{"sleep", "300"},
	})
	require.NoError(t, err)
	awaitRunning(t, ctx, client, ns, pod, ctr, 30*time.Second)

	var stdout bytes.Buffer
	attach := newExecCapture(&stdout)
	err = client.ExecInContainer(ctx, ns, pod, ctr, []string{"cat", "/mnt/config/config.txt"}, attach)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "mount-value")
}

// TestClientIntegration_EnvVars verifies environment variables are
// propagated to the container.
func TestClientIntegration_EnvVars(t *testing.T) {
	skipUnlessServicesRunning(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newIntegrationClient(t, ctx)

	const (
		ns  = "integration"
		pod = "env-pod"
		ctr = "app"
	)
	t.Cleanup(func() { _ = client.RemoveContainers(context.Background(), ns, pod, 0) })

	err := client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    ns,
		PodName:         pod,
		Name:            ctr,
		Image:           integrationClientImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env: []corev1.EnvVar{
			{Name: "FOO", Value: "bar"},
			{Name: "DB_HOST", Value: "postgres:5432"},
		},
		Command: []string{"sleep", "300"},
	})
	require.NoError(t, err)
	awaitRunning(t, ctx, client, ns, pod, ctr, 30*time.Second)

	var stdout bytes.Buffer
	attach := newExecCapture(&stdout)
	err = client.ExecInContainer(ctx, ns, pod, ctr, []string{"printenv", "FOO"}, attach)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "bar")

	stdout.Reset()
	err = client.ExecInContainer(ctx, ns, pod, ctr, []string{"printenv", "DB_HOST"}, attach)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "postgres:5432")
}

// TestClientIntegration_PostStartHook verifies PostStartAction executes
// after the container starts.
func TestClientIntegration_PostStartHook(t *testing.T) {
	skipUnlessServicesRunning(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newIntegrationClient(t, ctx)

	const (
		ns  = "integration"
		pod = "poststart-pod"
		ctr = "app"
	)
	t.Cleanup(func() { _ = client.RemoveContainers(context.Background(), ns, pod, 0) })

	err := client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    ns,
		PodName:         pod,
		Name:            ctr,
		Image:           integrationClientImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sleep", "300"},
		PostStartAction: &resource.ExecAction{
			Command:         []string{"sh", "-c", "touch /tmp/poststart-marker"},
			TimeoutDuration: 30 * time.Second,
		},
	})
	require.NoError(t, err)
	awaitRunning(t, ctx, client, ns, pod, ctr, 30*time.Second)

	// PostStartAction runs asynchronously; give it time
	require.Eventually(t, func() bool {
		var stdout bytes.Buffer
		attach := newExecCapture(&stdout)
		err := client.ExecInContainer(ctx, ns, pod, ctr, []string{"test", "-f", "/tmp/poststart-marker"}, attach)
		return err == nil
	}, 30*time.Second, 1*time.Second, "poststart marker file never appeared")
}

// TestClientIntegration_MultiContainer tests creating multiple containers
// for the same pod and cleaning them all up.
func TestClientIntegration_MultiContainer(t *testing.T) {
	skipUnlessServicesRunning(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newIntegrationClient(t, ctx)

	const (
		ns  = "integration"
		pod = "multi-pod"
	)
	containers := []string{"main", "sidecar-a", "sidecar-b"}
	t.Cleanup(func() { _ = client.RemoveContainers(context.Background(), ns, pod, 0) })

	for _, name := range containers {
		err := client.CreateContainer(ctx, rm.ContainerParams{
			PodNamespace:    ns,
			PodName:         pod,
			Name:            name,
			Image:           integrationClientImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"sleep", "300"},
		})
		require.NoError(t, err, "create %s", name)
	}

	for _, name := range containers {
		awaitRunning(t, ctx, client, ns, pod, name, 30*time.Second)
	}

	result, err := client.GetContainers(ctx, ns, pod)
	require.NoError(t, err)
	require.Len(t, result, 3)

	gotNames := make([]string, len(result))
	for i, c := range result {
		gotNames[i] = c.Name
	}
	assert.ElementsMatch(t, containers, gotNames)

	err = client.RemoveContainers(ctx, ns, pod, 0)
	require.NoError(t, err)

	for _, name := range containers {
		assert.False(t, client.IsContainerPresent(ctx, ns, pod, name))
	}
}

// TestClientIntegration_ImagePullAlways verifies PullAlways re-pulls the image.
func TestClientIntegration_ImagePullAlways(t *testing.T) {
	skipUnlessServicesRunning(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := newIntegrationClient(t, ctx)

	const (
		ns  = "integration"
		pod = "pullalways-pod"
		ctr = "app"
	)
	t.Cleanup(func() { _ = client.RemoveContainers(context.Background(), ns, pod, 0) })

	err := client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    ns,
		PodName:         pod,
		Name:            ctr,
		Image:           integrationClientImage,
		ImagePullPolicy: corev1.PullAlways,
		Command:         []string{"sleep", "300"},
	})
	require.NoError(t, err)
	awaitRunning(t, ctx, client, ns, pod, ctr, 60*time.Second)
}

// TestClientIntegration_ImagePullNever verifies PullNever skips pulling.
func TestClientIntegration_ImagePullNever(t *testing.T) {
	skipUnlessServicesRunning(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := newIntegrationClient(t, ctx)

	const (
		ns  = "integration"
		pod = "pullnever-pod"
		ctr = "app"
	)
	t.Cleanup(func() { _ = client.RemoveContainers(context.Background(), ns, pod, 0) })

	err := client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    ns,
		PodName:         pod,
		Name:            ctr,
		Image:           "nonexistent-image-that-does-not-exist:v999",
		ImagePullPolicy: corev1.PullNever,
		Command:         []string{"sleep", "300"},
	})
	require.NoError(t, err, "CreateContainer returns nil because creation is async")

	// The container should fail because the image doesn't exist locally
	require.Eventually(t, func() bool {
		containers, err := client.GetContainers(ctx, ns, pod)
		if err != nil || len(containers) == 0 {
			return false
		}
		return containers[0].State.Error != ""
	}, 30*time.Second, 1*time.Second, "container should fail with missing image")
}

// newExecCapture creates an AttachIO that captures stdout.
func newExecCapture(stdout *bytes.Buffer) *execCapture {
	return &execCapture{stdout: stdout}
}

type execCapture struct{ stdout *bytes.Buffer }

func (e *execCapture) TTY() bool                         { return false }
func (e *execCapture) Stdin() io.Reader                   { return nil }
func (e *execCapture) Stdout() io.WriteCloser             { return nopWC{e.stdout} }
func (e *execCapture) Stderr() io.WriteCloser             { return nopWC{io.Discard} }
func (e *execCapture) Resize() <-chan api.TermSize        { return nil }

type nopWC struct{ io.Writer }

func (nopWC) Close() error { return nil }
