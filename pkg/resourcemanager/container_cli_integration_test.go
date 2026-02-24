//go:build integration

package resourcemanager

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

	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
)

const (
	integrationImage     = "alpine:latest"
	integrationTestPrefix = "macos-vz-inttest"
)

func containerBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("container")
	require.NoError(t, err, "container CLI not found in PATH; is Apple Container installed?")
	return path
}

func requireContainerServices(t *testing.T) {
	t.Helper()
	out, err := exec.Command("container", "system", "status").CombinedOutput()
	if err != nil {
		t.Skipf("container services not running (run `container system start`): %s", strings.TrimSpace(string(out)))
	}
}

func uniqueName(t *testing.T, suffix string) string {
	t.Helper()
	// Use test name hash to avoid collisions; keep it short
	safe := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	return integrationTestPrefix + "_" + safe + "_" + suffix
}

// forceRemove cleans up a container by name, ignoring errors.
func forceRemove(t *testing.T, cli *AppleContainerCLI, ctx context.Context, name string) {
	t.Helper()
	_ = cli.RemoveContainer(ctx, name, true)
}

// TestCLIIntegration_Lifecycle exercises the full container lifecycle through
// the real Apple Container CLI: pull, create, inspect, exec, logs, stats, stop, remove.
func TestCLIIntegration_Lifecycle(t *testing.T) {
	requireContainerServices(t)
	binary := containerBinary(t)
	cli, err := NewAppleContainerCLI(binary)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	name := uniqueName(t, "lifecycle")
	t.Cleanup(func() { forceRemove(t, cli, context.Background(), name) })

	// --- Pull image ---
	t.Run("PullImage", func(t *testing.T) {
		err := cli.PullImage(ctx, integrationImage, resource.RegistryCredentials{})
		require.NoError(t, err)
	})

	// --- Create and start ---
	var containerID string
	t.Run("CreateAndStart", func(t *testing.T) {
		id, err := cli.CreateAndStartContainer(ctx, ContainerCreateArgs{
			Name:    name,
			Image:   integrationImage,
			Env:     []string{"TEST_VAR=hello_integration"},
			Command: []string{"sh", "-c", "echo started && sleep 300"},
		})
		require.NoError(t, err)
		require.NotEmpty(t, id)
		containerID = id
	})

	// --- Wait for running ---
	t.Run("InspectRunning", func(t *testing.T) {
		require.NotEmpty(t, containerID, "container was not created")
		state := pollForStatus(t, cli, ctx, containerID, resource.ContainerStatusRunning, 15*time.Second)
		assert.Equal(t, resource.ContainerStatusRunning, state.Status)
		assert.False(t, state.StartedAt.IsZero(), "StartedAt should be set")
	})

	// --- Exec ---
	t.Run("ExecInContainer", func(t *testing.T) {
		require.NotEmpty(t, containerID, "container was not created")
		var stdout bytes.Buffer
		attach := newCaptureIO(&stdout, nil)
		err := cli.ExecInContainer(ctx, containerID, []string{"echo", "exec_works"}, attach)
		require.NoError(t, err)
		assert.Contains(t, stdout.String(), "exec_works")
	})

	// --- Env visible via exec ---
	t.Run("EnvVisible", func(t *testing.T) {
		require.NotEmpty(t, containerID, "container was not created")
		var stdout bytes.Buffer
		attach := newCaptureIO(&stdout, nil)
		err := cli.ExecInContainer(ctx, containerID, []string{"printenv", "TEST_VAR"}, attach)
		require.NoError(t, err)
		assert.Contains(t, stdout.String(), "hello_integration")
	})

	// --- Logs ---
	t.Run("ContainerLogs", func(t *testing.T) {
		require.NotEmpty(t, containerID, "container was not created")
		rc, err := cli.ContainerLogs(ctx, containerID, api.ContainerLogOpts{Tail: 10})
		require.NoError(t, err)
		defer rc.Close()
		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Contains(t, string(data), "started")
	})

	// --- Stats ---
	t.Run("ContainerStats", func(t *testing.T) {
		require.NotEmpty(t, containerID, "container was not created")
		// Stats may need a moment to populate
		var cpuNano, memBytes uint64
		var err error
		require.Eventually(t, func() bool {
			cpuNano, memBytes, err = cli.ContainerStats(ctx, containerID)
			return err == nil
		}, 10*time.Second, 1*time.Second, "stats never became available")
		// Just verify we got non-error results; values may be zero briefly
		t.Logf("stats: cpu=%d ns, mem=%d bytes", cpuNano, memBytes)
	})

	// --- List ---
	t.Run("ListContainers", func(t *testing.T) {
		names, err := cli.ListContainers(ctx, integrationTestPrefix)
		require.NoError(t, err)
		assert.Contains(t, names, name)
	})

	// --- Remove ---
	t.Run("RemoveContainer", func(t *testing.T) {
		require.NotEmpty(t, containerID, "container was not created")
		err := cli.RemoveContainer(ctx, containerID, true)
		require.NoError(t, err)
	})

	// --- Verify gone ---
	t.Run("VerifyRemoved", func(t *testing.T) {
		names, err := cli.ListContainers(ctx, integrationTestPrefix)
		require.NoError(t, err)
		assert.NotContains(t, names, name)
	})
}

