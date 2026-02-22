package resourcemanager_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	eventmocks "github.com/agoda-com/macOS-vz-kubelet/pkg/event/mocks"
	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
	rm "github.com/agoda-com/macOS-vz-kubelet/pkg/resourcemanager"
	climocks "github.com/agoda-com/macOS-vz-kubelet/pkg/resourcemanager/mocks"

	corev1 "k8s.io/api/core/v1"
)

// --- helpers ---

func newTestClient(t *testing.T) (*rm.AppleContainerClient, *climocks.MockCLIExecutor, *eventmocks.MockEventRecorder) {
	t.Helper()
	cli := climocks.NewMockCLIExecutor(t)
	rec := eventmocks.NewMockEventRecorder(t)

	cli.On("ListContainers", mock.Anything, rm.ContainerNamePrefix).
		Return([]string{}, nil)

	client, err := rm.NewAppleContainerClient(context.Background(), cli, rec)
	require.NoError(t, err)
	return client, cli, rec
}

// wasCalled checks if a mock method was called without logging test failures.
func wasCalled(m *mock.Mock, method string) bool {
	for _, call := range m.Calls {
		if call.Method == method {
			return true
		}
	}
	return false
}

func awaitCall(t *testing.T, m *mock.Mock, method string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return wasCalled(m, method)
	}, 2*time.Second, 50*time.Millisecond)
}

// --- constructor tests ---

func TestNewAppleContainerClient_NilCLI(t *testing.T) {
	rec := eventmocks.NewMockEventRecorder(t)
	_, err := rm.NewAppleContainerClient(context.Background(), nil, rec)
	assert.Error(t, err)
}

func TestNewAppleContainerClient_CleansDanglingContainers(t *testing.T) {
	cli := climocks.NewMockCLIExecutor(t)
	rec := eventmocks.NewMockEventRecorder(t)

	cli.On("ListContainers", mock.Anything, rm.ContainerNamePrefix).
		Return([]string{"macos-vz_ns_pod_ctr"}, nil)
	cli.On("RemoveContainer", mock.Anything, "macos-vz_ns_pod_ctr", true).
		Return(nil)

	_, err := rm.NewAppleContainerClient(context.Background(), cli, rec)
	require.NoError(t, err)
	cli.AssertCalled(t, "RemoveContainer", mock.Anything, "macos-vz_ns_pod_ctr", true)
}

// --- CreateContainer tests ---

func TestCreateContainer_Success(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	cli.On("PullImage", mock.Anything, "alpine:latest", resource.RegistryCredentials{}).
		Return(nil)
	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Return("ctr-id-123", nil)

	rec.On("PullingImage", mock.Anything, "alpine:latest", "app").Return()
	rec.On("PulledImage", mock.Anything, "alpine:latest", "app", mock.AnythingOfType("string")).Return()
	rec.On("CreatedContainer", mock.Anything, "app").Return()
	rec.On("StartedContainer", mock.Anything, "app").Return()

	err := client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "app",
		Image:           "alpine:latest",
		ImagePullPolicy: corev1.PullIfNotPresent,
	})
	require.NoError(t, err)

	awaitCall(t, &cli.Mock, "CreateAndStartContainer")
	cli.AssertCalled(t, "PullImage", mock.Anything, "alpine:latest", resource.RegistryCredentials{})
}

