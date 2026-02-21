package resourcemanager

import (
	"context"
	"testing"
	"time"

	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
)

// --- buildRunArgs tests ---

func TestBuildRunArgs_Minimal(t *testing.T) {
	args := buildRunArgs(ContainerCreateArgs{
		Name:  "ctr",
		Image: "alpine",
	})
	assert.Equal(t, []string{
		"run", "--detach", "--name", "ctr", "alpine",
	}, args)
}

func TestBuildRunArgs_Full(t *testing.T) {
	args := buildRunArgs(ContainerCreateArgs{
		Name:       "ctr",
		Image:      "nginx",
		Env:        []string{"A=1", "B=2"},
		Binds:      []string{"/host:/ctr:rw"},
		Command:    []string{"/bin/sh"},
		Args:       []string{"-c", "echo hi"},
		WorkingDir: "/app",
		TTY:        true,
		Stdin:      true,
	})
	expected := []string{
		"run", "--detach", "--name", "ctr",
		"--env", "A=1", "--env", "B=2",
		"--volume", "/host:/ctr:rw",
		"--workdir", "/app",
		"--tty",
		"--interactive",
		"nginx",
		"/bin/sh", "-c", "echo hi",
	}
	assert.Equal(t, expected, args)
}

// --- buildLogArgs tests ---

func TestBuildLogArgs_Default(t *testing.T) {
	args := buildLogArgs("my-ctr", api.ContainerLogOpts{})
	assert.Equal(t, []string{"logs", "my-ctr"}, args)
}

func TestBuildLogArgs_FollowAndTail(t *testing.T) {
	args := buildLogArgs("my-ctr", api.ContainerLogOpts{Follow: true, Tail: 50})
	assert.Equal(t, []string{"logs", "--follow", "-n", "50", "my-ctr"}, args)
}

// --- buildExecArgs tests ---

func TestBuildExecArgs_NoAttach(t *testing.T) {
	args := buildExecArgs("ctr", []string{"ls", "-la"}, nil)
	assert.Equal(t, []string{"exec", "ctr", "ls", "-la"}, args)
}

// --- JSON parsing tests ---

func TestFilterContainersByPrefix_MatchesCorrectly(t *testing.T) {
	data := `[{"name":"macos-vz_ns_pod_a","status":"running"},{"name":"other_ctr","status":"stopped"},{"name":"macos-vz_ns_pod_b","status":"stopped"}]`
	matched, err := filterContainersByPrefix([]byte(data), "macos-vz")
	require.NoError(t, err)
	assert.Equal(t, []string{"macos-vz_ns_pod_a", "macos-vz_ns_pod_b"}, matched)
}

func TestFilterContainersByPrefix_Empty(t *testing.T) {
	matched, err := filterContainersByPrefix([]byte("[]"), "macos-vz")
	require.NoError(t, err)
	assert.Empty(t, matched)
}

func TestFilterContainersByPrefix_InvalidJSON(t *testing.T) {
	_, err := filterContainersByPrefix([]byte("not json"), "macos-vz")
	assert.Error(t, err)
}

func TestParseInspectJSON_Running(t *testing.T) {
	ctx := context.Background()
	data := `{"state":{"status":"running","startedAt":"2025-06-15T10:30:00Z","finishedAt":"","exitCode":0,"error":""}}`
	state, err := parseInspectJSON(ctx, []byte(data))
	require.NoError(t, err)
	assert.Equal(t, resource.ContainerStatusRunning, state.Status)
	assert.Equal(t, time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC), state.StartedAt)
	assert.True(t, state.FinishedAt.IsZero())
	assert.Equal(t, 0, state.ExitCode)
}

func TestParseInspectJSON_Exited(t *testing.T) {
	ctx := context.Background()
	data := `{"state":{"status":"exited","startedAt":"2025-06-15T10:30:00Z","finishedAt":"2025-06-15T10:31:00Z","exitCode":137,"error":"OOM killed"}}`
	state, err := parseInspectJSON(ctx, []byte(data))
	require.NoError(t, err)
	assert.Equal(t, resource.ContainerStatusDead, state.Status)
	assert.Equal(t, 137, state.ExitCode)
	assert.Equal(t, "OOM killed", state.Error)
}

func TestParseInspectJSON_InvalidJSON(t *testing.T) {
	_, err := parseInspectJSON(context.Background(), []byte("nope"))
	assert.Error(t, err)
}

func TestMapStatusString(t *testing.T) {
	tests := []struct {
		input    string
		expected resource.ContainerStatus
	}{
		{"running", resource.ContainerStatusRunning},
		{"Running", resource.ContainerStatusRunning},
		{"created", resource.ContainerStatusCreated},
		{"paused", resource.ContainerStatusPaused},
		{"restarting", resource.ContainerStatusRestarting},
		{"dead", resource.ContainerStatusDead},
		{"exited", resource.ContainerStatusDead},
		{"stopped", resource.ContainerStatusDead},
		{"unknown-state", resource.ContainerStatusUnknown},
		{"", resource.ContainerStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, mapStatusString(tt.input))
		})
	}
}

func TestParseCLIStatsJSON_Success(t *testing.T) {
	data := `{"cpuNano":500000,"memBytes":104857600}`
	cpu, mem, err := parseCLIStatsJSON([]byte(data))
	require.NoError(t, err)
	assert.Equal(t, uint64(500000), cpu)
	assert.Equal(t, uint64(104857600), mem)
}

func TestParseCLIStatsJSON_InvalidJSON(t *testing.T) {
	_, _, err := parseCLIStatsJSON([]byte("bad"))
	assert.Error(t, err)
}

func TestParseTimeSafe_Valid(t *testing.T) {
	ts := parseTimeSafe(context.Background(), "2025-06-15T10:30:00Z")
	assert.Equal(t, time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC), ts)
}

func TestParseTimeSafe_Empty(t *testing.T) {
	ts := parseTimeSafe(context.Background(), "")
	assert.True(t, ts.IsZero())
}

func TestParseTimeSafe_Invalid(t *testing.T) {
	ts := parseTimeSafe(context.Background(), "not-a-time")
	assert.True(t, ts.IsZero())
}