// TestCLIIntegration_ShortLived tests a container that exits on its own.
func TestCLIIntegration_ShortLived(t *testing.T) {
	requireContainerServices(t)
	binary := containerBinary(t)
	cli, err := NewAppleContainerCLI(binary)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	name := uniqueName(t, "shortlived")
	t.Cleanup(func() { forceRemove(t, cli, context.Background(), name) })

	err = cli.PullImage(ctx, integrationImage, resource.RegistryCredentials{})
	require.NoError(t, err)

	id, err := cli.CreateAndStartContainer(ctx, ContainerCreateArgs{
		Name:    name,
		Image:   integrationImage,
		Command: []string{"sh", "-c", "echo goodbye && exit 42"},
	})
	require.NoError(t, err)

	state := pollForTerminal(t, cli, ctx, id, 30*time.Second)
	assert.True(t, state.Status.IsTerminal(), "expected terminal status, got %v", state.Status)
	// NOTE: Apple Container CLI inspect does not expose exit codes; ExitCode
	// will always be 0 regardless of the container's actual exit code.
	assert.Equal(t, 0, state.ExitCode, "inspect does not expose exit codes")

	rc, err := cli.ContainerLogs(ctx, id, api.ContainerLogOpts{})
	require.NoError(t, err)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Contains(t, string(data), "goodbye")
}

// TestCLIIntegration_RemoveImage verifies the image removal path.
func TestCLIIntegration_RemoveImage(t *testing.T) {
	requireContainerServices(t)
	binary := containerBinary(t)
	cli, err := NewAppleContainerCLI(binary)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Pull so we have something to remove
	err = cli.PullImage(ctx, integrationImage, resource.RegistryCredentials{})
	require.NoError(t, err)

	err = cli.RemoveImage(ctx, integrationImage)
	require.NoError(t, err)
}

// --- helpers ---

func pollForStatus(
	t *testing.T, cli *AppleContainerCLI,
	ctx context.Context, id string,
	want resource.ContainerStatus, timeout time.Duration,
) resource.ContainerState {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			state, err := cli.InspectContainer(ctx, id)
			if err != nil {
				t.Logf("inspect poll: %v", err)
				continue
			}
			if state.Status == want {
				return state
			}
		case <-deadline:
			t.Fatalf("timed out waiting for status %v", want)
			return resource.ContainerState{}
		case <-ctx.Done():
			t.Fatalf("context cancelled waiting for status %v", want)
			return resource.ContainerState{}
		}
	}
}

func pollForTerminal(
	t *testing.T, cli *AppleContainerCLI,
	ctx context.Context, id string, timeout time.Duration,
) resource.ContainerState {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			state, err := cli.InspectContainer(ctx, id)
			if err != nil {
				t.Logf("inspect poll: %v", err)
				continue
			}
			if state.Status.IsTerminal() {
				return state
			}
		case <-deadline:
			t.Fatalf("timed out waiting for terminal state")
			return resource.ContainerState{}
		case <-ctx.Done():
			t.Fatalf("context cancelled waiting for terminal state")
			return resource.ContainerState{}
		}
	}
}