func TestCreateContainer_DuplicateReturnsError(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	cli.On("PullImage", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Return("ctr-id", nil).Maybe()

	rec.On("PullingImage", mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	rec.On("PulledImage", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	rec.On("CreatedContainer", mock.Anything, mock.Anything).Return().Maybe()
	rec.On("StartedContainer", mock.Anything, mock.Anything).Return().Maybe()

	params := rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "dup",
		Image:           "alpine:latest",
		ImagePullPolicy: corev1.PullIfNotPresent,
	}

	require.NoError(t, client.CreateContainer(ctx, params))

	err := client.CreateContainer(ctx, params)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestCreateContainer_PullNever(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Return("ctr-id", nil)
	rec.On("CreatedContainer", mock.Anything, "skip").Return()
	rec.On("StartedContainer", mock.Anything, "skip").Return()

	err := client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "skip",
		Image:           "alpine:latest",
		ImagePullPolicy: corev1.PullNever,
	})
	require.NoError(t, err)

	awaitCall(t, &cli.Mock, "CreateAndStartContainer")
	assert.False(t, wasCalled(&cli.Mock, "PullImage"))
}

func TestCreateContainer_PullAlwaysRemovesFirst(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	cli.On("RemoveImage", mock.Anything, "alpine:latest").Return(nil)
	cli.On("PullImage", mock.Anything, "alpine:latest", resource.RegistryCredentials{}).Return(nil)
	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Return("ctr-id", nil)

	rec.On("PullingImage", mock.Anything, "alpine:latest", "always").Return()
	rec.On("PulledImage", mock.Anything, "alpine:latest", "always", mock.AnythingOfType("string")).Return()
	rec.On("CreatedContainer", mock.Anything, "always").Return()
	rec.On("StartedContainer", mock.Anything, "always").Return()

	err := client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "always",
		Image:           "alpine:latest",
		ImagePullPolicy: corev1.PullAlways,
	})
	require.NoError(t, err)

	awaitCall(t, &cli.Mock, "CreateAndStartContainer")
	cli.AssertCalled(t, "RemoveImage", mock.Anything, "alpine:latest")
}

// --- RemoveContainers tests ---

func TestRemoveContainers_NotTracked(t *testing.T) {
	client, _, _ := newTestClient(t)
	err := client.RemoveContainers(context.Background(), "ns", "ghost", 0)
	assert.Error(t, err)
}

// --- GetContainers tests ---

func TestGetContainers_NotTracked(t *testing.T) {
	client, _, _ := newTestClient(t)
	_, err := client.GetContainers(context.Background(), "ns", "ghost")
	assert.Error(t, err)
}

func TestGetContainers_WithTracked(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	cli.On("PullImage", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Return("id-1", nil)
	cli.On("InspectContainer", mock.Anything, "id-1").
		Return(resource.ContainerState{Status: resource.ContainerStatusRunning}, nil)

	rec.On("PullingImage", mock.Anything, mock.Anything, mock.Anything).Return()
	rec.On("PulledImage", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	rec.On("CreatedContainer", mock.Anything, mock.Anything).Return()
	rec.On("StartedContainer", mock.Anything, mock.Anything).Return()

	require.NoError(t, client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "web",
		Image:           "nginx",
		ImagePullPolicy: corev1.PullIfNotPresent,
	}))

	awaitCall(t, &cli.Mock, "CreateAndStartContainer")

	containers, err := client.GetContainers(ctx, "ns", "pod")
	require.NoError(t, err)
	require.Len(t, containers, 1)
	assert.Equal(t, "web", containers[0].Name)
	assert.Equal(t, resource.ContainerStatusRunning, containers[0].State.Status)
}

// --- IsContainerPresent tests ---

func TestIsContainerPresent_False(t *testing.T) {
	client, _, _ := newTestClient(t)
	assert.False(t, client.IsContainerPresent(context.Background(), "ns", "pod", "nope"))
}

// --- GetContainerStats tests ---

func TestGetContainerStats_NotFound(t *testing.T) {
	client, _, _ := newTestClient(t)
	_, err := client.GetContainerStats(context.Background(), "ns", "pod", "ctr")
	assert.Error(t, err)
}

// --- containerCreateArgs tests ---

