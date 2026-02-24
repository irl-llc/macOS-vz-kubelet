package resourcemanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
	"github.com/virtual-kubelet/virtual-kubelet/log"
	"github.com/virtual-kubelet/virtual-kubelet/node/api"
)

// AppleContainerCLI shells out to the Apple `container` binary.
type AppleContainerCLI struct {
	binary string
}

// NewAppleContainerCLI creates a CLI executor targeting the given binary path.
func NewAppleContainerCLI(binary string) (*AppleContainerCLI, error) {
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("container binary %q: %w", binary, err)
	}
	return &AppleContainerCLI{binary: resolved}, nil
}

// ListContainers returns container names matching a prefix.
func (c *AppleContainerCLI) ListContainers(ctx context.Context, namePrefix string) ([]string, error) {
	out, err := c.run(ctx, "list", "--all", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	return filterContainersByPrefix(out, namePrefix)
}

// CreateAndStartContainer runs a container in detached mode and returns its name.
func (c *AppleContainerCLI) CreateAndStartContainer(ctx context.Context, args ContainerCreateArgs) (string, error) {
	cliArgs := buildRunArgs(args)
	out, err := c.run(ctx, cliArgs...)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RemoveContainer deletes a container, optionally forcing removal.
func (c *AppleContainerCLI) RemoveContainer(ctx context.Context, id string, force bool) error {
	args := []string{"delete", id}
	if force {
		args = []string{"delete", "--force", id}
	}
	_, err := c.run(ctx, args...)
	return err
}

// InspectContainer parses `container inspect` JSON into a ContainerState.
func (c *AppleContainerCLI) InspectContainer(ctx context.Context, id string) (resource.ContainerState, error) {
	out, err := c.run(ctx, "inspect", id)
	if err != nil {
		return resource.ContainerState{}, fmt.Errorf("inspect %s: %w", id, err)
	}
	return parseInspectJSON(ctx, out)
}

// ContainerLogs streams log output from a container.
func (c *AppleContainerCLI) ContainerLogs(ctx context.Context, name string, opts api.ContainerLogOpts) (io.ReadCloser, error) {
	args := buildLogArgs(name, opts)
	return c.stream(ctx, args...)
}

// ExecInContainer runs a command inside a running container, wiring I/O.
func (c *AppleContainerCLI) ExecInContainer(ctx context.Context, name string, cmd []string, attach api.AttachIO) error {
	args := buildExecArgs(name, cmd, attach)
	return c.runInteractive(ctx, args, attach)
}

// AttachToContainer connects to a container's primary process streams.
func (c *AppleContainerCLI) AttachToContainer(ctx context.Context, name string, attach api.AttachIO) error {
	args := buildAttachShellArgs(name, attach)
	return c.runInteractive(ctx, args, attach)
}

// PullImage fetches an image, optionally authenticating first.
func (c *AppleContainerCLI) PullImage(ctx context.Context, image string, creds resource.RegistryCredentials) error {
	if !creds.IsEmpty() {
		if err := c.registryLogin(ctx, creds); err != nil {
			return err
		}
	}
	_, err := c.run(ctx, "image", "pull", image)
	return err
}

// RemoveImage deletes a local container image.
func (c *AppleContainerCLI) RemoveImage(ctx context.Context, image string) error {
	_, err := c.run(ctx, "image", "delete", "--force", image)
	return err
}

// ContainerStats returns CPU nanoseconds and memory bytes for a container.
func (c *AppleContainerCLI) ContainerStats(ctx context.Context, id string) (uint64, uint64, error) {
	out, err := c.run(ctx, "stats", "--format", "json", "--no-stream", id)
	if err != nil {
		return 0, 0, fmt.Errorf("stats %s: %w", id, err)
	}
	return parseCLIStatsJSON(out)
}

func (c *AppleContainerCLI) registryLogin(ctx context.Context, creds resource.RegistryCredentials) error {
	cmd := exec.CommandContext(ctx, c.binary, "registry", "login",
		"--username", creds.Username, "--password-stdin", creds.Server)
	cmd.Stdin = strings.NewReader(creds.Password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("registry login: %s: %w", string(out), err)
	}
	return nil
}

// run executes the CLI synchronously and returns stdout.
func (c *AppleContainerCLI) run(ctx context.Context, args ...string) ([]byte, error) {
	log.G(ctx).Debugf("container %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, c.binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w", stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

// stream starts the CLI and returns stdout as a ReadCloser.
func (c *AppleContainerCLI) stream(ctx context.Context, args ...string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, c.binary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &cmdReadCloser{cmd: cmd, pipe: stdout}, nil
}

// runInteractive executes the CLI with stdin/stdout/stderr wired to attach.
func (c *AppleContainerCLI) runInteractive(ctx context.Context, args []string, attach api.AttachIO) error {
	cmd := exec.CommandContext(ctx, c.binary, args...)
	wireAttachIO(cmd, attach)
	return cmd.Run()
}

func wireAttachIO(cmd *exec.Cmd, attach api.AttachIO) {
	if attach == nil {
		return
	}
	cmd.Stdin = attach.Stdin()
	cmd.Stdout = attach.Stdout()
	cmd.Stderr = attach.Stderr()
}

// cmdReadCloser wraps a running command's stdout pipe, waiting on close.
type cmdReadCloser struct {
	cmd  *exec.Cmd
	pipe io.ReadCloser
}

func (r *cmdReadCloser) Read(p []byte) (int, error) {
	return r.pipe.Read(p)
}

func (r *cmdReadCloser) Close() error {
	pipeErr := r.pipe.Close()
	waitErr := r.cmd.Wait()
	return errors.Join(pipeErr, waitErr)
}

// --- Argument builders ---

func buildRunArgs(args ContainerCreateArgs) []string {
	a := []string{"run", "--detach", "--name", args.Name}
	a = appendEnvArgs(a, args.Env)
	a = appendBindArgs(a, args.Binds)
	a = appendRunFlags(a, args)
	a = append(a, args.Image)
	a = appendCommandAndArgs(a, args.Command, args.Args)
	return a
}

func appendEnvArgs(a, env []string) []string {
	for _, e := range env {
		a = append(a, "--env", e)
	}
	return a
}

func appendBindArgs(a, binds []string) []string {
	for _, b := range binds {
		a = append(a, "--volume", b)
	}
	return a
}

func appendRunFlags(a []string, args ContainerCreateArgs) []string {
	if args.WorkingDir != "" {
		a = append(a, "--workdir", args.WorkingDir)
	}
	if args.TTY {
		a = append(a, "--tty")
	}
	if args.Stdin {
		a = append(a, "--interactive")
	}
	a = appendSecurityFlags(a, args)
	a = appendResourceFlags(a, args)
	return a
}

func appendSecurityFlags(a []string, args ContainerCreateArgs) []string {
	if args.User != "" {
		a = append(a, "--user", args.User)
	}
	if args.ReadOnlyRootFS {
		a = append(a, "--read-only")
	}
	for _, cap := range args.CapAdd {
		a = append(a, "--cap-add", cap)
	}
	for _, cap := range args.CapDrop {
		a = append(a, "--cap-drop", cap)
	}
	return a
}

func appendResourceFlags(a []string, args ContainerCreateArgs) []string {
	if args.MemoryLimitBytes > 0 {
		a = append(a, "--memory", strconv.FormatInt(args.MemoryLimitBytes, 10))
	}
	if args.CPULimit > 0 {
		a = append(a, "--cpus", strconv.FormatFloat(args.CPULimit, 'f', -1, 64))
	}
	return a
}

func appendCommandAndArgs(a, cmd, cmdArgs []string) []string {
	a = append(a, cmd...)
	a = append(a, cmdArgs...)
	return a
}

func buildLogArgs(name string, opts api.ContainerLogOpts) []string {
	args := []string{"logs"}
	if opts.Follow {
		args = append(args, "--follow")
	}
	if opts.Tail > 0 {
		args = append(args, "-n", strconv.Itoa(opts.Tail))
	}
	return append(args, name)
}

func buildExecArgs(name string, cmd []string, attach api.AttachIO) []string {
	args := []string{"exec"}
	if attach != nil && attach.TTY() {
		args = append(args, "--tty")
	}
	if attach != nil && attach.Stdin() != nil {
		args = append(args, "--interactive")
	}
	args = append(args, name)
	return append(args, cmd...)
}

func buildAttachShellArgs(name string, attach api.AttachIO) []string {
	args := []string{"exec"}
	if attach != nil && attach.TTY() {
		args = append(args, "--tty")
	}
	if attach != nil && attach.Stdin() != nil {
		args = append(args, "--interactive")
	}
	return append(args, name, "/bin/sh")
}

// --- JSON parsing ---

type cliContainerEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func filterContainersByPrefix(data []byte, prefix string) ([]string, error) {
	var entries []cliContainerEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse container list: %w", err)
	}
	var matched []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name, prefix) {
			matched = append(matched, e.Name)
		}
	}
	return matched, nil
}

type cliInspectResult struct {
	State cliContainerState `json:"state"`
}

type cliContainerState struct {
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
	ExitCode   int    `json:"exitCode"`
	Error      string `json:"error"`
}

func parseInspectJSON(ctx context.Context, data []byte) (resource.ContainerState, error) {
	var result cliInspectResult
	if err := json.Unmarshal(data, &result); err != nil {
		return resource.ContainerState{}, fmt.Errorf("parse inspect: %w", err)
	}
	return mapCLIState(ctx, result.State), nil
}

func mapCLIState(ctx context.Context, s cliContainerState) resource.ContainerState {
	return resource.ContainerState{
		Status:     mapStatusString(s.Status),
		StartedAt:  parseTimeSafe(ctx, s.StartedAt),
		FinishedAt: parseTimeSafe(ctx, s.FinishedAt),
		ExitCode:   s.ExitCode,
		Error:      s.Error,
	}
}

func mapStatusString(s string) resource.ContainerStatus {
	switch strings.ToLower(s) {
	case "running":
		return resource.ContainerStatusRunning
	case "created":
		return resource.ContainerStatusCreated
	case "paused":
		return resource.ContainerStatusPaused
	case "restarting":
		return resource.ContainerStatusRestarting
	case "dead", "exited", "stopped":
		return resource.ContainerStatusDead
	default:
		return resource.ContainerStatusUnknown
	}
}

func parseTimeSafe(ctx context.Context, s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		log.G(ctx).WithError(err).Warnf("failed to parse timestamp %q", s)
	}
	return t
}

type cliStatsResult struct {
	CPUNano  uint64 `json:"cpuNano"`
	MemBytes uint64 `json:"memBytes"`
}

func parseCLIStatsJSON(data []byte) (uint64, uint64, error) {
	var result cliStatsResult
	if err := json.Unmarshal(data, &result); err != nil {
		return 0, 0, fmt.Errorf("parse stats: %w", err)
	}
	return result.CPUNano, result.MemBytes, nil
}