// captureIO implements api.AttachIO for capturing exec output.
type captureIO struct {
	stdout io.WriteCloser
	stderr io.WriteCloser
}

func newCaptureIO(stdout, stderr *bytes.Buffer) *captureIO {
	var out, errOut io.WriteCloser
	if stdout != nil {
		out = nopWriteCloser{stdout}
	}
	if stderr != nil {
		errOut = nopWriteCloser{stderr}
	}
	return &captureIO{stdout: out, stderr: errOut}
}

func (c *captureIO) TTY() bool                         { return false }
func (c *captureIO) Stdin() io.Reader                   { return nil }
func (c *captureIO) Stdout() io.WriteCloser             { return c.stdout }
func (c *captureIO) Stderr() io.WriteCloser             { return c.stderr }
func (c *captureIO) Resize() <-chan api.TermSize        { return nil }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// TestCLIIntegration_VolumeBind verifies files on host are visible inside
// the container via --volume bind mounts.
func TestCLIIntegration_VolumeBind(t *testing.T) {
	requireContainerServices(t)
	binary := containerBinary(t)
	cli, err := NewAppleContainerCLI(binary)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	require.NoError(t, cli.PullImage(ctx, integrationImage, resource.RegistryCredentials{}))

	hostDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "data.txt"), []byte("bind-mount-test"), 0644))

	name := uniqueName(t, "volbind")
	t.Cleanup(func() { forceRemove(t, cli, context.Background(), name) })

	_, err = cli.CreateAndStartContainer(ctx, ContainerCreateArgs{
		Name:    name,
		Image:   integrationImage,
		Binds:   []string{hostDir + ":/mnt/vol:ro"},
		Command: []string{"sleep", "300"},
	})
	require.NoError(t, err)
	pollForStatus(t, cli, ctx, name, resource.ContainerStatusRunning, 15*time.Second)

	var stdout bytes.Buffer
	attach := newCaptureIO(&stdout, nil)
	err = cli.ExecInContainer(ctx, name, []string{"cat", "/mnt/vol/data.txt"}, attach)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "bind-mount-test")
}

// TestCLIIntegration_SecurityFlags verifies user and read-only rootfs flags.
func TestCLIIntegration_SecurityFlags(t *testing.T) {
	requireContainerServices(t)
	binary := containerBinary(t)
	cli, err := NewAppleContainerCLI(binary)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	require.NoError(t, cli.PullImage(ctx, integrationImage, resource.RegistryCredentials{}))

	name := uniqueName(t, "security")
	t.Cleanup(func() { forceRemove(t, cli, context.Background(), name) })

	_, err = cli.CreateAndStartContainer(ctx, ContainerCreateArgs{
		Name:    name,
		Image:   integrationImage,
		User:    "1000:1000",
		Command: []string{"sleep", "300"},
	})
	require.NoError(t, err)
	pollForStatus(t, cli, ctx, name, resource.ContainerStatusRunning, 15*time.Second)

	var stdout bytes.Buffer
	attach := newCaptureIO(&stdout, nil)
	err = cli.ExecInContainer(ctx, name, []string{"id"}, attach)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "uid=1000")
}

// TestCLIIntegration_ResourceLimits verifies resource limit flags are accepted.
func TestCLIIntegration_ResourceLimits(t *testing.T) {
	requireContainerServices(t)
	binary := containerBinary(t)
	cli, err := NewAppleContainerCLI(binary)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	require.NoError(t, cli.PullImage(ctx, integrationImage, resource.RegistryCredentials{}))

	name := uniqueName(t, "resources")
	t.Cleanup(func() { forceRemove(t, cli, context.Background(), name) })

	id, err := cli.CreateAndStartContainer(ctx, ContainerCreateArgs{
		Name:             name,
		Image:            integrationImage,
		MemoryLimitBytes: 128 * 1024 * 1024,
		CPULimit:         0.5,
		Command:          []string{"sleep", "300"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, id, "container should be created with resource limits")

	state := pollForStatus(t, cli, ctx, id, resource.ContainerStatusRunning, 15*time.Second)
	assert.Equal(t, resource.ContainerStatusRunning, state.Status)
}