func TestContainerCreateArgs_EnvAndBinds(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	var captured rm.ContainerCreateArgs
	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Run(func(args mock.Arguments) {
			captured = args.Get(1).(rm.ContainerCreateArgs)
		}).
		Return("id", nil)

	rec.On("CreatedContainer", mock.Anything, mock.Anything).Return()
	rec.On("StartedContainer", mock.Anything, mock.Anything).Return()

	require.NoError(t, client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "envtest",
		Image:           "busybox",
		ImagePullPolicy: corev1.PullNever,
		Env: []corev1.EnvVar{
			{Name: "FOO", Value: "bar"},
		},
		Command:    []string{"/bin/sh"},
		Args:       []string{"-c", "echo hello"},
		WorkingDir: "/app",
		TTY:        true,
		Stdin:      true,
	}))

	awaitCall(t, &cli.Mock, "CreateAndStartContainer")

	assert.Equal(t, "busybox", captured.Image)
	assert.Equal(t, []string{"FOO=bar"}, captured.Env)
	assert.Equal(t, []string{"/bin/sh"}, captured.Command)
	assert.Equal(t, []string{"-c", "echo hello"}, captured.Args)
	assert.Equal(t, "/app", captured.WorkingDir)
	assert.True(t, captured.TTY)
	assert.True(t, captured.Stdin)
}

func TestContainerCreateArgs_NameFormat(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	var captured rm.ContainerCreateArgs
	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Run(func(args mock.Arguments) {
			captured = args.Get(1).(rm.ContainerCreateArgs)
		}).
		Return("id", nil)
	rec.On("CreatedContainer", mock.Anything, mock.Anything).Return()
	rec.On("StartedContainer", mock.Anything, mock.Anything).Return()

	require.NoError(t, client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "default",
		PodName:         "my-pod",
		Name:            "sidecar",
		Image:           "redis:7",
		ImagePullPolicy: corev1.PullNever,
	}))

	awaitCall(t, &cli.Mock, "CreateAndStartContainer")
	assert.Equal(t, "macos-vz_default_my-pod_sidecar", captured.Name)
	assert.Equal(t, "redis:7", captured.Image)
}

// --- Dangling cleanup with multiple containers ---

func TestNewAppleContainerClient_CleansMultiple(t *testing.T) {
	cli := climocks.NewMockCLIExecutor(t)
	rec := eventmocks.NewMockEventRecorder(t)

	names := []string{"macos-vz_a_b_c", "macos-vz_d_e_f"}
	cli.On("ListContainers", mock.Anything, rm.ContainerNamePrefix).Return(names, nil)
	for _, name := range names {
		cli.On("RemoveContainer", mock.Anything, name, true).Return(nil)
	}

	_, err := rm.NewAppleContainerClient(context.Background(), cli, rec)
	require.NoError(t, err)
	cli.AssertNumberOfCalls(t, "RemoveContainer", 2)
}

// --- PullImage error propagation ---

func TestCreateContainer_PullError(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	pullErr := fmt.Errorf("network timeout")
	cli.On("PullImage", mock.Anything, "broken:latest", resource.RegistryCredentials{}).
		Return(pullErr)

	rec.On("PullingImage", mock.Anything, "broken:latest", "fail").Return()
	rec.On("BackOffPullImage", mock.Anything, "broken:latest", "fail", pullErr).Return()

	require.NoError(t, client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "fail",
		Image:           "broken:latest",
		ImagePullPolicy: corev1.PullIfNotPresent,
	}))

	awaitCall(t, &rec.Mock, "BackOffPullImage")
	assert.False(t, wasCalled(&cli.Mock, "CreateAndStartContainer"))
}

// --- RemoveContainers after creation ---

func TestRemoveContainers_Success(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Return("id-rm", nil)
	cli.On("RemoveContainer", mock.Anything, "id-rm", true).Return(nil)

	rec.On("CreatedContainer", mock.Anything, mock.Anything).Return()
	rec.On("StartedContainer", mock.Anything, mock.Anything).Return()

	require.NoError(t, client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "rm-me",
		Image:           "busybox",
		ImagePullPolicy: corev1.PullNever,
	}))

	awaitCall(t, &cli.Mock, "CreateAndStartContainer")

	err := client.RemoveContainers(ctx, "ns", "pod", 0)
	require.NoError(t, err)
	cli.AssertCalled(t, "RemoveContainer", mock.Anything, "id-rm", true)
}

// --- WaitForContainer tests ---

func TestWaitForContainer_NotTracked(t *testing.T) {
	client, _, _ := newTestClient(t)
	_, err := client.WaitForContainer(context.Background(), "ns", "pod", "ctr")
	assert.Error(t, err)
}

func TestWaitForContainer_ExitZero(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Return("init-id", nil)
	rec.On("CreatedContainer", mock.Anything, mock.Anything).Return()
	rec.On("StartedContainer", mock.Anything, mock.Anything).Return()

	// First inspect returns running, second returns dead with exit 0
	cli.On("InspectContainer", mock.Anything, "init-id").
		Return(resource.ContainerState{Status: resource.ContainerStatusRunning}, nil).Once()
	cli.On("InspectContainer", mock.Anything, "init-id").
		Return(resource.ContainerState{Status: resource.ContainerStatusDead, ExitCode: 0}, nil).Once()

	require.NoError(t, client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "init",
		Image:           "busybox",
		ImagePullPolicy: corev1.PullNever,
	}))
	awaitCall(t, &cli.Mock, "CreateAndStartContainer")

	exitCode, err := client.WaitForContainer(ctx, "ns", "pod", "init")
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestWaitForContainer_ExitNonZero(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Return("fail-id", nil)
	rec.On("CreatedContainer", mock.Anything, mock.Anything).Return()
	rec.On("StartedContainer", mock.Anything, mock.Anything).Return()

	cli.On("InspectContainer", mock.Anything, "fail-id").
		Return(resource.ContainerState{Status: resource.ContainerStatusDead, ExitCode: 1}, nil)

	require.NoError(t, client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "fail-init",
		Image:           "busybox",
		ImagePullPolicy: corev1.PullNever,
	}))
	awaitCall(t, &cli.Mock, "CreateAndStartContainer")

	exitCode, err := client.WaitForContainer(ctx, "ns", "pod", "fail-init")
	require.NoError(t, err)
	assert.Equal(t, 1, exitCode)
}

// --- RemoveContainer (singular) tests ---

func TestRemoveContainer_NotTracked(t *testing.T) {
	client, _, _ := newTestClient(t)
	err := client.RemoveContainer(context.Background(), "ns", "pod", "nope")
	assert.Error(t, err)
}

func TestRemoveContainer_Success(t *testing.T) {
	client, cli, rec := newTestClient(t)
	ctx := context.Background()

	cli.On("CreateAndStartContainer", mock.Anything, mock.AnythingOfType("resourcemanager.ContainerCreateArgs")).
		Return("rm-id", nil)
	cli.On("RemoveContainer", mock.Anything, "rm-id", true).Return(nil)
	rec.On("CreatedContainer", mock.Anything, mock.Anything).Return()
	rec.On("StartedContainer", mock.Anything, mock.Anything).Return()

	require.NoError(t, client.CreateContainer(ctx, rm.ContainerParams{
		PodNamespace:    "ns",
		PodName:         "pod",
		Name:            "rm-single",
		Image:           "busybox",
		ImagePullPolicy: corev1.PullNever,
	}))
	awaitCall(t, &cli.Mock, "CreateAndStartContainer")

	err := client.RemoveContainer(ctx, "ns", "pod", "rm-single")
	require.NoError(t, err)
	cli.AssertCalled(t, "RemoveContainer", mock.Anything, "rm-id", true)
}

// --- GetContainersListResult ---

func TestGetContainersListResult_Empty(t *testing.T) {
	client, _, _ := newTestClient(t)
	result, err := client.GetContainersListResult(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result)
}
